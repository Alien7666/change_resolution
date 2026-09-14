package scaling

// This file is the whole of the NVAPI boundary: DLL loading, entry-point resolution,
// struct layout, the three-pass configuration read, and the single syscall that can
// change anything. Everything above the nvapi seam in controller.go decides; nothing
// in this file does. That split exists because none of the code below can run on a
// machine without an NVIDIA driver, so keeping a decision here would be keeping a
// decision no test could reach.
//
// Two hazards in here are unlike anything else in this repository, and both are
// called out again where they occur:
//
//   - NV_DISPLAYCONFIG_PATH_INFO stores raw pointers in struct fields. The garbage
//     collector does not trace a uintptr, and go vet's unsafeptr check cannot see
//     through a store into a struct, so every syscall is bracketed by an explicit
//     runtime.KeepAlive over every backing slice. This is the reason nativeConfig
//     owns all four slices as one object.
//   - NvAPI_DISP_GetDisplayIdByDisplayName takes an ANSI byte string. It is the only
//     non-UTF-16 Win32-style string boundary in the project.

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/Alien7666/change_resolution/internal/domain"
	"golang.org/x/sys/windows"
)

// Interface ids come from NVIDIA's own nvapi_interface.h (github.com/NVIDIA/nvapi),
// not from a table copied off a blog. Every one of them is fetched through
// nvapi64.dll's exported nvapi_QueryInterface, which is why no cgo is needed and why
// CGO_ENABLED=0 keeps working.
const (
	idInitialize         uint32 = 0x0150E828
	idUnload             uint32 = 0xD22BDD7E
	idGetErrorMessage    uint32 = 0x6C2D048C
	idGetDisplayConfig   uint32 = 0x11ABCCF8
	idSetDisplayConfig   uint32 = 0x5D8CF8DE
	idGetDisplayIDByName uint32 = 0xAE457190
)

// statusInvalidArgument is the status this file returns for a payload it refuses to
// hand to the driver at all. It is NVAPI's own value for the same meaning, so the
// controller's error text stays truthful about what went wrong.
const statusInvalidArgument status = -5

// The driver decides how many paths there are, which makes the count untrusted input
// that sizes an allocation. These bounds are far above any real desktop (this machine
// reports four) and exist only so a nonsense value is refused instead of allocated.
const (
	maxDisplayPaths   uint32 = 64
	maxTargetsPerPath uint32 = 64
)

var statusNames = map[status]string{
	0:    "NVAPI_OK",
	-1:   "NVAPI_ERROR",
	-2:   "NVAPI_LIBRARY_NOT_FOUND",
	-3:   "NVAPI_NO_IMPLEMENTATION",
	-4:   "NVAPI_API_NOT_INITIALIZED",
	-5:   "NVAPI_INVALID_ARGUMENT",
	-6:   "NVAPI_NVIDIA_DEVICE_NOT_FOUND",
	-8:   "NVAPI_INVALID_POINTER",
	-9:   "NVAPI_INCOMPATIBLE_STRUCT_VERSION",
	-104: "NVAPI_NOT_SUPPORTED",
	-108: "NVAPI_DEVICE_BUSY",
	-157: "NVAPI_ERROR_DRIVER_RELOAD_REQUIRED",
	-174: "NVAPI_INSUFFICIENT_BUFFER",
}

// statusName gives the NVAPI symbol a user can search for. The spec requires the
// symbol and the driver's own message text, not a bare number.
func statusName(value status) string {
	if name, ok := statusNames[value]; ok {
		return name
	}
	return fmt.Sprintf("NVAPI_STATUS(%d)", value)
}

// ---------------------------------------------------------------------------
// Struct layout, x64. Mirrors nvapi.h with Go's natural alignment; every size is
// asserted in nvapi_windows_test.go against the number measured on the target
// driver. The blank fields are the C structs' explicit padding and reserved words:
// Go zeroes them, and nothing here ever writes them.
// ---------------------------------------------------------------------------

// NV_TIMINGEXT
type timingExt struct {
	Flag   uint32
	RR     uint16
	RRx1k  uint32
	Aspect uint32
	Rep    uint16
	Status uint32
	Name   [40]byte
}

// NV_TIMING
type timing struct {
	HVisible, HBorder, HFrontPorch, HSyncWidth, HTotal uint16
	HSyncPol                                           uint8
	VVisible, VBorder, VFrontPorch, VSyncWidth, VTotal uint16
	VSyncPol                                           uint8
	Interlaced                                         uint16
	Pclk                                               uint32
	Etc                                                timingExt
}

// NV_DISPLAYCONFIG_PATH_ADVANCED_TARGET_INFO_V1. Scaling is NV_SCALING, and it is the
// only word in this entire payload the tool is allowed to change.
type advTargetInfo struct {
	Version        uint32
	Rotation       uint32
	Scaling        uint32
	RefreshRate1K  uint32
	Flags          uint32 // interlaced:1 primary:1 rsvd:1 disableVirtualModeSupport:1 isPreferredUnscaledTarget:1 reserved:27
	Connector      uint32
	TVFormat       uint32
	TimingOverride uint32
	Timing         timing
}

// NV_DISPLAYCONFIG_PATH_TARGET_INFO_V2. Details is a raw pointer held in a struct
// field; see the file comment.
type targetInfo struct {
	DisplayID uint32
	_         uint32
	Details   uintptr
	TargetID  uint32
	_         uint32
}

// NV_RESOLUTION
type resolution struct{ Width, Height, ColorDepth uint32 }

// NV_DISPLAYCONFIG_SOURCE_MODE_INFO_V1
type sourceMode struct {
	Resolution  resolution
	ColorFormat uint32
	PosX, PosY  int32
	Spanning    uint32
	Flags       uint32 // bGDIPrimary:1 bSLIFocus:1 reserved:30
}

// NV_DISPLAYCONFIG_PATH_INFO_V2. TargetInfo, SourceModeInfo and OSAdapterID are raw
// pointers held in struct fields; see the file comment.
type pathInfo struct {
	Version         uint32
	SourceID        uint32
	TargetInfoCount uint32
	_               uint32
	TargetInfo      uintptr
	SourceModeInfo  uintptr
	Flags           uint32 // IsNonNVIDIAAdapter:1 reserved:31
	_               uint32
	OSAdapterID     uintptr
}

// makeVersion is nvapi.h's MAKE_NVAPI_VERSION: the struct's size in the low word and
// the structure version in the high word. Getting either half wrong is answered with
// NVAPI_INCOMPATIBLE_STRUCT_VERSION rather than with a corrupt read, which is what
// makes the negative control in the integration test meaningful.
func makeVersion(size uintptr, version uint32) uint32 {
	return uint32(size) | (version << 16)
}

func pathInfoVersion() uint32      { return makeVersion(unsafe.Sizeof(pathInfo{}), 2) }
func advTargetInfoVersion() uint32 { return makeVersion(unsafe.Sizeof(advTargetInfo{}), 1) }

// ---------------------------------------------------------------------------
// The backing store and its deterministic view
// ---------------------------------------------------------------------------

// nativeConfig owns every allocation the driver was given a pointer into. It is one
// object rather than four locals so that a single keepAlive call can cover the whole
// payload, and so that the config handed upward cannot outlive part of it.
type nativeConfig struct {
	count    uint32
	paths    []pathInfo
	targets  [][]targetInfo
	details  [][]advTargetInfo
	srcModes []sourceMode
}

// keepAlive is the counterpart of every pointer this file stores into a struct field.
// It must be called after the syscall returns, never before, and it must name every
// slice: the uintptr fields inside paths are invisible to the collector, so paths
// being reachable does not make targets, details or srcModes reachable.
func (n *nativeConfig) keepAlive() {
	runtime.KeepAlive(n.paths)
	runtime.KeepAlive(n.targets)
	runtime.KeepAlive(n.details)
	runtime.KeepAlive(n.srcModes)
	runtime.KeepAlive(n)
}

func numberField(name string, value *uint32) payloadField {
	return payloadField{name: name, number: value}
}

func textField(name string, value any) payloadField {
	return payloadField{name: name, value: fmt.Sprint(value)}
}

// pointerField flattens an address to nil/non-nil on purpose. Two reads of an
// unchanged configuration allocate at different addresses, so rendering the number
// would make the divergence check fire on noise and teach the reader to ignore it.
func pointerField(name string, value uintptr) payloadField {
	if value == 0 {
		return payloadField{name: name, value: "nil"}
	}
	return payloadField{name: name, value: "non-nil"}
}

// publish turns the backing store into the deterministic view controller.go diffs
// before and after it changes the scaling word.
//
// Every uint32 in the payload is published as a live pointer into the backing struct,
// which is what makes the diff able to catch the failure it exists for: if the
// Scaling field's offset ever stops agreeing with the driver's, the write lands on a
// neighbouring word and that neighbour's line moves instead of the scaling line.
// Fields that are not uint32 are rendered as text at read time — payloadField.number
// is a *uint32 and the seam is not this file's to change — which costs nothing here
// because the one writable field in the whole payload is a uint32 and so are all of
// its neighbours.
func (n *nativeConfig) publish() *config {
	cfg := &config{native: n}
	cfg.fields = append(cfg.fields, numberField("pathInfoCount", &n.count))

	for pathIndex := range n.paths {
		path := &n.paths[pathIndex]
		prefix := fmt.Sprintf("paths[%d]", pathIndex)
		cfg.fields = append(cfg.fields,
			numberField(prefix+".version", &path.Version),
			numberField(prefix+".sourceId", &path.SourceID),
			numberField(prefix+".targetInfoCount", &path.TargetInfoCount),
			pointerField(prefix+".targetInfo", path.TargetInfo),
			pointerField(prefix+".sourceModeInfo", path.SourceModeInfo),
			numberField(prefix+".flags", &path.Flags),
			pointerField(prefix+".osAdapterId", path.OSAdapterID),
		)
		if pathIndex < len(n.srcModes) {
			cfg.fields = append(cfg.fields, sourceModeFields(prefix+".sourceMode", &n.srcModes[pathIndex])...)
		}
		if pathIndex >= len(n.targets) {
			continue
		}
		for targetIndex := range n.targets[pathIndex] {
			target := &n.targets[pathIndex][targetIndex]
			detail := &n.details[pathIndex][targetIndex]
			targetPrefix := fmt.Sprintf("%s.targets[%d]", prefix, targetIndex)
			scalingName := targetPrefix + ".details.scaling"

			cfg.targets = append(cfg.targets, configTarget{
				displayID:    target.DisplayID,
				scaling:      &detail.Scaling,
				scalingField: scalingName,
			})
			cfg.fields = append(cfg.fields,
				numberField(targetPrefix+".displayId", &target.DisplayID),
				pointerField(targetPrefix+".details", target.Details),
				numberField(targetPrefix+".targetId", &target.TargetID),
				numberField(targetPrefix+".details.version", &detail.Version),
				numberField(targetPrefix+".details.rotation", &detail.Rotation),
				numberField(scalingName, &detail.Scaling),
				numberField(targetPrefix+".details.refreshRate1K", &detail.RefreshRate1K),
				numberField(targetPrefix+".details.flags", &detail.Flags),
				numberField(targetPrefix+".details.connector", &detail.Connector),
				numberField(targetPrefix+".details.tvFormat", &detail.TVFormat),
				numberField(targetPrefix+".details.timingOverride", &detail.TimingOverride),
			)
			cfg.fields = append(cfg.fields, timingFields(targetPrefix+".details.timing", &detail.Timing)...)
		}
	}
	return cfg
}

func sourceModeFields(prefix string, mode *sourceMode) []payloadField {
	return []payloadField{
		numberField(prefix+".resolution.width", &mode.Resolution.Width),
		numberField(prefix+".resolution.height", &mode.Resolution.Height),
		numberField(prefix+".resolution.colorDepth", &mode.Resolution.ColorDepth),
		numberField(prefix+".colorFormat", &mode.ColorFormat),
		textField(prefix+".posX", mode.PosX),
		textField(prefix+".posY", mode.PosY),
		numberField(prefix+".spanning", &mode.Spanning),
		numberField(prefix+".flags", &mode.Flags),
	}
}

func timingFields(prefix string, value *timing) []payloadField {
	return []payloadField{
		textField(prefix+".hVisible", value.HVisible),
		textField(prefix+".hBorder", value.HBorder),
		textField(prefix+".hFrontPorch", value.HFrontPorch),
		textField(prefix+".hSyncWidth", value.HSyncWidth),
		textField(prefix+".hTotal", value.HTotal),
		textField(prefix+".hSyncPol", value.HSyncPol),
		textField(prefix+".vVisible", value.VVisible),
		textField(prefix+".vBorder", value.VBorder),
		textField(prefix+".vFrontPorch", value.VFrontPorch),
		textField(prefix+".vSyncWidth", value.VSyncWidth),
		textField(prefix+".vTotal", value.VTotal),
		textField(prefix+".vSyncPol", value.VSyncPol),
		textField(prefix+".interlaced", value.Interlaced),
		numberField(prefix+".pclk", &value.Pclk),
		numberField(prefix+".etc.flag", &value.Etc.Flag),
		textField(prefix+".etc.rr", value.Etc.RR),
		numberField(prefix+".etc.rrx1k", &value.Etc.RRx1k),
		numberField(prefix+".etc.aspect", &value.Etc.Aspect),
		textField(prefix+".etc.rep", value.Etc.Rep),
		numberField(prefix+".etc.status", &value.Etc.Status),
		textField(prefix+".etc.name", fmt.Sprintf("%x", value.Etc.Name[:])),
	}
}

// ---------------------------------------------------------------------------
// Loading and entry-point resolution
// ---------------------------------------------------------------------------

// NewLazySystemDLL, not NewLazyDLL, and that is a security requirement rather than a
// style choice: it loads with LOAD_LIBRARY_SEARCH_SYSTEM32, the same way
// internal/display loads user32.dll. This tool ships as a single portable exe that
// users run straight out of Downloads, which is the textbook DLL-planting location,
// and the default search order would let a file named nvapi64.dll sitting beside the
// exe take over the process.
var (
	nvapiDLL            = windows.NewLazySystemDLL("nvapi64.dll")
	nvapiQueryInterface = nvapiDLL.NewProc("nvapi_QueryInterface")
)

// windowsNVAPI holds resolved entry-point addresses, never the DLL's own symbols:
// nvapi64.dll exports exactly one usable symbol and everything else is reached
// through it.
type windowsNVAPI struct {
	initializeProc     uintptr
	unloadProc         uintptr
	getErrorMessage    uintptr
	getDisplayConfig   uintptr
	setDisplayConfig   uintptr
	getDisplayIDByName uintptr
}

// NewWindowsController builds the production scaling controller.
//
// resolve is injected rather than imported: internal/scaling must not import
// internal/display, and internal/display must not import internal/scaling. The
// composition root passes display.Controller.ResolveTarget, so the one identity
// ladder in the program is reused here instead of being written a second time.
//
// The domain.Target that resolve returns never leaves this package. Its DeviceName
// is converted to an NVAPI displayId inside a single function and dropped there,
// because any \\.\DISPLAYn that survives an NvAPI_DISP_SetDisplayConfig may by then
// name a different monitor.
func NewWindowsController(resolve func(domain.MonitorIdentity) (domain.Target, error)) Controller {
	api, err := loadNVAPI()
	if err != nil {
		// nil, not a typed nil pointer: the controller tests api == nil.
		return newController(nil, resolve, err)
	}
	return newController(api, resolve, nil)
}

// loadNVAPI is the whole of the vendor detection's first layer. It is functional, not
// a string comparison: a machine with an NVIDIA-branded adapter that cannot load the
// DLL or resolve the entry points does not have this feature, and a machine that can
// does, whatever the adapter calls itself.
func loadNVAPI() (*windowsNVAPI, error) {
	if err := nvapiDLL.Load(); err != nil {
		return nil, fmt.Errorf("load nvapi64.dll: %w", err)
	}
	if err := nvapiQueryInterface.Find(); err != nil {
		return nil, fmt.Errorf("resolve nvapi_QueryInterface in nvapi64.dll: %w", err)
	}

	query := nvapiQueryInterface.Addr()
	api := &windowsNVAPI{
		initializeProc:     queryInterface(query, idInitialize),
		unloadProc:         queryInterface(query, idUnload),
		getErrorMessage:    queryInterface(query, idGetErrorMessage),
		getDisplayConfig:   queryInterface(query, idGetDisplayConfig),
		setDisplayConfig:   queryInterface(query, idSetDisplayConfig),
		getDisplayIDByName: queryInterface(query, idGetDisplayIDByName),
	}

	// NvAPI_GetErrorMessage is deliberately absent from this list. Losing it costs a
	// sentence of error text; the others are the feature.
	for _, required := range []struct {
		name    string
		address uintptr
	}{
		{name: "NvAPI_Initialize", address: api.initializeProc},
		{name: "NvAPI_Unload", address: api.unloadProc},
		{name: "NvAPI_DISP_GetDisplayConfig", address: api.getDisplayConfig},
		{name: "NvAPI_DISP_SetDisplayConfig", address: api.setDisplayConfig},
		{name: "NvAPI_DISP_GetDisplayIdByDisplayName", address: api.getDisplayIDByName},
	} {
		if required.address == 0 {
			return nil, fmt.Errorf("this NVIDIA driver does not provide %s", required.name)
		}
	}
	return api, nil
}

func queryInterface(query uintptr, id uint32) uintptr {
	address, _, _ := syscall.SyscallN(query, uintptr(id))
	return address
}

// toStatus reinterprets the register NVAPI returned as the signed NvAPI_Status it is.
// The double conversion is not redundant: uintptr -> uint32 truncates the 64-bit
// register, and uint32 -> int32 is what makes 0xFFFFFFF7 read as -9.
func toStatus(result uintptr) status {
	return status(int32(uint32(result)))
}

// ---------------------------------------------------------------------------
// The nvapi seam
// ---------------------------------------------------------------------------

func (a *windowsNVAPI) initialize() status {
	result, _, _ := syscall.SyscallN(a.initializeProc)
	return toStatus(result)
}

func (a *windowsNVAPI) unload() status {
	result, _, _ := syscall.SyscallN(a.unloadProc)
	return toStatus(result)
}

func (a *windowsNVAPI) readConfig() (*config, status) {
	native, callStatus := a.readNative(pathInfoVersion(), advTargetInfoVersion())
	if callStatus != statusOK {
		return nil, callStatus
	}
	return native.publish(), statusOK
}

// readNative is NvAPI_DISP_GetDisplayConfig's three-pass protocol: count the paths,
// learn each path's target count, then allocate and bind the target and source-mode
// arrays and read the whole thing.
//
// The version words are parameters so the opt-in hardware test can drive this exact
// sequence with a deliberately wrong stamp. That negative control is what turns "the
// read did not fail, so the version is probably right" into "the driver rejects every
// version except this one".
//
// Every pass re-stamps rather than trusting what is already in the buffer, for the
// same reason loadCurrentMode resets DmSize on every call: the driver owes the caller
// nothing about the bytes it was handed.
func (a *windowsNVAPI) readNative(pathVersion, advVersion uint32) (*nativeConfig, status) {
	var count uint32
	result, _, _ := syscall.SyscallN(a.getDisplayConfig, uintptr(unsafe.Pointer(&count)), 0)
	runtime.KeepAlive(&count)
	if callStatus := toStatus(result); callStatus != statusOK {
		return nil, callStatus
	}
	if count > maxDisplayPaths {
		return nil, statusInvalidArgument
	}

	native := &nativeConfig{count: count}
	if count == 0 {
		return native, statusOK
	}

	// Pass 2 fills in TargetInfoCount per path. It also overwrites Version, which is
	// why pass 3 stamps it again.
	native.paths = make([]pathInfo, count)
	for index := range native.paths {
		native.paths[index] = pathInfo{Version: pathVersion}
	}
	pathCount := count
	result, _, _ = syscall.SyscallN(a.getDisplayConfig,
		uintptr(unsafe.Pointer(&pathCount)),
		uintptr(unsafe.Pointer(&native.paths[0])))
	native.keepAlive()
	runtime.KeepAlive(&pathCount)
	if callStatus := toStatus(result); callStatus != statusOK {
		return nil, callStatus
	}
	if pathCount != count {
		return nil, statusInvalidArgument
	}

	native.targets = make([][]targetInfo, count)
	native.details = make([][]advTargetInfo, count)
	native.srcModes = make([]sourceMode, count)
	for index := range native.paths {
		targetCount := native.paths[index].TargetInfoCount
		if targetCount > maxTargetsPerPath {
			return nil, statusInvalidArgument
		}
		if targetCount > 0 {
			native.targets[index] = make([]targetInfo, targetCount)
			native.details[index] = make([]advTargetInfo, targetCount)
			for target := uint32(0); target < targetCount; target++ {
				native.details[index][target] = advTargetInfo{Version: advVersion}
				// The raw pointer in a struct field: nothing but keepAlive keeps the
				// details array reachable once this address is a uintptr.
				native.targets[index][target] = targetInfo{
					Details: uintptr(unsafe.Pointer(&native.details[index][target])),
				}
			}
			native.paths[index].TargetInfo = uintptr(unsafe.Pointer(&native.targets[index][0]))
		}
		native.paths[index].SourceModeInfo = uintptr(unsafe.Pointer(&native.srcModes[index]))
		native.paths[index].Version = pathVersion
	}

	pathCount = count
	result, _, _ = syscall.SyscallN(a.getDisplayConfig,
		uintptr(unsafe.Pointer(&pathCount)),
		uintptr(unsafe.Pointer(&native.paths[0])))
	native.keepAlive()
	runtime.KeepAlive(&pathCount)
	if callStatus := toStatus(result); callStatus != statusOK {
		return nil, callStatus
	}
	if pathCount != count {
		return nil, statusInvalidArgument
	}
	return native, statusOK
}

// writeConfig is the only place in the program that calls NvAPI_DISP_SetDisplayConfig.
// It takes the flag word from the caller and does not inspect it: controller.go's
// checkFlags is the single guard, so that the rule "0x00 or validate-only, nothing
// else" lives in one testable place rather than being half-enforced twice.
//
// The payload is whatever readConfig produced and the decision layer changed exactly
// one word of. Nothing is synthesised here.
func (a *windowsNVAPI) writeConfig(cfg *config, flags uint32) status {
	if cfg == nil {
		return statusInvalidArgument
	}
	native, ok := cfg.native.(*nativeConfig)
	if !ok || native == nil || native.count == 0 || len(native.paths) == 0 {
		return statusInvalidArgument
	}
	result, _, _ := syscall.SyscallN(a.setDisplayConfig,
		uintptr(native.count),
		uintptr(unsafe.Pointer(&native.paths[0])),
		uintptr(flags))
	native.keepAlive()
	return toStatus(result)
}

// displayIDByName is the ANSI boundary. NvAPI_DISP_GetDisplayIdByDisplayName takes a
// NUL-terminated narrow byte string (`\\.\DISPLAY1`), not the UTF-16 that every other
// Win32 call in this repository uses, so the usual windows.UTF16PtrFromString would
// hand the driver two bytes per character and resolve nothing.
//
// The name arrives here and dies here. It is never stored on the controller and never
// returned, because a GDI name that outlives one NVAPI set may name another monitor.
func (a *windowsNVAPI) displayIDByName(name string) (uint32, status) {
	encoded, err := ansiDeviceName(name)
	if err != nil {
		return 0, statusInvalidArgument
	}
	var displayID uint32
	result, _, _ := syscall.SyscallN(a.getDisplayIDByName,
		uintptr(unsafe.Pointer(&encoded[0])),
		uintptr(unsafe.Pointer(&displayID)))
	runtime.KeepAlive(encoded)
	runtime.KeepAlive(&displayID)
	if callStatus := toStatus(result); callStatus != statusOK {
		return 0, callStatus
	}
	return displayID, statusOK
}

// ansiDeviceName validates before it converts. A GDI device name is `\\.\DISPLAYn`,
// which is pure ASCII, and ASCII is the one range where UTF-8 and every Windows ANSI
// code page agree byte for byte. Anything outside it would be transcoded into a name
// the driver reads differently, so it is refused rather than guessed at — this is the
// one place in the program where a silently wrong string selects a different monitor.
func ansiDeviceName(name string) ([]byte, error) {
	if name == "" {
		return nil, errors.New("empty display device name")
	}
	for index := 0; index < len(name); index++ {
		if name[index] == 0 || name[index] >= 0x80 {
			return nil, fmt.Errorf("display device name %q is not ASCII at byte %d", name, index)
		}
	}
	return append([]byte(name), 0), nil
}

// errorMessage pairs the NVAPI symbol with the driver's own text. The symbol comes
// first and always: it is what a user can search for, and NvAPI_GetErrorMessage is
// unavailable exactly when things have gone worst.
func (a *windowsNVAPI) errorMessage(value status) string {
	name := statusName(value)
	if a.getErrorMessage == 0 {
		return name
	}
	// NvAPI_ShortString is char[64], ANSI like every NVAPI string.
	var message [64]byte
	result, _, _ := syscall.SyscallN(a.getErrorMessage,
		uintptr(uint32(value)),
		uintptr(unsafe.Pointer(&message[0])))
	runtime.KeepAlive(&message)
	if toStatus(result) != statusOK {
		return name
	}
	text := cString(message[:])
	if text == "" || text == name {
		return name
	}
	return name + ": " + text
}

func cString(buffer []byte) string {
	if end := bytes.IndexByte(buffer, 0); end >= 0 {
		buffer = buffer[:end]
	}
	return string(buffer)
}
