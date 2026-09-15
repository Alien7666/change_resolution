package display

import (
	"errors"
	"os"
	"reflect"
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
		"DISPLAY_DEVICE_ACTIVE":         displayDeviceActive,
		"EDD_GET_DEVICE_INTERFACE_NAME": eddGetDeviceInterfaceName,
	} {
		want := map[string]uint32{
			"DM_POSITION": 0x00000020, "DM_BITSPERPEL": 0x00040000,
			"DM_PELSWIDTH": 0x00080000, "DM_PELSHEIGHT": 0x00100000,
			"DM_DISPLAYFREQUENCY": 0x00400000, "CDS_TEST": 0x00000002,
			"CDS_FULLSCREEN": 0x00000004, "DISPLAY_DEVICE_ATTACH": 0x00000001,
			"DISPLAY_DEVICE_ACTIVE":         0x00000001,
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
// listTargets used to throw DeviceString away. What this test pins is that the
// monitor's own name survives the enumeration at all when no CCD name is available,
// and that a monitor reporting no name at all falls to the bottom rung of the ladder
// instead of showing the user a blank row. The hardware ID is not invented text: it
// is the other thing this same read already reported.
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
	if got := targets[1].Identity.Label; got != `MONITOR\ACR0D0D\0004` {
		t.Errorf("Label=%q, want the hardware ID for a monitor that reports no name", got)
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

// EnumDisplayDevicesW can retain a monitor that the adapter could present while its
// GDI view is not currently on. It is not part of the live desktop and must not enter
// identity matching or manufacture a false mirror refusal beside the active monitor.
func TestWindowsNativeReportsOnlyActiveMonitorsOnAnAttachedAdapter(t *testing.T) {
	const inactivePath = `\\?\DISPLAY#OLD0001#5&2b9d4d4&0&UID4999#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Mi Monitor 27", `MONITOR\XMI27B2\0009`),
				newInactiveMonitorDevice(t, "Old Monitor", `MONITOR\OLD0001\0001`),
			},
			{adapter: `\.\DISPLAY1`, interfaceName: true}: {
				newMonitorDevice(t, "Mi Monitor 27", miMonitorInterfacePath),
				newInactiveMonitorDevice(t, "Old Monitor", inactivePath),
			},
		},
	}

	targets, err := (&windowsNative{api: api}).listTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Identity.InstancePath != miMonitorInterfacePath {
		t.Fatalf("targets=%#v, want only the active monitor", targets)
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

// planDeviceOrder and appliedDeviceOrder are the two orders an apply has: the one
// the plan lists its changes in, which is the order the displays were read in, and
// the one the calls actually crossed into Win32 in. A grow test is only worth
// anything while those two disagree.
func planDeviceOrder(plan domain.LayoutPlan) []string {
	order := make([]string, len(plan.Changes))
	for i, change := range plan.Changes {
		order[i] = change.DeviceName
	}
	return order
}

func appliedDeviceOrder(api *fakeWin32) []string {
	order := make([]string, len(api.calls))
	for i, call := range api.calls {
		order[i] = call.device
	}
	return order
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
	// The plan still lists the target second, where the read order put it, so the
	// sequence above cannot have come from the plan's order. Only the rule moves the
	// target to the end, and this is the assertion that says so rather than leaving
	// it to be read off two literals that happen to differ.
	if planned := planDeviceOrder(plan); reflect.DeepEqual(planned, appliedDeviceOrder(api)) {
		t.Fatalf("plan order %v is already the apply order; the test cannot tell the rule from the list", planned)
	}
	assertDesktop(t, api, layoutFrom(plan, before))
}

// A mode picker offers plenty of modes that are wider and shorter than the one the
// monitor is running, and the ordering rule reads "grows" as either axis, not both:
// 2560x1440 to 3440x1080 takes the growing branch and its mode goes last, even
// though it hands back 360 px of height at the same time. That is the right reading.
// The neighbours this desktop has to move are the ones beside the target, and they
// cannot move outward into space the target has not given up yet -- while the height
// it frees is space nothing is waiting for, because no display sits below it.
func TestWindowsNativeApplyLayoutAppliesTheTargetLastWhenItGrowsInOnlyOneAxis(t *testing.T) {
	before := offsetTargetLayout()
	api := fakeDesktop(t, before)
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(before, `\.\DISPLAY1`, mixedAxisMode)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.applyLayout(plan); err != nil {
		t.Fatal(err)
	}
	assertApplySequence(t, api, plan, []string{
		`\.\DISPLAY3`, `\.\DISPLAY2`, `\.\DISPLAY5`, `\.\DISPLAY1`,
	})
	if planned := planDeviceOrder(plan); reflect.DeepEqual(planned, appliedDeviceOrder(api)) {
		t.Fatalf("plan order %v is already the apply order; the test cannot tell the rule from the list", planned)
	}
	assertDesktop(t, api, layoutFrom(plan, before))
}

// desktopStates reads the fake desktop back the way the tool would, so a test can
// look at an arrangement that existed only between two Win32 calls.
func desktopStates(api *fakeWin32, devices []string) []domain.DisplayState {
	states := make([]domain.DisplayState, 0, len(devices))
	for _, device := range devices {
		reported := api.modes[device]
		states = append(states, domain.DisplayState{
			DeviceName: device,
			Mode:       modeOf(reported),
			Position:   domain.Point{X: reported.DmPosition.X, Y: reported.DmPosition.Y},
		})
	}
	return states
}

// overlappingPair names the two displays of an arrangement that sit on each other,
// which is the thing orderForApply exists to keep out of every intermediate desktop.
func overlappingPair(states []domain.DisplayState) (string, string, bool) {
	for i := range states {
		for j := i + 1; j < len(states); j++ {
			if overlaps(states[i], states[j]) {
				return states[i].DeviceName, states[j].DeviceName, true
			}
		}
	}
	return "", "", false
}

// The ordering rule is one decision per apply, not one per display, and a
// mixed-axis mode is where that costs something. 1920x1440 to 2560x1080 grows the
// width, so every neighbour moves first -- including the display below, which is
// waiting on the height the target has not given up yet. For exactly one call the
// two of them overlap.
//
// The trade is recorded here rather than left to be rediscovered on a live desktop.
// The other order is no better: applying the target first would put its new width
// on top of a neighbour that has not moved out of the way. Only a per-display order
// avoids both, and the plan for this release rules the target last. The failure mode
// is bounded on the way out: a driver that repacks the desktop instead of accepting
// the overlapping step is caught by verifyApplied, and everything already changed is
// rolled back -- the user gets an error, never a silently rearranged desktop.
//
// A purely growing target never reaches this: with both deltas outward, no
// neighbour ever moves toward the target at all.
func TestWindowsNativeApplyLayoutOrdersOncePerApplyNotOncePerDisplay(t *testing.T) {
	devices := []string{`\.\DISPLAY1`, `\.\DISPLAY2`, `\.\DISPLAY6`}
	before := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: devices[0], Mode: miMonitorGame, Position: domain.Point{}, Primary: true},
		{DeviceName: devices[1], Mode: topMode, Position: domain.Point{X: 1920, Y: 0}},
		{DeviceName: devices[2], Mode: topMode, Position: domain.Point{X: 0, Y: 1440}},
	}}
	api := fakeDesktop(t, before)
	var intermediate [][]domain.DisplayState
	api.afterChange = func(f *fakeWin32, _ win32Call) {
		intermediate = append(intermediate, desktopStates(f, devices))
	}
	native := &windowsNative{api: api}
	plan, err := PlanModeChange(before, devices[0], ultrawideMode)
	if err != nil {
		t.Fatal(err)
	}

	if err := native.applyLayout(plan); err != nil {
		t.Fatal(err)
	}
	assertApplySequence(t, api, plan, []string{devices[1], devices[2], devices[0]})
	if len(intermediate) != 3 {
		t.Fatalf("recorded %d intermediate desktops, want one per call", len(intermediate))
	}
	first, second, found := overlappingPair(intermediate[1])
	if !found || first != devices[0] || second != devices[2] {
		t.Fatalf("after the display below moved, overlapping pair = (%q, %q, %v); want %s and %s",
			first, second, found, devices[0], devices[2])
	}
	if _, _, found := overlappingPair(intermediate[2]); found {
		t.Fatal("the apply ended on an overlapping desktop, which the planner had proved safe")
	}
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

// EnumDisplayDevicesW answers "Generic PnP Monitor" for most screens, which in a
// four-row monitor list is the same as answering nothing. The CCD friendly name is
// what the user recognises, and the join between the two reads is the device
// interface path, so the name only lands on the monitor it belongs to.
func TestWindowsNativeLabelsMonitorsWithTheirCCDFriendlyName(t *testing.T) {
	const acerInterfacePath = `\\?\DISPLAY#ACR0D0D#5&2b9d4d4&0&UID4360#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
			newDisplayDevice(t, `\.\DISPLAY2`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Generic PnP Monitor", `MONITOR\XMI27B2\0009`),
			},
			{adapter: `\.\DISPLAY1`, interfaceName: true}: {
				newMonitorDevice(t, "Generic PnP Monitor", miMonitorInterfacePath),
			},
			{adapter: `\.\DISPLAY2`}: {
				newMonitorDevice(t, "Generic PnP Monitor", `MONITOR\ACR0D0D\0004`),
			},
			{adapter: `\.\DISPLAY2`, interfaceName: true}: {
				newMonitorDevice(t, "Generic PnP Monitor", acerInterfacePath),
			},
		},
	}
	// The keys are spelled differently from the enumerated paths on purpose: the CCD
	// API and EnumDisplayDevicesW do not agree on casing, and a case-sensitive join
	// would quietly give every monitor the wrong name or none at all.
	namer := &fakeNamer{names: map[string]string{
		strings.ToLower(miMonitorInterfacePath): "Mi Monitor 27",
		strings.ToLower(acerInterfacePath):      "Acer XB271HU",
	}}
	native := &windowsNative{api: api, namer: namer}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Mi Monitor 27", "Acer XB271HU"}
	if len(targets) != len(want) {
		t.Fatalf("targets=%#v, want %d", targets, len(want))
	}
	for i, label := range want {
		if targets[i].Identity.Label != label {
			t.Errorf("targets[%d].Label=%q, want %q", i, targets[i].Identity.Label, label)
		}
	}
}

// The friendly name is an enrichment and never a precondition. A CCD read that fails
// outright -- an old Windows, a driver that will not answer, a desktop changing
// under the read -- costs the user a prettier label and nothing else: the
// enumeration still reports every monitor, and every monitor still carries the name
// its own driver reported.
func TestWindowsNativeFallsBackToTheDeviceStringWhenTheCCDReadFails(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Acer XB271HU", `MONITOR\ACR0D0D\0004`),
			},
			{adapter: `\.\DISPLAY1`, interfaceName: true}: {
				newMonitorDevice(t, "Acer XB271HU", miMonitorInterfacePath),
			},
		},
	}
	namer := &fakeNamer{err: errors.New("QueryDisplayConfig: the driver is not responding")}
	native := &windowsNative{api: api, namer: namer}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatalf("a failed name lookup became an enumeration error: %v", err)
	}
	if len(targets) != 1 || targets[0].Identity.Label != "Acer XB271HU" {
		t.Fatalf("targets=%#v, want one labelled by its own DeviceString", targets)
	}
}

// The bottom rung. A monitor whose CCD name is missing and whose own name is the
// placeholder Windows invents is still worth a row the user can tell apart, and the
// hardware ID at least names the model.
func TestWindowsNativeFallsBackToTheHardwareIDWhenNoNameIsUsable(t *testing.T) {
	api := &fakeWin32{
		adapters: []displayDevice{
			newDisplayDevice(t, `\.\DISPLAY1`, "", displayDeviceAttachedToDesktop),
		},
		monitors: map[monitorKey][]displayDevice{
			{adapter: `\.\DISPLAY1`}: {
				newMonitorDevice(t, "Generic PnP Monitor", `MONITOR\XMI27B2\0009`),
			},
		},
	}
	native := &windowsNative{api: api, namer: &fakeNamer{}}

	targets, err := native.listTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Identity.Label != `MONITOR\XMI27B2\0009` {
		t.Fatalf("targets=%#v, want the hardware ID as the label", targets)
	}
}

// The whole configuration is read once per enumeration, not once per monitor.
// QueryDisplayConfig walks every path on the machine, so calling it per monitor
// would re-read the same desktop four times and, worse, could observe four
// different desktops.
func TestWindowsNativeReadsTheFriendlyNamesOncePerEnumeration(t *testing.T) {
	namer := &fakeNamer{}
	native := &windowsNative{api: measuredDesktop(t), namer: namer}

	if _, err := native.listTargets(); err != nil {
		t.Fatal(err)
	}
	if namer.calls != 1 {
		t.Errorf("friendly-name lookups=%d, want exactly one per enumeration", namer.calls)
	}
}

// Reading the real display configuration is a read, so it stays inside the opt-in
// gate's promise of "enumeration and CDS_TEST only". It is also the only thing that
// proves the struct sizes above against the API that actually writes into them: a
// wrong size shows up here as a missing or mangled name, not as a silent success.
func TestWindowsControllerIntegrationReadsEveryMonitorsFriendlyName(t *testing.T) {
	if os.Getenv("RUN_DISPLAY_INTEGRATION") != "1" {
		t.Skip("set RUN_DISPLAY_INTEGRATION=1")
	}
	targets, err := NewWindowsController().Targets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("no attached monitor was reported")
	}
	for _, target := range targets {
		t.Logf("%s label=%q hardwareID=%q instancePath=%q",
			target.DeviceName, target.Identity.Label,
			target.Identity.HardwareID, target.Identity.InstancePath)
		if target.Identity.Label == "" {
			t.Errorf("%s carries no label; the ladder must always end somewhere", target.DeviceName)
		}
	}
}

// soleDisplayDesktop is a one-monitor desktop at the Mi Monitor's native mode, which
// is all the mode-enumeration tests need: enumModes reads one adapter and never
// looks at the arrangement.
func soleDisplayDesktop(t *testing.T, device string) *fakeWin32 {
	t.Helper()
	return fakeDesktop(t, domain.Layout{Displays: []domain.DisplayState{{
		DeviceName: device, Mode: miMonitorNative, Position: domain.Point{}, Primary: true,
	}}})
}

// The filter has unit tests of its own, and they would all still pass if the
// conversion out of DEVMODEW dropped dmFields or dmDisplayFlags on the floor: an
// entry whose declarations arrived as zero is dropped by the required-fields rule,
// and one whose flags arrived as zero is silently kept. So the dirty list is walked
// through the adapter too, and the dirt is the shape a real driver produces -- a
// scaling-variant duplicate, an interlaced timing, a placeholder refresh rate, a
// 16-bit entry, and one member the driver filled in without declaring.
func TestWindowsNativeEnumModesFiltersTheListTheDriverReports(t *testing.T) {
	const device = `\.\DISPLAY1`
	api := soleDisplayDesktop(t, device)
	api.enumModes = map[string][]devMode{device: {
		listedMode(2560, 1440, 180, 32),
		listedMode(1920, 1440, 180, 32),
		listedMode(1920, 1440, 180, 32),
		interlaced(listedMode(1920, 1440, 180, 32)),
		listedMode(1920, 1440, 1, 32),
		listedMode(1920, 1440, 180, 16),
		withoutField(listedMode(1280, 1024, 60, 32), dmPelsHeight),
	}}
	native := &windowsNative{api: api}

	got, err := native.enumModes(device)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Mode{miMonitorNative, miMonitorGame}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enumModes=%#v,\nwant %#v", got, want)
	}
}

// The mode on the screen is always in the list. A driver that does not enumerate the
// mode it is currently running -- a custom timing built in the control panel, a list
// the driver trimmed -- would otherwise produce a picker that does not contain what
// the user is looking at. The separate ENUM_CURRENT_SETTINGS read is what supplies
// it, so this also pins that enumModes performs that read at all.
func TestWindowsNativeEnumModesIncludesACurrentModeTheDriverDidNotList(t *testing.T) {
	const device = `\.\DISPLAY1`
	api := soleDisplayDesktop(t, device)
	api.enumModes = map[string][]devMode{device: {listedMode(1920, 1440, 180, 32)}}
	native := &windowsNative{api: api}

	got, err := native.enumModes(device)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.Mode{miMonitorNative, miMonitorGame}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("enumModes=%#v,\nwant the current mode merged in: %#v", got, want)
	}
}

// A monitor whose reported list is entirely unusable is a state the settings flow
// describes rather than an error it reports. Here even the current mode is one the
// tool would not apply, so nothing is left -- and an empty list must come back as an
// empty list, because the caller's message ("this monitor reports no usable mode")
// is different from the one it prints when a read failed.
func TestWindowsNativeEnumModesReportsAnEmptyCatalogueRatherThanAnError(t *testing.T) {
	const device = `\.\DISPLAY1`
	api := soleDisplayDesktop(t, device)
	api.modes[device] = listedMode(1920, 1440, 180, 16)
	api.enumModes = map[string][]devMode{device: {listedMode(1920, 1440, 1, 32)}}
	native := &windowsNative{api: api}

	got, err := native.enumModes(device)
	if err != nil {
		t.Fatalf("an empty catalogue was reported as a failure: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("enumModes=%#v, want nothing", got)
	}
}

// The walk is EnumDisplaySettingsW(device, i, &dm) from i = 0 until it answers FALSE,
// and every call gets a freshly zeroed DEVMODEW with dmSize re-asserted. Reusing one
// buffer is the tempting version and it is wrong twice over: GDI is not promised to
// leave dmSize intact, and the previous answer's dmFields and dmDisplayFlags would
// still be set, so the filter would judge each entry partly on the one before it.
func TestWindowsNativeEnumModesWalksFromZeroWithAFreshBufferEachCall(t *testing.T) {
	const device = `\.\DISPLAY4`
	api := soleDisplayDesktop(t, device)
	api.enumModes = map[string][]devMode{device: {
		interlaced(listedMode(2560, 1440, 180, 32)),
		listedMode(1920, 1440, 180, 32),
		listedMode(1280, 1024, 60, 32),
	}}
	native := &windowsNative{api: api}

	if _, err := native.enumModes(device); err != nil {
		t.Fatal(err)
	}

	var indexed []uint32
	currentReads := 0
	for i, call := range api.enumSettings {
		if call.device != device {
			t.Errorf("read %d lpszDeviceName=%q, want %q", i, call.device, device)
		}
		if want := uint16(unsafe.Sizeof(devMode{})); call.size != want {
			t.Errorf("read %d input DEVMODEW.dmSize=%d, want %d", i, call.size, want)
		}
		if call.fields != 0 || call.displayFlags != 0 {
			t.Errorf("read %d reused a buffer: dmFields=%#x dmDisplayFlags=%#x, want a zeroed DEVMODEW",
				i, call.fields, call.displayFlags)
		}
		if call.modeNumber == enumCurrentSettings {
			currentReads++
			continue
		}
		indexed = append(indexed, call.modeNumber)
	}
	if want := []uint32{0, 1, 2, 3}; !reflect.DeepEqual(indexed, want) {
		t.Errorf("iModeNum sequence=%v, want %v: from zero, one at a time, one past the end", indexed, want)
	}
	if currentReads != 1 {
		t.Errorf("ENUM_CURRENT_SETTINGS reads=%d, want exactly one", currentReads)
	}
}

// FALSE past the end of the list is the walk's only terminator, so a driver that
// never gives it would spin forever inside a call the user made from the settings
// dialog -- a hang with no message and no way out. The bound is a guard on that, not
// a filter: it is far past any real driver's list.
func TestWindowsNativeEnumModesStopsWhenTheDriverNeverEndsTheWalk(t *testing.T) {
	const device = `\.\DISPLAY1`
	api := soleDisplayDesktop(t, device)
	api.endlessModeWalk = true
	native := &windowsNative{api: api}

	got, err := native.enumModes(device)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("the walk returned nothing at all")
	}
	if reads := uint32(len(api.enumSettings)); reads > maxEnumeratedModes+1 {
		t.Fatalf("reads=%d, want the walk bounded at %d plus the current-settings read",
			reads, maxEnumeratedModes)
	}
}

// The current mode is not optional here: the whole list is built around it, and a
// device whose live settings cannot be read is a device that is no longer there.
// Returning the enumerated list anyway would hand the settings dialog a catalogue
// for a monitor that has gone, and every mode in it would be a choice the user
// cannot make.
func TestWindowsNativeEnumModesFailsWhenTheCurrentModeCannotBeRead(t *testing.T) {
	const device = `\.\DISPLAY1`
	api := soleDisplayDesktop(t, device)
	api.currentReadFails = map[string]bool{device: true}
	api.enumModes = map[string][]devMode{device: {listedMode(1920, 1440, 180, 32)}}
	native := &windowsNative{api: api}

	got, err := native.enumModes(device)
	if err == nil {
		t.Fatalf("enumModes=%#v, want the failed read reported", got)
	}
	if !strings.Contains(err.Error(), "EnumDisplaySettingsW") {
		t.Fatalf("err=%v does not name the call that failed", err)
	}
}

// Enumerating modes is a read. It changes nothing, which is what lets the first-run
// wizard call it, so the test that says so is worth more than it looks: a single
// CDS_TEST here would be inside the opt-in gate's promise and still wrong, because
// the wizard runs before the user has chosen anything to test.
func TestWindowsNativeEnumModesNeverCrossesIntoChangeDisplaySettings(t *testing.T) {
	const device = `\.\DISPLAY1`
	api := soleDisplayDesktop(t, device)
	api.enumModes = map[string][]devMode{device: {listedMode(1920, 1440, 180, 32)}}
	native := &windowsNative{api: api}

	if _, err := native.enumModes(device); err != nil {
		t.Fatal(err)
	}
	if len(api.calls) != 0 {
		t.Fatalf("enumerating modes changed something: %+v", api.calls)
	}
}

// Enumeration is a read, so it stays inside the opt-in gate's promise of
// "enumeration and CDS_TEST only". It is also the only place the walk meets a real
// driver's list, which is where the dirt the filter exists for actually lives: the
// numbers logged here are the evidence that the rules hold against hundreds of real
// entries rather than a handful of fixtures.
func TestWindowsControllerIntegrationEnumeratesEveryMonitorsModes(t *testing.T) {
	if os.Getenv("RUN_DISPLAY_INTEGRATION") != "1" {
		t.Skip("set RUN_DISPLAY_INTEGRATION=1")
	}
	c := NewWindowsController()
	targets, err := c.Targets()
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("no attached monitor was reported")
	}
	for _, target := range targets {
		modes, err := c.EnumModes(target)
		if err != nil {
			t.Errorf("%s: %v", target.DeviceName, err)
			continue
		}
		current, err := c.CurrentMode(target)
		if err != nil {
			t.Errorf("%s: %v", target.DeviceName, err)
			continue
		}
		native, _ := domain.NativeMode(modes)
		t.Logf("%s %q: %d modes, current %s, native %s (%s)",
			target.DeviceName, target.Identity.Label, len(modes), describeMode(current),
			describeMode(native), domain.AspectLabel(native.Width, native.Height))
		if len(modes) == 0 {
			t.Errorf("%s reported no usable mode at all", target.DeviceName)
			continue
		}

		seen := make(map[modeKey]bool, len(modes))
		carriesCurrent := false
		for i, mode := range modes {
			if i < 12 {
				t.Logf("    %s  %s", describeMode(mode), domain.AspectLabel(mode.Width, mode.Height))
			}
			if mode.BitsPerPixel != applicableBitsPerPixel {
				t.Errorf("%s: %s survived the colour-depth filter", target.DeviceName, describeMode(mode))
			}
			if mode.RefreshHz < minimumRefreshHz || mode.Width == 0 || mode.Height == 0 {
				t.Errorf("%s: %s survived the value filter", target.DeviceName, describeMode(mode))
			}
			key := modeKey{width: mode.Width, height: mode.Height, refreshHz: mode.RefreshHz}
			if seen[key] {
				t.Errorf("%s: %s appears twice", target.DeviceName, describeMode(mode))
			}
			seen[key] = true
			if i > 0 && !domain.LargerMode(modes[i-1], mode) {
				t.Errorf("%s: %s is listed before %s", target.DeviceName,
					describeMode(modes[i-1]), describeMode(mode))
			}
			if mode == current {
				carriesCurrent = true
			}
		}
		if !carriesCurrent {
			t.Errorf("%s: the catalogue does not contain the mode the monitor is running, %s",
				target.DeviceName, describeMode(current))
		}
	}
}
