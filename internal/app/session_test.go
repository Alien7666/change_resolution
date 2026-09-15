package app

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/scaling"
)

type displayCall struct {
	operation string
	target    domain.Target
	mode      domain.Mode
	plan      domain.LayoutPlan
}

type fakeDisplay struct {
	mu      sync.Mutex
	target  domain.Target
	targets []domain.Target
	current domain.Mode
	modes   []domain.Mode
	layout  domain.Layout
	calls   []displayCall
	fail    map[string]error
	hook    func(string)
}

func (d *fakeDisplay) record(operation string, target domain.Target, mode domain.Mode, plan domain.LayoutPlan) error {
	d.mu.Lock()
	d.calls = append(d.calls, displayCall{operation, target, mode, plan})
	err, hook := d.fail[operation], d.hook
	d.mu.Unlock()
	if hook != nil {
		hook(operation)
	}
	return err
}

// Targets is also the read-only identity snapshot a managed session uses to bind
// every saved DISPLAYn to the same physical monitor until restore.
func (d *fakeDisplay) Targets() ([]domain.Target, error) {
	err := d.record("targets", domain.Target{}, domain.Mode{}, domain.LayoutPlan{})
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.targets) != 0 {
		return append([]domain.Target(nil), d.targets...), err
	}
	// Provider tests build the shared fake directly. Supply the same complete,
	// trustworthy monitor snapshot the session fixture does without making those
	// tests know about this fake's new internal field.
	targets := make([]domain.Target, len(d.layout.Displays))
	for i, state := range d.layout.Displays {
		if state.DeviceName == d.target.DeviceName {
			targets[i] = d.target
			continue
		}
		targets[i] = domain.Target{
			DeviceName: state.DeviceName,
			Identity: domain.MonitorIdentity{
				InstancePath: "test-instance:" + state.DeviceName,
				HardwareID:   "MONITOR\\TEST\\" + state.DeviceName,
			},
		}
	}
	return targets, err
}

// ResolveTarget records the whole identity it was asked for instead of echoing one
// key back, which is what lets a test assert the session hands the profile's monitor
// through untouched rather than only the part of it that used to be a string.
func (d *fakeDisplay) ResolveTarget(identity domain.MonitorIdentity) (domain.Target, error) {
	err := d.record("resolve", domain.Target{Identity: identity}, domain.Mode{}, domain.LayoutPlan{})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.target, err
}

// EnumModes is what a monitor reports it can do. The session does not browse modes
// today -- the profile names one -- so recording the call is what lets a test say the
// session asked a monitor nothing it did not need to.
func (d *fakeDisplay) EnumModes(target domain.Target) ([]domain.Mode, error) {
	err := d.record("modes", target, domain.Mode{}, domain.LayoutPlan{})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.modes, err
}

func (d *fakeDisplay) CurrentMode(target domain.Target) (domain.Mode, error) {
	err := d.record("current", target, domain.Mode{}, domain.LayoutPlan{})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current, err
}

func (d *fakeDisplay) CurrentLayout() (domain.Layout, error) {
	err := d.record("layout", domain.Target{}, domain.Mode{}, domain.LayoutPlan{})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.layout, err
}

func (d *fakeDisplay) TestMode(target domain.Target, mode domain.Mode) error {
	return d.record("test", target, mode, domain.LayoutPlan{})
}

// ApplyLayout keeps the fake desktop in step with the transaction, so a test can
// compare the arrangement a restore produced against the one it started from.
func (d *fakeDisplay) ApplyLayout(plan domain.LayoutPlan) error {
	mode := plannedMode(plan)
	if err := d.record("apply", domain.Target{}, mode, plan); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.current = mode
	d.layout = applyPlan(d.layout, plan)
	return nil
}

// plannedMode is the one mode a plan writes: every other display is only moved.
func plannedMode(plan domain.LayoutPlan) domain.Mode {
	for _, change := range plan.Changes {
		if change.SetMode {
			return change.Mode
		}
	}
	return domain.Mode{}
}

func applyPlan(before domain.Layout, plan domain.LayoutPlan) domain.Layout {
	displays := make([]domain.DisplayState, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		state, _ := before.Find(change.DeviceName)
		state.Position = change.Position
		if change.SetMode {
			state.Mode = change.Mode
		}
		displays = append(displays, state)
	}
	return domain.Layout{Displays: displays}
}

func (d *fakeDisplay) takeCalls() []displayCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := append([]displayCall(nil), d.calls...)
	d.calls = nil
	return result
}

func (d *fakeDisplay) setFailure(operation string, err error) {
	d.mu.Lock()
	d.fail[operation] = err
	d.mu.Unlock()
}

type processResult struct {
	running bool
	err     error
}

type fakeChecker struct {
	called  chan string
	results chan processResult
	closed  chan struct{}
}

func (c *fakeChecker) Running(name string) (bool, error) {
	select {
	case c.called <- name:
	case <-c.closed:
		return false, errors.New("checker closed")
	}
	select {
	case result := <-c.results:
		return result.running, result.err
	case <-c.closed:
		return false, errors.New("checker closed")
	}
}

type fakeTicker struct {
	mu      sync.Mutex
	ticks   chan time.Time
	stopped bool
}

func (t *fakeTicker) C() <-chan time.Time { return t.ticks }

func (t *fakeTicker) Stop() {
	t.mu.Lock()
	t.stopped = true
	t.mu.Unlock()
}

func (t *fakeTicker) isStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

type fakeClock struct {
	mu        sync.Mutex
	now       time.Time
	tickers   []*fakeTicker
	intervals []time.Duration
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTicker(interval time.Duration) sessionTicker {
	c.mu.Lock()
	defer c.mu.Unlock()
	ticker := &fakeTicker{ticks: make(chan time.Time, 1)}
	c.tickers = append(c.tickers, ticker)
	c.intervals = append(c.intervals, interval)
	return ticker
}

func (c *fakeClock) tick(index int, at time.Time) {
	c.mu.Lock()
	c.now = at
	ticker := c.tickers[index]
	c.mu.Unlock()
	ticker.ticks <- at
}

func (c *fakeClock) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.tickers)
}

type sessionFixture struct {
	s        *Session
	display  *fakeDisplay
	checker  *fakeChecker
	clock    *fakeClock
	profile  domain.Profile
	original domain.Mode
	changes  chan Snapshot
}

const (
	targetDevice = `\.\DISPLAY4`
	rightDevice  = `\.\DISPLAY2`
	topRight     = `\.\DISPLAY5`
	topLeft      = `\.\DISPLAY3`
	// belowDevice only exists for the arrangements a mode picker makes reachable: a
	// display under the target moves whenever the target's height changes, and the
	// fixture's four displays are all beside or above it.
	belowDevice = `\.\DISPLAY6`
)

var (
	fixtureNative = domain.Mode{Width: 3440, Height: 1440, RefreshHz: 120, BitsPerPixel: 32}
	fixtureSide   = domain.Mode{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32}
	fixtureTop    = domain.Mode{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}
)

// fixtureLayout is a contiguous four display desktop for the given target mode: the
// target is the primary at the origin and the other three sit against it, exactly
// where a width change has to leave them. Passing the game mode therefore describes
// the desktop the tool must produce, and passing the native mode the one it must
// come back to.
func fixtureLayout(targetMode domain.Mode) domain.Layout {
	offset := int32(targetMode.Width) - int32(domain.LegacySeedProfile().GameMode.Width)
	return domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: targetDevice, Mode: targetMode, Position: domain.Point{}, Primary: true},
		{DeviceName: rightDevice, Mode: fixtureSide, Position: domain.Point{X: 1920 + offset}},
		{DeviceName: topRight, Mode: fixtureTop, Position: domain.Point{X: 1922 + offset, Y: -1080}},
		{DeviceName: topLeft, Mode: fixtureTop, Position: domain.Point{X: 2 + offset, Y: -1080}},
	}}
}

func newFixture(t *testing.T) *sessionFixture {
	t.Helper()
	return newFixtureWith(t, domain.LegacySeedProfile(), fixtureNative, fixtureLayout(fixtureNative))
}

// newFixtureWith builds the session around an explicit profile and desktop.
//
// The shipped profile only ever narrowed its target, so every test that drives a
// game mode larger than the one the monitor is running -- which is every mode a
// picker will offer -- comes through here. The desktop is passed in as well because
// a target that grows is not the only new shape: an arrangement the planner cannot
// make safe is one a user can now ask for, and that takes a desktop the fixture's
// contiguous four does not model.
func newFixtureWith(t *testing.T, profile domain.Profile, original domain.Mode, desktop domain.Layout) *sessionFixture {
	t.Helper()
	return newFixtureWrapped(t, profile, original, desktop, nil)
}

// newFixtureWrapped is newFixtureWith with one seam: build is handed the fake desktop
// this function just assembled and returns the pair of controllers the session is
// actually constructed with. That is how the Task 15 renaming fakes get between the
// session and this file's fake without this file having to know what GPU scaling is.
// A nil build, or a nil scaling controller from it, leaves the session with no scaling
// support, which is the state every test written before Task 15 assumes.
func newFixtureWrapped(t *testing.T, profile domain.Profile, original domain.Mode, desktop domain.Layout,
	build func(*fakeDisplay) (display.Controller, scaling.Controller)) *sessionFixture {
	t.Helper()
	targets := make([]domain.Target, len(desktop.Displays))
	for i, state := range desktop.Displays {
		targets[i] = domain.Target{
			DeviceName: state.DeviceName,
			Identity: domain.MonitorIdentity{
				InstancePath: "test-instance:" + state.DeviceName,
				HardwareID:   "MONITOR\\TEST\\" + state.DeviceName,
			},
		}
	}
	configuredTarget, ok := targetByDevice(targets, targetDevice)
	if !ok {
		t.Fatalf("fixture layout has no configured target %s", targetDevice)
	}
	configuredTarget.Identity.HardwareID = profile.Monitor.HardwareID
	configuredTarget.MatchedBy = domain.MatchHardwareID
	for i := range targets {
		if targets[i].DeviceName == targetDevice {
			targets[i] = configuredTarget
		}
	}
	fake := &fakeDisplay{
		target:  configuredTarget,
		targets: targets,
		current: original, layout: desktop, fail: make(map[string]error),
	}
	var displays display.Controller = fake
	var scalings scaling.Controller
	if build != nil {
		displays, scalings = build(fake)
	}
	checker := &fakeChecker{called: make(chan string, 8), results: make(chan processResult), closed: make(chan struct{})}
	clock := &fakeClock{now: time.Unix(100, 0)}
	f := &sessionFixture{display: fake, checker: checker, clock: clock, profile: profile, original: original, changes: make(chan Snapshot, 128)}
	f.s = newSession(displays, checker, scalings, profile, clock)
	f.s.SetOnChange(func(snapshot Snapshot) { f.changes <- snapshot })
	t.Cleanup(func() {
		close(checker.closed)
		fake.mu.Lock()
		fake.fail = make(map[string]error)
		fake.hook = nil
		fake.mu.Unlock()
		if err := f.s.Shutdown(); err != nil {
			t.Errorf("cleanup Shutdown: %v", err)
		}
	})
	return f
}

func targetByDevice(targets []domain.Target, deviceName string) (domain.Target, bool) {
	for _, target := range targets {
		if target.DeviceName == deviceName {
			return target, true
		}
	}
	return domain.Target{}, false
}

// setDesktop replaces the whole fake desktop, which is how a test models a monitor
// that is already in the game mode before the session ever touched it.
func (f *sessionFixture) setDesktop(mode domain.Mode) {
	f.display.mu.Lock()
	defer f.display.mu.Unlock()
	f.display.current = mode
	f.display.layout = fixtureLayout(mode)
}

func (f *sessionFixture) desktop() domain.Layout {
	f.display.mu.Lock()
	defer f.display.mu.Unlock()
	return f.display.layout
}

func waitValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for coordinated operation")
		var zero T
		return zero
	}
}

func (f *sessionFixture) waitSnapshot(t *testing.T, predicate func(Snapshot) bool) Snapshot {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case snapshot := <-f.changes:
			if predicate(snapshot) {
				return snapshot
			}
		case <-timeout:
			t.Fatalf("timed out waiting for snapshot; current = %+v", f.s.Snapshot())
			return Snapshot{}
		}
	}
}

// tick drives one poll of the watcher at index and answers its process check.
func (f *sessionFixture) tick(t *testing.T, index int, seconds int, result processResult) {
	t.Helper()
	f.clock.tick(index, time.Unix(100+int64(seconds), 0))
	if name := waitValue(t, f.checker.called); name != f.profile.ProcessName {
		t.Fatalf("watched process = %q", name)
	}
	f.checker.results <- result
}

func (f *sessionFixture) poll(t *testing.T, index int, seconds int, result processResult) Snapshot {
	t.Helper()
	previous := f.s.Snapshot().Revision
	f.tick(t, index, seconds, result)
	return f.waitSnapshot(t, func(snapshot Snapshot) bool {
		return snapshot.Revision > previous && snapshot.State != StateRestoring
	})
}

func assertOperations(t *testing.T, calls []displayCall, want ...string) {
	t.Helper()
	got := make([]string, len(calls))
	for i, call := range calls {
		got[i] = call.operation
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestEnableCapturesCurrentModeTestsThenApplies(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "layout", "targets", "test", "apply")
	if calls[0].target.Identity != f.profile.Monitor || calls[3].mode != f.profile.GameMode || calls[4].mode != f.profile.GameMode {
		t.Fatalf("wrong target or mode: %+v", calls)
	}
	if got := f.s.Snapshot(); !got.Managed || !got.AtGameMode || got.State != StateWaitingForGame {
		t.Fatalf("snapshot = %+v", got)
	}
	if f.clock.count() != 1 || f.clock.intervals[0] != time.Second {
		t.Fatal("Enable must start one one-second watcher")
	}
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	if len(f.display.takeCalls()) != 0 || f.clock.count() != 1 {
		t.Fatal("managed Enable is not idempotent")
	}
}

// Restoring is the other half of the promise: the target goes back to its saved
// mode and every display goes back to the coordinate it had before the tool moved
// it, so the desktop the user gets back is the one they started with.
func TestDisableRestoresTheCapturedModeAndArrangement(t *testing.T) {
	f := newFixture(t)
	before := f.desktop()
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()

	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "layout", "targets", "test", "apply")
	if calls[3].mode != f.original || calls[4].mode != f.original {
		t.Fatalf("restore = %+v", calls)
	}
	if got := f.desktop(); !reflect.DeepEqual(got, before) {
		t.Fatalf("restored desktop = %+v, want %+v", got, before)
	}
	if got := f.s.Snapshot(); got.Managed || got.AtGameMode || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestEnableFailureDoesNotStartActiveSession(t *testing.T) {
	for _, operation := range []string{"resolve", "layout", "targets", "test", "apply"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			failure := errors.New("display failed")
			f.display.setFailure(operation, failure)
			if err := f.s.Enable(); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if got := f.s.Snapshot(); got.Managed || got.AtGameMode || got.State != StateError || !errors.Is(got.Err, failure) {
				t.Fatalf("snapshot = %+v", got)
			}
			if f.clock.count() != 0 {
				t.Fatal("failed Enable started watcher")
			}
			calls := f.display.takeCalls()
			for _, call := range calls {
				if operation != "apply" && call.operation == "apply" {
					t.Fatal("applied after failure")
				}
			}
		})
	}
}

func TestShutdownRestoresOnlyWhenSessionAppliedMode(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "startup", true: "managed"}[managed], func(t *testing.T) {
			f := newFixture(t)
			if managed {
				if err := f.s.Enable(); err != nil {
					t.Fatal(err)
				}
			}
			f.display.takeCalls()
			if err := f.s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			calls := f.display.takeCalls()
			if managed {
				assertOperations(t, calls, "resolve", "layout", "targets", "test", "apply")
			} else if len(calls) != 0 {
				t.Fatalf("startup Shutdown touched display: %+v", calls)
			}
			if err := f.s.Shutdown(); err != nil {
				t.Fatal(err)
			}
			if len(f.display.takeCalls()) != 0 {
				t.Fatal("repeated Shutdown changed display")
			}
		})
	}
}

func TestManualDisableWinsOverPendingAutomaticRestore(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})
	f.poll(t, 0, 2, processResult{})
	f.display.takeCalls()
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets", "test", "apply")
	if !f.clock.tickers[0].isStopped() {
		t.Fatal("Disable returned before watcher stopped")
	}
	f.clock.tick(0, time.Unix(110, 0))
	if got := f.s.Snapshot(); got.Managed || got.CurrentMode != f.original {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestDisableUsesFallbackWhenAppStartsInAnUnmanagedGameMode(t *testing.T) {
	f := newFixture(t)
	f.setDesktop(f.profile.GameMode)
	got, err := f.s.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if !got.AtGameMode || got.Managed || f.clock.count() != 0 {
		t.Fatalf("startup = %+v", got)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "current")
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout")
	if f.s.Snapshot().Managed || f.clock.count() != 0 {
		t.Fatal("Enable adopted unmanaged game mode")
	}
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "layout", "test", "apply")
	if calls[2].mode != *f.profile.FallbackMode || calls[3].mode != *f.profile.FallbackMode {
		t.Fatalf("fallback = %+v", calls)
	}
	// Widening the target again would land on its neighbour, so the fallback has to
	// plan the desktop as much as the managed path does.
	if got := f.desktop(); !reflect.DeepEqual(got, fixtureLayout(*f.profile.FallbackMode)) {
		t.Fatalf("fallback desktop = %+v", got)
	}
}

func TestRefreshAndShutdownLeaveUnmanagedGameModeUntouched(t *testing.T) {
	f := newFixture(t)
	f.setDesktop(f.profile.GameMode)
	if got, err := f.s.Refresh(); err != nil || !got.AtGameMode || got.Managed {
		t.Fatalf("snapshot = %+v, err = %v", got, err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "current")
	if err := f.s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if len(f.display.takeCalls()) != 0 || f.clock.count() != 0 {
		t.Fatal("startup mutated mode or started watcher")
	}
	select {
	case <-f.checker.called:
		t.Fatal("startup read processes")
	default:
	}
}

func TestManualDisableFreshReadsInsteadOfTrustingStaleSnapshot(t *testing.T) {
	f := newFixture(t)
	f.setDesktop(f.profile.GameMode)
	f.s.Refresh()
	f.display.takeCalls()
	f.setDesktop(f.original)
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout")
	if f.s.Snapshot().AtGameMode {
		t.Fatal("stale observed mode retained")
	}
}

// The UI disables the mode toggle by testing Snapshot.Err with errors.Is, so every
// wrapper between display.Controller and Snapshot.Err must preserve the sentinel.
func TestEnableKeepsUnsupportedModeSentinelInSnapshotErr(t *testing.T) {
	f := newFixture(t)
	rejection := fmt.Errorf("%w: ChangeDisplaySettingsExW: display mode is not supported",
		display.ErrModeNotSupported)
	f.display.setFailure("test", rejection)

	err := f.s.Enable()
	if !errors.Is(err, display.ErrModeNotSupported) {
		t.Fatalf("Enable error = %v, lost display.ErrModeNotSupported", err)
	}
	got := f.s.Snapshot()
	if got.State != StateError || !errors.Is(got.Err, display.ErrModeNotSupported) {
		t.Fatalf("snapshot = %+v, Err lost display.ErrModeNotSupported", got)
	}
	if got.Managed || got.AtGameMode {
		t.Fatalf("snapshot = %+v", got)
	}
}

// Resolve failures must keep reaching the UI as display.ErrTargetNotFound.
func TestRefreshKeepsTargetNotFoundSentinelInSnapshotErr(t *testing.T) {
	f := newFixture(t)
	f.display.setFailure("resolve", fmt.Errorf("%w: %s", display.ErrTargetNotFound, f.profile.Monitor.HardwareID))

	got, err := f.s.Refresh()
	if !errors.Is(err, display.ErrTargetNotFound) {
		t.Fatalf("Refresh error = %v, lost display.ErrTargetNotFound", err)
	}
	if got.State != StateError || !errors.Is(got.Err, display.ErrTargetNotFound) {
		t.Fatalf("snapshot = %+v, Err lost display.ErrTargetNotFound", got)
	}
}

// The delayed restore is the only display change the user never triggers directly,
// so it must run once, no earlier than the profile delay, and with the saved mode.
func TestGameExitRestoresSavedModeAfterDelay(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()

	if got := f.poll(t, 0, 1, processResult{running: true}); got.State != StateGameRunning {
		t.Fatalf("running snapshot = %+v", got)
	}
	for _, second := range []int{2, 3, 4} {
		if got := f.poll(t, 0, second, processResult{}); got.State != StateRestorePending {
			t.Fatalf("at %ds: snapshot = %+v", second, got)
		}
	}
	if calls := f.display.takeCalls(); len(calls) != 0 {
		t.Fatalf("display changed before the delay elapsed: %+v", calls)
	}

	got := f.poll(t, 0, 5, processResult{})
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "layout", "targets", "test", "apply")
	if calls[3].mode != f.original || calls[4].mode != f.original {
		t.Fatalf("automatic restore used the wrong mode: %+v", calls)
	}
	if got.Managed || got.AtGameMode || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("restored snapshot = %+v", got)
	}
	if !f.clock.tickers[0].isStopped() {
		t.Fatal("watcher kept running after the automatic restore")
	}
}

// A game that reappears inside the restore window cancels the pending restore and
// restarts the delay from the new absence.
func TestGameReturningDuringRestoreWindowCancelsRestore(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()

	f.poll(t, 0, 1, processResult{running: true})
	f.poll(t, 0, 2, processResult{})
	if got := f.poll(t, 0, 3, processResult{running: true}); got.State != StateGameRunning {
		t.Fatalf("returning game snapshot = %+v", got)
	}
	// The original deadline was 5s; nothing may restore at or after it.
	for _, second := range []int{4, 5, 6} {
		if got := f.poll(t, 0, second, processResult{}); got.State != StateRestorePending {
			t.Fatalf("at %ds: snapshot = %+v", second, got)
		}
	}
	if calls := f.display.takeCalls(); len(calls) != 0 {
		t.Fatalf("cancelled restore still changed the display: %+v", calls)
	}

	got := f.poll(t, 0, 7, processResult{})
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets", "test", "apply")
	if got.Managed || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("restored snapshot = %+v", got)
	}
}

// The configured mode stays applied for as long as the game never shows up: the
// watcher only ever arms a restore after it has seen the process at least once.
func TestGameNeverAppearingLeavesModeUnchanged(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()

	for _, second := range []int{1, 2, 3, 4, 5, 10, 100} {
		if got := f.poll(t, 0, second, processResult{}); got.State != StateWaitingForGame {
			t.Fatalf("at %ds: snapshot = %+v", second, got)
		}
	}
	if calls := f.display.takeCalls(); len(calls) != 0 {
		t.Fatalf("unseen game triggered a display change: %+v", calls)
	}
	if got := f.s.Snapshot(); !got.Managed || !got.AtGameMode {
		t.Fatalf("snapshot = %+v", got)
	}
}

// A restore the user asked for can fail transiently. Ownership survives, so the
// poll loop has to survive with it: the user dismisses the dialog, keeps playing,
// and the automatic restore is what finally puts the display back.
func TestFailedManualRestoreKeepsTheWatcherRunning(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})

	failure := errors.New("driver refused the mode change")
	f.display.setFailure("apply", failure)
	if err := f.s.Disable(); !errors.Is(err, failure) {
		t.Fatalf("Disable error = %v", err)
	}
	got := f.s.Snapshot()
	if !got.Managed || got.State != StateError {
		t.Fatalf("failed restore dropped ownership: %+v", got)
	}
	if got.AutoRestoreFailures != 0 {
		t.Fatalf("a restore the user triggered was counted as automatic: %+v", got)
	}
	if f.clock.count() != 2 {
		t.Fatalf("watchers started = %d, want a replacement after the failed restore", f.clock.count())
	}
	if !f.clock.tickers[0].isStopped() {
		t.Fatal("the superseded watcher was left running")
	}

	// The replacement watcher must still drive the automatic restore end to end.
	f.display.setFailure("apply", nil)
	f.display.takeCalls()
	if got := f.poll(t, 1, 2, processResult{running: true}); got.State != StateGameRunning {
		t.Fatalf("replacement watcher is not polling: %+v", got)
	}
	f.poll(t, 1, 3, processResult{})
	f.poll(t, 1, 4, processResult{})

	got = f.poll(t, 1, 6, processResult{})
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "layout", "targets", "test", "apply")
	if calls[4].mode != f.original {
		t.Fatalf("automatic restore used the wrong mode: %+v", calls)
	}
	if got.Managed || got.AtGameMode || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("restored snapshot = %+v", got)
	}
}

// Exit runs the same restore. Refusing to close on a failure is only safe if the
// session the user keeps using is still watching the game.
func TestFailedShutdownRestoreKeepsTheWatcherRunning(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})

	failure := errors.New("driver refused the mode change")
	f.display.setFailure("apply", failure)
	if err := f.s.Shutdown(); !errors.Is(err, failure) {
		t.Fatalf("Shutdown error = %v", err)
	}
	if got := f.s.Snapshot(); !got.Managed || got.AutoRestoreFailures != 0 {
		t.Fatalf("snapshot = %+v", got)
	}
	if f.clock.count() != 2 {
		t.Fatalf("watchers started = %d, want a replacement after the failed restore", f.clock.count())
	}
	if !f.clock.tickers[0].isStopped() {
		t.Fatal("the superseded watcher was left running")
	}
	if got := f.poll(t, 1, 2, processResult{running: true}); got.State != StateGameRunning {
		t.Fatalf("replacement watcher is not polling: %+v", got)
	}

	f.display.setFailure("apply", nil)
	f.display.takeCalls()
	if err := f.s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets", "test", "apply")
}

// Nothing prompts the user when the delayed restore fails, so the session records
// the failure for the UI to announce. It must count one failure per restore, not
// one per poll, and it must keep watching so a later game exit can try again.
// TestTheKeepAliveWatcherRetriesByItselfAfterAFullDelay covers the one behaviour that
// changed outside the scaling cycle. The replacement watcher now starts from a tracker
// seeded with gameSeen, so a failed automatic restore is retried after one further full
// delay instead of waiting for the game to be seen running a second time -- which is
// what ensureWatcher's comment has always promised. fired is still not carried over, so
// the two attempts are always a whole delay apart rather than one poll apart.
func TestTheKeepAliveWatcherRetriesByItselfAfterAFullDelay(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})
	f.poll(t, 0, 2, processResult{})

	failure := errors.New("driver refused the mode change")
	f.display.setFailure("apply", failure)
	f.tick(t, 0, 5, processResult{})
	f.waitSnapshot(t, func(snapshot Snapshot) bool { return snapshot.AutoRestoreFailures > 0 })
	f.display.setFailure("apply", nil)
	f.display.takeCalls()

	// The game is never seen running again on this watcher.
	if got := f.poll(t, 1, 6, processResult{}); got.State != StateRestorePending {
		t.Fatalf("the replacement watcher did not arm itself: %+v", got)
	}
	if got := f.poll(t, 1, 8, processResult{}); got.State != StateRestorePending {
		t.Fatalf("retried before the full delay elapsed: %+v", got)
	}
	if calls := f.display.takeCalls(); len(calls) != 0 {
		t.Fatalf("retried before the full delay elapsed: %+v", calls)
	}
	got := f.poll(t, 1, 9, processResult{})
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets", "test", "apply")
	if got.Managed || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("the retry did not restore: %+v", got)
	}
	if got.AutoRestoreFailures != 1 {
		t.Fatalf("failures = %d, want the one failed attempt", got.AutoRestoreFailures)
	}
}

func TestFailedAutomaticRestoreIsCountedOnceAndKeepsTheWatcherRunning(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})
	f.poll(t, 0, 2, processResult{})

	failure := errors.New("driver refused the mode change")
	f.display.setFailure("apply", failure)
	f.display.takeCalls()

	f.tick(t, 0, 5, processResult{})
	got := f.waitSnapshot(t, func(snapshot Snapshot) bool { return snapshot.AutoRestoreFailures > 0 })
	if got.AutoRestoreFailures != 1 {
		t.Fatalf("failures = %d, want exactly one", got.AutoRestoreFailures)
	}
	if got.State != StateError || !errors.Is(got.Err, failure) {
		t.Fatalf("snapshot = %+v", got)
	}
	if !got.Managed {
		t.Fatalf("failed automatic restore dropped ownership: %+v", got)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets", "test", "apply")
	if f.clock.count() != 2 {
		t.Fatalf("watchers started = %d, want a replacement after the failed restore", f.clock.count())
	}

	// The game is still gone. Polling it must not retry the restore, or the UI
	// would raise one notification per second. The replacement watcher inherits the
	// seen flag and nothing else, so it counts down from this first absent poll rather
	// than firing again immediately.
	for _, second := range []int{6, 7, 8} {
		if got := f.poll(t, 1, second, processResult{}); got.State != StateRestorePending {
			t.Fatalf("at %ds: snapshot = %+v", second, got)
		}
	}
	if calls := f.display.takeCalls(); len(calls) != 0 {
		t.Fatalf("the replacement watcher retried the restore on every poll: %+v", calls)
	}
	if got := f.s.Snapshot(); got.AutoRestoreFailures != 1 {
		t.Fatalf("failures = %d, want one notification per failed restore", got.AutoRestoreFailures)
	}

	// A later game exit restores through the replacement watcher.
	f.display.setFailure("apply", nil)
	f.poll(t, 1, 9, processResult{running: true})
	f.poll(t, 1, 10, processResult{})
	got = f.poll(t, 1, 13, processResult{})
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets", "test", "apply")
	if got.Managed || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("restored snapshot = %+v", got)
	}
}

// The defect this session had to fix: narrowing the target left a gap its
// neighbours never closed, and the mouse could not cross it. Enable now moves every
// display that sits after the target, and the primary target itself stays put.
func TestEnableClosesTheGapItWouldOtherwiseOpen(t *testing.T) {
	f := newFixture(t)
	before := f.desktop()

	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	want := fixtureLayout(f.profile.GameMode)
	if got := f.desktop(); !reflect.DeepEqual(got, want) {
		t.Fatalf("desktop = %+v, want %+v", got, want)
	}
	moved := 0
	for _, display := range f.desktop().Displays {
		was, _ := before.Find(display.DeviceName)
		if display.DeviceName == targetDevice {
			if display.Position != (domain.Point{}) {
				t.Fatalf("the primary target moved to %+v", display.Position)
			}
			continue
		}
		if display.Position != was.Position {
			moved++
		}
	}
	if moved != len(before.Displays)-1 {
		t.Fatalf("displays moved = %d, want every display beside the target", moved)
	}
}

// Position is all a display that is not the target may give up, on the way in and
// on the way back out.
func TestEnableAndDisableNeverRewriteAnotherDisplayMode(t *testing.T) {
	f := newFixture(t)
	before := f.desktop()
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	plan := calls[len(calls)-1].plan
	if len(plan.Changes) != len(before.Displays) {
		t.Fatalf("plan = %+v", plan)
	}
	for _, change := range plan.Changes {
		if target := change.DeviceName == targetDevice; change.SetMode != target {
			t.Fatalf("%s: SetMode = %v", change.DeviceName, change.SetMode)
		}
	}
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	for _, display := range f.desktop().Displays {
		was, _ := before.Find(display.DeviceName)
		if display.Mode != was.Mode {
			t.Fatalf("%s mode = %+v, want the untouched %+v", display.DeviceName, display.Mode, was.Mode)
		}
	}
}

// A desktop the session cannot rearrange safely must leave it exactly as it is: no
// pre-flight, no apply, no ownership and no watcher, with the reason in the snapshot.
func TestEnableAbortsWithoutTouchingAnUnsafeDesktop(t *testing.T) {
	unsafeDesktops := map[string]domain.Layout{
		"no primary display": {Displays: []domain.DisplayState{
			{DeviceName: targetDevice, Mode: fixtureNative},
			{DeviceName: rightDevice, Mode: fixtureSide, Position: domain.Point{X: 3440}},
		}},
		"target was not read": {Displays: []domain.DisplayState{
			{DeviceName: rightDevice, Mode: fixtureSide, Primary: true},
		}},
		"a display was not read": {Displays: []domain.DisplayState{
			{DeviceName: targetDevice, Mode: fixtureNative, Primary: true},
			{DeviceName: rightDevice},
		}},
		"displays already overlap": {Displays: []domain.DisplayState{
			{DeviceName: targetDevice, Mode: fixtureNative, Primary: true},
			{DeviceName: rightDevice, Mode: fixtureSide, Position: domain.Point{X: 2000}},
		}},
	}
	for name, desktop := range unsafeDesktops {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.display.mu.Lock()
			f.display.layout = desktop
			f.display.mu.Unlock()

			err := f.s.Enable()
			if !errors.Is(err, display.ErrLayoutUnsafe) {
				t.Fatalf("Enable error = %v, want display.ErrLayoutUnsafe", err)
			}
			assertOperations(t, f.display.takeCalls(), "resolve", "layout")
			got := f.s.Snapshot()
			if got.State != StateError || !errors.Is(got.Err, display.ErrLayoutUnsafe) {
				t.Fatalf("snapshot = %+v", got)
			}
			if got.Managed || got.AtGameMode || f.clock.count() != 0 {
				t.Fatalf("an aborted Enable took ownership: %+v", got)
			}
			if !reflect.DeepEqual(f.desktop(), desktop) {
				t.Fatal("an aborted Enable changed the desktop")
			}
		})
	}
}

// Saved coordinates belong to the arrangement they were read from. If the target is
// no longer part of it the displays have been renumbered, so the restore aborts with
// the mode still applied and still owned, rather than moving displays onto each other.
func TestRestoreAbortsWhenTheSavedArrangementNoLongerFitsTheDesktop(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	applied := f.desktop()
	f.display.takeCalls()
	f.display.mu.Lock()
	f.display.target.DeviceName = `\.\DISPLAY7`
	f.display.mu.Unlock()

	err := f.s.Disable()
	if !errors.Is(err, display.ErrLayoutUnsafe) {
		t.Fatalf("Disable error = %v, want display.ErrLayoutUnsafe", err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve")
	if !reflect.DeepEqual(f.desktop(), applied) {
		t.Fatal("an aborted restore changed the desktop")
	}
	if got := f.s.Snapshot(); !got.Managed || got.State != StateError {
		t.Fatalf("an aborted restore dropped ownership: %+v", got)
	}

	// Ownership survived, so once the numbering matches the saved arrangement again
	// the restore the session still owes the user runs to completion.
	f.display.mu.Lock()
	f.display.target.DeviceName = targetDevice
	f.display.mu.Unlock()
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	if got := f.desktop(); !reflect.DeepEqual(got, fixtureLayout(f.original)) {
		t.Fatalf("restored desktop = %+v", got)
	}
}

// Every DISPLAYn from the saved layout can still exist after Windows swaps the names
// of two physical monitors. The freshly resolved target must remain bound to the name
// it owned when Enable captured the layout; membership alone cannot prove that.
func TestRestoreRefusesWhenTheResolvedTargetNowUsesAnotherSavedDeviceName(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()
	applied := f.desktop()
	f.display.mu.Lock()
	f.display.target.DeviceName = rightDevice
	f.display.mu.Unlock()

	err := f.s.Disable()
	if !errors.Is(err, display.ErrLayoutUnsafe) {
		t.Fatalf("Disable error = %v, want display.ErrLayoutUnsafe", err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve")
	if got := f.s.Snapshot(); !got.Managed || got.State != StateError {
		t.Fatalf("an aborted restore dropped ownership: %+v", got)
	}
	if !reflect.DeepEqual(f.desktop(), applied) {
		t.Fatal("an aborted restore changed the desktop")
	}

	f.display.mu.Lock()
	f.display.target.DeviceName = targetDevice
	f.display.mu.Unlock()
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
}

// The configured target can keep its DISPLAYn while two neighbours exchange theirs.
// Device-set equality cannot detect that swap; the saved per-device identities must.
func TestRestoreRefusesWhenNeighboursExchangeSavedDeviceNames(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()
	applied := f.desktop()

	f.display.mu.Lock()
	left, right := -1, -1
	for i, target := range f.display.targets {
		switch target.DeviceName {
		case rightDevice:
			left = i
		case topRight:
			right = i
		}
	}
	if left < 0 || right < 0 {
		f.display.mu.Unlock()
		t.Fatal("fixture is missing neighbour targets")
	}
	f.display.targets[left].Identity, f.display.targets[right].Identity =
		f.display.targets[right].Identity, f.display.targets[left].Identity
	f.display.mu.Unlock()

	err := f.s.Disable()
	if !errors.Is(err, display.ErrLayoutUnsafe) {
		t.Fatalf("Disable error = %v, want display.ErrLayoutUnsafe", err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets")
	if got := f.s.Snapshot(); !got.Managed || got.State != StateError {
		t.Fatalf("an aborted restore dropped ownership: %+v", got)
	}
	if !reflect.DeepEqual(f.desktop(), applied) {
		t.Fatal("an aborted restore changed the desktop")
	}

	f.display.mu.Lock()
	f.display.targets[left].Identity, f.display.targets[right].Identity =
		f.display.targets[right].Identity, f.display.targets[left].Identity
	f.display.mu.Unlock()
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
}

func TestEnableRefusesAnUntrustworthyOrAmbiguousDisplayBinding(t *testing.T) {
	tests := map[string]func(*fakeDisplay){
		"a display has no stable identity": func(d *fakeDisplay) {
			d.targets[1].Identity = domain.MonitorIdentity{}
		},
		"two device names carry the same fallback identity": func(d *fakeDisplay) {
			d.targets[0].Identity.InstancePath = ""
			d.targets[1].Identity.InstancePath = ""
			d.targets[1].Identity.HardwareID = d.targets[0].Identity.HardwareID
		},
		"one device name maps to several monitor rows": func(d *fakeDisplay) {
			duplicate := d.targets[1]
			duplicate.Identity.InstancePath += ":clone"
			d.targets = append(d.targets, duplicate)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.display.mu.Lock()
			mutate(f.display)
			before := f.display.layout
			f.display.mu.Unlock()

			err := f.s.Enable()
			if !errors.Is(err, display.ErrLayoutUnsafe) {
				t.Fatalf("Enable error = %v, want display.ErrLayoutUnsafe", err)
			}
			assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets")
			if got := f.s.Snapshot(); got.Managed || got.State != StateError {
				t.Fatalf("refused binding took ownership: %+v", got)
			}
			if !reflect.DeepEqual(f.desktop(), before) {
				t.Fatal("refused binding changed the desktop")
			}
		})
	}
}

// EnumDisplayDevicesW may fail to provide an interface path. An exact, unique full
// hardware ID still gives the session a conservative binding for this managed cycle.
func TestManagedRestoreUsesUniqueExactHardwareIDsWhenPathsAreUnavailable(t *testing.T) {
	f := newFixture(t)
	f.display.mu.Lock()
	for i := range f.display.targets {
		f.display.targets[i].Identity.InstancePath = ""
	}
	f.display.target.Identity.InstancePath = ""
	f.display.mu.Unlock()

	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRestoreIgnoresLabelsAndInstancePathCasing(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.mu.Lock()
	for i := range f.display.targets {
		f.display.targets[i].Identity.InstancePath = strings.ToUpper(f.display.targets[i].Identity.InstancePath)
		f.display.targets[i].Identity.Label = "a newly reported friendly name"
	}
	f.display.target.Identity.InstancePath = strings.ToUpper(f.display.target.Identity.InstancePath)
	f.display.target.Identity.Label = "a newly reported friendly name"
	f.display.mu.Unlock()

	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
}

// Restore preserves a neighbour's current mode. If that mode grew while managed,
// the saved coordinates must be checked against its new size before TestMode or any
// native layout apply is attempted.
func TestRestoreRefusesSavedCoordinatesThatOverlapACurrentNeighbourMode(t *testing.T) {
	original := domain.Mode{Width: 100, Height: 100, RefreshHz: 60, BitsPerPixel: 32}
	game := domain.Mode{Width: 80, Height: 100, RefreshHz: 60, BitsPerPixel: 32}
	side := original
	desktop := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: targetDevice, Mode: original, Position: domain.Point{}, Primary: true},
		{DeviceName: rightDevice, Mode: side, Position: domain.Point{X: 100}},
		{DeviceName: topRight, Mode: side, Position: domain.Point{X: 200}},
	}}
	f := newFixtureWith(t, profileWithGameMode(game), original, desktop)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()

	grown := domain.Mode{Width: 200, Height: 100, RefreshHz: 60, BitsPerPixel: 32}
	f.display.mu.Lock()
	f.display.layout = domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: targetDevice, Mode: game, Position: domain.Point{}, Primary: true},
		{DeviceName: rightDevice, Mode: grown, Position: domain.Point{X: 80}},
		{DeviceName: topRight, Mode: side, Position: domain.Point{X: 280}},
	}}
	unsafeCurrent := f.display.layout
	f.display.mu.Unlock()

	err := f.s.Disable()
	if !errors.Is(err, display.ErrLayoutUnsafe) {
		t.Fatalf("Disable error = %v, want display.ErrLayoutUnsafe", err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets")
	if got := f.s.Snapshot(); !got.Managed || got.State != StateError {
		t.Fatalf("an aborted restore dropped ownership: %+v", got)
	}
	if !reflect.DeepEqual(f.desktop(), unsafeCurrent) {
		t.Fatal("an aborted restore changed the desktop")
	}

	// Put the external neighbour change back. The retained ownership can now finish
	// the same restore without rebuilding or guessing at a different snapshot.
	f.display.mu.Lock()
	f.display.layout = domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: targetDevice, Mode: game, Position: domain.Point{}, Primary: true},
		{DeviceName: rightDevice, Mode: side, Position: domain.Point{X: 80}},
		{DeviceName: topRight, Mode: side, Position: domain.Point{X: 180}},
	}}
	f.display.mu.Unlock()
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
}

// fixtureLarger is wider and taller than the desktop's native mode. Every session
// test above this line narrows the target, because the built-in profile was the
// only profile there was; the moment a picker lists what the monitor reports, most
// of what it lists is larger than the mode the monitor is running.
var fixtureLarger = domain.Mode{Width: 3840, Height: 1600, RefreshHz: 120, BitsPerPixel: 32}

// profileWithGameMode is the configuration a user produces as soon as they can
// choose: the shipped monitor and watched process, and a mode of their own.
func profileWithGameMode(mode domain.Mode) domain.Profile {
	profile := domain.LegacySeedProfile()
	profile.GameMode = mode
	return profile
}

// assertNoOverlappingDisplays holds the desktop to the property the planning layer
// exists for. Comparing coordinates alone would still pass if the target had grown
// straight over the display beside it.
func assertNoOverlappingDisplays(t *testing.T, layout domain.Layout) {
	t.Helper()
	for i, a := range layout.Displays {
		for _, b := range layout.Displays[i+1:] {
			if a.Position.X < b.Position.X+int32(b.Mode.Width) &&
				b.Position.X < a.Position.X+int32(a.Mode.Width) &&
				a.Position.Y < b.Position.Y+int32(b.Mode.Height) &&
				b.Position.Y < a.Position.Y+int32(a.Mode.Height) {
				t.Fatalf("%s %+v and %s %+v overlap", a.DeviceName, a, b.DeviceName, b)
			}
		}
	}
}

// The whole enable path with a mode that is larger than the one the monitor is
// running: the neighbours have to be pushed outward before the target can occupy
// the space, and the desktop that comes out of it is the contiguous one.
func TestEnableAppliesALargerGameModeWithoutOverlappingTheNeighbours(t *testing.T) {
	f := newFixtureWith(t, profileWithGameMode(fixtureLarger), fixtureNative, fixtureLayout(fixtureNative))
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "layout", "targets", "test", "apply")
	if calls[3].mode != fixtureLarger || calls[4].mode != fixtureLarger {
		t.Fatalf("tested and applied modes = %+v", calls)
	}
	desktop := f.desktop()
	if !reflect.DeepEqual(desktop, fixtureLayout(fixtureLarger)) {
		t.Fatalf("desktop = %+v, want %+v", desktop, fixtureLayout(fixtureLarger))
	}
	// Said directly rather than left to the comparison: the display beside the
	// target is exactly one target-width away, so the target grew into space that
	// was made for it instead of onto its neighbour.
	right, ok := desktop.Find(rightDevice)
	if !ok || right.Position.X != int32(fixtureLarger.Width) {
		t.Fatalf("%s sits at %+v, want x=%d", rightDevice, right.Position, fixtureLarger.Width)
	}
	assertNoOverlappingDisplays(t, desktop)
	if got := f.s.Snapshot(); !got.Managed || !got.AtGameMode ||
		got.CurrentMode != fixtureLarger || got.State != StateWaitingForGame {
		t.Fatalf("snapshot = %+v", got)
	}
	if f.clock.count() != 1 {
		t.Fatal("Enable must start one watcher")
	}
}

// A picked mode can ask for an arrangement that cannot be made safe, and this is
// the shape that does it: a mode that grows one axis while it gives the other back
// closes the gap the display below is standing in. The session must refuse before
// it tests or applies anything, keep the desktop exactly as it found it, and say
// which two displays collided -- that message is now something a user reads.
func TestEnableAbortsWhenALargerModeHasNoSafeArrangement(t *testing.T) {
	original := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	portrait := domain.Mode{Width: 1440, Height: 2560, RefreshHz: 60, BitsPerPixel: 32}
	desktop := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: targetDevice, Mode: original, Position: domain.Point{}, Primary: true},
		{DeviceName: rightDevice, Mode: portrait, Position: domain.Point{X: 1920, Y: 0}},
		{DeviceName: belowDevice, Mode: fixtureTop, Position: domain.Point{X: 1920, Y: 2560}},
	}}
	wider := domain.Mode{Width: 2560, Height: 1080, RefreshHz: 120, BitsPerPixel: 32}
	f := newFixtureWith(t, profileWithGameMode(wider), original, desktop)

	err := f.s.Enable()
	if !errors.Is(err, display.ErrLayoutUnsafe) {
		t.Fatalf("err = %v, want ErrLayoutUnsafe", err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout")
	if got := f.desktop(); !reflect.DeepEqual(got, desktop) {
		t.Fatalf("a refused enable changed the desktop: %+v", got)
	}
	got := f.s.Snapshot()
	if got.Managed || got.State != StateError || !errors.Is(got.Err, display.ErrLayoutUnsafe) {
		t.Fatalf("snapshot = %+v", got)
	}
	for _, device := range []string{rightDevice, belowDevice} {
		if !strings.Contains(got.Message, device) {
			t.Fatalf("message = %q, which never names %s", got.Message, device)
		}
	}
	if f.clock.count() != 0 {
		t.Fatal("a refused enable started a watcher")
	}
}

// The other half of the promise, from a larger mode: the saved arrangement was
// captured around a target that then grew, so restoring it has to narrow the target
// and walk every neighbour back in. The desktop the user gets back is the one they
// started with.
func TestDisableReturnsTheDesktopFromALargerGameMode(t *testing.T) {
	f := newFixtureWith(t, profileWithGameMode(fixtureLarger), fixtureNative, fixtureLayout(fixtureNative))
	before := f.desktop()
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()

	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "layout", "targets", "test", "apply")
	if calls[3].mode != f.original || calls[4].mode != f.original {
		t.Fatalf("restore = %+v", calls)
	}
	if got := f.desktop(); !reflect.DeepEqual(got, before) {
		t.Fatalf("restored desktop = %+v, want %+v", got, before)
	}
	assertNoOverlappingDisplays(t, f.desktop())
	if got := f.s.Snapshot(); got.Managed || got.AtGameMode ||
		got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("snapshot = %+v", got)
	}
}

// ---------------------------------------------------------------------------
// The session runs on the profile the user configured, not on the one the tool
// shipped with. Everything below this line is about a configuration the shipped
// build could not express: a mode that is not 4:3, no fallback mode at all, and
// no watched process.
// ---------------------------------------------------------------------------

// fourByThreeMode and wideGameMode are deliberately the wrong way round. The session
// used to call its applied state "FourByThree", and the only way to show that name
// was an assumption rather than a description is to configure a game mode with a
// different shape and start from a desktop that has the old one.
var (
	fourByThreeMode = domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	wideGameMode    = domain.Mode{Width: 2560, Height: 1080, RefreshHz: 120, BitsPerPixel: 32}
)

// profileWithoutFallback records no fallback mode, which is what the settings dialog
// writes unless the user overrides it: the mode a restore goes back to is derived
// from what the monitor reports at the moment it is needed, never frozen into a file
// written on another machine.
func profileWithoutFallback(gameMode domain.Mode) domain.Profile {
	profile := profileWithGameMode(gameMode)
	profile.FallbackMode = nil
	return profile
}

// profileWithoutProcess watches nothing. It is a legitimate configuration -- the user
// toggles by hand -- and not a half-finished one.
func profileWithoutProcess() domain.Profile {
	profile := domain.LegacySeedProfile()
	profile.ProcessName = ""
	return profile
}

// setModes replaces what the fake monitor reports it can run.
func (f *sessionFixture) setModes(modes ...domain.Mode) {
	f.display.mu.Lock()
	defer f.display.mu.Unlock()
	f.display.modes = modes
}

// renameDisplay models a Windows renumbering: the same screen is still attached and
// still where it was, under a name the session has never seen.
func (f *sessionFixture) renameDisplay(from, to string) {
	f.display.mu.Lock()
	defer f.display.mu.Unlock()
	displays := append([]domain.DisplayState(nil), f.display.layout.Displays...)
	for i := range displays {
		if displays[i].DeviceName == from {
			displays[i].DeviceName = to
		}
	}
	for i := range f.display.targets {
		if f.display.targets[i].DeviceName == from {
			f.display.targets[i].DeviceName = to
		}
	}
	f.display.layout = domain.Layout{Displays: displays}
}

// The snapshot reports whether the desktop is at the configured mode. It is not a
// statement about the shape of that mode: a 4:3 desktop that is not what the profile
// asked for is not the applied state, and the mode the profile did ask for is, at
// whatever ratio the user picked.
func TestSnapshotReportsTheConfiguredModeNotAFourByThreeAssumption(t *testing.T) {
	f := newFixtureWith(t, profileWithGameMode(wideGameMode), fourByThreeMode, fixtureLayout(fourByThreeMode))

	got, err := f.s.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if got.AtGameMode || got.CurrentMode != fourByThreeMode {
		t.Fatalf("a 4:3 desktop that is not the configured mode reported AtGameMode: %+v", got)
	}

	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	got = f.s.Snapshot()
	if !got.AtGameMode || got.CurrentMode != wideGameMode || !got.Managed {
		t.Fatalf("snapshot = %+v", got)
	}
	if !strings.Contains(got.Message, domain.ModeLabel(wideGameMode)) {
		t.Fatalf("message = %q, which never names the configured mode", got.Message)
	}
	for _, shipped := range []string{"4:3", "2K", "1920", "1440"} {
		if strings.Contains(got.Message, shipped) {
			t.Fatalf("message = %q, which still describes the profile the tool shipped with", got.Message)
		}
	}
	assertNoOverlappingDisplays(t, f.desktop())
}

// A profile with no fallback mode restores to the monitor's own native mode, worked
// out from the modes it reports rather than remembered from the machine this tool was
// written for.
func TestDisableDerivesTheFallbackFromTheEnumeratedModesWhenNoneIsConfigured(t *testing.T) {
	profile := profileWithoutFallback(domain.LegacySeedProfile().GameMode)
	f := newFixtureWith(t, profile, fixtureNative, fixtureLayout(fixtureNative))
	// What the monitor reports, deliberately unordered and with smaller modes in it.
	f.setModes(fixtureSide, fixtureTop, fixtureNative, profile.GameMode)
	f.setDesktop(profile.GameMode)

	got, err := f.s.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "current", "modes")
	if !got.FallbackKnown || got.FallbackMode != fixtureNative || got.FallbackReason != "" {
		t.Fatalf("snapshot = %+v, want the derived %+v", got, fixtureNative)
	}

	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "layout", "modes", "test", "apply")
	if calls[3].mode != fixtureNative || calls[4].mode != fixtureNative {
		t.Fatalf("restore = %+v, want the derived fallback %+v", calls, fixtureNative)
	}
	if desktop := f.desktop(); !reflect.DeepEqual(desktop, fixtureLayout(fixtureNative)) {
		t.Fatalf("restored desktop = %+v, want %+v", desktop, fixtureLayout(fixtureNative))
	}
}

// The two ways deriving a fallback can fail are different events and have to read
// differently: a monitor that could not be asked at all, and a monitor that answered
// with nothing this tool can apply. Neither is allowed to become a guess.
func TestDisableRefusesAndExplainsWhenTheFallbackCannotBeDerived(t *testing.T) {
	enumerationFailed := errors.New("EnumDisplaySettingsW: the monitor stopped answering")
	cases := map[string]struct {
		arrange func(*sessionFixture)
		says    string
		omits   string
	}{
		// The enumeration itself failed. The monitor resolved a moment ago and has
		// gone since, so the message carries the driver's own words.
		"the monitor could not be asked": {
			arrange: func(f *sessionFixture) { f.display.setFailure("modes", enumerationFailed) },
			says:    enumerationFailed.Error(),
			omits:   "沒有回報",
		},
		// The enumeration worked and everything in it was filtered out. Nothing is
		// broken; this monitor simply reports no mode this tool could apply.
		"the monitor reports no usable mode": {
			arrange: func(f *sessionFixture) { f.setModes() },
			says:    "沒有回報",
			omits:   enumerationFailed.Error(),
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			profile := profileWithoutFallback(domain.LegacySeedProfile().GameMode)
			f := newFixtureWith(t, profile, fixtureNative, fixtureLayout(fixtureNative))
			f.setDesktop(profile.GameMode)
			testCase.arrange(f)
			untouched := f.desktop()

			err := f.s.Disable()
			if !errors.Is(err, ErrFallbackUnknown) {
				t.Fatalf("Disable error = %v, want ErrFallbackUnknown", err)
			}
			assertOperations(t, f.display.takeCalls(), "resolve", "layout", "modes")

			got := f.s.Snapshot()
			if got.FallbackKnown || got.FallbackMode != (domain.Mode{}) {
				t.Fatalf("snapshot = %+v, want no fallback the UI could offer", got)
			}
			if !strings.Contains(got.FallbackReason, testCase.says) {
				t.Fatalf("reason = %q, which never says %q", got.FallbackReason, testCase.says)
			}
			if strings.Contains(got.FallbackReason, testCase.omits) {
				t.Fatalf("reason = %q, which reads like the other failure", got.FallbackReason)
			}
			if !reflect.DeepEqual(f.desktop(), untouched) {
				t.Fatal("a refused restore changed the desktop")
			}
		})
	}
	// The enumeration failure keeps the driver's error reachable, so a caller that
	// knows the sentinel can still tell a missing monitor from an empty catalogue.
	profile := profileWithoutFallback(domain.LegacySeedProfile().GameMode)
	f := newFixtureWith(t, profile, fixtureNative, fixtureLayout(fixtureNative))
	f.setDesktop(profile.GameMode)
	f.display.setFailure("modes", enumerationFailed)
	if err := f.s.Disable(); !errors.Is(err, enumerationFailed) {
		t.Fatalf("Disable error = %v, which lost the enumeration failure", err)
	}
}

// A profile that watches nothing must not start a poll loop. The process list is
// never read, and the state says the session is manual so nobody waits for a restore
// that was never armed.
func TestEnableStartsNoWatcherWhenNoProcessIsConfigured(t *testing.T) {
	f := newFixtureWith(t, profileWithoutProcess(), fixtureNative, fixtureLayout(fixtureNative))
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	if f.clock.count() != 0 {
		t.Fatalf("watchers started = %d, want none when nothing is watched", f.clock.count())
	}
	select {
	case name := <-f.checker.called:
		t.Fatalf("the process list was read for %q", name)
	default:
	}
	got := f.s.Snapshot()
	if !got.Managed || !got.AtGameMode {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.State != StateManualOnly {
		t.Fatalf("state = %q, want %q", got.State, StateManualOnly)
	}
	if got.Profile.ProcessName != "" {
		t.Fatalf("snapshot profile = %+v, want the empty process the user configured", got.Profile)
	}
}

// Manual is the whole point of that configuration, so the manual path has to be
// whole: the mode goes on, comes back off, and the desktop is the one it started as.
func TestManualDisableStillWorksWithNoProcessConfigured(t *testing.T) {
	f := newFixtureWith(t, profileWithoutProcess(), fixtureNative, fixtureLayout(fixtureNative))
	before := f.desktop()
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()

	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout", "targets", "test", "apply")
	if got := f.desktop(); !reflect.DeepEqual(got, before) {
		t.Fatalf("restored desktop = %+v, want %+v", got, before)
	}
	if got := f.s.Snapshot(); got.Managed || got.AtGameMode || got.State != StateNative {
		t.Fatalf("snapshot = %+v", got)
	}
	if f.clock.count() != 0 {
		t.Fatal("a manual-only session started a watcher")
	}
}

// A restore moves every display in the saved arrangement, not only the target, so
// every one of them has to still be attached. The target-only check let a renumbered
// neighbour through and the failure surfaced from inside the apply, naming nothing
// the user could act on.
func TestRestoreNamesTheNeighbourThatIsNoLongerInTheSavedLayout(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()
	f.renameDisplay(rightDevice, `\.\DISPLAY9`)
	applied := f.desktop()

	err := f.s.Disable()
	if !errors.Is(err, display.ErrLayoutUnsafe) {
		t.Fatalf("Disable error = %v, want display.ErrLayoutUnsafe", err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "layout")
	got := f.s.Snapshot()
	if !strings.Contains(got.Message, rightDevice) {
		t.Fatalf("message = %q, which never names the display that is no longer attached", got.Message)
	}
	if !got.Managed || got.State != StateError {
		t.Fatalf("an aborted restore dropped ownership: %+v", got)
	}
	if !reflect.DeepEqual(f.desktop(), applied) {
		t.Fatal("an aborted restore changed the desktop")
	}

	// The numbering comes back and so does the restore the session still owes.
	f.renameDisplay(`\.\DISPLAY9`, rightDevice)
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	if got := f.desktop(); !reflect.DeepEqual(got, fixtureLayout(f.original)) {
		t.Fatalf("restored desktop = %+v", got)
	}
}

// Every string the window shows is generated from the profile, so the profile has to
// reach the window -- and it travels in the snapshot rather than through a reference
// to the session, which the window is not allowed to hold.
func TestSnapshotCarriesTheProfileSoTheUINeverInventsAString(t *testing.T) {
	profile := profileWithGameMode(wideGameMode)
	f := newFixtureWith(t, profile, fixtureNative, fixtureLayout(fixtureNative))

	got := f.s.Snapshot()
	if !reflect.DeepEqual(got.Profile, profile) {
		t.Fatalf("profile = %+v, want %+v", got.Profile, profile)
	}
	if got.MatchedBy != domain.MatchNone {
		t.Fatalf("MatchedBy = %v before anything was resolved", got.MatchedBy)
	}
	// The fallback is a pointer now. A snapshot that handed out the session's own
	// pointer would let a renderer reach through it and change what a restore applies.
	if got.Profile.FallbackMode == profile.FallbackMode {
		t.Fatal("the snapshot shares the session's fallback mode pointer")
	}

	if _, err := f.s.Refresh(); err != nil {
		t.Fatal(err)
	}
	got = f.s.Snapshot()
	if !reflect.DeepEqual(got.Profile, profile) {
		t.Fatalf("profile = %+v, want %+v", got.Profile, profile)
	}
	if got.MatchedBy != domain.MatchHardwareID || got.MatchedBy != got.Target.MatchedBy {
		t.Fatalf("MatchedBy = %v, want the rung the resolved target reported (%v)", got.MatchedBy, got.Target.MatchedBy)
	}
	if !got.FallbackKnown || got.FallbackMode != *profile.FallbackMode {
		t.Fatalf("snapshot = %+v, want the configured fallback %+v", got, *profile.FallbackMode)
	}
}
