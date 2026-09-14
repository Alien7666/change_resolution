package display

import (
	"errors"
	"os"
	"strings"
	"testing"
	"unsafe"

	"github.com/Alien7666/change_resolution/internal/domain"
)

// cdsUpdateRegistry is the flag this package must never send: it would write a
// temporary game mode into the user's permanent settings.
const cdsUpdateRegistry uint32 = 0x00000001

func TestWindowsStructsMatchWin32ABI(t *testing.T) {
	if got := unsafe.Sizeof(displayDevice{}); got != 840 {
		t.Fatalf("DISPLAY_DEVICEW size=%d", got)
	}
	if got := unsafe.Sizeof(devMode{}); got != 220 {
		t.Fatalf("DEVMODEW size=%d", got)
	}
}

// The flag values are the contract with wingdi.h. CDS_UPDATEREGISTRY is 0x00000001
// and would make the game mode the user's permanent setting, so it must not be the
// value of any constant this package sends. CDS_NORESET (0x10000000) must not appear
// either: the driver on the target machine answers a CDS_FULLSCREEN|CDS_NORESET call
// with DISP_CHANGE_BADFLAGS, and its only documented partner is the one flag this
// tool is forbidden to send.
func TestWindowsFlagsMatchWin32AndExcludeUpdateRegistry(t *testing.T) {
	const cdsNoReset uint32 = 0x10000000
	for name, got := range map[string]uint32{
		"DM_POSITION":                   dmPosition,
		"DM_BITSPERPEL":                 dmBitsPerPel,
		"DM_PELSWIDTH":                  dmPelsWidth,
		"DM_PELSHEIGHT":                 dmPelsHeight,
		"DM_DISPLAYFREQUENCY":           dmDisplayFrequency,
		"CDS_TEST":                      cdsTest,
		"CDS_FULLSCREEN":                cdsFullscreen,
		"DISPLAY_DEVICE_ATTACH":         displayDeviceAttachedToDesktop,
		"EDD_GET_DEVICE_INTERFACE_NAME": eddGetDeviceInterfaceName,
	} {
		want := map[string]uint32{
			"DM_POSITION": 0x00000020, "DM_BITSPERPEL": 0x00040000,
			"DM_PELSWIDTH": 0x00080000, "DM_PELSHEIGHT": 0x00100000,
			"DM_DISPLAYFREQUENCY": 0x00400000, "CDS_TEST": 0x00000002,
			"CDS_FULLSCREEN": 0x00000004, "DISPLAY_DEVICE_ATTACH": 0x00000001,
			"EDD_GET_DEVICE_INTERFACE_NAME": 0x00000001,
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
		"apply":      cdsFullscreen,
	} {
		if flags&cdsUpdateRegistry != 0 {
			t.Errorf("the %s call carries CDS_UPDATEREGISTRY: %#x", name, flags)
		}
		if flags&cdsNoReset != 0 {
			t.Errorf("the %s call carries CDS_NORESET: %#x", name, flags)
		}
	}
}

// miMonitorInterfacePath is the shape EnumDisplayDevicesW reports under
// EDD_GET_DEVICE_INTERFACE_NAME. The UID... segment names the graphics card output
// port, which is what makes the path able to tell two units of one model apart.
const miMonitorInterfacePath = `\\?\DISPLAY#XMI27B2#5&2b9d4d4&0&UID4357#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`

// The expected Target is spelled out whole rather than field by field: listTargets
// is the only place an identity enters the tool, so an exact comparison is also the
// assertion that it invents nothing beyond what the two reads reported.
func TestWindowsNativeMapsMonitorHardwareIDToAttachedAdapter(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DETACHED`, "", 0),
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DETACHED`}: {
				newMonitorDevice(t, "Ignored Monitor", `MONITOR\IGNORED\0001`),
			},
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Mi Monitor 27", `MONITOR\XMI27B2\0009`),
			},
			{adapter: `\.\DISPLAY1`, interfaceName: true}: {
				newMonitorDevice(t, "Mi Monitor 27", miMonitorInterfacePath),
			},
		},
	}
	native := &windowsNative{api: api}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Target{
		DeviceName: `\.\DISPLAY1`,
		Identity: domain.MonitorIdentity{
			InstancePath: miMonitorInterfacePath,
			HardwareID:   `MONITOR\XMI27B2\0009`,
			Label:        "Mi Monitor 27",
		},
	}
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("targets=%#v, want exactly one %#v", targets, want)
	}
	for _, size := range api.displayDeviceSizes {
		if size != uint32(unsafe.Sizeof(displayDevice{})) {
			t.Fatalf("DISPLAY_DEVICEW cb=%d", size)
		}
	}
}

// EDD_GET_DEVICE_INTERFACE_NAME replaces the content of DISPLAY_DEVICEW.DeviceID
// rather than adding a member to the struct, so one call can report the hardware ID
// or the interface path but never both. Each monitor index is therefore read twice,
// and this test is what holds the implementation to that: the fake answers the two
// keys with different DeviceID content, so a single read could only ever fill one of
// the two rungs of the identity ladder.
func TestWindowsNativeReadsTheDeviceInterfacePathAndTheHardwareID(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Mi Monitor 27", `MONITOR\XMI27B2\0009`),
			},
			{adapter: `\.\DISPLAY1`, interfaceName: true}: {
				newMonitorDevice(t, "Mi Monitor 27", miMonitorInterfacePath),
			},
		},
	}
	native := &windowsNative{api: api}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets=%#v", targets)
	}
	if got := targets[0].Identity.HardwareID; got != `MONITOR\XMI27B2\0009` {
		t.Errorf("HardwareID=%q", got)
	}
	if got := targets[0].Identity.InstancePath; got != miMonitorInterfacePath {
		t.Errorf("InstancePath=%q", got)
	}
}

// The label is display-only and never participates in a comparison, but it is the
// only thing that tells the user which row of a monitor list is which screen, and
// listTargets used to throw DeviceString away. Task 4 layers the CCD friendly name
// on top of this; what this test pins is that the monitor's own name survives the
// enumeration at all, and that a monitor that reports no name produces an empty
// label rather than something invented.
func TestWindowsNativeKeepsTheMonitorsOwnDeviceStringAsALabel(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
			newDisplayDevice(t, `\.\DISPLAY2`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Mi Monitor 27", `MONITOR\XMI27B2\0009`),
			},
			{adapter: `\.\DISPLAY2`}: {
				newMonitorDevice(t, "", `MONITOR\ACR0D0D\0004`),
			},
		},
	}
	native := &windowsNative{api: api}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets=%#v", targets)
	}
	if got := targets[0].Identity.Label; got != "Mi Monitor 27" {
		t.Errorf("Label=%q, want the monitor's own DeviceString", got)
	}
	if got := targets[1].Identity.Label; got != "" {
		t.Errorf("Label=%q, want an empty label for a monitor that reports no name", got)
	}
}

// A cloned or mirrored adapter drives more than one monitor, and Win32 reports both
// under the same \\.\DISPLAYn. listTargets reports one target per monitor, so the
// two share a DeviceName -- that repetition is the only evidence Task 5's mirror
// check has to work from, because the tool cannot change the mode of one of two
// monitors that share an adapter and must refuse rather than change both.
func TestWindowsNativeReportsOneTargetPerMonitorNotPerAdapter(t *testing.T) {
	const clonedPath = `\\?\DISPLAY#ACR0D0D#5&2b9d4d4&0&UID4358#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Mi Monitor 27", `MONITOR\XMI27B2\0009`),
				newMonitorDevice(t, "Acer", `MONITOR\ACR0D0D\0004`),
			},
			{adapter: `\.\DISPLAY1`, interfaceName: true}: {
				newMonitorDevice(t, "Mi Monitor 27", miMonitorInterfacePath),
				newMonitorDevice(t, "Acer", clonedPath),
			},
		},
	}
	native := &windowsNative{api: api}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets=%#v, want one per monitor", targets)
	}
	for i, target := range targets {
		if target.DeviceName != `\.\DISPLAY1` {
			t.Fatalf("target %d DeviceName=%q, want the shared adapter", i, target.DeviceName)
		}
	}
	if targets[0].Identity.InstancePath == targets[1].Identity.InstancePath {
		t.Fatalf("both monitors carry one interface path: %#v", targets)
	}
}

// The second read is an enrichment, not a precondition. A monitor whose interface
// path cannot be read is still selectable by hardware ID, so an unanswered second
// call leaves InstancePath empty and drops nothing: refusing to report the monitor
// would take away the rung that still works.
func TestWindowsNativeStillReportsAMonitorWhoseInterfacePathIsUnavailable(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Mi Monitor 27", `MONITOR\XMI27B2\0009`),
			},
		},
	}
	native := &windowsNative{api: api}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	want := domain.Target{
		DeviceName: `\.\DISPLAY1`,
		Identity: domain.MonitorIdentity{
			HardwareID: `MONITOR\XMI27B2\0009`,
			Label:      "Mi Monitor 27",
		},
	}
	if len(targets) != 1 || targets[0] != want {
		t.Fatalf("targets=%#v, want exactly one %#v", targets, want)
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
	if len(api.enumSettings) != 1 {
		t.Fatalf("EnumDisplaySettingsW calls=%d", len(api.enumSettings))
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

// offsetTargetLayout is the measured desktop with the target in the middle of the
// read order. Plan order alone would then put the target neither first nor last, so
// the order the calls actually reach Win32 in can only come from the ordering rule.
func offsetTargetLayout() domain.Layout {
	return domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY3`, Mode: topMode, Position: domain.Point{X: 642, Y: -1080}},
		{DeviceName: `\.\DISPLAY1`, Mode: miMonitorNative, Position: domain.Point{X: 0, Y: 0}, Primary: true},
		{DeviceName: `\.\DISPLAY2`, Mode: sideMode, Position: domain.Point{X: 2560, Y: 0}},
		{DeviceName: `\.\DISPLAY5`, Mode: topMode, Position: domain.Point{X: 2562, Y: -1080}},
	}}
}

func findChange(t *testing.T, plan domain.LayoutPlan, device string) domain.LayoutChange {
	t.Helper()
	for _, change := range plan.Changes {
		if change.DeviceName == device {
			return change
		}
	}
	t.Fatalf("%s is not part of the plan: %+v", device, plan.Changes)
	return domain.LayoutChange{}
}

// assertApplySequence pins the whole Win32 crossing of an apply: one call per
// display in exactly the given order, each naming its own device and carrying the
// planned position, CDS_FULLSCREEN and nothing else -- CDS_UPDATEREGISTRY least of
// all.
func assertApplySequence(t *testing.T, api *fakeWin32, plan domain.LayoutPlan, order []string) {
	t.Helper()
	if len(api.calls) != len(order) {
		t.Fatalf("calls=%d, want one per display: %+v", len(api.calls), api.calls)
	}
	for i, device := range order {
		call := api.calls[i]
		if call.device != device {
			t.Fatalf("call %d device=%q, want %q", i, call.device, device)
		}
		if call.flags != cdsFullscreen {
			t.Fatalf("call %d flags=%#x, want CDS_FULLSCREEN alone", i, call.flags)
		}
		if call.flags&cdsUpdateRegistry != 0 {
			t.Fatalf("call %d carries CDS_UPDATEREGISTRY: %#x", i, call.flags)
		}
		if call.mode == nil {
			t.Fatalf("call %d carried no DEVMODEW", i)
		}
		planned := findChange(t, plan, device)
		if call.mode.DmPosition != (pointL{X: planned.Position.X, Y: planned.Position.Y}) {
			t.Fatalf("call %d dmPosition=%+v, want %+v", i, call.mode.DmPosition, planned.Position)
		}
		if want := uint16(unsafe.Sizeof(devMode{})); call.mode.DmSize != want {
			t.Fatalf("call %d dmSize=%d, want %d", i, call.mode.DmSize, want)
		}
	}
}

// assertDesktop holds the fake driver's state to an arrangement, position and whole
// mode, which is how a test says "nothing was left moved".
func assertDesktop(t *testing.T, api *fakeWin32, want domain.Layout) {
	t.Helper()
	for _, display := range want.Displays {
		got, ok := api.modes[display.DeviceName]
		if !ok {
			t.Fatalf("%s is missing from the desktop", display.DeviceName)
		}
		if got.DmPosition != (pointL{X: display.Position.X, Y: display.Position.Y}) {
			t.Fatalf("%s sits at %+v, want %+v", display.DeviceName, got.DmPosition, display.Position)
		}
		if modeOf(got) != display.Mode {
			t.Fatalf("%s runs %+v, want %+v", display.DeviceName, modeOf(got), display.Mode)
		}
	}
}

// The measured case: the Mi Monitor drops from 2560 to 1920 wide at the same height.
// A target that does not grow frees its space first, so its own mode goes first and
// the displays beside it then close the gap; moving them first would walk them into
// a display that is still 2560 wide, and Windows repacks a desktop that overlaps.
func TestWindowsNativeApplyLayoutAppliesTheTargetFirstWhenItShrinks(t *testing.T) {
	before := offsetTargetLayout()
	api := fakeDesktop(t, before)
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(before, `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.applyLayout(plan); err != nil {
		t.Fatal(err)
	}
	assertApplySequence(t, api, plan, []string{
		`\.\DISPLAY1`, `\.\DISPLAY3`, `\.\DISPLAY2`, `\.\DISPLAY5`,
	})
	assertDesktop(t, api, layoutFrom(plan, before))
}

// Growing the target back is the same rule with the sign flipped: the target cannot
// occupy space its neighbours still hold, so they move away first and its mode is
// the last call.
func TestWindowsNativeApplyLayoutMovesTheOtherDisplaysFirstWhenTheTargetGrows(t *testing.T) {
	narrowed, err := PlanModeChange(offsetTargetLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}
	before := layoutFrom(narrowed, offsetTargetLayout())
	api := fakeDesktop(t, before)
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(before, `\.\DISPLAY1`, miMonitorNative)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.applyLayout(plan); err != nil {
		t.Fatal(err)
	}
	assertApplySequence(t, api, plan, []string{
		`\.\DISPLAY3`, `\.\DISPLAY2`, `\.\DISPLAY5`, `\.\DISPLAY1`,
	})
	assertDesktop(t, api, layoutFrom(plan, before))
}

// A display that is not the target may only be moved. Declaring DM_POSITION alone is
// what keeps its resolution, refresh rate and colour depth out of the write set,
// whatever the plan happens to carry.
func TestWindowsNativeApplyLayoutMovesOtherDisplaysWithoutTouchingTheirMode(t *testing.T) {
	api := measuredDesktop(t)
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.applyLayout(plan); err != nil {
		t.Fatal(err)
	}
	before := measuredLayout()
	for i, call := range api.calls {
		reported, _ := before.Find(call.device)
		if call.device == `\.\DISPLAY1` {
			want := dmPosition | dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel
			if call.mode.DmFields != want {
				t.Fatalf("target DmFields=%#x, want %#x", call.mode.DmFields, want)
			}
			if modeOf(*call.mode) != miMonitorGame {
				t.Fatalf("target mode=%#v", modeOf(*call.mode))
			}
			continue
		}
		if call.mode.DmFields != dmPosition {
			t.Fatalf("call %d (%s) DmFields=%#x, want DM_POSITION alone", i, call.device, call.mode.DmFields)
		}
		if modeOf(*call.mode) != reported.Mode {
			t.Fatalf("%s mode=%#v, want the driver reported %#v",
				call.device, modeOf(*call.mode), reported.Mode)
		}
	}
	// And the mode really is still theirs once the driver has taken the write.
	assertDesktop(t, api, layoutFrom(plan, before))
}

// A sequential apply has no atomicity of its own, so it has to provide it: the
// display that was rejected stops the apply, and every display already changed goes
// back to where it was. The caller sees the rejection itself, not a second
// diagnostic about a desktop that was left alone after all.
func TestWindowsNativeApplyLayoutRollsBackWhenADisplayRejectsItsChange(t *testing.T) {
	before := offsetTargetLayout()
	api := fakeDesktop(t, before)
	api.changeResults = map[string]int32{`\.\DISPLAY2`: dispChangeBadParam}
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(before, `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	err = native.applyLayout(plan)
	if err == nil {
		t.Fatal("a rejected display was reported as success")
	}
	if !strings.Contains(err.Error(), `ChangeDisplaySettingsExW(\.\DISPLAY2): invalid parameter`) {
		t.Fatalf("err=%v lost the rejection that caused it", err)
	}
	if errors.Is(err, ErrLayoutPartlyApplied) {
		t.Fatalf("a completed rollback was reported as a partly applied desktop: %v", err)
	}
	wantCalls := []string{
		`\.\DISPLAY1`, `\.\DISPLAY3`, `\.\DISPLAY2`, // apply, stopping at the rejection
		`\.\DISPLAY3`, `\.\DISPLAY1`, // rollback, most recent first
	}
	if len(api.calls) != len(wantCalls) {
		t.Fatalf("calls=%+v, want %v", api.calls, wantCalls)
	}
	for i, device := range wantCalls {
		if api.calls[i].device != device {
			t.Fatalf("call %d device=%q, want %q", i, api.calls[i].device, device)
		}
		if api.calls[i].flags != cdsFullscreen {
			t.Fatalf("call %d flags=%#x, want CDS_FULLSCREEN alone", i, api.calls[i].flags)
		}
	}
	assertDesktop(t, api, before)
}

// When the rollback fails too the desktop is left in an arrangement nobody chose.
// Reporting only the original failure would tell the user their displays are
// untouched, which is the one thing that is no longer true.
func TestWindowsNativeApplyLayoutReportsAFailedRollbackDistinctly(t *testing.T) {
	before := offsetTargetLayout()
	api := fakeDesktop(t, before)
	// The target takes its mode, the next display refuses, and putting the target
	// back refuses as well.
	api.resultFor = func(index int, _ win32Call) int32 {
		if index == 0 {
			return dispChangeSuccessful
		}
		return dispChangeBadParam
	}
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(before, `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	err = native.applyLayout(plan)
	if !errors.Is(err, ErrLayoutPartlyApplied) {
		t.Fatalf("err=%v does not wrap ErrLayoutPartlyApplied", err)
	}
	if !strings.Contains(err.Error(), `ChangeDisplaySettingsExW(\.\DISPLAY3): invalid parameter`) {
		t.Fatalf("err=%v lost the rejection that caused it", err)
	}
	if !strings.Contains(err.Error(), "rolling the desktop back also failed") {
		t.Fatalf("err=%v does not say the desktop was left moved", err)
	}
	if len(api.calls) != 3 {
		t.Fatalf("calls=%+v, want the apply, the rejection and one rollback attempt", api.calls)
	}
	if got := modeOf(api.modes[`\.\DISPLAY1`]); got != miMonitorGame {
		t.Fatalf("the target was not left in the applied mode: %+v", got)
	}
}

// Windows may repack the desktop on its own during a mode change, so
// DISP_CHANGE_SUCCESSFUL on every call is not proof the plan is what landed. A
// display the tool cannot account for may be one the mouse can no longer reach, so
// the mismatch is an error rather than a shrug.
func TestWindowsNativeApplyLayoutReportsADesktopThatDoesNotMatchThePlan(t *testing.T) {
	tests := map[string]struct {
		repack func(f *fakeWin32, call win32Call)
		want   string
	}{
		"a display was moved somewhere else": {
			repack: func(f *fakeWin32, _ win32Call) {
				moved := f.modes[`\.\DISPLAY2`]
				moved.DmPosition = pointL{X: 3000, Y: 0}
				f.modes[`\.\DISPLAY2`] = moved
			},
			want: `\.\DISPLAY2 sits at (3000,0), the plan put it at (1920,0)`,
		},
		"the target got a different mode": {
			repack: func(f *fakeWin32, _ win32Call) {
				substituted := f.modes[`\.\DISPLAY1`]
				substituted.DmDisplayFrequency = 60
				f.modes[`\.\DISPLAY1`] = substituted
			},
			want: `\.\DISPLAY1 runs 1920x1440 @ 60 Hz 32 bpp, the plan asked for 1920x1440 @ 180 Hz 32 bpp`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			api := measuredDesktop(t)
			api.afterChange = tt.repack
			native := &windowsNative{api: api}
			plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
			if err != nil {
				t.Fatal(err)
			}

			err = native.applyLayout(plan)
			if !errors.Is(err, ErrLayoutNotVerified) {
				t.Fatalf("err=%v does not wrap ErrLayoutNotVerified", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err=%v does not report %q", err, tt.want)
			}
		})
	}
}

// A display the tool can no longer read is not a desktop it may call applied: the
// unreadable one could be anywhere.
func TestWindowsNativeApplyLayoutReportsADisplayItCannotReadBack(t *testing.T) {
	api := measuredDesktop(t)
	api.afterChange = func(f *fakeWin32, _ win32Call) {
		if len(f.adapters) == len(measuredLayout().Displays) {
			f.adapters = f.adapters[:len(f.adapters)-1]
		}
	}
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	err = native.applyLayout(plan)
	if !errors.Is(err, ErrLayoutNotVerified) {
		t.Fatalf("err=%v does not wrap ErrLayoutNotVerified", err)
	}
	if !strings.Contains(err.Error(), "is no longer attached") {
		t.Fatalf("err=%v", err)
	}
}

// The CDS_TEST pre-flight is still the first thing that crosses into Win32, it still
// names the target alone, and the apply that follows never re-tests a mode it is
// about to write.
func TestWindowsNativePreflightPrecedesTheApplyAndNamesTheTarget(t *testing.T) {
	api := measuredDesktop(t)
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.testMode(`\.\DISPLAY1`, miMonitorGame); err != nil {
		t.Fatal(err)
	}
	if err := native.applyLayout(plan); err != nil {
		t.Fatal(err)
	}
	if api.calls[0].flags != cdsTest || api.calls[0].device != `\.\DISPLAY1` {
		t.Fatalf("the first crossing was not the pre-flight on the target: %+v", api.calls[0])
	}
	for i, call := range api.calls[1:] {
		if call.flags&cdsTest != 0 {
			t.Fatalf("apply call %d re-tested the mode: %#x", i, call.flags)
		}
	}
	if len(api.calls) != 1+len(plan.Changes) {
		t.Fatalf("calls=%+v, want the pre-flight plus one per display", api.calls)
	}
}

func TestWindowsNativeApplyLayoutRefusesAnEmptyPlan(t *testing.T) {
	api := measuredDesktop(t)
	native := &windowsNative{api: api}

	if err := native.applyLayout(domain.LayoutPlan{}); err == nil {
		t.Fatal("an empty plan was applied")
	}
	if len(api.calls) != 0 {
		t.Fatalf("calls=%+v", api.calls)
	}
}

// Every Win32 read path must name the resolved device, ask for the live mode with
// ENUM_CURRENT_SETTINGS, and declare the DEVMODEW buffer size before the call.
// Getting any of the three wrong would read another display or a short buffer.
func TestWindowsNativeEnumUsesResolvedDeviceCurrentSettingsAndBufferSize(t *testing.T) {
	const device = `\.\DISPLAY4`
	gameMode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	soleDisplay := domain.Layout{Displays: []domain.DisplayState{{
		DeviceName: device, Mode: miMonitorNative, Position: domain.Point{}, Primary: true,
	}}}

	for name, read := range map[string]func(*windowsNative) error{
		"currentMode": func(n *windowsNative) error {
			_, err := n.currentMode(device)
			return err
		},
		"currentLayout": func(n *windowsNative) error {
			_, err := n.currentLayout()
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
			api := fakeDesktop(t, soleDisplay)
			if err := read(&windowsNative{api: api}); err != nil {
				t.Fatal(err)
			}
			if len(api.enumSettings) == 0 {
				t.Fatal("nothing read the display settings")
			}
			for i, call := range api.enumSettings {
				if call.device != device {
					t.Errorf("read %d lpszDeviceName=%q, want %q", i, call.device, device)
				}
				if call.modeNumber != enumCurrentSettings {
					t.Errorf("read %d iModeNum=%#x, want ENUM_CURRENT_SETTINGS (%#x)",
						i, call.modeNumber, enumCurrentSettings)
				}
				if want := uint16(unsafe.Sizeof(devMode{})); call.size != want {
					t.Errorf("read %d input DEVMODEW.dmSize=%d, want %d", i, call.size, want)
				}
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
	if got := describeMode(miMonitorGame); got != "1920x1440 @ 180 Hz 32 bpp" {
		t.Errorf("describeMode=%q", got)
	}
}

func TestWindowsControllerCanTestMiMonitorMode(t *testing.T) {
	if os.Getenv("RUN_DISPLAY_INTEGRATION") != "1" {
		t.Skip("set RUN_DISPLAY_INTEGRATION=1")
	}
	c := NewWindowsController()
	// The seed identity is the one the shipping tool ran on -- no instance path and
	// ModelWasUnique true -- so this read is also the check that the hardware-ID rung
	// still finds the Mi Monitor on the real adapter list.
	target, err := c.ResolveTarget(domain.LegacySeedProfile().Monitor)
	if err != nil {
		t.Fatal(err)
	}
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	if err := c.TestMode(target, mode); err != nil {
		t.Fatal(err)
	}
}
