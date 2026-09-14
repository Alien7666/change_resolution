package app

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
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
	current domain.Mode
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

// Targets is the public monitor list the settings dialog reads. The session never
// calls it -- it resolves one configured identity rather than browsing monitors --
// and recording the call is what lets a test say so.
func (d *fakeDisplay) Targets() ([]domain.Target, error) {
	err := d.record("targets", domain.Target{}, domain.Mode{}, domain.LayoutPlan{})
	d.mu.Lock()
	defer d.mu.Unlock()
	return []domain.Target{d.target}, err
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
	profile := domain.LegacySeedProfile()
	original := fixtureNative
	displays := &fakeDisplay{
		target:  domain.Target{DeviceName: targetDevice, Identity: domain.MonitorIdentity{HardwareID: profile.Monitor.HardwareID}},
		current: original, layout: fixtureLayout(original), fail: make(map[string]error),
	}
	checker := &fakeChecker{called: make(chan string, 8), results: make(chan processResult), closed: make(chan struct{})}
	clock := &fakeClock{now: time.Unix(100, 0)}
	f := &sessionFixture{display: displays, checker: checker, clock: clock, profile: profile, original: original, changes: make(chan Snapshot, 128)}
	f.s = newSession(displays, checker, profile, clock)
	f.s.SetOnChange(func(snapshot Snapshot) { f.changes <- snapshot })
	t.Cleanup(func() {
		close(checker.closed)
		displays.mu.Lock()
		displays.fail = make(map[string]error)
		displays.hook = nil
		displays.mu.Unlock()
		if err := f.s.Shutdown(); err != nil {
			t.Errorf("cleanup Shutdown: %v", err)
		}
	})
	return f
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
	assertOperations(t, calls, "resolve", "layout", "test", "apply")
	if calls[0].target.Identity != f.profile.Monitor || calls[2].mode != f.profile.GameMode || calls[3].mode != f.profile.GameMode {
		t.Fatalf("wrong target or mode: %+v", calls)
	}
	if got := f.s.Snapshot(); !got.Managed || !got.FourByThree || got.State != StateWaitingForGame {
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
	assertOperations(t, calls, "resolve", "test", "apply")
	if calls[1].mode != f.original || calls[2].mode != f.original {
		t.Fatalf("restore = %+v", calls)
	}
	if got := f.desktop(); !reflect.DeepEqual(got, before) {
		t.Fatalf("restored desktop = %+v, want %+v", got, before)
	}
	if got := f.s.Snapshot(); got.Managed || got.FourByThree || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestEnableFailureDoesNotStartActiveSession(t *testing.T) {
	for _, operation := range []string{"resolve", "layout", "test", "apply"} {
		t.Run(operation, func(t *testing.T) {
			f := newFixture(t)
			failure := errors.New("display failed")
			f.display.setFailure(operation, failure)
			if err := f.s.Enable(); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if got := f.s.Snapshot(); got.Managed || got.FourByThree || got.State != StateError || !errors.Is(got.Err, failure) {
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
				assertOperations(t, calls, "resolve", "test", "apply")
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
	assertOperations(t, f.display.takeCalls(), "resolve", "test", "apply")
	if !f.clock.tickers[0].isStopped() {
		t.Fatal("Disable returned before watcher stopped")
	}
	f.clock.tick(0, time.Unix(110, 0))
	if got := f.s.Snapshot(); got.Managed || got.CurrentMode != f.original {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestDisableUsesFallbackWhenAppStartsInUnmanagedFourByThreeMode(t *testing.T) {
	f := newFixture(t)
	f.setDesktop(f.profile.GameMode)
	got, err := f.s.Refresh()
	if err != nil {
		t.Fatal(err)
	}
	if !got.FourByThree || got.Managed || f.clock.count() != 0 {
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
	if calls[2].mode != f.profile.FallbackNativeMode || calls[3].mode != f.profile.FallbackNativeMode {
		t.Fatalf("fallback = %+v", calls)
	}
	// Widening the target again would land on its neighbour, so the fallback has to
	// plan the desktop as much as the managed path does.
	if got := f.desktop(); !reflect.DeepEqual(got, fixtureLayout(f.profile.FallbackNativeMode)) {
		t.Fatalf("fallback desktop = %+v", got)
	}
}

func TestRefreshAndShutdownLeaveUnmanagedGameModeUntouched(t *testing.T) {
	f := newFixture(t)
	f.setDesktop(f.profile.GameMode)
	if got, err := f.s.Refresh(); err != nil || !got.FourByThree || got.Managed {
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
	if f.s.Snapshot().FourByThree {
		t.Fatal("stale observed mode retained")
	}
}

// The UI disables the 4:3 control by testing Snapshot.Err with errors.Is, so every
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
	if got.Managed || got.FourByThree {
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
	assertOperations(t, calls, "resolve", "test", "apply")
	if calls[1].mode != f.original || calls[2].mode != f.original {
		t.Fatalf("automatic restore used the wrong mode: %+v", calls)
	}
	if got.Managed || got.FourByThree || got.CurrentMode != f.original || got.State != StateNative {
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
	assertOperations(t, f.display.takeCalls(), "resolve", "test", "apply")
	if got.Managed || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("restored snapshot = %+v", got)
	}
}

// 4:3 stays applied for as long as the game never shows up: the watcher only ever
// arms a restore after it has seen the process at least once.
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
	if got := f.s.Snapshot(); !got.Managed || !got.FourByThree {
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
	assertOperations(t, calls, "resolve", "test", "apply")
	if calls[2].mode != f.original {
		t.Fatalf("automatic restore used the wrong mode: %+v", calls)
	}
	if got.Managed || got.FourByThree || got.CurrentMode != f.original || got.State != StateNative {
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
	assertOperations(t, f.display.takeCalls(), "resolve", "test", "apply")
}

// Nothing prompts the user when the delayed restore fails, so the session records
// the failure for the UI to announce. It must count one failure per restore, not
// one per poll, and it must keep watching so a later game exit can try again.
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
	assertOperations(t, f.display.takeCalls(), "resolve", "test", "apply")
	if f.clock.count() != 2 {
		t.Fatalf("watchers started = %d, want a replacement after the failed restore", f.clock.count())
	}

	// The game is still gone. Polling it must not retry the restore, or the UI
	// would raise one notification per second.
	for _, second := range []int{6, 7, 8} {
		if got := f.poll(t, 1, second, processResult{}); got.State != StateWaitingForGame {
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
	assertOperations(t, f.display.takeCalls(), "resolve", "test", "apply")
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
			if got.Managed || got.FourByThree || f.clock.count() != 0 {
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
