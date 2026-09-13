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

	dmPosition         uint32 = 0x00000020
	dmBitsPerPel       uint32 = 0x00040000
	dmPelsWidth        uint32 = 0x00080000
	dmPelsHeight       uint32 = 0x00100000
	dmDisplayFrequency uint32 = 0x00400000

	// CDS_UPDATEREGISTRY (0x00000001) is deliberately absent from this file: the mode
	// change must stay a run-time one that dies with the process. CDS_NORESET
	// (0x10000000) is absent too; applyLayout records the measurement that rules it
	// out.
	cdsTest       uint32 = 0x00000002
	cdsFullscreen uint32 = 0x00000004

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
}

type user32API struct{}

var (
	user32DLL                = windows.NewLazySystemDLL("user32.dll")
	enumDisplayDevicesW      = user32DLL.NewProc("EnumDisplayDevicesW")
	enumDisplaySettingsW     = user32DLL.NewProc("EnumDisplaySettingsW")
	changeDisplaySettingsExW = user32DLL.NewProc("ChangeDisplaySettingsExW")
)

func NewWindowsController() Controller {
	return newController(&windowsNative{api: user32API{}})
}

func (n *windowsNative) listTargets() ([]domain.Target, error) {
	var targets []domain.Target
	err := n.eachAttachedAdapter(func(adapter displayDevice, adapterName string) error {
		for monitorIndex := uint32(0); ; monitorIndex++ {
			monitor := displayDevice{Cb: uint32(unsafe.Sizeof(displayDevice{}))}
			ok, _ := n.api.enumDisplayDevices(&adapter.DeviceName[0], monitorIndex, &monitor, 0)
			if !ok {
				break
			}
			targets = append(targets, domain.Target{
				DeviceName: adapterName,
				HardwareID: windows.UTF16ToString(monitor.DeviceID[:]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return targets, nil
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
// transaction was meant to buy are bought here instead: orderForApply keeps every
// intermediate desktop free of overlaps, rollBack puts back whatever was already
// changed when a later call fails, and verifyApplied holds the result to the plan.
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
