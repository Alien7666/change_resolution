package display

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
)

type nativeTest struct {
	deviceName string
	mode       domain.Mode
}

type fakeNative struct {
	targets    []domain.Target
	mode       domain.Mode
	layout     domain.Layout
	listErr    error
	currentErr error
	layoutErr  error
	testErr    error
	applyErr   error
	currentFor string
	tests      []nativeTest
	applied    []domain.LayoutPlan
}

func (f *fakeNative) listTargets() ([]domain.Target, error) {
	return f.targets, f.listErr
}

func (f *fakeNative) currentMode(deviceName string) (domain.Mode, error) {
	f.currentFor = deviceName
	return f.mode, f.currentErr
}

func (f *fakeNative) currentLayout() (domain.Layout, error) {
	return f.layout, f.layoutErr
}

func (f *fakeNative) testMode(deviceName string, mode domain.Mode) error {
	f.tests = append(f.tests, nativeTest{deviceName: deviceName, mode: mode})
	return f.testErr
}

func (f *fakeNative) applyLayout(plan domain.LayoutPlan) error {
	f.applied = append(f.applied, plan)
	return f.applyErr
}

func TestResolveTargetMatchesHardwareIDPrefixCaseInsensitively(t *testing.T) {
	api := &fakeNative{targets: []domain.Target{
		{DeviceName: `\.\DISPLAY2`, HardwareID: `MONITOR\ACR0D0D\0004`},
		{DeviceName: `\.\DISPLAY1`, HardwareID: `monitor\xmi27b2\0009`},
	}}
	c := newController(api)
	got, err := c.ResolveTarget(`MONITOR\XMI27B2`)
	if err != nil || got.DeviceName != `\.\DISPLAY1` {
		t.Fatalf("target=%#v err=%v", got, err)
	}
}

func TestResolveTargetReturnsSentinelWhenNoTargetMatches(t *testing.T) {
	api := &fakeNative{targets: []domain.Target{{
		DeviceName: `\.\DISPLAY2`, HardwareID: `MONITOR\ACR0D0D\0004`,
	}}}
	c := newController(api)

	_, err := c.ResolveTarget(`MONITOR\XMI27B2`)
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestCurrentModeUsesOnlyResolvedDevice(t *testing.T) {
	mode := domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	api := &fakeNative{mode: mode}
	c := newController(api)
	target := domain.Target{DeviceName: `\.\DISPLAY1`, HardwareID: `MONITOR\XMI27B2\0009`}

	got, err := c.CurrentMode(target)
	if err != nil || got != mode {
		t.Fatalf("mode=%#v err=%v", got, err)
	}
	if api.currentFor != target.DeviceName {
		t.Fatalf("device=%q", api.currentFor)
	}
}

func TestCurrentLayoutReportsEveryDisplayItRead(t *testing.T) {
	api := &fakeNative{layout: measuredLayout()}
	c := newController(api)

	got, err := c.CurrentLayout()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, measuredLayout()) {
		t.Fatalf("layout=%#v", got)
	}
}

// The pre-flight is the only call that may name a single device, and it must be the
// resolved target with the requested mode.
func TestTestModeUsesOnlyResolvedDevice(t *testing.T) {
	api := &fakeNative{}
	c := newController(api)
	target := domain.Target{DeviceName: `\.\DISPLAY1`, HardwareID: `MONITOR\XMI27B2\0009`}
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}

	if err := c.TestMode(target, mode); err != nil {
		t.Fatal(err)
	}
	want := []nativeTest{{deviceName: target.DeviceName, mode: mode}}
	if !reflect.DeepEqual(api.tests, want) {
		t.Fatalf("tests=%#v", api.tests)
	}
	if len(api.applied) != 0 {
		t.Fatalf("the pre-flight applied something: %#v", api.applied)
	}
}

// The whole arrangement reaches the native layer as one plan. Splitting it per
// display here would be exactly the half applied desktop the transaction avoids.
func TestApplyLayoutHandsTheWholePlanToTheNativeLayer(t *testing.T) {
	api := &fakeNative{}
	c := newController(api)
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.ApplyLayout(plan); err != nil {
		t.Fatal(err)
	}
	if len(api.applied) != 1 || !reflect.DeepEqual(api.applied[0], plan) {
		t.Fatalf("applied=%#v", api.applied)
	}
	if len(api.tests) != 0 {
		t.Fatalf("apply ran a pre-flight of its own: %#v", api.tests)
	}
}

func TestTestModeWrapsPreflightRejectionWithSentinel(t *testing.T) {
	rejection := errors.New(`ChangeDisplaySettingsExW(\.\DISPLAY1): display mode is not supported`)
	api := &fakeNative{testErr: rejection}
	c := newController(api)
	target := domain.Target{DeviceName: `\.\DISPLAY1`, HardwareID: `MONITOR\XMI27B2\0009`}
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}

	err := c.TestMode(target, mode)
	if !errors.Is(err, ErrModeNotSupported) {
		t.Fatalf("err=%v does not wrap ErrModeNotSupported", err)
	}
	if !errors.Is(err, rejection) {
		t.Fatalf("err=%v lost the underlying Win32 diagnostic", err)
	}
	if len(api.tests) != 1 {
		t.Fatalf("tests=%#v", api.tests)
	}
}

// A failed apply must stay distinguishable from an unsupported mode: only the
// pre-flight rejection disables the 4:3 control, a one-off apply failure is retryable.
func TestApplyLayoutFailureIsNotReportedAsUnsupportedMode(t *testing.T) {
	failure := errors.New("driver failed the display mode change")
	api := &fakeNative{applyErr: failure}
	c := newController(api)
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	err = c.ApplyLayout(plan)
	if !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	if errors.Is(err, ErrModeNotSupported) {
		t.Fatalf("apply failure reported as unsupported mode: %v", err)
	}
}
