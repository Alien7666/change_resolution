package display

import (
	"os"
	"testing"
	"unsafe"

	"github.com/Alien7666/change_resolution/internal/domain"
	"golang.org/x/sys/windows"
)

type fakeWin32 struct {
	adapters           []displayDevice
	monitors           map[string][]displayDevice
	current            devMode
	displayDeviceSizes []uint32
	enumSettingsCalls  int
	enumSettingsDevice string
	enumSettingsMode   uint32
	enumSettingsSizes  []uint16
	changeDevice       string
	changedMode        devMode
	changeFlags        uint32
	changeResult       int32
}

func (f *fakeWin32) enumDisplayDevices(
	deviceName *uint16,
	index uint32,
	device *displayDevice,
	_ uint32,
) (bool, error) {
	f.displayDeviceSizes = append(f.displayDeviceSizes, device.Cb)
	devices := f.adapters
	if deviceName != nil {
		devices = f.monitors[windows.UTF16PtrToString(deviceName)]
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
	f.enumSettingsCalls++
	f.enumSettingsDevice = windows.UTF16PtrToString(deviceName)
	f.enumSettingsMode = modeNumber
	f.enumSettingsSizes = append(f.enumSettingsSizes, mode.DmSize)
	*mode = f.current
	return true, nil
}

func (f *fakeWin32) changeDisplaySettingsEx(deviceName *uint16, mode *devMode, flags uint32) int32 {
	f.changeDevice = windows.UTF16PtrToString(deviceName)
	f.changedMode = *mode
	f.changeFlags = flags
	return f.changeResult
}

func TestWindowsStructsMatchWin32ABI(t *testing.T) {
	if got := unsafe.Sizeof(displayDevice{}); got != 840 {
		t.Fatalf("DISPLAY_DEVICEW size=%d", got)
	}
	if got := unsafe.Sizeof(devMode{}); got != 220 {
		t.Fatalf("DEVMODEW size=%d", got)
	}
}

func TestWindowsNativeMapsMonitorHardwareIDToAttachedAdapter(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\\.\DETACHED`, "", 0),
			newDisplayDevice(t, `\\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[string][]displayDevice{
			`\\.\DETACHED`: {
				newDisplayDevice(t, "", `MONITOR\IGNORED\0001`, 0),
			},
			`\\.\DISPLAY1`: {
				newDisplayDevice(t, "", `MONITOR\XMI27B2\0009`, 0),
			},
		},
	}
	native := &windowsNative{api: api}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Target{DeviceName: `\\.\DISPLAY1`, HardwareID: `MONITOR\XMI27B2\0009`}
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("targets=%#v", targets)
	}
	for _, size := range api.displayDeviceSizes {
		if size != uint32(unsafe.Sizeof(displayDevice{})) {
			t.Fatalf("DISPLAY_DEVICEW cb=%d", size)
		}
	}
}

func TestWindowsNativeCurrentModeUsesTargetDevice(t *testing.T) {
	api := &fakeWin32{current: devMode{
		DmPelsWidth:        2560,
		DmPelsHeight:       1440,
		DmDisplayFrequency: 180,
		DmBitsPerPel:       32,
	}}
	native := &windowsNative{api: api}

	got, err := native.currentMode(`\\.\DISPLAY1`)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	if got != want {
		t.Fatalf("mode=%#v", got)
	}
}

func TestWindowsNativeChangeModePreservesDriverStateAndUsesExactFlags(t *testing.T) {
	wantMode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	tests := []struct {
		name      string
		testOnly  bool
		wantFlags uint32
	}{
		{name: "test", testOnly: true, wantFlags: cdsTest},
		{name: "apply", testOnly: false, wantFlags: cdsFullscreen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeWin32{current: devMode{
				DmFields:       0xffffffff,
				DmPosition:     pointL{X: 123, Y: 456},
				DmDisplayFlags: 77,
			}}
			native := &windowsNative{api: api}

			if err := native.changeMode(`\\.\DISPLAY1`, wantMode, tt.testOnly); err != nil {
				t.Fatal(err)
			}
			if api.enumSettingsCalls != 1 {
				t.Fatalf("EnumDisplaySettingsW calls=%d", api.enumSettingsCalls)
			}
			if api.changeDevice != `\\.\DISPLAY1` {
				t.Fatalf("device=%q", api.changeDevice)
			}
			if api.changeFlags != tt.wantFlags {
				t.Fatalf("flags=%#x", api.changeFlags)
			}
			wantFields := dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel
			if api.changedMode.DmFields != wantFields {
				t.Fatalf("DmFields=%#x", api.changedMode.DmFields)
			}
			gotMode := domain.Mode{
				Width:        api.changedMode.DmPelsWidth,
				Height:       api.changedMode.DmPelsHeight,
				RefreshHz:    api.changedMode.DmDisplayFrequency,
				BitsPerPixel: api.changedMode.DmBitsPerPel,
			}
			if gotMode != wantMode {
				t.Fatalf("mode=%#v", gotMode)
			}
			if api.changedMode.DmPosition != (pointL{X: 123, Y: 456}) || api.changedMode.DmDisplayFlags != 77 {
				t.Fatalf("driver state was not preserved: %#v", api.changedMode)
			}
		})
	}
}

// Both Win32 read paths must name the resolved device, ask for the live mode with
// ENUM_CURRENT_SETTINGS, and declare the DEVMODEW buffer size before the call.
// Getting any of the three wrong would read another display or a short buffer.
func TestWindowsNativeEnumUsesResolvedDeviceCurrentSettingsAndBufferSize(t *testing.T) {
	const device = `\.\DISPLAY4`
	gameMode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}

	for name, read := range map[string]func(*windowsNative) error{
		"currentMode": func(n *windowsNative) error {
			_, err := n.currentMode(device)
			return err
		},
		"changeMode": func(n *windowsNative) error {
			return n.changeMode(device, gameMode, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			api := &fakeWin32{}
			if err := read(&windowsNative{api: api}); err != nil {
				t.Fatal(err)
			}
			if api.enumSettingsCalls != 1 {
				t.Fatalf("EnumDisplaySettingsW calls=%d", api.enumSettingsCalls)
			}
			if api.enumSettingsDevice != device {
				t.Errorf("lpszDeviceName=%q, want %q", api.enumSettingsDevice, device)
			}
			if api.enumSettingsMode != enumCurrentSettings {
				t.Errorf("iModeNum=%#x, want ENUM_CURRENT_SETTINGS (%#x)",
					api.enumSettingsMode, enumCurrentSettings)
			}
			want := uint16(unsafe.Sizeof(devMode{}))
			if len(api.enumSettingsSizes) != 1 || api.enumSettingsSizes[0] != want {
				t.Errorf("input DEVMODEW.dmSize=%v, want [%d]", api.enumSettingsSizes, want)
			}
		})
	}
}

func TestDescribeResultCoversDocumentedValues(t *testing.T) {
	tests := []struct {
		result int32
		want   string
	}{
		{dispChangeSuccessful, "successful"},
		{dispChangeRestart, "restart required"},
		{dispChangeFailed, "driver failed the display mode change"},
		{dispChangeBadMode, "display mode is not supported"},
		{dispChangeNotUpdated, "registry settings were not updated"},
		{dispChangeBadFlags, "invalid flags"},
		{dispChangeBadParam, "invalid parameter"},
		{dispChangeBadDualView, "dual-view mode is not supported"},
		{42, "unknown result 42"},
	}
	for _, tt := range tests {
		if got := describeResult(tt.result); got != tt.want {
			t.Errorf("describeResult(%d)=%q want %q", tt.result, got, tt.want)
		}
	}
}

func TestWindowsControllerCanTestMiMonitorMode(t *testing.T) {
	if os.Getenv("RUN_DISPLAY_INTEGRATION") != "1" {
		t.Skip("set RUN_DISPLAY_INTEGRATION=1")
	}
	c := NewWindowsController()
	target, err := c.ResolveTarget(`MONITOR\XMI27B2`)
	if err != nil {
		t.Fatal(err)
	}
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	if err := c.TestMode(target, mode); err != nil {
		t.Fatal(err)
	}
}

func newDisplayDevice(t *testing.T, name, hardwareID string, stateFlags uint32) displayDevice {
	t.Helper()
	device := displayDevice{StateFlags: stateFlags}
	copyUTF16(t, device.DeviceName[:], name)
	copyUTF16(t, device.DeviceID[:], hardwareID)
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
