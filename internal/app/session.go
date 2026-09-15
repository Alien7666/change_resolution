package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/process"
)

type State string

const (
	StateNative   State = "native"
	StateApplying State = "applying"

	// StateManualOnly is the applied state of a profile that watches no process: the
	// configured mode is on and owned, and nothing but the user will take it off
	// again. It is deliberately not StateWaitingForGame -- nothing is being waited
	// for, and a status line that said so would promise a restore nobody armed.
	StateManualOnly State = "manual-only"

	StateWaitingForGame State = "waiting-for-game"
	StateGameRunning    State = "game-running"
	StateRestorePending State = "restore-pending"
	StateRestoring      State = "restoring"
	StateError          State = "error"
)

var ErrClosed = errors.New("display session is closed")

// ErrFallbackUnknown reports that a restore was asked for and the session could not
// work out which mode to restore to. It is reachable only for a profile that records
// no fallback of its own, where the answer comes from the monitor -- and a monitor
// that cannot be asked, or that reports nothing this tool could apply, leaves the
// question genuinely unanswered. The session refuses rather than substituting a mode
// nobody chose.
var ErrFallbackUnknown = errors.New("無法決定要恢復的顯示模式")

type Snapshot struct {
	State       State
	Target      domain.Target
	CurrentMode domain.Mode

	// AtGameMode reports that the desktop is at the mode the profile configured. It
	// says nothing about the shape of that mode: the field it replaced was called
	// FourByThree, which was only ever a description of the profile the tool shipped
	// with and became an assumption the moment the user could choose.
	AtGameMode bool

	Managed  bool
	Revision uint64
	Message  string
	Err      error

	// Profile is the configuration this session runs on, copied rather than shared so
	// that reading a snapshot cannot reach back into the session. It travels in every
	// snapshot because the window renders every string it shows from it, and a window
	// holding its own copy of a monitor name would be a second source of truth.
	Profile domain.Profile

	// MatchedBy is the rung of the identity ladder the target was resolved on. A
	// monitor found through its hardware ID rather than the stored interface path is
	// the same screen on another port, which is something the user gets told.
	MatchedBy domain.MatchLevel

	// FallbackMode is what a restore would apply to a desktop this session never
	// applied the game mode to: the profile's own fallback, or the one derived from
	// the modes the monitor reports. FallbackKnown is false when neither exists, and
	// FallbackReason then says which of the two ways it failed, so the UI disables
	// the button with an explanation instead of offering a guess.
	FallbackMode   domain.Mode
	FallbackKnown  bool
	FallbackReason string

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

// displayBinding is the part of a monitor identity that can prove a saved DISPLAYn
// still names the same physical monitor. Labels are deliberately absent. The exact
// interface path is preferred; when Windows cannot report one, an exact full hardware
// ID is accepted only while it is unique across the captured desktop.
type displayBinding struct {
	kind  uint8
	value string
}

const (
	bindingInstancePath uint8 = iota + 1
	bindingHardwareID
)

// Session owns the saved desktop layout only after this instance successfully applies
// GameMode.
// opMu serializes complete workflows. mu protects snapshots and notifications only;
// no external display, process, or callback code runs while mu is held.
type Session struct {
	opMu sync.Mutex
	mu   sync.Mutex

	displays  display.Controller
	processes process.Checker
	profile   domain.Profile
	clock     sessionClock

	// fallback is the answer to "what would a restore apply", recomputed on every
	// read so that the mode the window offers is the mode a restore would use. It is
	// kept beside the snapshot rather than only in it because the snapshot carries
	// the reason as text while a refusal has to carry the error.
	fallback fallback

	saved               domain.Layout
	savedBindings       map[string]displayBinding
	managedTargetDevice string
	managed             bool
	closed              bool
	generation          uint64
	watcher             *sessionWatcher
	watchers            sync.WaitGroup
	shutdownDone        chan struct{}

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
	// The session owns its configuration outright. A caller that kept the profile it
	// passed in must not be able to reach through FallbackMode and change what a
	// restore applies half a session later.
	profile = profile.Copy()
	derived := configuredFallback(profile)
	s := &Session{
		displays: displays, processes: processes, profile: profile, clock: clock,
		fallback: derived,
		snapshot: Snapshot{
			State:          StateNative,
			Revision:       1,
			Message:        "尚未套用 " + domain.ModeLabel(profile.GameMode),
			Profile:        profile.Copy(),
			FallbackMode:   derived.mode,
			FallbackKnown:  derived.known,
			FallbackReason: derived.reason,
		},
		notify: make(chan struct{}, 1), notifyStop: make(chan struct{}), notifyDone: make(chan struct{}),
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

// observed records a read: what the target is, what it is running, and -- because a
// read is also the only chance to ask -- what a restore would put it back to.
//
// The enumeration behind that last question runs before mu is taken, because no
// display call may happen while a snapshot is being published.
func (s *Session) observed(target domain.Target, mode domain.Mode) {
	derived := s.resolveFallback(target)
	s.updateSnapshot(func(snapshot *Snapshot) {
		// Written under mu with the snapshot it describes, so a refusal and the text
		// explaining it can never come from different reads.
		s.fallback = derived
		snapshot.Target, snapshot.CurrentMode = target, mode
		snapshot.MatchedBy = target.MatchedBy
		snapshot.AtGameMode = mode == s.profile.GameMode
		snapshot.FallbackMode = derived.mode
		snapshot.FallbackKnown = derived.known
		snapshot.FallbackReason = derived.reason
	})
}

// fallback is what a restore would apply to a desktop this session does not own.
type fallback struct {
	mode   domain.Mode
	known  bool
	reason string
	err    error
}

// configuredFallback is the answer for a profile that records a fallback of its own:
// the user overrode the derivation, and nothing has to be read to honour that.
func configuredFallback(profile domain.Profile) fallback {
	if profile.FallbackMode != nil {
		return fallback{mode: *profile.FallbackMode, known: true}
	}
	return fallback{reason: "尚未讀取顯示器回報的顯示模式，還不知道要恢復成哪一個"}
}

// resolveFallback answers the question a restore of an unowned desktop asks. For a
// profile that records no fallback the answer comes from the monitor, and the two
// ways that can fail are different events told apart deliberately: an enumeration
// that errored means the monitor stopped answering, while an enumeration that
// succeeded and filtered down to nothing means this monitor reports no mode the tool
// could apply. Sending the user to look for the same thing in both cases would send
// half of them looking for the wrong thing.
//
// It calls the display controller, so it must not run under mu.
func (s *Session) resolveFallback(target domain.Target) fallback {
	if configured := configuredFallback(s.profile); configured.known {
		return configured
	}
	modes, err := s.displays.EnumModes(target)
	if err != nil {
		return refusedFallback(fmt.Errorf("%w：讀取顯示器支援的顯示模式失敗：%w", ErrFallbackUnknown, err))
	}
	mode, ok := domain.DeriveFallback(modes)
	if !ok {
		return refusedFallback(fmt.Errorf("%w：顯示器沒有回報任何這個工具能套用的顯示模式", ErrFallbackUnknown))
	}
	return fallback{mode: mode, known: true}
}

func refusedFallback(err error) fallback {
	return fallback{reason: err.Error(), err: err}
}

func (s *Session) currentFallback() fallback {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fallback
}

// gameModeLabel is how this session names the mode it was configured with. Every
// message the session writes goes through it, so none of them can describe a mode
// the user did not choose.
func (s *Session) gameModeLabel() string {
	return domain.ModeLabel(s.profile.GameMode)
}

func bindingOf(identity domain.MonitorIdentity) (displayBinding, bool) {
	if identity.InstancePath != "" {
		return displayBinding{kind: bindingInstancePath, value: strings.ToLower(identity.InstancePath)}, true
	}
	if identity.HardwareID != "" {
		return displayBinding{kind: bindingHardwareID, value: strings.ToLower(identity.HardwareID)}, true
	}
	return displayBinding{}, false
}

// captureDisplayBindings takes a second read of the monitor identities after the
// layout was read. Every layout device must map to exactly one target, every target
// must belong to the layout, and the stable keys must be unique. A topology changing
// between those reads is therefore a refusal rather than a guessed binding.
func captureDisplayBindings(layout domain.Layout, targets []domain.Target) (map[string]displayBinding, error) {
	byDevice := make(map[string]domain.Target, len(targets))
	for _, target := range targets {
		if target.DeviceName == "" {
			return nil, fmt.Errorf("%w: a monitor identity reported no device name", display.ErrLayoutUnsafe)
		}
		if _, duplicate := byDevice[target.DeviceName]; duplicate {
			return nil, fmt.Errorf("%w: %s maps to several monitor identities",
				display.ErrLayoutUnsafe, target.DeviceName)
		}
		byDevice[target.DeviceName] = target
	}
	if len(byDevice) != len(layout.Displays) {
		return nil, fmt.Errorf("%w: the layout and monitor identity reads disagree on the display set",
			display.ErrLayoutUnsafe)
	}

	bindings := make(map[string]displayBinding, len(layout.Displays))
	owners := make(map[displayBinding]string, len(layout.Displays))
	for _, state := range layout.Displays {
		target, ok := byDevice[state.DeviceName]
		if !ok {
			return nil, fmt.Errorf("%w: %s has no monitor identity",
				display.ErrLayoutUnsafe, state.DeviceName)
		}
		binding, ok := bindingOf(target.Identity)
		if !ok {
			return nil, fmt.Errorf("%w: %s has no stable monitor identity",
				display.ErrLayoutUnsafe, state.DeviceName)
		}
		if owner, duplicate := owners[binding]; duplicate {
			return nil, fmt.Errorf("%w: %s and %s report the same monitor identity",
				display.ErrLayoutUnsafe, owner, state.DeviceName)
		}
		owners[binding] = state.DeviceName
		bindings[state.DeviceName] = binding
	}
	return bindings, nil
}

func (s *Session) readDisplayBindings(layout domain.Layout) (map[string]displayBinding, error) {
	targets, err := s.displays.Targets()
	if err != nil {
		return nil, err
	}
	return captureDisplayBindings(layout, targets)
}

func verifyDisplayBindings(saved, current map[string]displayBinding) error {
	if len(saved) != len(current) {
		return fmt.Errorf("%w: the saved monitor identity set changed", display.ErrLayoutUnsafe)
	}
	for deviceName, expected := range saved {
		observed, ok := current[deviceName]
		if !ok {
			return fmt.Errorf("%w: the saved monitor identity for %s is missing",
				display.ErrLayoutUnsafe, deviceName)
		}
		if observed != expected {
			return fmt.Errorf("%w: %s now names a different physical monitor",
				display.ErrLayoutUnsafe, deviceName)
		}
	}
	return nil
}

func verifyResolvedBinding(target domain.Target, bindings map[string]displayBinding) error {
	expected, ok := bindings[target.DeviceName]
	if !ok {
		return fmt.Errorf("%w: the resolved target %s has no captured monitor identity",
			display.ErrLayoutUnsafe, target.DeviceName)
	}
	observed, ok := bindingOf(target.Identity)
	if !ok || observed != expected {
		return fmt.Errorf("%w: the resolved target %s changed identity during the display read",
			display.ErrLayoutUnsafe, target.DeviceName)
	}
	return nil
}

func (s *Session) readCurrent() (domain.Target, domain.Mode, error) {
	target, err := s.displays.ResolveTarget(s.profile.Monitor)
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
	target, err := s.displays.ResolveTarget(s.profile.Monitor)
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
	s.state(StateApplying, "正在驗證並套用 "+s.gameModeLabel())
	target, layout, current, err := s.readLayout()
	if err != nil {
		return err
	}
	if current.Mode == s.profile.GameMode {
		s.state(StateNative, "目前已是 "+s.gameModeLabel()+"；可手動恢復原始顯示模式")
		return nil
	}
	plan, err := display.PlanModeChange(layout, target.DeviceName, s.profile.GameMode)
	if err != nil {
		return s.fail("plan display layout", err)
	}
	bindings, err := s.readDisplayBindings(layout)
	if err != nil {
		return s.fail("read display identities", err)
	}
	if err := verifyResolvedBinding(target, bindings); err != nil {
		return s.fail("bind target display", err)
	}
	if err := s.apply(target, s.profile.GameMode, plan); err != nil {
		return err
	}
	s.saved, s.savedBindings = layout, bindings
	s.managedTargetDevice, s.managed = target.DeviceName, true
	state, message := StateWaitingForGame, s.waitingMessage()
	if s.profile.ProcessName == "" {
		state, message = StateManualOnly, s.gameModeLabel()+" 已套用（未設定要監看的程式，不會自動恢復）"
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.CurrentMode, snapshot.AtGameMode, snapshot.Managed = s.profile.GameMode, true, true
		snapshot.State, snapshot.Message, snapshot.Err = state, message, nil
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
	target, err := s.displays.ResolveTarget(s.profile.Monitor)
	if err != nil {
		return s.fail("resolve target for restore", err)
	}
	if s.managedTargetDevice == "" || target.DeviceName != s.managedTargetDevice {
		return s.fail("plan display layout", fmt.Errorf(
			"%w: the configured monitor moved from %s to %s while its mode was managed",
			display.ErrLayoutUnsafe, s.managedTargetDevice, target.DeviceName))
	}
	saved, ok := s.saved.Find(target.DeviceName)
	if !ok {
		return s.fail("plan display layout", fmt.Errorf("%w: the saved layout does not contain %s",
			display.ErrLayoutUnsafe, target.DeviceName))
	}
	// A restore moves every display in the saved arrangement, not only the target, so
	// every one of them has to still be attached under the name it was read as.
	// Checking the target alone let a renumbered neighbour through, and the failure
	// then surfaced from inside the apply naming nothing the user could act on.
	attached, err := s.displays.CurrentLayout()
	if err != nil {
		return s.fail("read display layout", err)
	}
	if missing, gone := firstMissingDisplay(s.saved, attached); gone {
		return s.fail("plan display layout", fmt.Errorf("%w: the saved layout's %s is no longer attached",
			display.ErrLayoutUnsafe, missing))
	}
	bindings, err := s.readDisplayBindings(attached)
	if err != nil {
		return s.fail("read display identities", err)
	}
	if err := verifyDisplayBindings(s.savedBindings, bindings); err != nil {
		return s.fail("plan display layout", err)
	}
	if err := verifyResolvedBinding(target, bindings); err != nil {
		return s.fail("bind target display", err)
	}
	plan, err := display.PlanRestore(s.saved, attached, target.DeviceName)
	if err != nil {
		return s.fail("plan display layout", err)
	}
	return s.finishRestore(target, saved.Mode, plan)
}

// restoreFallback is the escape hatch for a desktop that is already at the configured
// mode and that this session never applied. There is no saved arrangement to return
// to, so the fallback mode is planned against the desktop as it looks right now,
// which is also what keeps a wider mode from landing on top of the display beside it.
//
// The mode itself comes from the read that just happened rather than from a second
// enumeration, so what a restore applies and what the window said it would apply are
// the same answer and cannot drift apart between the sentence and the click.
func (s *Session) restoreFallback() error {
	target, layout, current, err := s.readLayout()
	if err != nil {
		return err
	}
	if current.Mode != s.profile.GameMode {
		s.state(StateNative, "目前未使用 "+s.gameModeLabel())
		return nil
	}
	derived := s.currentFallback()
	if !derived.known {
		return s.fail("derive fallback mode", derived.err)
	}
	plan, err := display.PlanModeChange(layout, target.DeviceName, derived.mode)
	if err != nil {
		return s.fail("plan display layout", err)
	}
	s.state(StateRestoring, "正在恢復 "+domain.ModeLabel(derived.mode))
	return s.finishRestore(target, derived.mode, plan)
}

// firstMissingDisplay names the first display in the saved arrangement that is no
// longer part of the desktop, if there is one.
func firstMissingDisplay(saved, attached domain.Layout) (string, bool) {
	for _, state := range saved.Displays {
		if _, ok := attached.Find(state.DeviceName); !ok {
			return state.DeviceName, true
		}
	}
	return "", false
}

func (s *Session) finishRestore(target domain.Target, mode domain.Mode, plan domain.LayoutPlan) error {
	if err := s.apply(target, mode, plan); err != nil {
		return err
	}
	s.managed = false
	s.saved = domain.Layout{}
	s.savedBindings = nil
	s.managedTargetDevice = ""
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.Target, snapshot.CurrentMode = target, mode
		snapshot.MatchedBy = target.MatchedBy
		snapshot.AtGameMode, snapshot.Managed = mode == s.profile.GameMode, false
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

// waitingMessage is what the session says while it owns the mode and the watched
// process has not appeared.
func (s *Session) waitingMessage() string {
	return s.gameModeLabel() + " 已套用，等待 " + s.profile.ProcessName
}

// startWatcher is the single place a poll loop begins, which is why the profile that
// watches nothing is refused here rather than at each call site. Such a profile is a
// configuration and not a gap: the user toggles by hand, nothing is polled, and the
// process list is never read.
func (s *Session) startWatcher() {
	if s.profile.ProcessName == "" {
		return
	}
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
		s.state(StateWaitingForGame, s.waitingMessage())
	}
	return true
}
