package display

import (
	"os"
	"testing"
	"unsafe"

	"github.com/Alien7666/change_resolution/internal/domain"
	"golang.org/x/sys/windows"
)

// win32Call is one ChangeDisplaySettingsExW crossing. A nil mode records the
// committing call, which passes neither a device nor a DEVMODEW.
type win32Call struct {
	device string
	mode   *devMode
	flags  uint32
}

type fakeWin32 struct {
	adapters           []displayDevice
	monitors           map[string][]displayDevice
	current            devMode
	modes              map[string]devMode
	displayDeviceSizes []uint32
	enumSettingsCalls  int
	enumSettingsDevice string
	enumSettingsMode   uint32
	enumSettingsSizes  []uint16
	calls              []win32Call
	changeResult       int32
	changeResults      map[string]int32
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
	name := windows.UTF16PtrToString(deviceName)
	f.enumSettingsCalls++
	f.enumSettingsDevice = name
	f.enumSettingsMode = modeNumber
	f.enumSettingsSizes = append(f.enumSettingsSizes, mode.DmSize)
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
		staged := *mode
		call.mode = &staged
	}
	f.calls = append(f.calls, call)
	if result, ok := f.changeResults[name]; ok {
		return result
	}
	return f.changeResult
}

func (f *fakeWin32) lastMode(t *testing.T) devMode {
	t.Helper()
	if len(f.calls) == 0 || f.calls[len(f.calls)-1].mode == nil {
		t.Fatalf("no DEVMODEW reached ChangeDisplaySettingsExW: %+v", f.calls)
	}
	return *f.calls[len(f.calls)-1].mode
}

func TestWindowsStructsMatchWin32ABI(t *testing.T) {
	if got := unsafe.Sizeof(displayDevice{}); got != 840 {
		t.Fatalf("DISPLAY_DEVICEW size=%d", got)
	}
	if got := unsafe.Sizeof(devMode{}); got != 220 {
		t.Fatalf("DEVMODEW size=%d", got)
	}
}

// The flag values are the contract with wingdi.h. CDS_UPDATEREGISTRY is 0x00000001
// and would make the game mode the user permanent setting, so it must not be the
// value of any constant this package sends, CDS_NORESET included.
func TestWindowsFlagsMatchWin32AndExcludeUpdateRegistry(t *testing.T) {
	const cdsUpdateRegistry uint32 = 0x00000001
	for name, got := range map[string]uint32{
		"DM_POSITION":           dmPosition,
		"DM_BITSPERPEL":         dmBitsPerPel,
		"DM_PELSWIDTH":          dmPelsWidth,
		"DM_PELSHEIGHT":         dmPelsHeight,
		"DM_DISPLAYFREQUENCY":   dmDisplayFrequency,
		"CDS_TEST":              cdsTest,
		"CDS_FULLSCREEN":        cdsFullscreen,
		"CDS_NORESET":           cdsNoReset,
		"DISPLAY_DEVICE_ATTACH": displayDeviceAttachedToDesktop,
	} {
		want := map[string]uint32{
			"DM_POSITION": 0x00000020, "DM_BITSPERPEL": 0x00040000,
			"DM_PELSWIDTH": 0x00080000, "DM_PELSHEIGHT": 0x00100000,
			"DM_DISPLAYFREQUENCY": 0x00400000, "CDS_TEST": 0x00000002,
			"CDS_FULLSCREEN": 0x00000004, "CDS_NORESET": 0x10000000,
			"DISPLAY_DEVICE_ATTACH": 0x00000001,
		}[name]
		if got != want {
			t.Errorf("%s=%#x, want %#x", name, got, want)
		}
	}
	if displayDevicePrimaryDevice != 0x00000004 {
		t.Errorf("DISPLAY_DEVICE_PRIMARY_DEVICE=%#x", displayDevicePrimaryDevice)
	}
	for name, flags := range map[string]uint32{
		"pre-flight": cdsTest,
		"staging":    cdsFullscreen | cdsNoReset,
		"commit":     commitFlags,
	} {
		if flags&cdsUpdateRegistry != 0 {
			t.Errorf("the %s call carries CDS_UPDATEREGISTRY: %#x", name, flags)
		}
	}
}

func TestWindowsNativeMapsMonitorHardwareIDToAttachedAdapter(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DETACHED`, "", 0),
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[string][]displayDevice{
			`\.\DETACHED`: {
				newDisplayDevice(t, "", `MONITOR\IGNORED\0001`, 0),
			},
			`\.\DISPLAY1`: {
				newDisplayDevice(t, "", `MONITOR\XMI27B2\0009`, 0),
			},
		},
	}
	native := &windowsNative{api: api}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Target{DeviceName: `\.\DISPLAY1`, HardwareID: `MONITOR\XMI27B2\0009`}
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

	got, err := native.currentMode(`\.\DISPLAY1`)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	if got != want {
		t.Fatalf("mode=%#v", got)
	}
}

// The layout read has to carry each attached display position and which one is
// primary: without both, nothing downstream can keep the desktop contiguous or
// anchored. A detached adapter is not part of the desktop and is skipped.
func TestWindowsNativeCurrentLayoutReadsPositionsAndPrimaryFlag(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "",
				displayDeviceAttachedToDesktop|displayDevicePrimaryDevice),
			newDisplayDevice(t, `\.\DETACHED`, "", 0),
			newDisplayDevice(t, `\.\DISPLAY2`, "", displayDeviceAttachedToDesktop),
		},
		modes: map[string]devMode{
			`\.\DISPLAY1`: {
				DmPosition: pointL{X: 0, Y: 0}, DmPelsWidth: 2560, DmPelsHeight: 1440,
				DmDisplayFrequency: 180, DmBitsPerPel: 32,
			},
			`\.\DISPLAY2`: {
				DmPosition: pointL{X: 2560, Y: -120}, DmPelsWidth: 1920, DmPelsHeight: 1080,
				DmDisplayFrequency: 60, DmBitsPerPel: 32,
			},
		},
	}
	native := &windowsNative{api: api}

	got, err := native.currentLayout()
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Layout{Displays: []domain.DisplayState{
		{
			DeviceName: `\.\DISPLAY1`,
			Mode:       domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
			Position:   domain.Point{X: 0, Y: 0}, Primary: true,
		},
		{
			DeviceName: `\.\DISPLAY2`,
			Mode:       domain.Mode{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32},
			Position:   domain.Point{X: 2560, Y: -120},
		},
	}}
	if len(got.Displays) != len(want.Displays) {
		t.Fatalf("layout=%#v", got)
	}
	for i, display := range got.Displays {
		if display != want.Displays[i] {
			t.Fatalf("display %d = %#v, want %#v", i, display, want.Displays[i])
		}
	}
}

// The pre-flight still names the target alone, declares only the four mode members
// and leaves every other DEVMODEW member the driver reported untouched.
func TestWindowsNativeTestModePreservesDriverStateAndUsesExactFlags(t *testing.T) {
	wantMode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	api := &fakeWin32{current: devMode{
		DmFields:       0xffffffff,
		DmPosition:     pointL{X: 123, Y: 456},
		DmDisplayFlags: 77,
	}}
	native := &windowsNative{api: api}

	if err := native.testMode(`\.\DISPLAY1`, wantMode); err != nil {
		t.Fatal(err)
	}
	if api.enumSettingsCalls != 1 {
		t.Fatalf("EnumDisplaySettingsW calls=%d", api.enumSettingsCalls)
	}
	if len(api.calls) != 1 || api.calls[0].device != `\.\DISPLAY1` {
		t.Fatalf("calls=%+v", api.calls)
	}
	if api.calls[0].flags != cdsTest {
		t.Fatalf("flags=%#x", api.calls[0].flags)
	}
	staged := api.lastMode(t)
	if want := dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel; staged.DmFields != want {
		t.Fatalf("DmFields=%#x, want %#x", staged.DmFields, want)
	}
	if modeOf(staged) != wantMode {
		t.Fatalf("mode=%#v", modeOf(staged))
	}
	if staged.DmPosition != (pointL{X: 123, Y: 456}) || staged.DmDisplayFlags != 77 {
		t.Fatalf("driver state was not preserved: %#v", staged)
	}
	if want := uint16(unsafe.Sizeof(devMode{})); staged.DmSize != want {
		t.Fatalf("DEVMODEW.dmSize=%d, want %d", staged.DmSize, want)
	}
}

// stagedLayout is the measured desktop as the driver would report it, so applyLayout
// reads a real DEVMODEW per display instead of one shared fixture.
func stagedLayout(t *testing.T) *fakeWin32 {
	t.Helper()
	modes := make(map[string]devMode, len(measuredLayout().Displays))
	for _, display := range measuredLayout().Displays {
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
	return &fakeWin32{modes: modes}
}

// The transaction is what reaches Win32: one CDS_NORESET call per display, then one
// committing call with no device and no DEVMODEW. Nothing is applied display by
// display, so the desktop never exists in a half rearranged state.
func TestWindowsNativeApplyLayoutStagesEveryDisplayThenCommitsOnce(t *testing.T) {
	api := stagedLayout(t)
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.applyLayout(plan); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != len(plan.Changes)+1 {
		t.Fatalf("calls=%d, want one per display plus one commit: %+v", len(api.calls), api.calls)
	}
	for i, change := range plan.Changes {
		call := api.calls[i]
		if call.device != change.DeviceName {
			t.Fatalf("call %d device=%q, want %q", i, call.device, change.DeviceName)
		}
		if call.flags != cdsFullscreen|cdsNoReset {
			t.Fatalf("call %d flags=%#x, want CDS_FULLSCREEN|CDS_NORESET", i, call.flags)
		}
		if call.mode == nil {
			t.Fatalf("call %d carried no DEVMODEW", i)
		}
		if call.mode.DmPosition != (pointL{X: change.Position.X, Y: change.Position.Y}) {
			t.Fatalf("call %d dmPosition=%+v, want %+v", i, call.mode.DmPosition, change.Position)
		}
		if want := uint16(unsafe.Sizeof(devMode{})); call.mode.DmSize != want {
			t.Fatalf("call %d dmSize=%d, want %d", i, call.mode.DmSize, want)
		}
	}
	commit := api.calls[len(api.calls)-1]
	if commit.device != "" || commit.mode != nil || commit.flags != commitFlags {
		t.Fatalf("commit call = %+v, want a NULL device and NULL DEVMODEW", commit)
	}
}

// A display that is not the target may only be moved. Declaring DM_POSITION alone
// is what keeps its resolution, refresh rate and colour depth out of the write set,
// whatever the plan happens to carry.
func TestWindowsNativeApplyLayoutMovesOtherDisplaysWithoutTouchingTheirMode(t *testing.T) {
	api := stagedLayout(t)
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.applyLayout(plan); err != nil {
		t.Fatal(err)
	}
	before := measuredLayout()
	for i, change := range plan.Changes {
		staged := api.calls[i].mode
		reported, _ := before.Find(change.DeviceName)
		if change.DeviceName == `\.\DISPLAY1` {
			want := dmPosition | dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel
			if staged.DmFields != want {
				t.Fatalf("target DmFields=%#x, want %#x", staged.DmFields, want)
			}
			if modeOf(*staged) != miMonitorGame {
				t.Fatalf("target mode=%#v", modeOf(*staged))
			}
			continue
		}
		if staged.DmFields != dmPosition {
			t.Fatalf("%s DmFields=%#x, want DM_POSITION alone", change.DeviceName, staged.DmFields)
		}
		if modeOf(*staged) != reported.Mode {
			t.Fatalf("%s mode=%#v, want the driver reported %#v",
				change.DeviceName, modeOf(*staged), reported.Mode)
		}
	}
}

// A staging failure must abort before the commit: with nothing committed the
// desktop is exactly as it was, which is why the commit is the last call.
func TestWindowsNativeApplyLayoutAbortsBeforeCommittingAPartialLayout(t *testing.T) {
	api := stagedLayout(t)
	api.changeResults = map[string]int32{`\.\DISPLAY5`: dispChangeBadParam}
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.applyLayout(plan); err == nil {
		t.Fatal("a rejected display was reported as success")
	}
	for _, call := range api.calls {
		if call.device == "" {
			t.Fatalf("the layout was committed after a staging failure: %+v", api.calls)
		}
	}
	if len(api.calls) != 3 {
		t.Fatalf("calls=%+v, want staging to stop at the rejected display", api.calls)
	}
}

func TestWindowsNativeApplyLayoutRefusesAnEmptyPlan(t *testing.T) {
	api := stagedLayout(t)
	native := &windowsNative{api: api}

	if err := native.applyLayout(domain.LayoutPlan{}); err == nil {
		t.Fatal("an empty plan was applied")
	}
	if len(api.calls) != 0 {
		t.Fatalf("calls=%+v", api.calls)
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
		"testMode": func(n *windowsNative) error {
			return n.testMode(device, gameMode)
		},
		"applyLayout": func(n *windowsNative) error {
			return n.applyLayout(domain.LayoutPlan{Changes: []domain.LayoutChange{{
				DeviceName: device, Position: domain.Point{}, Mode: gameMode, SetMode: true,
			}}})
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
	if got := describeDevice(""); got != "commit" {
		t.Errorf("describeDevice(nil device)=%q", got)
	}
	if got := describeDevice(`\.\DISPLAY1`); got != `\.\DISPLAY1` {
		t.Errorf("describeDevice=%q", got)
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
