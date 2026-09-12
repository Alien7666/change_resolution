package display

import (
	"errors"
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
)

type nativeChange struct {
	deviceName string
	mode       domain.Mode
	test       bool
}

type fakeNative struct {
	targets    []domain.Target
	mode       domain.Mode
	listErr    error
	currentErr error
	changeErr  error
	currentFor string
	changes    []nativeChange
}

func (f *fakeNative) listTargets() ([]domain.Target, error) {
	return f.targets, f.listErr
}

func (f *fakeNative) currentMode(deviceName string) (domain.Mode, error) {
	f.currentFor = deviceName
	return f.mode, f.currentErr
}

func (f *fakeNative) changeMode(deviceName string, mode domain.Mode, test bool) error {
	f.changes = append(f.changes, nativeChange{deviceName: deviceName, mode: mode, test: test})
	return f.changeErr
}

func TestResolveTargetMatchesHardwareIDPrefixCaseInsensitively(t *testing.T) {
	api := &fakeNative{targets: []domain.Target{
		{DeviceName: `\\.\DISPLAY2`, HardwareID: `MONITOR\ACR0D0D\0004`},
		{DeviceName: `\\.\DISPLAY1`, HardwareID: `monitor\xmi27b2\0009`},
	}}
	c := newController(api)
	got, err := c.ResolveTarget(`MONITOR\XMI27B2`)
	if err != nil || got.DeviceName != `\\.\DISPLAY1` {
		t.Fatalf("target=%#v err=%v", got, err)
	}
}

func TestResolveTargetReturnsSentinelWhenNoTargetMatches(t *testing.T) {
	api := &fakeNative{targets: []domain.Target{{
		DeviceName: `\\.\DISPLAY2`, HardwareID: `MONITOR\ACR0D0D\0004`,
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
	target := domain.Target{DeviceName: `\\.\DISPLAY1`, HardwareID: `MONITOR\XMI27B2\0009`}

	got, err := c.CurrentMode(target)
	if err != nil || got != mode {
		t.Fatalf("mode=%#v err=%v", got, err)
	}
	if api.currentFor != target.DeviceName {
		t.Fatalf("device=%q", api.currentFor)
	}
}

func TestTestAndApplyUseOnlyResolvedDevice(t *testing.T) {
	api := &fakeNative{}
	c := newController(api)
	target := domain.Target{DeviceName: `\\.\DISPLAY1`, HardwareID: `MONITOR\XMI27B2\0009`}
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	if err := c.TestMode(target, mode); err != nil {
		t.Fatal(err)
	}
	if err := c.ApplyMode(target, mode); err != nil {
		t.Fatal(err)
	}
	if len(api.changes) != 2 || !api.changes[0].test || api.changes[1].test {
		t.Fatalf("changes=%#v", api.changes)
	}
	for _, change := range api.changes {
		if change.deviceName != target.DeviceName {
			t.Fatalf("device=%q", change.deviceName)
		}
		if change.mode != mode {
			t.Fatalf("mode=%#v", change.mode)
		}
	}
}
