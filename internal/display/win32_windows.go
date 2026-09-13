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

	// CDS_UPDATEREGISTRY (0x00000001) is deliberately absent from this file: the
	// mode change must stay a run-time one that dies with the process. cdsNoReset
	// stages a display's change without applying it, so the whole arrangement can be
	// committed by one later call instead of the desktop rearranging itself display
	// by display.
	cdsTest       uint32 = 0x00000002
	cdsFullscreen uint32 = 0x00000004
	cdsNoReset    uint32 = 0x10000000

	// commitFlags applies everything staged with cdsNoReset. The committing call
	// passes no device and no DEVMODEW, so it carries no flags of its own.
	commitFlags uint32 = 0

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

// applyLayout stages every display's change with CDS_NORESET and then commits the
// whole arrangement with a single NULL device / NULL DEVMODEW call. Nothing takes
// effect until that commit, so a staging failure aborts with the desktop untouched
// instead of leaving it half rearranged.
//
// Only the target carries mode fields. Every other display declares DM_POSITION and
// nothing else, which is what keeps its resolution, refresh rate and colour depth
// out of the write set entirely.
func (n *windowsNative) applyLayout(plan domain.LayoutPlan) error {
	if len(plan.Changes) == 0 {
		return errors.New("ChangeDisplaySettingsExW: refusing to apply an empty layout plan")
	}
	for _, change := range plan.Changes {
		deviceNamePtr, err := windows.UTF16PtrFromString(change.DeviceName)
		if err != nil {
			return fmt.Errorf("device name: %w", err)
		}
		dm, err := n.loadCurrentMode(deviceNamePtr, change.DeviceName)
		if err != nil {
			return err
		}
		dm.DmPosition = pointL{X: change.Position.X, Y: change.Position.Y}
		dm.DmFields = dmPosition
		if change.SetMode {
			dm = withMode(dm, change.Mode)
		}
		if err := n.change(deviceNamePtr, change.DeviceName, &dm, cdsFullscreen|cdsNoReset); err != nil {
			return err
		}
	}
	return n.change(nil, "", nil, commitFlags)
}

func (n *windowsNative) change(deviceNamePtr *uint16, deviceName string, dm *devMode, flags uint32) error {
	if dm != nil {
		// Re-assert dmSize from the struct own size rather than trust that GDI left
		// the caller-supplied buffer size intact across EnumDisplaySettingsW.
		dm.DmSize = uint16(unsafe.Sizeof(*dm))
	}
	if result := n.api.changeDisplaySettingsEx(deviceNamePtr, dm, flags); result != dispChangeSuccessful {
		return fmt.Errorf("ChangeDisplaySettingsExW(%s): %s", describeDevice(deviceName), describeResult(result))
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

func describeDevice(deviceName string) string {
	if deviceName == "" {
		return "commit"
	}
	return deviceName
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
