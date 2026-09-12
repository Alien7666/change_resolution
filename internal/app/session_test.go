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
}

type fakeDisplay struct {
	mu      sync.Mutex
	target  domain.Target
	current domain.Mode
	calls   []displayCall
	fail    map[string]error
	hook    func(string)
}

func (d *fakeDisplay) record(operation string, target domain.Target, mode domain.Mode) error {
	d.mu.Lock()
	d.calls = append(d.calls, displayCall{operation, target, mode})
	err, hook := d.fail[operation], d.hook
	d.mu.Unlock()
	if hook != nil {
		hook(operation)
	}
	return err
}

func (d *fakeDisplay) ResolveTarget(prefix string) (domain.Target, error) {
	err := d.record("resolve", domain.Target{HardwareID: prefix}, domain.Mode{})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.target, err
}

func (d *fakeDisplay) CurrentMode(target domain.Target) (domain.Mode, error) {
	err := d.record("current", target, domain.Mode{})
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current, err
}

func (d *fakeDisplay) TestMode(target domain.Target, mode domain.Mode) error {
	return d.record("test", target, mode)
}

func (d *fakeDisplay) ApplyMode(target domain.Target, mode domain.Mode) error {
	if err := d.record("apply", target, mode); err != nil {
		return err
	}
	d.mu.Lock()
	d.current = mode
	d.mu.Unlock()
	return nil
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

func newFixture(t *testing.T) *sessionFixture {
	t.Helper()
	profile := domain.DefaultProfile()
	original := domain.Mode{Width: 3440, Height: 1440, RefreshHz: 120, BitsPerPixel: 32}
	displays := &fakeDisplay{
		target:  domain.Target{DeviceName: `\\.\DISPLAY4`, HardwareID: profile.MonitorHardwareID},
		current: original, fail: make(map[string]error),
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
	assertOperations(t, calls, "resolve", "current", "test", "apply")
	if calls[0].target.HardwareID != f.profile.MonitorHardwareID || calls[2].mode != f.profile.GameMode || calls[3].mode != f.profile.GameMode {
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

func TestDisableRestoresCapturedMode(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()
	f.display.mu.Lock()
	f.display.target.DeviceName = `\\.\DISPLAY7`
	f.display.mu.Unlock()
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "test", "apply")
	if calls[1].mode != f.original || calls[2].mode != f.original || calls[2].target.DeviceName != `\\.\DISPLAY7` {
		t.Fatalf("restore = %+v", calls)
	}
	if got := f.s.Snapshot(); got.Managed || got.FourByThree || got.CurrentMode != f.original || got.State != StateNative {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestEnableFailureDoesNotStartActiveSession(t *testing.T) {
	for _, operation := range []string{"resolve", "current", "test", "apply"} {
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
	f.display.current = f.profile.GameMode
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
	assertOperations(t, f.display.takeCalls(), "resolve", "current")
	if f.s.Snapshot().Managed || f.clock.count() != 0 {
		t.Fatal("Enable adopted unmanaged game mode")
	}
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	calls := f.display.takeCalls()
	assertOperations(t, calls, "resolve", "current", "test", "apply")
	if calls[2].mode != f.profile.FallbackNativeMode || calls[3].mode != f.profile.FallbackNativeMode {
		t.Fatalf("fallback = %+v", calls)
	}
}

func TestRefreshAndShutdownLeaveUnmanagedGameModeUntouched(t *testing.T) {
	f := newFixture(t)
	f.display.current = f.profile.GameMode
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
	f.display.current = f.profile.GameMode
	f.s.Refresh()
	f.display.takeCalls()
	f.display.current = f.original
	if err := f.s.Disable(); err != nil {
		t.Fatal(err)
	}
	assertOperations(t, f.display.takeCalls(), "resolve", "current")
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
	f.display.setFailure("resolve", fmt.Errorf("%w: %s", display.ErrTargetNotFound, f.profile.MonitorHardwareID))

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
