package display

import (
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// This file is the read-only half of the Connecting and Configuring Displays (CCD)
// API, and it exists for one reason: EnumDisplayDevicesW usually answers "Generic
// PnP Monitor" when asked what a monitor is called. In a settings list where picking
// the wrong row applies a display mode to the wrong screen, four identical rows are
// not a cosmetic problem.
//
// Three entry points are used and no others. GetDisplayConfigBufferSizes and
// QueryDisplayConfig read the arrangement currently in use; DisplayConfigGetDeviceInfo
// asks one target what its model is called. The writing half of this API -- the one
// that stores a configuration in the display database -- is the same mistake
// CDS_UPDATEREGISTRY would be, and no part of it is bound here.
//
// Everything in this file degrades quietly. A name that cannot be read is a name the
// caller does not get; it is never an error the user is shown, and it never stops a
// monitor from being enumerated, selected or driven.

const (
	// qdcOnlyActivePaths is QDC_ONLY_ACTIVE_PATHS: report the paths that are driving
	// a monitor right now, which is the same set of monitors EnumDisplayDevicesW
	// reports as attached, so the two reads describe one desktop.
	qdcOnlyActivePaths uint32 = 0x00000002

	// displayConfigDeviceInfoGetTargetName is DISPLAYCONFIG_DEVICE_INFO_GET_TARGET_NAME,
	// the only request packet type this file ever fills in.
	displayConfigDeviceInfoGetTargetName uint32 = 2

	errorSuccess            int32 = 0
	errorInsufficientBuffer int32 = 122

	// ccdQueryAttempts bounds the size-then-query retry. The two calls are separate
	// crossings and the desktop may change between them -- a monitor waking up is
	// enough -- which Windows reports as ERROR_INSUFFICIENT_BUFFER and expects the
	// caller to answer by sizing again. A desktop that keeps changing must cost a
	// bounded number of reads, not an unbounded loop on a UI thread.
	ccdQueryAttempts = 5
)

// The struct layouts below are the ABI contract with wingdi.h. QueryDisplayConfig
// writes into an array whose element size it infers from the count it was given, so
// a struct that is the wrong size is not a compile error and not a wrong value: it is
// a write past the end of a Go slice. ccd_windows_test.go pins every size and the
// offsets that matter, and the opt-in integration test proves them against the API
// that actually does the writing.

type luid struct {
	LowPart  uint32
	HighPart int32
}

type displayConfigRational struct {
	Numerator   uint32
	Denominator uint32
}

type displayConfigPathSourceInfo struct {
	AdapterID   luid
	ID          uint32
	ModeInfoIdx uint32
	StatusFlags uint32
}

type displayConfigPathTargetInfo struct {
	AdapterID        luid
	ID               uint32
	ModeInfoIdx      uint32
	OutputTechnology uint32
	Rotation         uint32
	Scaling          uint32
	RefreshRate      displayConfigRational
	ScanLineOrdering uint32
	TargetAvailable  int32
	StatusFlags      uint32
}

type displayConfigPathInfo struct {
	SourceInfo displayConfigPathSourceInfo
	TargetInfo displayConfigPathTargetInfo
	Flags      uint32
}

// displayConfigModeInfo is declared for its size alone. QueryDisplayConfig insists on
// somewhere to put the mode array even when the caller only wants the paths, and
// nothing here reads a mode: the modes this tool cares about come from
// EnumDisplaySettingsW. The union is therefore an opaque block of the size the SDK
// gives it rather than three variant structs that would never be read.
type displayConfigModeInfo struct {
	InfoType  uint32
	ID        uint32
	AdapterID luid
	Data      [48]byte
}

type displayConfigDeviceInfoHeader struct {
	Type      uint32
	Size      uint32
	AdapterID luid
	ID        uint32
}

type displayConfigTargetDeviceName struct {
	Header                    displayConfigDeviceInfoHeader
	Flags                     uint32
	OutputTechnology          uint32
	EDIDManufactureID         uint16
	EDIDProductCodeID         uint16
	ConnectorInstance         uint32
	MonitorFriendlyDeviceName [64]uint16
	MonitorDevicePath         [128]uint16
}

// friendlyNamer is the seam the enumeration holds. It exists so the fake desktop can
// supply names, and so a machine where the API is unavailable or unwilling is an
// ordinary test case rather than a branch nothing ever runs.
type friendlyNamer interface {
	// friendlyNames reports each active monitor's model name, keyed by its device
	// interface path lower-cased. The key is the same string EnumDisplayDevicesW
	// reports in DISPLAY_DEVICEW.DeviceID under EDD_GET_DEVICE_INTERFACE_NAME, which
	// is what lets a name found through one API land on the monitor found through
	// the other. The two APIs do not agree on casing, hence the lower-casing on both
	// sides.
	friendlyNames() (map[string]string, error)
}

type ccdAPI interface {
	available() error
	getDisplayConfigBufferSizes(flags uint32, pathCount, modeCount *uint32) (int32, error)
	queryDisplayConfig(
		flags uint32,
		pathCount *uint32,
		paths []displayConfigPathInfo,
		modeCount *uint32,
		modes []displayConfigModeInfo,
	) (int32, error)
	displayConfigGetDeviceInfo(request *displayConfigTargetDeviceName) (int32, error)
}

type ccdNamer struct {
	api ccdAPI
}

func newCCDNamer() *ccdNamer {
	return &ccdNamer{api: user32CCD{}}
}

func (n *ccdNamer) friendlyNames() (map[string]string, error) {
	if err := n.api.available(); err != nil {
		return nil, fmt.Errorf("display configuration API unavailable: %w", err)
	}
	paths, err := n.activePaths()
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(paths))
	for _, path := range paths {
		request := displayConfigTargetDeviceName{
			Header: displayConfigDeviceInfoHeader{
				Type:      displayConfigDeviceInfoGetTargetName,
				Size:      uint32(unsafe.Sizeof(displayConfigTargetDeviceName{})),
				AdapterID: path.TargetInfo.AdapterID,
				ID:        path.TargetInfo.ID,
			},
		}
		// One target that will not answer costs that one monitor its name and
		// nothing more. The whole lookup is an enrichment, so every failure inside
		// it is partial at worst.
		if status, _ := n.api.displayConfigGetDeviceInfo(&request); status != errorSuccess {
			continue
		}
		devicePath := windows.UTF16ToString(request.MonitorDevicePath[:])
		name := strings.TrimSpace(windows.UTF16ToString(request.MonitorFriendlyDeviceName[:]))
		if devicePath == "" || name == "" {
			// A name with no path cannot be joined to a monitor, and a path with no
			// name has nothing to contribute. Neither is an error; neither is stored.
			continue
		}
		names[strings.ToLower(devicePath)] = name
	}
	return names, nil
}

// activePaths reads the paths that are driving a monitor right now. The size and the
// query are two separate crossings, so the answer to "the desktop changed under you"
// is to size again rather than to trust the count from the previous attempt.
func (n *ccdNamer) activePaths() ([]displayConfigPathInfo, error) {
	var lastStatus int32
	for attempt := 0; attempt < ccdQueryAttempts; attempt++ {
		var pathCount, modeCount uint32
		status, callErr := n.api.getDisplayConfigBufferSizes(qdcOnlyActivePaths, &pathCount, &modeCount)
		if status != errorSuccess {
			return nil, ccdError("GetDisplayConfigBufferSizes", status, callErr)
		}
		if pathCount == 0 {
			return nil, nil
		}
		paths := make([]displayConfigPathInfo, pathCount)
		modes := make([]displayConfigModeInfo, modeCount)
		status, callErr = n.api.queryDisplayConfig(
			qdcOnlyActivePaths, &pathCount, paths, &modeCount, modes)
		switch {
		case status == errorSuccess:
			if int(pathCount) > len(paths) {
				// The driver reported more paths than it was given room for without
				// saying so. Trusting the count would read past the slice.
				return nil, fmt.Errorf(
					"QueryDisplayConfig reported %d paths into a buffer of %d", pathCount, len(paths))
			}
			return paths[:pathCount], nil
		case status == errorInsufficientBuffer:
			lastStatus = status
		default:
			return nil, ccdError("QueryDisplayConfig", status, callErr)
		}
	}
	return nil, ccdError("QueryDisplayConfig", lastStatus, nil)
}

// chooseLabel is the name ladder, and it is the only place a monitor's display name
// is decided.
//
//  1. the CCD model name, which is what the user reads on the bezel;
//  2. the monitor's own DeviceString, when the driver bothered to fill it in with
//     something other than the placeholder Windows invents;
//  3. the hardware ID, which at least names the model.
//
// The last rung is not "show nothing". A row with no text tells the user there is a
// monitor but not which one; the placeholder at least says a monitor is there, so it
// is kept when there is no hardware ID to fall back to either. Nothing here is ever
// an error: a label is display-only and never takes part in matching, so failing to
// find a prettier one is not a failure the user needs to hear about.
func chooseLabel(friendly, deviceString, hardwareID string) string {
	if name := strings.TrimSpace(friendly); usableName(name) {
		return name
	}
	if name := strings.TrimSpace(deviceString); usableName(name) {
		return name
	}
	if id := strings.TrimSpace(hardwareID); id != "" {
		return id
	}
	return strings.TrimSpace(deviceString)
}

// genericMonitorNames are the strings Windows supplies when no monitor driver
// claimed the screen. They are not names; they are the absence of one repeated once
// per monitor, which is exactly what makes a four-row list unusable.
//
// The list is deliberately conservative and matched whole, never by substring: a
// spelling that is not here costs the user a prettier label on that row and nothing
// else, whereas a loose match could throw away a real product name that happens to
// contain one of these words.
var genericMonitorNames = map[string]struct{}{
	"generic pnp monitor":       {},
	"generic non-pnp monitor":   {},
	"default monitor":           {},
	"non-plug and play monitor": {},
	"一般隨插即用監視器":                 {},
	"泛用隨插即用監視器":                 {},
	"預設監視器":                     {},
}

func usableName(name string) bool {
	if name == "" {
		return false
	}
	_, generic := genericMonitorNames[strings.ToLower(name)]
	return !generic
}

// lookupFriendlyName joins a monitor found through EnumDisplayDevicesW to a name
// found through the CCD API. A monitor whose interface path could not be read has no
// key to join on and simply gets no name, which is the same quiet fallback as a
// lookup that fails.
func lookupFriendlyName(names map[string]string, instancePath string) string {
	if len(names) == 0 || instancePath == "" {
		return ""
	}
	return names[strings.ToLower(instancePath)]
}

func ccdError(operation string, status int32, callErr error) error {
	if status == errorSuccess && callErr != nil {
		return fmt.Errorf("%s: %w", operation, callErr)
	}
	return fmt.Errorf("%s: %w", operation, syscall.Errno(uint32(status)))
}

// user32CCD is the real binding. The three procs are resolved lazily out of the same
// user32.dll this package already loads for EnumDisplayDevicesW.
type user32CCD struct{}

var (
	getDisplayConfigBufferSizesW = user32DLL.NewProc("GetDisplayConfigBufferSizes")
	queryDisplayConfigW          = user32DLL.NewProc("QueryDisplayConfig")
	displayConfigGetDeviceInfoW  = user32DLL.NewProc("DisplayConfigGetDeviceInfo")
)

// available asks whether user32 exports the three entry points before any of them is
// called. Calling a proc a DLL does not export panics inside the syscall package, and
// a panic is not a quiet fallback.
func (user32CCD) available() error {
	for _, proc := range []*windows.LazyProc{
		getDisplayConfigBufferSizesW, queryDisplayConfigW, displayConfigGetDeviceInfoW,
	} {
		if err := proc.Find(); err != nil {
			return err
		}
	}
	return nil
}

func (user32CCD) getDisplayConfigBufferSizes(flags uint32, pathCount, modeCount *uint32) (int32, error) {
	r1, _, callErr := getDisplayConfigBufferSizesW.Call(
		uintptr(flags),
		uintptr(unsafe.Pointer(pathCount)),
		uintptr(unsafe.Pointer(modeCount)),
	)
	return int32(r1), callErr
}

// queryDisplayConfig takes the arrays as slices so the nil-pointer case belongs to
// this function rather than to every caller: a desktop can legitimately report zero
// modes, and &modes[0] on an empty slice panics.
//
// currentTopologyId is passed as NULL, which the API requires for any flags other
// than QDC_DATABASE_CURRENT -- the one flag that reads the stored configuration
// database instead of the live desktop.
func (user32CCD) queryDisplayConfig(
	flags uint32,
	pathCount *uint32,
	paths []displayConfigPathInfo,
	modeCount *uint32,
	modes []displayConfigModeInfo,
) (int32, error) {
	var pathPtr, modePtr unsafe.Pointer
	if len(paths) > 0 {
		pathPtr = unsafe.Pointer(&paths[0])
	}
	if len(modes) > 0 {
		modePtr = unsafe.Pointer(&modes[0])
	}
	r1, _, callErr := queryDisplayConfigW.Call(
		uintptr(flags),
		uintptr(unsafe.Pointer(pathCount)),
		uintptr(pathPtr),
		uintptr(unsafe.Pointer(modeCount)),
		uintptr(modePtr),
		0,
	)
	runtime.KeepAlive(paths)
	runtime.KeepAlive(modes)
	return int32(r1), callErr
}

func (user32CCD) displayConfigGetDeviceInfo(request *displayConfigTargetDeviceName) (int32, error) {
	r1, _, callErr := displayConfigGetDeviceInfoW.Call(uintptr(unsafe.Pointer(request)))
	runtime.KeepAlive(request)
	return int32(r1), callErr
}
