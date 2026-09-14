package display

import (
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
	"golang.org/x/sys/windows"
)

// win32Call is one ChangeDisplaySettingsExW crossing.
type win32Call struct {
	device string
	mode   *devMode
	flags  uint32
}

// enumSettingsCall is one EnumDisplaySettingsW crossing, recorded so a test can hold
// every read to the resolved device, ENUM_CURRENT_SETTINGS and the buffer size.
type enumSettingsCall struct {
	device     string
	modeNumber uint32
	size       uint16
}

// monitorKey is how the fake indexes the monitor side of EnumDisplayDevicesW. The
// adapter alone is not enough: EDD_GET_DEVICE_INTERFACE_NAME does not add a member
// to DISPLAY_DEVICEW, it replaces the content of DeviceID, so the same monitor index
// answers with the hardware ID for one key and with the device interface path for
// the other. A single list per adapter could only model one of the two reads.
type monitorKey struct {
	adapter       string
	interfaceName bool
}

type fakeWin32 struct {
	adapters           []displayDevice
	monitors           map[monitorKey][]displayDevice
	current            devMode
	modes              map[string]devMode
	displayDeviceSizes []uint32
	enumSettings       []enumSettingsCall
	calls              []win32Call
	changeResult       int32
	changeResults      map[string]int32

	// resultFor decides a crossing's result by call index, which is how a test makes
	// a display accept its change and then refuse to give it back.
	resultFor func(index int, call win32Call) int32

	// afterChange runs after every applied change and lets a test act as a driver
	// that rearranges the desktop on its own, which is what the post-apply check is
	// there to catch.
	afterChange func(f *fakeWin32, call win32Call)
}

func (f *fakeWin32) enumDisplayDevices(
	deviceName *uint16,
	index uint32,
	device *displayDevice,
	flags uint32,
) (bool, error) {
	f.displayDeviceSizes = append(f.displayDeviceSizes, device.Cb)
	devices := f.adapters
	if deviceName != nil {
		devices = f.monitors[monitorKey{
			adapter:       windows.UTF16PtrToString(deviceName),
			interfaceName: flags&eddGetDeviceInterfaceName != 0,
		}]
	}
	if int(index) >= len(devices) {
		return false, nil
	}
	cb := device.Cb
	*device = devices[index]
	device.Cb = cb
	return true, nil
}

func (f *fakeWin32) enumDisplaySettings(deviceName *uint16, modeNumber uint32, mode *devMode) (bool, error) {
	name := windows.UTF16PtrToString(deviceName)
	f.enumSettings = append(f.enumSettings, enumSettingsCall{
		device: name, modeNumber: modeNumber, size: mode.DmSize,
	})
	if known, ok := f.modes[name]; ok {
		*mode = known
		return true, nil
	}
	*mode = f.current
	return true, nil
}

func (f *fakeWin32) changeDisplaySettingsEx(deviceName *uint16, mode *devMode, flags uint32) int32 {
	name := windows.UTF16PtrToString(deviceName)
	call := win32Call{device: name, flags: flags}
	if mode != nil {
		written := *mode
		call.mode = &written
	}
	index := len(f.calls)
	f.calls = append(f.calls, call)
	result := f.resultOf(index, call)
	if result != dispChangeSuccessful || call.mode == nil || flags&cdsTest != 0 {
		return result
	}
	f.write(name, *call.mode)
	if f.afterChange != nil {
		f.afterChange(f, call)
	}
	return result
}

func (f *fakeWin32) resultOf(index int, call win32Call) int32 {
	if f.resultFor != nil {
		return f.resultFor(index, call)
	}
	if result, ok := f.changeResults[call.device]; ok {
		return result
	}
	return f.changeResult
}

// write takes only the members the caller declared in dmFields, which is the whole
// point of declaring them: a display that is only moved must come out of an apply
// with the mode it went in with.
func (f *fakeWin32) write(name string, written devMode) {
	if f.modes == nil {
		f.modes = make(map[string]devMode)
	}
	state, ok := f.modes[name]
	if !ok {
		state = f.current
	}
	if written.DmFields&dmPosition != 0 {
		state.DmPosition = written.DmPosition
	}
	if written.DmFields&dmPelsWidth != 0 {
		state.DmPelsWidth = written.DmPelsWidth
	}
	if written.DmFields&dmPelsHeight != 0 {
		state.DmPelsHeight = written.DmPelsHeight
	}
	if written.DmFields&dmDisplayFrequency != 0 {
		state.DmDisplayFrequency = written.DmDisplayFrequency
	}
	if written.DmFields&dmBitsPerPel != 0 {
		state.DmBitsPerPel = written.DmBitsPerPel
	}
	f.modes[name] = state
}

func (f *fakeWin32) lastMode(t *testing.T) devMode {
	t.Helper()
	if len(f.calls) == 0 || f.calls[len(f.calls)-1].mode == nil {
		t.Fatalf("no DEVMODEW reached ChangeDisplaySettingsExW: %+v", f.calls)
	}
	return *f.calls[len(f.calls)-1].mode
}

// fakeDesktop is a driver reporting the given arrangement, and it keeps that
// arrangement in step with the writes it accepts, so a test can read the desktop
// back after an apply or a rollback the same way the tool does.
func fakeDesktop(t *testing.T, layout domain.Layout) *fakeWin32 {
	t.Helper()
	adapters := make([]displayDevice, 0, len(layout.Displays))
	modes := make(map[string]devMode, len(layout.Displays))
	for _, display := range layout.Displays {
		state := displayDeviceAttachedToDesktop
		if display.Primary {
			state |= displayDevicePrimaryDevice
		}
		adapters = append(adapters, newDisplayDevice(t, display.DeviceName, "", state))
		modes[display.DeviceName] = devMode{
			DmFields:           0xffffffff,
			DmPosition:         pointL{X: display.Position.X, Y: display.Position.Y},
			DmPelsWidth:        display.Mode.Width,
			DmPelsHeight:       display.Mode.Height,
			DmDisplayFrequency: display.Mode.RefreshHz,
			DmBitsPerPel:       display.Mode.BitsPerPixel,
			DmDisplayFlags:     77,
		}
	}
	return &fakeWin32{adapters: adapters, modes: modes}
}

// measuredDesktop is the real four monitor arrangement this tool runs on.
func measuredDesktop(t *testing.T) *fakeWin32 {
	t.Helper()
	return fakeDesktop(t, measuredLayout())
}

func newDisplayDevice(t *testing.T, name, hardwareID string, stateFlags uint32) displayDevice {
	t.Helper()
	device := displayDevice{StateFlags: stateFlags}
	copyUTF16(t, device.DeviceName[:], name)
	copyUTF16(t, device.DeviceID[:], hardwareID)
	return device
}

// newMonitorDevice is the monitor side of EnumDisplayDevicesW, where DeviceName is
// not the adapter name and DeviceID holds whichever of the two identifiers the
// caller's flags asked for. DeviceString is the monitor's own name, which is what a
// label is derived from.
func newMonitorDevice(t *testing.T, deviceString, deviceID string) displayDevice {
	t.Helper()
	device := displayDevice{}
	copyUTF16(t, device.DeviceString[:], deviceString)
	copyUTF16(t, device.DeviceID[:], deviceID)
	return device
}

func copyUTF16(t *testing.T, dst []uint16, value string) {
	t.Helper()
	encoded, err := windows.UTF16FromString(value)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > len(dst) {
		t.Fatalf("UTF-16 fixture too long: %q", value)
	}
	copy(dst, encoded)
}
