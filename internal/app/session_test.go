package app

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

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

func (f *sessionFixture) poll(t *testing.T, index int, seconds int, result processResult) Snapshot {
	t.Helper()
	previous := f.s.Snapshot().Revision
	f.clock.tick(index, time.Unix(100+int64(seconds), 0))
	if name := waitValue(t, f.checker.called); name != f.profile.ProcessName {
		t.Fatalf("watched process = %q", name)
	}
	f.checker.results <- result
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
	got := f.s.Refresh()
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
	if got := f.s.Refresh(); !got.FourByThree || got.Managed {
		t.Fatalf("snapshot = %+v", got)
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
