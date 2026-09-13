package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/process"
)

type State string

const (
	StateNative         State = "native"
	StateApplying       State = "applying"
	StateWaitingForGame State = "waiting-for-game"
	StateGameRunning    State = "game-running"
	StateRestorePending State = "restore-pending"
	StateRestoring      State = "restoring"
	StateError          State = "error"
)

var ErrClosed = errors.New("display session is closed")

type Snapshot struct {
	State       State
	Target      domain.Target
	CurrentMode domain.Mode
	FourByThree bool
	Managed     bool
	Revision    uint64
	Message     string
	Err         error

	// AutoRestoreFailures counts the restores this session started by itself and
	// could not complete. Nothing prompts the user on that path, so the UI tracks
	// the counter to raise exactly one notification per failed automatic restore
	// instead of one per poll.
	AutoRestoreFailures uint64
}

type sessionTicker interface {
	C() <-chan time.Time
	Stop()
}

type sessionClock interface {
	Now() time.Time
	NewTicker(time.Duration) sessionTicker
}

type realClock struct{}
type realTicker struct{ *time.Ticker }

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTicker(interval time.Duration) sessionTicker {
	return realTicker{time.NewTicker(interval)}
}
func (t realTicker) C() <-chan time.Time { return t.Ticker.C }

type sessionWatcher struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// Session owns the saved desktop layout only after this instance successfully applies
// GameMode.
// opMu serializes complete workflows. mu protects snapshots and notifications only;
// no external display, process, or callback code runs while mu is held.
type Session struct {
	opMu sync.Mutex
	mu   sync.Mutex

	displays     display.Controller
	processes    process.Checker
	profile      domain.Profile
	clock        sessionClock
	saved        domain.Layout
	managed      bool
	closed       bool
	generation   uint64
	watcher      *sessionWatcher
	watchers     sync.WaitGroup
	shutdownDone chan struct{}

	snapshot            Snapshot
	onChange            func(Snapshot)
	handlerVersion      uint64
	notify              chan struct{}
	notifyStop          chan struct{}
	notifyDone          chan struct{}
	notificationsClosed bool
}

// NewSession creates an idle session. Refresh is the explicit read-only startup probe.
func NewSession(displays display.Controller, processes process.Checker, profile domain.Profile) *Session {
	return newSession(displays, processes, profile, realClock{})
}

func newSession(displays display.Controller, processes process.Checker, profile domain.Profile, clock sessionClock) *Session {
	s := &Session{
		displays: displays, processes: processes, profile: profile, clock: clock,
		snapshot: Snapshot{State: StateNative, Revision: 1, Message: "尚未啟用 4:3"},
		notify:   make(chan struct{}, 1), notifyStop: make(chan struct{}), notifyDone: make(chan struct{}),
		shutdownDone: make(chan struct{}),
	}
	go s.dispatchChanges()
	return s
}

// SetOnChange replaces the single observer; nil disables future notifications.
// Notifications are ordered and may coalesce intermediate snapshots. Callbacks can
// call Snapshot and must return for Shutdown to finish; UI callers should therefore
// run Shutdown off the UI thread when their callback synchronizes with that thread.
func (s *Session) SetOnChange(fn func(Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notificationsClosed {
		return
	}
	s.onChange = fn
	s.handlerVersion++
	s.signalChangeLocked()
}

func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

func (s *Session) updateSnapshot(change func(*Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.snapshot)
	s.snapshot.Revision++
	s.signalChangeLocked()
}

func (s *Session) signalChangeLocked() {
	if s.notificationsClosed {
		return
	}
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *Session) dispatchChanges() {
	defer close(s.notifyDone)
	var lastRevision, lastHandler uint64
	for {
		select {
		case <-s.notifyStop:
			return
		case <-s.notify:
			s.mu.Lock()
			fn, snapshot, version, stopped := s.onChange, s.snapshot, s.handlerVersion, s.notificationsClosed
			s.mu.Unlock()
			if stopped {
				return
			}
			if fn != nil && (snapshot.Revision > lastRevision || version != lastHandler) {
				fn(snapshot)
				lastRevision, lastHandler = snapshot.Revision, version
			}
		}
	}
}

func (s *Session) state(state State, message string) {
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.State, snapshot.Message, snapshot.Err = state, message, nil
	})
}

func (s *Session) fail(operation string, err error) error {
	err = fmt.Errorf("%s: %w", operation, err)
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.State, snapshot.Message, snapshot.Err = StateError, err.Error(), err
	})
	return err
}

func (s *Session) observed(target domain.Target, mode domain.Mode) {
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.Target, snapshot.CurrentMode = target, mode
		snapshot.FourByThree = mode == s.profile.GameMode
	})
}

func (s *Session) readCurrent() (domain.Target, domain.Mode, error) {
	target, err := s.displays.ResolveTarget(s.profile.MonitorHardwareID)
	if err != nil {
		return domain.Target{}, domain.Mode{}, s.fail("resolve target", err)
	}
	mode, err := s.displays.CurrentMode(target)
	if err != nil {
		return target, domain.Mode{}, s.fail("read current mode", err)
	}
	s.observed(target, mode)
	return target, mode, nil
}

// readLayout resolves the target and reads the whole desktop arrangement. Any mode
// change moves the displays beside the target, so the arrangement, not the target's
// mode on its own, is what an apply and a fallback restore have to be planned from.
func (s *Session) readLayout() (domain.Target, domain.Layout, domain.DisplayState, error) {
	target, err := s.displays.ResolveTarget(s.profile.MonitorHardwareID)
	if err != nil {
		return domain.Target{}, domain.Layout{}, domain.DisplayState{}, s.fail("resolve target", err)
	}
	layout, err := s.displays.CurrentLayout()
	if err != nil {
		return target, domain.Layout{}, domain.DisplayState{}, s.fail("read display layout", err)
	}
	current, ok := layout.Find(target.DeviceName)
	if !ok {
		return target, domain.Layout{}, domain.DisplayState{}, s.fail("read display layout",
			fmt.Errorf("%w: %s is not among the attached displays",
				display.ErrLayoutUnsafe, target.DeviceName))
	}
	s.observed(target, current.Mode)
	return target, layout, current, nil
}

// Refresh re-reads the target monitor and its current mode without changing it.
// It stays useful when every mutating control is disabled: a monitor that was
// asleep or on another input at startup is only noticed by reading again. The
// returned error describes this read alone, while Snapshot.Err may still carry an
// earlier failure that is still true, such as a restore that did not complete.
func (s *Session) Refresh() (Snapshot, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.closed {
		return s.Snapshot(), ErrClosed
	}
	_, _, err := s.readCurrent()
	if err == nil && !s.managed {
		s.state(StateNative, "已讀取目前顯示模式")
	}
	return s.Snapshot(), err
}

func (s *Session) Enable() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.managed {
		return nil
	}
	s.state(StateApplying, "正在驗證並套用 4:3")
	target, layout, current, err := s.readLayout()
	if err != nil {
		return err
	}
	if current.Mode == s.profile.GameMode {
		s.state(StateNative, "目前已是 4:3；可手動恢復 2K")
		return nil
	}
	plan, err := display.PlanModeChange(layout, target.DeviceName, s.profile.GameMode)
	if err != nil {
		return s.fail("plan display layout", err)
	}
	if err := s.apply(target, s.profile.GameMode, plan); err != nil {
		return err
	}
	s.saved, s.managed = layout, true
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.CurrentMode, snapshot.FourByThree, snapshot.Managed = s.profile.GameMode, true, true
		snapshot.State, snapshot.Message, snapshot.Err = StateWaitingForGame, "4:3 已啟用，等待遊戲", nil
	})
	s.startWatcher()
	return nil
}

// apply pre-flights the target's mode and only then applies the whole arrangement.
// The plan reaching this point has already been proved safe, so the only thing left
// between it and the desktop is the driver; the display layer applies the displays
// in an order that never overlaps them, rolls back what it already changed if a
// call fails, and reports a desktop that did not end up matching the plan.
func (s *Session) apply(target domain.Target, mode domain.Mode, plan domain.LayoutPlan) error {
	if err := s.displays.TestMode(target, mode); err != nil {
		return s.fail("test display mode", err)
	}
	if err := s.displays.ApplyLayout(plan); err != nil {
		return s.fail("apply display layout", err)
	}
	return nil
}

// stopWatcher invalidates ownership before cancellation. Callers must release opMu
// before joining: a poll may already be waiting to acquire the operation gate.
func (s *Session) stopWatcher() *sessionWatcher {
	s.generation++
	watcher := s.watcher
	s.watcher = nil
	if watcher != nil {
		watcher.cancel()
	}
	return watcher
}

func joinWatcher(watcher *sessionWatcher) {
	if watcher != nil {
		<-watcher.done
	}
}

// ensureWatcher puts the poll loop back after a restore failed while this session
// still owns the applied mode: the automatic restore is then the only thing left
// that can return the display without another click. Callers hold opMu, and the
// replacement starts from a fresh tracker on purpose — a tracker that already
// fired its restore would never arm another one. A watcher goroutine may call
// this: it replaces itself and must not join the watcher it stopped.
func (s *Session) ensureWatcher() {
	if s.closed || !s.managed || s.watcher != nil {
		return
	}
	s.startWatcher()
}

func (s *Session) Disable() error {
	s.opMu.Lock()
	if s.closed {
		s.opMu.Unlock()
		return ErrClosed
	}
	watcher := s.stopWatcher()
	err := s.restore(true)
	s.ensureWatcher()
	s.opMu.Unlock()
	joinWatcher(watcher)
	return err
}

// restore runs only under opMu. Unmanaged fallback is reserved for explicit Disable.
// Saved ownership survives every resolve/plan/test/apply failure so restoration is
// retryable.
func (s *Session) restore(allowFallback bool) error {
	if s.managed {
		return s.restoreSaved()
	}
	if !allowFallback {
		return nil
	}
	return s.restoreFallback()
}

// restoreSaved puts the desktop back exactly as this session found it: the target's
// saved mode and every display's saved coordinate, applied together. The target is
// resolved again rather than taken from the saved layout, and a target that is no
// longer part of that layout aborts the restore: the displays have been renumbered
// under the tool, so the saved coordinates belong to an arrangement that is gone.
func (s *Session) restoreSaved() error {
	s.state(StateRestoring, "正在恢復原始顯示模式")
	target, err := s.displays.ResolveTarget(s.profile.MonitorHardwareID)
	if err != nil {
		return s.fail("resolve target for restore", err)
	}
	saved, ok := s.saved.Find(target.DeviceName)
	if !ok {
		return s.fail("plan display layout", fmt.Errorf("%w: the saved layout does not contain %s",
			display.ErrLayoutUnsafe, target.DeviceName))
	}
	plan, err := display.PlanRestore(s.saved, target.DeviceName)
	if err != nil {
		return s.fail("plan display layout", err)
	}
	return s.finishRestore(target, saved.Mode, plan)
}

// restoreFallback is the escape hatch for a 4:3 desktop this session never applied.
// There is no saved arrangement to return to, so the native mode from the profile is
// planned against the desktop as it looks right now, which is also what keeps the
// wider mode from landing on top of the display beside it.
func (s *Session) restoreFallback() error {
	target, layout, current, err := s.readLayout()
	if err != nil {
		return err
	}
	if current.Mode != s.profile.GameMode {
		s.state(StateNative, "目前未使用 4:3")
		return nil
	}
	plan, err := display.PlanModeChange(layout, target.DeviceName, s.profile.FallbackNativeMode)
	if err != nil {
		return s.fail("plan display layout", err)
	}
	s.state(StateRestoring, "正在恢復 2K 顯示模式")
	return s.finishRestore(target, s.profile.FallbackNativeMode, plan)
}

func (s *Session) finishRestore(target domain.Target, mode domain.Mode, plan domain.LayoutPlan) error {
	if err := s.apply(target, mode, plan); err != nil {
		return err
	}
	s.managed = false
	s.saved = domain.Layout{}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.Target, snapshot.CurrentMode = target, mode
		snapshot.FourByThree, snapshot.Managed = mode == s.profile.GameMode, false
		snapshot.State, snapshot.Message, snapshot.Err = StateNative, "已恢復顯示模式", nil
	})
	return nil
}

// Shutdown restores owned state and joins all watchers and in-flight callbacks.
// A restore failure leaves the session open and retryable. Successful repeated
// calls wait for the same shutdown barrier before returning.
func (s *Session) Shutdown() error {
	s.opMu.Lock()
	if s.closed {
		s.opMu.Unlock()
		<-s.shutdownDone
		return nil
	}
	watcher := s.stopWatcher()
	if err := s.restore(false); err != nil {
		s.ensureWatcher()
		s.opMu.Unlock()
		joinWatcher(watcher)
		return err
	}
	s.closed = true
	s.mu.Lock()
	s.notificationsClosed = true
	s.onChange = nil
	close(s.notifyStop)
	s.mu.Unlock()
	s.opMu.Unlock()
	s.watchers.Wait()
	<-s.notifyDone
	close(s.shutdownDone)
	return nil
}

func (s *Session) startWatcher() {
	s.generation++
	generation := s.generation
	ctx, cancel := context.WithCancel(context.Background())
	watcher := &sessionWatcher{cancel: cancel, done: make(chan struct{})}
	s.watcher = watcher
	ticker := s.clock.NewTicker(time.Second)
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		defer close(watcher.done)
		defer ticker.Stop()
		tracker := newGameTracker(s.profile.RestoreDelay)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C():
			}
			if ctx.Err() != nil {
				return
			}
			running, err := s.processes.Running(s.profile.ProcessName)
			if !s.observeGame(generation, tracker, running, err) {
				return
			}
		}
	}()
}

func (s *Session) observeGame(generation uint64, tracker *gameTracker, running bool, err error) bool {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.closed || !s.managed || generation != s.generation {
		return false
	}
	if err != nil {
		s.fail("check game process", err)
		return true
	}
	result := tracker.Observe(running, s.clock.Now())
	if result.ShouldRestore {
		s.stopWatcher()
		// This goroutine must not join itself; it exits immediately after restoration.
		// A failure keeps ownership, so a replacement watcher takes over the polling
		// and the counter tells the UI to announce this one failure.
		if err := s.restore(false); err != nil {
			// The replacement watcher is in place before the counter is published, so
			// an observer that reacts to the failure already sees a watched session.
			s.ensureWatcher()
			s.updateSnapshot(func(snapshot *Snapshot) { snapshot.AutoRestoreFailures++ })
		}
		return false
	}
	switch {
	case running:
		s.state(StateGameRunning, "遊戲執行中")
	case result.SeenGame:
		s.state(StateRestorePending, "遊戲已關閉，等待恢復顯示模式")
	default:
		s.state(StateWaitingForGame, "4:3 已啟用，等待遊戲")
	}
	return true
}
