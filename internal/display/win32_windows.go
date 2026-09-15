package display

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/Alien7666/change_resolution/internal/domain"
	"golang.org/x/sys/windows"
)

const (
	enumCurrentSettings uint32 = 0xffffffff

	displayDeviceAttachedToDesktop uint32 = 0x00000001
	displayDevicePrimaryDevice     uint32 = 0x00000004

	// eddGetDeviceInterfaceName (EDD_GET_DEVICE_INTERFACE_NAME) makes
	// EnumDisplayDevicesW report a monitor device interface path in
	// DISPLAY_DEVICEW.DeviceID. It replaces that member content rather than adding
	// a member, so the hardware ID and the interface path cannot come from one call
	// and each monitor is enumerated twice.
	eddGetDeviceInterfaceName uint32 = 0x00000001

	// The four dmFields bits a mode is made of live in modes.go, beside the filter
	// that reads them. dmPosition stays here because it belongs to the arrangement,
	// not to a mode: it is the one member every non-target display declares.
	dmPosition uint32 = 0x00000020

	// CDS_UPDATEREGISTRY (0x00000001) is deliberately absent from this file: the mode
	// change must stay a run-time one that dies with the process. CDS_NORESET
	// (0x10000000) is absent too; applyLayout records the measurement that rules it
	// out.
	cdsTest       uint32 = 0x00000002
	cdsFullscreen uint32 = 0x00000004

	// maxEnumeratedModes bounds the indexed EnumDisplaySettingsW walk. The walk's only
	// terminator is the driver answering FALSE past the end of its list, so a driver
	// that never does would spin inside a call the user made from the settings dialog:
	// a hang with no message and no way out. Real lists run to a few hundred entries,
	// so this is a guard against a broken driver and never a filter on a working one.
	maxEnumeratedModes uint32 = 4096

	dispChangeSuccessful  int32 = 0
	dispChangeRestart     int32 = 1
	dispChangeFailed      int32 = -1
	dispChangeBadMode     int32 = -2
	dispChangeNotUpdated  int32 = -3
	dispChangeBadFlags    int32 = -4
	dispChangeBadParam    int32 = -5
	dispChangeBadDualView int32 = -6
)

type displayDevice struct {
	Cb           uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

type pointL struct {
	X int32
	Y int32
}

type devMode struct {
	DmDeviceName                               [32]uint16
	DmSpecVersion, DmDriverVersion             uint16
	DmSize, DmDriverExtra                      uint16
	DmFields                                   uint32
	DmPosition                                 pointL
	DmDisplayOrientation, DmDisplayFixedOutput uint32
	DmColor, DmDuplex, DmYResolution           int16
	DmTTOption, DmCollate                      int16
	DmFormName                                 [32]uint16
	DmLogPixels                                uint16
	DmBitsPerPel, DmPelsWidth, DmPelsHeight    uint32
	DmDisplayFlags, DmDisplayFrequency         uint32
	DmICMMethod, DmICMIntent, DmMediaType      uint32
	DmDitherType, DmReserved1, DmReserved2     uint32
	DmPanningWidth, DmPanningHeight            uint32
}

type win32API interface {
	enumDisplayDevices(deviceName *uint16, index uint32, device *displayDevice, flags uint32) (bool, error)
	enumDisplaySettings(deviceName *uint16, modeNumber uint32, mode *devMode) (bool, error)
	changeDisplaySettingsEx(deviceName *uint16, mode *devMode, flags uint32) int32
}

type windowsNative struct {
	api win32API
	// namer supplies the model names the CCD API reports. It is a seam rather than a
	// direct call so that a machine where the API is unavailable, and a test that
	// wants no names at all, are ordinary cases instead of untested branches. A nil
	// namer means exactly that: no friendly names, and the ladder starts one rung
	// lower.
	namer friendlyNamer
}

type user32API struct{}

var (
	user32DLL                = windows.NewLazySystemDLL("user32.dll")
	enumDisplayDevicesW      = user32DLL.NewProc("EnumDisplayDevicesW")
	enumDisplaySettingsW     = user32DLL.NewProc("EnumDisplaySettingsW")
	changeDisplaySettingsExW = user32DLL.NewProc("ChangeDisplaySettingsExW")
)

func NewWindowsController() Controller {
	return newController(&windowsNative{api: user32API{}, namer: newCCDNamer()})
}

// listTargets reports one target per attached monitor, carrying the identity the
// profile is matched against. Each monitor index is enumerated twice, and that is
// forced by the API rather than chosen: EDD_GET_DEVICE_INTERFACE_NAME does not add
// a member to DISPLAY_DEVICEW, it replaces the content of DeviceID. Without the flag
// that member holds the hardware ID (MONITOR\XMI27B2\0009, a model); with it, the
// device interface path (\\?\DISPLAY#XMI27B2#...#UID4357#{...}, a unit on a particular
// output port). Both rungs of the identity ladder are needed, so both reads happen.
//
// A monitor is reported even when the second read is not answered. The interface
// path is an enrichment: a monitor without one is still selectable by hardware ID,
// and dropping it would take away the rung that does work. The same is true of the
// model name read through the CCD API, which is display-only and never matched on.
func (n *windowsNative) listTargets() ([]domain.Target, error) {
	names := n.friendlyNames()
	var targets []domain.Target
	err := n.eachAttachedAdapter(func(adapter displayDevice, adapterName string) error {
		for monitorIndex := uint32(0); ; monitorIndex++ {
			monitor := displayDevice{Cb: uint32(unsafe.Sizeof(displayDevice{}))}
			ok, _ := n.api.enumDisplayDevices(&adapter.DeviceName[0], monitorIndex, &monitor, 0)
			if !ok {
				break
			}
			identity := domain.MonitorIdentity{
				HardwareID: windows.UTF16ToString(monitor.DeviceID[:]),
			}
			withInterface := displayDevice{Cb: uint32(unsafe.Sizeof(displayDevice{}))}
			if ok, _ := n.api.enumDisplayDevices(
				&adapter.DeviceName[0], monitorIndex, &withInterface, eddGetDeviceInterfaceName,
			); ok {
				identity.InstancePath = windows.UTF16ToString(withInterface.DeviceID[:])
			}
			// The label is the one part of an identity that is never compared, and it
			// is the only part the user reads. The interface path is what joins the
			// CCD name to this monitor; DeviceString is what the driver called it;
			// the hardware ID is the floor.
			identity.Label = chooseLabel(
				lookupFriendlyName(names, identity.InstancePath),
				windows.UTF16ToString(monitor.DeviceString[:]),
				identity.HardwareID,
			)
			targets = append(targets, domain.Target{DeviceName: adapterName, Identity: identity})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return targets, nil
}

// friendlyNames reads every monitor's model name once for the whole enumeration.
// QueryDisplayConfig walks the entire desktop, so asking per monitor would repeat
// the work and, worse, could describe several different desktops in one list.
//
// Every failure is swallowed here on purpose, and this is the one place in the
// package where that is right. A label is display-only: it never takes part in
// matching and never reaches ChangeDisplaySettingsExW, so failing to find a prettier
// one is not something the user can act on. Turning it into an error would replace a
// complete, correct monitor list with a refusal.
func (n *windowsNative) friendlyNames() map[string]string {
	if n.namer == nil {
		return nil
	}
	names, err := n.namer.friendlyNames()
	if err != nil {
		return nil
	}
	return names
}

// currentLayout reads every attached display. A display whose settings cannot be
// read fails the whole read: planning a contiguous desktop from a layout that is
// missing one of its displays would move the others on top of it.
func (n *windowsNative) currentLayout() (domain.Layout, error) {
	var displays []domain.DisplayState
	err := n.eachAttachedAdapter(func(adapter displayDevice, adapterName string) error {
		deviceNamePtr, err := windows.UTF16PtrFromString(adapterName)
		if err != nil {
			return fmt.Errorf("device name: %w", err)
		}
		dm, err := n.loadCurrentMode(deviceNamePtr, adapterName)
		if err != nil {
			return err
		}
		displays = append(displays, domain.DisplayState{
			DeviceName: adapterName,
			Mode:       modeOf(dm),
			Position:   domain.Point{X: dm.DmPosition.X, Y: dm.DmPosition.Y},
			Primary:    adapter.StateFlags&displayDevicePrimaryDevice != 0,
		})
		return nil
	})
	if err != nil {
		return domain.Layout{}, err
	}
	return domain.Layout{Displays: displays}, nil
}

func (n *windowsNative) eachAttachedAdapter(visit func(adapter displayDevice, name string) error) error {
	for adapterIndex := uint32(0); ; adapterIndex++ {
		adapter := displayDevice{Cb: uint32(unsafe.Sizeof(displayDevice{}))}
		ok, _ := n.api.enumDisplayDevices(nil, adapterIndex, &adapter, 0)
		if !ok {
			return nil
		}
		if adapter.StateFlags&displayDeviceAttachedToDesktop == 0 {
			continue
		}
		if err := visit(adapter, windows.UTF16ToString(adapter.DeviceName[:])); err != nil {
			return err
		}
	}
}

func (n *windowsNative) currentMode(deviceName string) (domain.Mode, error) {
	deviceNamePtr, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return domain.Mode{}, fmt.Errorf("device name: %w", err)
	}
	dm, err := n.loadCurrentMode(deviceNamePtr, deviceName)
	if err != nil {
		return domain.Mode{}, err
	}
	return modeOf(dm), nil
}

// enumModes reads everything the target monitor reports it can do.
//
// EnumDisplaySettingsW is walked by index from 0 until it answers FALSE, and every
// call gets a freshly zeroed DEVMODEW with dmSize re-asserted. The zeroing is not
// tidiness: dmFields and dmDisplayFlags are what the filter judges an entry on, and a
// reused buffer would carry the previous answer's bits into the next verdict. dmSize
// is re-asserted for the same reason loadCurrentMode re-asserts it -- GDI is not
// promised to leave the caller's buffer size intact.
//
// The separate ENUM_CURRENT_SETTINGS read is what guarantees the mode on the screen
// is in the list even when the driver does not enumerate it. Failing that read fails
// the whole call: the device name came from a monitor list read moments ago, so a
// device that cannot be read now is a device that has gone, and a catalogue for a
// monitor that is no longer there is a list of choices the user cannot make.
//
// Nothing here writes. This is the call the first-run wizard makes before the user
// has chosen anything, and it must stay safe to make at any moment.
func (n *windowsNative) enumModes(deviceName string) ([]domain.Mode, error) {
	devicePtr, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return nil, fmt.Errorf("device name: %w", err)
	}
	current, err := n.loadCurrentMode(devicePtr, deviceName)
	if err != nil {
		return nil, err
	}
	var raw []rawMode
	for index := uint32(0); index < maxEnumeratedModes; index++ {
		dm := devMode{DmSize: uint16(unsafe.Sizeof(devMode{}))}
		ok, _ := n.api.enumDisplaySettings(devicePtr, index, &dm)
		if !ok {
			break
		}
		raw = append(raw, rawMode{
			Mode:         modeOf(dm),
			Fields:       dm.DmFields,
			DisplayFlags: dm.DmDisplayFlags,
		})
	}
	return catalogue(raw, modeOf(current)), nil
}

// testMode is the CDS_TEST pre-flight on the target alone. It declares only the
// four mode fields, so the driver judges the mode and nothing else.
func (n *windowsNative) testMode(deviceName string, mode domain.Mode) error {
	deviceNamePtr, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return fmt.Errorf("device name: %w", err)
	}
	dm, err := n.loadCurrentMode(deviceNamePtr, deviceName)
	if err != nil {
		return err
	}
	dm.DmFields = 0
	dm = withMode(dm, mode)
	return n.change(deviceNamePtr, deviceName, &dm, cdsTest)
}

// stagedChange is one display's part of an apply: the DEVMODEW that carries the
// change, and the same DEVMODEW carrying the values the driver reported before it,
// which is what putting that display back means.
type stagedChange struct {
	deviceName string
	devicePtr  *uint16
	next       devMode
	previous   devMode
	setsMode   bool
	growsMode  bool
}

// applyLayout applies the planned arrangement one display at a time, each with its
// own ChangeDisplaySettingsExW(device, &dm, CDS_FULLSCREEN) call.
//
// CDS_NORESET is not used, and this is not an oversight waiting to be "fixed" back
// into a staged transaction. MSDN documents CDS_NORESET as meaningful only
// alongside CDS_UPDATEREGISTRY, which this tool must never send because it would
// make a temporary game mode the user's permanent setting; and the staging call is
// in fact rejected outright by the driver this tool runs against. Measured on the
// target machine (RTX 3070, four displays, DISPLAY1 the primary at 2560x1440@180),
// each call re-reading that display's current settings and writing the identical
// values back, varying only the flag word:
//
//	CDS_FULLSCREEN|CDS_NORESET  ->  DISP_CHANGE_BADFLAGS
//	CDS_NORESET                 ->  DISP_CHANGE_BADFLAGS
//	CDS_FULLSCREEN              ->  DISP_CHANGE_SUCCESSFUL
//	flags = 0                   ->  DISP_CHANGE_SUCCESSFUL
//
// A sequential apply has no atomicity of its own, so the three things the staged
// transaction was meant to buy are bought here instead: orderForApply keeps the
// intermediate desktops free of overlaps in the axis that moves, rollBack puts back
// whatever was already changed when a later call fails, and verifyApplied holds the
// result to the plan. See orderForApply for the one shape it does not cover.
//
// Only the target carries mode fields. Every other display declares DM_POSITION and
// nothing else, which is what keeps its resolution, refresh rate and colour depth
// out of the write set entirely.
func (n *windowsNative) applyLayout(plan domain.LayoutPlan) error {
	if len(plan.Changes) == 0 {
		return errors.New("ChangeDisplaySettingsExW: refusing to apply an empty layout plan")
	}
	staged, err := n.stageChanges(plan)
	if err != nil {
		return err
	}
	var applied []stagedChange
	for _, step := range orderForApply(staged) {
		if err := n.change(step.devicePtr, step.deviceName, &step.next, cdsFullscreen); err != nil {
			return n.rollBack(applied, err)
		}
		applied = append(applied, step)
	}
	return n.verifyApplied(plan)
}

// stageChanges reads every display's DEVMODEW once, before anything is written, and
// derives both the write and its undo from it. Reading up front is what makes the
// undo the state the desktop had when this apply started, even for a display an
// earlier call in the same apply has already nudged.
func (n *windowsNative) stageChanges(plan domain.LayoutPlan) ([]stagedChange, error) {
	staged := make([]stagedChange, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		devicePtr, err := windows.UTF16PtrFromString(change.DeviceName)
		if err != nil {
			return nil, fmt.Errorf("device name: %w", err)
		}
		current, err := n.loadCurrentMode(devicePtr, change.DeviceName)
		if err != nil {
			return nil, err
		}
		next := current
		next.DmPosition = pointL{X: change.Position.X, Y: change.Position.Y}
		next.DmFields = dmPosition
		if change.SetMode {
			next = withMode(next, change.Mode)
		}
		// previous declares exactly the members next declares, so undoing a change
		// writes the same field set back and never widens it.
		previous := current
		previous.DmFields = next.DmFields
		staged = append(staged, stagedChange{
			deviceName: change.DeviceName,
			devicePtr:  devicePtr,
			next:       next,
			previous:   previous,
			setsMode:   change.SetMode,
			growsMode: change.SetMode &&
				(change.Mode.Width > current.DmPelsWidth || change.Mode.Height > current.DmPelsHeight),
		})
	}
	return staged, nil
}

// orderForApply decides which display crosses into Win32 first. A transient gap
// between displays is harmless; a transient overlap is not, because Windows repacks
// a desktop it considers invalid and the tool then no longer knows where anything
// sits.
//
// The order is decided once per apply rather than once per display, which leaves one
// shape uncovered: when the target grows in one axis and shrinks in the other, a
// neighbour in the shrinking axis moves inward before the target has given that space
// up, and overlaps it until the target's own call lands. Deciding per display would
// close it and is deliberately not done here. The consequence is bounded - a driver
// that repacks instead of accepting the step is caught by verifyApplied and rolled
// back - and the behaviour is pinned by
// TestWindowsNativeApplyLayoutOrdersOncePerApplyNotOncePerDisplay.
//
// A target that does not grow in either axis gives up its space the moment its mode
// lands, so it goes first and its neighbours close the gap behind it. A target that
// grows in either axis needs that space before it can occupy it, so the neighbours
// move away first and the target's mode goes last. The decision is read off the
// staged sizes -- the planned mode against the one the driver reported -- never off
// a particular resolution.
func orderForApply(staged []stagedChange) []stagedChange {
	target := -1
	for i, step := range staged {
		if step.setsMode {
			target = i
			break
		}
	}
	if target < 0 {
		return staged
	}
	ordered := make([]stagedChange, 0, len(staged))
	if !staged[target].growsMode {
		ordered = append(ordered, staged[target])
	}
	for i, step := range staged {
		if i != target {
			ordered = append(ordered, step)
		}
	}
	if staged[target].growsMode {
		ordered = append(ordered, staged[target])
	}
	return ordered
}

// rollBack puts every display this apply already changed back to what the driver
// reported before it started, most recent first, and returns the failure that caused
// it.
//
// A rollback call that fails itself is reported as exactly that: saying only that
// the apply failed would tell the user their desktop is untouched when it is not.
// The remaining displays are still attempted after such a failure, so as few as
// possible are left somewhere the user did not put them.
func (n *windowsNative) rollBack(applied []stagedChange, cause error) error {
	var failure error
	for i := len(applied) - 1; i >= 0; i-- {
		previous := applied[i].previous
		if err := n.change(applied[i].devicePtr, applied[i].deviceName, &previous, cdsFullscreen); err != nil {
			if failure == nil {
				failure = err
			}
		}
	}
	if failure != nil {
		return fmt.Errorf("%w: %w; rolling the desktop back also failed: %w",
			ErrLayoutPartlyApplied, cause, failure)
	}
	return cause
}

// verifyApplied re-reads the desktop and holds it to the plan. Every call having
// returned DISP_CHANGE_SUCCESSFUL is not proof that the desktop is the arrangement
// that was planned: Windows may repack displays on its own during a mode change.
//
// The rule chosen here is that any difference from the plan is an error. The
// tempting alternative -- accept a desktop that is merely still contiguous -- is not
// something this code can honestly check: the planner proves that displays do not
// overlap, which is not the same as proving the mouse can reach all of them, and a
// monitor parked out of reach is the exact failure this check exists for. Reporting
// a harmless repack costs the user one message; calling an unreachable monitor a
// success costs them the monitor.
//
// Nothing is rolled back here. Every call succeeded, so there is no failed call to
// undo, and a second unvalidated mode change on a desktop that already moved under
// the tool would be guesswork; the recovery that fits is a restore, which re-plans
// against the desktop as it actually is.
func (n *windowsNative) verifyApplied(plan domain.LayoutPlan) error {
	observed, err := n.currentLayout()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrLayoutNotVerified, err)
	}
	for _, change := range plan.Changes {
		state, ok := observed.Find(change.DeviceName)
		if !ok {
			return fmt.Errorf("%w: %s is no longer attached", ErrLayoutNotVerified, change.DeviceName)
		}
		if state.Position != change.Position {
			return fmt.Errorf("%w: %s sits at (%d,%d), the plan put it at (%d,%d)",
				ErrLayoutNotVerified, change.DeviceName,
				state.Position.X, state.Position.Y, change.Position.X, change.Position.Y)
		}
		if change.SetMode && state.Mode != change.Mode {
			return fmt.Errorf("%w: %s runs %s, the plan asked for %s",
				ErrLayoutNotVerified, change.DeviceName, describeMode(state.Mode), describeMode(change.Mode))
		}
	}
	return nil
}

func (n *windowsNative) change(deviceNamePtr *uint16, deviceName string, dm *devMode, flags uint32) error {
	if dm != nil {
		// Re-assert dmSize from the struct own size rather than trust that GDI left
		// the caller-supplied buffer size intact across EnumDisplaySettingsW.
		dm.DmSize = uint16(unsafe.Sizeof(*dm))
	}
	if result := n.api.changeDisplaySettingsEx(deviceNamePtr, dm, flags); result != dispChangeSuccessful {
		return fmt.Errorf("ChangeDisplaySettingsExW(%s): %s", deviceName, describeResult(result))
	}
	return nil
}

func (n *windowsNative) loadCurrentMode(deviceNamePtr *uint16, deviceName string) (devMode, error) {
	dm := devMode{DmSize: uint16(unsafe.Sizeof(devMode{}))}
	ok, callErr := n.api.enumDisplaySettings(deviceNamePtr, enumCurrentSettings, &dm)
	if !ok {
		operation := fmt.Sprintf("EnumDisplaySettingsW(%q)", deviceName)
		return devMode{}, win32CallError(operation, callErr)
	}
	return dm, nil
}

// withMode writes the four mode members and adds them to whatever the caller has
// already declared in dmFields, so a position-and-mode change stays one write.
func withMode(dm devMode, mode domain.Mode) devMode {
	dm.DmPelsWidth = mode.Width
	dm.DmPelsHeight = mode.Height
	dm.DmDisplayFrequency = mode.RefreshHz
	dm.DmBitsPerPel = mode.BitsPerPixel
	dm.DmFields |= dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel
	return dm
}

func modeOf(dm devMode) domain.Mode {
	return domain.Mode{
		Width:        dm.DmPelsWidth,
		Height:       dm.DmPelsHeight,
		RefreshHz:    dm.DmDisplayFrequency,
		BitsPerPixel: dm.DmBitsPerPel,
	}
}

func (user32API) enumDisplayDevices(
	deviceName *uint16,
	index uint32,
	device *displayDevice,
	flags uint32,
) (bool, error) {
	r1, _, callErr := enumDisplayDevicesW.Call(
		uintptr(unsafe.Pointer(deviceName)),
		uintptr(index),
		uintptr(unsafe.Pointer(device)),
		uintptr(flags),
	)
	return r1 != 0, callErr
}

func (user32API) enumDisplaySettings(
	deviceName *uint16,
	modeNumber uint32,
	mode *devMode,
) (bool, error) {
	r1, _, callErr := enumDisplaySettingsW.Call(
		uintptr(unsafe.Pointer(deviceName)),
		uintptr(modeNumber),
		uintptr(unsafe.Pointer(mode)),
	)
	return r1 != 0, callErr
}

func (user32API) changeDisplaySettingsEx(deviceName *uint16, mode *devMode, flags uint32) int32 {
	r1, _, _ := changeDisplaySettingsExW.Call(
		uintptr(unsafe.Pointer(deviceName)),
		uintptr(unsafe.Pointer(mode)),
		0,
		uintptr(flags),
		0,
	)
	return int32(r1)
}

func win32CallError(operation string, callErr error) error {
	if callErr == nil || errors.Is(callErr, windows.ERROR_SUCCESS) {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s: %w", operation, callErr)
}

func describeMode(mode domain.Mode) string {
	return fmt.Sprintf("%dx%d @ %d Hz %d bpp", mode.Width, mode.Height, mode.RefreshHz, mode.BitsPerPixel)
}

func describeResult(result int32) string {
	switch result {
	case dispChangeSuccessful:
		return "successful"
	case dispChangeRestart:
		return "restart required"
	case dispChangeFailed:
		return "driver failed the display mode change"
	case dispChangeBadMode:
		return "display mode is not supported"
	case dispChangeNotUpdated:
		return "registry settings were not updated"
	case dispChangeBadFlags:
		return "invalid flags"
	case dispChangeBadParam:
		return "invalid parameter"
	case dispChangeBadDualView:
		return "dual-view mode is not supported"
	default:
		return fmt.Sprintf("unknown result %d", result)
	}
}
