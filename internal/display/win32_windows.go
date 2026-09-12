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

	dmBitsPerPel       uint32 = 0x00040000
	dmPelsWidth        uint32 = 0x00080000
	dmPelsHeight       uint32 = 0x00100000
	dmDisplayFrequency uint32 = 0x00400000

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
	for adapterIndex := uint32(0); ; adapterIndex++ {
		adapter := displayDevice{Cb: uint32(unsafe.Sizeof(displayDevice{}))}
		ok, _ := n.api.enumDisplayDevices(nil, adapterIndex, &adapter, 0)
		if !ok {
			break
		}
		if adapter.StateFlags&displayDeviceAttachedToDesktop == 0 {
			continue
		}

		adapterName := windows.UTF16ToString(adapter.DeviceName[:])
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
	}
	return targets, nil
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
	return domain.Mode{
		Width:        dm.DmPelsWidth,
		Height:       dm.DmPelsHeight,
		RefreshHz:    dm.DmDisplayFrequency,
		BitsPerPixel: dm.DmBitsPerPel,
	}, nil
}

func (n *windowsNative) changeMode(deviceName string, mode domain.Mode, test bool) error {
	deviceNamePtr, err := windows.UTF16PtrFromString(deviceName)
	if err != nil {
		return fmt.Errorf("device name: %w", err)
	}
	dm, err := n.loadCurrentMode(deviceNamePtr, deviceName)
	if err != nil {
		return err
	}

	dm.DmPelsWidth = mode.Width
	dm.DmPelsHeight = mode.Height
	dm.DmDisplayFrequency = mode.RefreshHz
	dm.DmBitsPerPel = mode.BitsPerPixel
	dm.DmFields = dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel

	flags := cdsFullscreen
	if test {
		flags = cdsTest
	}
	// Re-assert dmSize from the struct's own size rather than trust that GDI
	// left EnumDisplaySettingsW's caller-supplied buffer size intact.
	dm.DmSize = uint16(unsafe.Sizeof(dm))
	result := n.api.changeDisplaySettingsEx(deviceNamePtr, &dm, flags)
	if result != dispChangeSuccessful {
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
