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
	"github.com/Alien7666/change_resolution/internal/scaling"
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

	// StateScalingCycle is a scaling change made while this session owns an applied
	// mode. It is one state for the whole of it rather than three, because the user
	// pressed one button and can do nothing until all of it is over; the status line
	// names the phase instead.
	StateScalingCycle State = "scaling-cycle"
)

var (
	ErrClosed = errors.New("display session is closed")

	// ErrDisplayRecoveryPending refuses any operation that could overwrite the only
	// exact layout captured before a display write whose result could not be proved.
	ErrDisplayRecoveryPending = errors.New("顯示配置仍待恢復，請先恢復原始顯示模式")
)

// ErrFallbackUnknown reports that a restore was asked for and the session could not
// work out which mode to restore to. It is reachable only for a profile that records
// no fallback of its own, where the answer comes from the monitor -- and a monitor
// that cannot be asked, or that reports nothing this tool could apply, leaves the
// question genuinely unanswered. The session refuses rather than substituting a mode
// nobody chose.
var ErrFallbackUnknown = errors.New("無法決定要恢復的顯示模式")

// ScalingSnapshot is everything the window renders about GPU scaling. It is a second,
// wholly independent availability state: nothing in it may disable the display-mode
// controls, because a missing NVIDIA driver says nothing about whether a display mode
// can be set.
type ScalingSnapshot struct {
	// Available and Reason are this button's own enablement. Reason is actionable text
	// -- which vendor, which driver, which monitor -- and belongs beside the button.
	Available bool
	Reason    string

	// Known says Effective was reported by the driver rather than assumed. The tool
	// never infers a scaling value it did not read.
	Known     bool
	Effective scaling.Value

	// Requested is the value of this session's last attempted write. A driver that
	// normalises that write leaves Matched false, which is not an error and not a
	// failure to recover from: both values are shown and the button is not drawn as on.
	Requested      scaling.Value
	RequestedKnown bool
	Matched        bool

	// Owned is true only while this run has a successful change of its own to undo, and
	// Saved is the effective value read immediately before that change. Together they
	// are the restore button's label and the value Shutdown puts back.
	Owned bool
	Saved scaling.Value
}

type Snapshot struct {
	State       State
	Target      domain.Target
	CurrentMode domain.Mode

	// AtGameMode reports that the desktop is at the mode the profile configured. It
	// says nothing about the shape of that mode: the field it replaced was called
	// FourByThree, which was only ever a description of the profile the tool shipped
	// with and became an assumption the moment the user could choose.
	AtGameMode bool

	Managed bool
	// RecoveryPending means a display write may have changed the desktop but did not
	// complete verifiably. Managed remains false because the configured mode was not
	// successfully applied; Restore still uses the exact pre-write layout.
	RecoveryPending bool
	Revision        uint64
	Message         string
	Err             error

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

	// NativeMode is the panel's native mode derived from its current mode catalogue.
	// It is deliberately separate from FallbackMode: an explicit fallback is a restore
	// target chosen by the user and may have a different aspect ratio. NativeKnown is
	// false when the latest refresh could not resolve or enumerate the monitor.
	NativeMode  domain.Mode
	NativeKnown bool

	// AutoRestoreFailures counts the restores this session started by itself and
	// could not complete. Nothing prompts the user on that path, so the UI tracks
	// the counter to raise exactly one notification per failed automatic restore
	// instead of one per poll.
	AutoRestoreFailures uint64

	// Scaling is the GPU-scaling row, kept whole and separate so that no scaling
	// failure can reach the display half's availability.
	Scaling ScalingSnapshot
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

// cyclePhase is the public face of a running scaling cycle. It masks the published
// snapshot; it never replaces the stored one. See viewLocked.
type cyclePhase struct {
	active  bool
	message string
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
// no external display, process, scaling, or callback code runs while mu is held.
type Session struct {
	opMu sync.Mutex
	mu   sync.Mutex

	displays  display.Controller
	processes process.Checker
	scalings  scaling.Controller
	profile   domain.Profile
	clock     sessionClock

	// fallback is the answer to "what would a restore apply", recomputed on every
	// read so that the mode the window offers is the mode a restore would use. It is
	// kept beside the snapshot rather than only in it because the snapshot carries
	// the reason as text while a refusal has to carry the error.
	fallback fallback

	// saved and its bindings hold the exact pre-write layout for either successful
	// managed ownership or an uncertain-write recovery obligation. The booleans keep
	// those meanings distinct; they are never both true.
	saved               domain.Layout
	savedBindings       map[string]displayBinding
	managedTargetDevice string
	managed             bool
	recoveryPending     bool
	closed              bool
	generation          uint64
	watcher             *sessionWatcher
	watchers            sync.WaitGroup
	shutdownDone        chan struct{}

	// The scaling trio is parallel to managed/saved and completely independent of it.
	// scalingOwned is true only after this run successfully changed the value;
	// scalingSaved is the effective value read immediately before that change, never
	// inferred; scalingID is the displayId ownership was taken on, which is what a
	// restore is checked against so it cannot land on another monitor.
	scalingOwned bool
	scalingSaved scaling.Value
	scalingID    uint32

	// gameSeen has exactly managed's lifetime: "the watched process has been seen
	// running during this ownership". It is the only field a replacement watcher
	// inherits. The countdown is never inherited -- missing, missingAt and fired always
	// start at zero -- which is why the tracker object is replaced rather than reused.
	gameSeen bool

	snapshot Snapshot

	// cycle is guarded by mu, not opMu, because viewLocked reads it on the notification
	// and Snapshot paths.
	cycle cyclePhase

	onChange            func(Snapshot)
	handlerVersion      uint64
	notify              chan struct{}
	notifyStop          chan struct{}
	notifyDone          chan struct{}
	notificationsClosed bool
}

// NewSession creates an idle session. Refresh is the explicit read-only startup probe.
// A nil scaling controller is a session with no GPU-scaling support, which reports
// itself as unavailable rather than panicking at the first press.
func NewSession(
	displays display.Controller,
	processes process.Checker,
	scalings scaling.Controller,
	profile domain.Profile,
) *Session {
	return newSession(displays, processes, scalings, profile, realClock{})
}

func newSession(
	displays display.Controller,
	processes process.Checker,
	scalings scaling.Controller,
	profile domain.Profile,
	clock sessionClock,
) *Session {
	// The session owns its configuration outright. A caller that kept the profile it
	// passed in must not be able to reach through FallbackMode and change what a
	// restore applies half a session later.
	profile = profile.Copy()
	derived := configuredFallback(profile)
	if scalings == nil {
		scalings = unavailableScaling{}
	}
	s := &Session{
		displays: displays, processes: processes, scalings: scalings,
		profile: profile, clock: clock,
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
	return s.viewLocked()
}

// viewLocked is what the outside world sees. During a scaling cycle s.managed
// legitimately goes false between the display restore and the re-apply, and s.snapshot
// records that honestly -- but Snapshot.Managed's only consumers are the window's
// control enablement and its restore button, so letting it flip mid-cycle would draw
// the toggle as "off" at the one moment pressing it would mean nothing. The stored
// truth is masked, never overwritten, so every exit from the cycle publishes the fact
// without anything having to be undone.
func (s *Session) viewLocked() Snapshot {
	snapshot := s.snapshot
	if !s.cycle.active {
		return snapshot
	}
	snapshot.State = StateScalingCycle
	snapshot.Message = s.cycle.message
	snapshot.Err = nil
	snapshot.Managed = true
	return snapshot
}

func (s *Session) updateSnapshot(change func(*Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.snapshot)
	s.snapshot.Revision++
	s.signalChangeLocked()
}

// cyclePhase names the step a running cycle is on and publishes it. The revision is
// bumped on the stored snapshot so the notification actually goes out, even though
// nothing the steps themselves wrote has changed.
func (s *Session) cyclePhase(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cycle = cyclePhase{active: true, message: message}
	s.snapshot.Revision++
	s.signalChangeLocked()
}

// endCycle stops the masking. It deliberately publishes nothing: every caller states
// the cycle's real end immediately afterwards, and a bare un-masking in between would
// show one notification's worth of half-finished fact.
func (s *Session) endCycle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cycle = cyclePhase{}
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
			fn, snapshot, version, stopped := s.onChange, s.viewLocked(), s.handlerVersion, s.notificationsClosed
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
		// With no explicit override, DeriveFallback is exactly domain.NativeMode over
		// this same fresh catalogue. Reuse that read instead of enumerating twice.
		if s.profile.FallbackMode == nil {
			snapshot.NativeMode = derived.mode
			snapshot.NativeKnown = derived.known
		}
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
	target, _, err := s.readCurrent()
	switch {
	case err != nil:
		s.clearNativeView()
	case s.profile.FallbackMode != nil:
		// Explicit fallback bypasses the enumeration used by fallback derivation, but
		// native aspect is a separate diagnostic fact the main window still needs.
		// Failure here never changes fallback or the display toggle's availability.
		s.refreshNativeView(target)
	}
	// Re-probing is part of a refresh: the user may have just installed the driver.
	// Its outcome is deliberately dropped rather than merged into err -- a GPU-scaling
	// failure is never allowed to make the display half look broken.
	_ = s.refreshScalingView()
	if err == nil && !s.managed && !s.recoveryPending {
		s.state(StateNative, "已讀取目前顯示模式")
	}
	return s.Snapshot(), err
}

func (s *Session) refreshNativeView(target domain.Target) {
	modes, err := s.displays.EnumModes(target)
	native, known := domain.NativeMode(modes)
	if err != nil {
		native, known = domain.Mode{}, false
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.NativeMode, snapshot.NativeKnown = native, known
	})
}

func (s *Session) clearNativeView() {
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.NativeMode, snapshot.NativeKnown = domain.Mode{}, false
	})
}

func (s *Session) Enable() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.recoveryPending {
		return ErrDisplayRecoveryPending
	}
	if s.managed {
		return nil
	}
	s.state(StateApplying, "正在驗證並套用 "+s.gameModeLabel())
	_, _, err := s.applyGameMode()
	return err
}

// applyGameMode is R1's display half and the only place display ownership is taken.
// Everything it needs is read here and now: a fresh target, a fresh layout, a fresh
// plan. It reuses nothing from before it was called, which is exactly what lets the
// scaling cycle call it immediately after an NVAPI set without carrying a device name
// across that set.
//
// The caller owns the opening status line, because Enable and the cycle describe the
// same work to the user differently. The state and message this settles on are
// returned so a caller whose publication was masked can restate them.
func (s *Session) applyGameMode() (State, string, error) {
	target, layout, current, err := s.readLayout()
	if err != nil {
		return StateError, "", err
	}
	if current.Mode == s.profile.GameMode {
		message := "目前已是 " + s.gameModeLabel() + "；可手動恢復原始顯示模式"
		s.state(StateNative, message)
		return StateNative, message, nil
	}
	plan, err := display.PlanModeChange(layout, target.DeviceName, s.profile.GameMode)
	if err != nil {
		return StateError, "", s.fail("plan display layout", err)
	}
	bindings, err := s.readDisplayBindings(layout)
	if err != nil {
		return StateError, "", s.fail("read display identities", err)
	}
	if err := verifyResolvedBinding(target, bindings); err != nil {
		return StateError, "", s.fail("bind target display", err)
	}
	if err := s.apply(target, s.profile.GameMode, plan); err != nil {
		if uncertainLayoutWrite(err) {
			s.keepDisplayRecovery(layout, bindings, target.DeviceName)
		}
		return StateError, "", err
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
	return state, message, nil
}

func uncertainLayoutWrite(err error) bool {
	return errors.Is(err, display.ErrLayoutNotVerified) || errors.Is(err, display.ErrLayoutPartlyApplied)
}

// keepDisplayRecovery retains the only identity-bound description of the desktop as
// it stood before a write whose final state is uncertain. It deliberately does not
// claim managed ownership or a successful game mode.
func (s *Session) keepDisplayRecovery(
	layout domain.Layout,
	bindings map[string]displayBinding,
	targetDevice string,
) {
	s.saved, s.savedBindings = layout, bindings
	s.managedTargetDevice = targetDevice
	s.managed = false
	s.recoveryPending = true
	s.gameSeen = false
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.AtGameMode = false
		snapshot.Managed = false
		snapshot.RecoveryPending = true
	})
}

// apply pre-flights the target's mode and only then applies the whole arrangement.
// The plan reaching this point has already been proved safe, so the only thing left
// between it and the desktop is the driver; the display layer applies the displays
// in an order that never overlaps them, rolls back what it already changed if a
// call fails, and reports a desktop that did not end up matching the plan.
//
// No NVAPI set ever happens inside this call or the ApplyLayout beneath it: that
// rollback state is keyed by device name, and a set in the middle of it would
// invalidate the whole undo set halfway through. opMu serializes workflows, but "sets
// only at the boundary" is not something a lock can express, so it is stated here.
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
// fired its restore would never arm another one. Only gameSeen is carried over, so
// the replacement re-arms after one further full delay instead of waiting for the
// game to be seen running a second time. A watcher goroutine may call this: it
// replaces itself and must not join the watcher it stopped.
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
	if s.managed || s.recoveryPending {
		return s.restoreSaved()
	}
	if !allowFallback {
		return nil
	}
	return s.restoreFallback()
}

// restoreSaved puts the desktop back exactly as this session found it: the target's
// saved mode and every display's saved coordinate, applied together. The target is
// resolved again rather than taken from the saved layout.
//
// The saved names are re-bound to the monitors that own them before anything is
// compared against them. A DISPLAYn is a slot, not a screen, and the tool's own NVAPI
// writes are one of the things that can move a screen between slots -- so a restore
// that insisted on the saved numbers refused the desktop it had itself renumbered, and
// refused it from the toggle, the restore button and the exit alike. What has to still
// be true is that every saved monitor is still attached, which remapSavedNames checks;
// which number it answers to now is not the tool's business.
func (s *Session) restoreSaved() error {
	message := "正在恢復原始顯示模式"
	if s.recoveryPending {
		message = "正在恢復寫入前的顯示配置"
	}
	s.state(StateRestoring, message)
	target, err := s.displays.ResolveTarget(s.profile.Monitor)
	if err != nil {
		return s.fail("resolve target for restore", err)
	}
	// A restore moves every display in the saved arrangement, not only the target, so
	// every one of them has to still be attached. Checking the target alone let a
	// renumbered neighbour through, and the failure then surfaced from inside the apply
	// naming nothing the user could act on.
	attached, err := s.displays.CurrentLayout()
	if err != nil {
		return s.fail("read display layout", err)
	}
	bindings, err := s.readDisplayBindings(attached)
	if err != nil {
		return s.fail("read display identities", err)
	}
	// Committed before the target check below, so a retry starts from names that are
	// already current instead of re-deriving them from the stale ones every time.
	savedLayout, savedBindings, renamed, err := remapSavedNames(s.saved, s.savedBindings, bindings)
	if err != nil {
		return s.fail("plan display layout", err)
	}
	s.saved, s.savedBindings = savedLayout, savedBindings
	if current, known := renamed[s.managedTargetDevice]; known {
		s.managedTargetDevice = current
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
	if missing, gone := firstMissingDisplay(s.saved, attached); gone {
		return s.fail("plan display layout", fmt.Errorf("%w: the saved layout's %s is no longer attached",
			display.ErrLayoutUnsafe, missing))
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
	bindings, err := s.readDisplayBindings(layout)
	if err != nil {
		return s.fail("read display identities", err)
	}
	if err := verifyResolvedBinding(target, bindings); err != nil {
		return s.fail("bind target display", err)
	}
	s.state(StateRestoring, "正在恢復 "+domain.ModeLabel(derived.mode))
	if err := s.finishRestore(target, derived.mode, plan); err != nil {
		if uncertainLayoutWrite(err) {
			s.keepDisplayRecovery(layout, bindings, target.DeviceName)
		}
		return err
	}
	return nil
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
	s.recoveryPending = false
	s.saved = domain.Layout{}
	s.savedBindings = nil
	s.managedTargetDevice = ""
	// gameSeen has managed's lifetime, so releasing ownership ends it. The scaling
	// cycle carries the flag across this point by hand, because there the ownership
	// really does continue -- it is only being put down and picked straight back up.
	s.gameSeen = false
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.Target, snapshot.CurrentMode = target, mode
		snapshot.MatchedBy = target.MatchedBy
		snapshot.AtGameMode, snapshot.Managed, snapshot.RecoveryPending = mode == s.profile.GameMode, false, false
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
	// R2 at exit: the display goes back first, and only then does the scaling value.
	// This is leave-only -- it puts back scalingSaved and applies nothing -- which is
	// what makes it a different shape from the cycle rather than a lesser version of it.
	s.restoreScalingAtExit()
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

// retireForReplacement is the in-process profile transition boundary. Unlike normal
// process shutdown, it must not swallow any restore obligation: the process remains
// alive, so closing the only owner would remove the user's retry path. beforeClose is
// run while opMu excludes every Session operation; SaveAndReplace uses that slot so a
// file is written only after the guard passes and before this session can change state.
func (s *Session) retireForReplacement(beforeClose func() error) error {
	s.opMu.Lock()
	if s.closed {
		s.opMu.Unlock()
		return ErrClosed
	}
	if s.managed || s.recoveryPending || s.scalingOwned {
		s.opMu.Unlock()
		return ErrSessionManaged
	}
	if beforeClose != nil {
		if err := beforeClose(); err != nil {
			s.opMu.Unlock()
			return err
		}
	}
	watcher := s.stopWatcher()
	s.closed = true
	s.mu.Lock()
	s.notificationsClosed = true
	s.onChange = nil
	close(s.notifyStop)
	s.mu.Unlock()
	s.opMu.Unlock()
	joinWatcher(watcher)
	s.watchers.Wait()
	<-s.notifyDone
	close(s.shutdownDone)
	return nil
}

// restoreScalingAtExit puts back the value this run changed and then gets out of the
// way. A failure here never stops the exit: the value is runtime-only and a reboot or
// a driver reload undoes it, while a tool that cannot be closed is a worse outcome
// than a scaling setting left changed. A display restore failure, by contrast, does
// hold the tool open, because a desktop in the wrong mode really is broken.
//
// The failure is left visible in the snapshot instead of returned: Scaling.Owned stays
// true, which is the structural signal that the value was not put back.
func (s *Session) restoreScalingAtExit() {
	if !s.scalingOwned {
		return
	}
	outcome, err := s.setScaling(true)
	s.bookScaling(true, outcome, err)
	if err != nil {
		failure := fmt.Errorf("退出時還原 GPU 縮放設定失敗：%w", err)
		s.finishScaling(outcome, StateError, failure.Error(), failure)
		return
	}
	s.publishScalingView(outcome)
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
	// Only the seen flag is inherited, and it is read here, under opMu, rather than
	// from inside the goroutine.
	seen := s.gameSeen
	ctx, cancel := context.WithCancel(context.Background())
	watcher := &sessionWatcher{cancel: cancel, done: make(chan struct{})}
	s.watcher = watcher
	ticker := s.clock.NewTicker(time.Second)
	s.watchers.Add(1)
	go func() {
		defer s.watchers.Done()
		defer close(watcher.done)
		defer ticker.Stop()
		tracker := newGameTracker(s.profile.RestoreDelay, seen)
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
	// The generation check is what stops a poll that came due during a scaling cycle:
	// stopWatcher bumps it before cancelling, so a poll already waiting at this gate
	// returns here without its tracker ever being consulted. The managed check stops
	// it a second, independent time -- s.managed legitimately goes false in the middle
	// of a cycle, and that false must never be read as "the game ended, restore now".
	if s.closed || !s.managed || generation != s.generation {
		return false
	}
	if err != nil {
		s.fail("check game process", err)
		return true
	}
	result := tracker.Observe(running, s.clock.Now())
	if result.SeenGame {
		s.gameSeen = true
	}
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

// ScalingFullScreenByGPU is the one value the apply button writes: full-screen scaling
// performed by the GPU. It is a function rather than a package variable so that no
// caller can change what this product means by "設為全螢幕縮放（GPU）".
func ScalingFullScreenByGPU() scaling.Value {
	return scaling.Value{Raw: 2, Mode: scaling.ModeFullScreen, By: scaling.ByGPU}
}

// ApplyGPUScaling asks the driver for full-screen scaling performed by the GPU. While
// this session owns an applied mode the press runs the whole cycle rather than being
// refused: the moment a user notices black bars is precisely the moment the game mode
// is on, so a button disabled exactly then is a button that does not exist.
func (s *Session) ApplyGPUScaling() error { return s.changeScaling(false) }

// RestoreGPUScaling puts back the effective value this run read immediately before it
// changed the setting. It is a quiet no-op when this run changed nothing: the tool
// never writes a scaling value it did not first read as the user's own. The two
// buttons are otherwise identical, including running the cycle while managed.
func (s *Session) RestoreGPUScaling() error { return s.changeScaling(true) }

// RefreshScaling re-probes the driver and re-reads the effective value. Both are
// reads, so it is safe on the startup path that must not change anything, and
// re-probing is what lets a user who just installed the driver get the button back.
func (s *Session) RefreshScaling() (Snapshot, error) {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.closed {
		return s.Snapshot(), ErrClosed
	}
	err := s.refreshScalingView()
	return s.Snapshot(), err
}

func (s *Session) changeScaling(restore bool) error {
	s.opMu.Lock()
	if s.closed {
		s.opMu.Unlock()
		return ErrClosed
	}
	if s.recoveryPending {
		s.opMu.Unlock()
		return ErrDisplayRecoveryPending
	}
	if restore && !s.scalingOwned {
		s.opMu.Unlock()
		return nil
	}
	if !s.managed {
		err := s.writeScaling(restore)
		s.opMu.Unlock()
		return err
	}
	// R5. The watcher is stopped inside opMu and joined outside it, for the same reason
	// Disable does: that goroutine may already be blocked on this very lock.
	watcher := s.stopWatcher()
	err := s.runScalingCycle(restore)
	s.opMu.Unlock()
	joinWatcher(watcher)
	return err
}

// writeScaling is a scaling change made while this session owns no display mode: one
// NVAPI set, then a fresh read of the target. There is no saved arrangement in flight,
// so R1 and R2 have nothing to order here -- only the tail read matters, because the
// set may have renumbered the name the window is showing.
func (s *Session) writeScaling(restore bool) error {
	s.state(StateApplying, scalingWriteMessage(restore))
	outcome, err := s.setScaling(restore)
	s.bookScaling(restore, outcome, err)
	s.refreshTargetView()
	if err != nil {
		failure := fmt.Errorf("%s失敗：%w", scalingActionLabel(restore), err)
		return s.finishScaling(outcome, StateError, failure.Error(), failure)
	}
	return s.finishScaling(outcome, StateNative, s.scalingResultMessage(outcome), nil)
}

// runScalingCycle is R5: R2's exit joined to R1's entry as one operation under one
// acquisition of opMu. saved is consumed and cleared before the NVAPI set and rebuilt
// from an entirely fresh read after it, so R2's promise -- no set between saved being
// built and being cleared -- still holds verbatim, in two stretches instead of one. No
// device name crosses the set: step 2's names are finished with before it, and step 4's
// do not exist until after it. The screens changing mode twice more is the known,
// accepted price of keeping that true.
//
// The caller has already stopped the watcher and joins it only after releasing opMu.
// opMu is never released between these steps, so no poll can interleave.
func (s *Session) runScalingCycle(restore bool) error {
	// gameSeen dies with managed, and step 2 legitimately ends ownership. Carrying it
	// by hand is what keeps the shipped promise intact across the cycle: a game that
	// ends while the screens are changing is still a game that was seen, so the
	// automatic restore it deserves is re-armed rather than swallowed. The countdown is
	// deliberately not carried -- step 4's tracker is a new object with missing,
	// missingAt and fired at zero, so the delay that follows is a full-length one.
	carriedSeen := s.gameSeen
	s.cyclePhase("正在變更 GPU 縮放：恢復原始排列…")

	// Step 2 -- the whole of R2. A failure keeps managed and saved so the user can
	// retry, puts the watcher back exactly as Disable's failure path does, and aborts
	// before any NVAPI call at all: saved is still live, and a set here is precisely
	// what R2 forbids. It would also renumber the device names the retry depends on.
	if err := s.restoreSaved(); err != nil {
		s.ensureWatcher()
		s.endCycle()
		failure := fmt.Errorf("恢復原始顯示排列失敗，GPU 縮放未變更：%w", err)
		s.updateSnapshot(func(snapshot *Snapshot) {
			snapshot.State, snapshot.Message, snapshot.Err = StateError, failure.Error(), failure
		})
		return failure
	}

	// Step 3 -- the one NVAPI set, with no saved layout and no device name alive across it.
	s.cyclePhase("正在變更 GPU 縮放：寫入縮放設定…")
	outcome, err := s.setScaling(restore)
	s.bookScaling(restore, outcome, err)
	if err != nil {
		// The game mode is deliberately not re-applied. The desktop is on the
		// arrangement the user had before they ever enabled it -- a known-good state
		// that needs no rescuing -- and a failed set may already have taken effect in
		// part, so planning a four-display change the user did not ask for would be a
		// second write stacked on an unknown one.
		s.refreshTargetView()
		s.endCycle()
		failure := fmt.Errorf("%s失敗，桌面已恢復為原始排列，工具不再管理顯示模式：%w",
			scalingActionLabel(restore), err)
		return s.finishScaling(outcome, StateError, failure.Error(), failure)
	}

	// Step 4 -- the whole of R1, redone rather than reused: fresh identity resolution,
	// fresh CurrentLayout, fresh PlanModeChange, and saved rebuilt from that fresh
	// layout. Nothing from step 2, and nothing from before the cycle, may appear here.
	s.cyclePhase("正在變更 GPU 縮放：重新套用 " + s.gameModeLabel() + "…")
	s.gameSeen = carriedSeen
	state, message, applyErr := s.applyGameMode()
	if !s.managed {
		s.gameSeen = false
	}
	if applyErr != nil {
		// The write really happened, so it stays booked -- rolling it back here would
		// mean exit could no longer undo it. The mode did not, so no display ownership
		// is taken: the desktop is on an arrangement this tool did not cause and has
		// nothing to restore. The end state is the ordinary unmanaged one plus whatever
		// that write left behind, and the retry is one press of the mode toggle.
		s.endCycle()
		var failure error
		if s.recoveryPending {
			failure = fmt.Errorf("GPU 縮放已變更為「%s」，但重新套用 %s 後無法確認顯示配置；已保留寫入前排列，請先恢復：%w",
				ScalingLabel(effectiveValue(outcome)), s.gameModeLabel(), applyErr)
		} else {
			failure = fmt.Errorf("GPU 縮放已變更為「%s」，但重新套用 %s 失敗，桌面維持在原始排列：%w",
				ScalingLabel(effectiveValue(outcome)), s.gameModeLabel(), applyErr)
		}
		return s.finishScaling(outcome, StateError, failure.Error(), failure)
	}
	s.endCycle()
	return s.finishScaling(outcome, state, message+"；"+s.scalingResultMessage(outcome), nil)
}

// setScaling issues this workflow's one NVAPI set, and is the only place in the session
// that writes scaling at all. R3 lives in its signature: it neither accepts nor returns
// a domain.Target or a domain.Layout, so the GDI name NVAPI needs is created and
// consumed inside internal/scaling and never becomes something this file could reuse.
//
// Restore goes through the controller's atomic entry point, which re-resolves the
// identity and compares it against scalingID inside its own critical section. A
// read-compare-apply written here instead would reopen exactly the target-swap race
// that entry point exists to close.
func (s *Session) setScaling(restore bool) (scaling.Outcome, error) {
	if restore {
		return s.scalings.Restore(s.profile.Monitor, s.scalingID, s.scalingSaved)
	}
	return s.scalings.Apply(s.profile.Monitor, ScalingFullScreenByGPU())
}

// bookScaling records only writes that really happened, and never rolls one back
// because something after it failed: a write the tool has forgotten is a write it can
// no longer undo at exit. A formal set the driver accepted counts even when the
// mandatory read-back afterwards did not come through.
func (s *Session) bookScaling(restore bool, outcome scaling.Outcome, err error) {
	if restore {
		// A restore whose value is already effective is a successful no-op: nothing was
		// written because nothing needed to be, and there is nothing left to undo.
		if outcome.Applied || (err == nil && !outcome.SetAttempted) {
			s.scalingOwned = false
		}
		return
	}
	if !outcome.Applied {
		return
	}
	s.scalingOwned = true
	s.scalingSaved = outcome.Previous.Effective
	s.scalingID = outcome.Previous.DisplayID
}

// finishScaling publishes the whole result of a scaling workflow in one snapshot: what
// the driver now reports, what this session owns, and the state the session really
// ended in. One publication, because two would show the user a half-updated row.
//
// Probe runs again first. That is how a controller which latched itself off after a
// struct-version mismatch or a diverged payload disables its own button for the rest of
// the run without this file having to know either rule.
func (s *Session) finishScaling(outcome scaling.Outcome, state State, message string, failure error) error {
	probe := s.scalings.Probe()
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.Scaling = s.scalingViewOf(probe, outcome, snapshot.Scaling)
		snapshot.State, snapshot.Message, snapshot.Err = state, message, failure
	})
	return failure
}

// publishScalingView records a scaling result without claiming the session's state,
// which is what the exit path needs: it must not overwrite a message the display half
// is still entitled to.
func (s *Session) publishScalingView(outcome scaling.Outcome) {
	probe := s.scalings.Probe()
	s.updateSnapshot(func(snapshot *Snapshot) {
		snapshot.Scaling = s.scalingViewOf(probe, outcome, snapshot.Scaling)
	})
}

func (s *Session) scalingViewOf(
	probe scaling.Availability,
	outcome scaling.Outcome,
	previous ScalingSnapshot,
) ScalingSnapshot {
	view := ScalingSnapshot{
		Available:      probe.Available,
		Reason:         probe.Reason,
		Known:          previous.Known,
		Effective:      previous.Effective,
		Requested:      outcome.Requested,
		RequestedKnown: outcome.SetAttempted,
		Matched:        outcome.Matched,
		Owned:          s.scalingOwned,
		Saved:          s.scalingSaved,
	}
	// A set that failed may still have taken effect in part, so the controller reads
	// back regardless and says whether that read is authoritative. Only then is the
	// value on screen a fact rather than an assumption.
	if outcome.ReadBackKnown {
		view.Known, view.Effective = true, outcome.State.Effective
	}
	return view
}

// refreshScalingView is the read-only half: probe, and when the driver is there, read
// the effective value. A monitor the driver refuses to map is this button's own
// unavailability -- it names this screen and that vendor's control panel, and touches
// nothing else in the window.
func (s *Session) refreshScalingView() error {
	probe := s.scalings.Probe()
	target := s.Snapshot().Target
	var state scaling.State
	var readErr error
	if probe.Available {
		state, readErr = s.scalings.Read(s.profile.Monitor)
	}
	s.updateSnapshot(func(snapshot *Snapshot) {
		view := ScalingSnapshot{
			Requested:      snapshot.Scaling.Requested,
			RequestedKnown: snapshot.Scaling.RequestedKnown,
			Owned:          s.scalingOwned,
			Saved:          s.scalingSaved,
		}
		switch {
		case !probe.Available:
			view.Reason = scalingUnavailableReason(target, probe, nil)
		case readErr != nil:
			view.Reason = scalingUnavailableReason(target, probe, readErr)
		default:
			view.Available, view.Known, view.Effective = true, true, state.Effective
			view.Matched = view.RequestedKnown && state.Effective.Raw == view.Requested.Raw
		}
		snapshot.Scaling = view
	})
	if !probe.Available {
		return probe.Err
	}
	return readErr
}

// scalingUnavailableReason turns driver facts into the actionable sentence beside the
// scaling button. Availability is still decided functionally by NVAPI; the adapter's
// DeviceString is used only after that decision to name the control panel to open.
func scalingUnavailableReason(target domain.Target, probe scaling.Availability, readErr error) string {
	if errors.Is(probe.Err, scaling.ErrNvapiInterfaceUnavailable) {
		return "這個 NVIDIA 驅動版本不提供需要的介面。"
	}
	if !probe.Available && errors.Is(probe.Err, scaling.ErrNvapiDLLUnavailable) {
		adapter := strings.TrimSpace(target.AdapterDeviceString)
		if adapter != "" {
			return "偵測到 " + adapter + "，但找不到可用的 NVIDIA 驅動。請到" +
				adapterControlPanel(adapter) + "設定全螢幕縮放。"
		}
		if reason := strings.TrimSpace(probe.Reason); reason != "" {
			return reason
		}
		return "找不到可用的 NVIDIA 驅動，請到顯示卡控制台手動設定全螢幕縮放。"
	}
	if !probe.Available {
		if reason := strings.TrimSpace(probe.Reason); reason != "" {
			return reason
		}
		if probe.Err != nil {
			return probe.Err.Error()
		}
		return "無法使用 GPU 縮放。"
	}
	if readErr == nil {
		return ""
	}
	if errors.Is(readErr, scaling.ErrNotNvidiaDisplay) ||
		errors.Is(readErr, scaling.ErrScalingTargetNotFound) ||
		errors.Is(readErr, scaling.ErrScalingTargetAmbiguous) {
		label := strings.TrimSpace(target.Identity.Label)
		if label == "" {
			label = "選取的顯示器"
		}
		return "「" + label + "」不在可唯一對應的 NVIDIA 顯示路徑上，" +
			"請到該顯示卡的控制台手動設定全螢幕縮放。"
	}
	return readErr.Error()
}

func adapterControlPanel(adapter string) string {
	lower := strings.ToLower(adapter)
	switch {
	case strings.Contains(lower, "amd"), strings.Contains(lower, "radeon"):
		return " AMD Software "
	case strings.Contains(lower, "intel"):
		return " Intel Graphics Command Center "
	case strings.Contains(lower, "nvidia"):
		return " NVIDIA 控制台"
	default:
		return "顯示卡控制台"
	}
}

// refreshTargetView re-reads the target at the tail of a workflow so the window never
// shows a \\.\DISPLAYn that an NVAPI set has already invalidated. Nothing is ever
// called with the name the window prints, but a stale one on screen costs the user
// their trust in everything else in it. A read failure here is not this workflow's
// result and must not replace the message that is.
func (s *Session) refreshTargetView() {
	target, err := s.displays.ResolveTarget(s.profile.Monitor)
	if err != nil {
		return
	}
	mode, err := s.displays.CurrentMode(target)
	if err != nil {
		return
	}
	s.observed(target, mode)
}

// scalingResultMessage reports what the driver says, never what the user hoped. A
// value the driver normalised is stated as both values and is not an error: nothing
// broke and nothing needs undoing. The tool never claims the black bars are gone --
// it cannot read that at all, so it says only what the scaling setting now is.
func (s *Session) scalingResultMessage(outcome scaling.Outcome) string {
	if !outcome.ReadBackKnown {
		return "GPU 縮放已寫入，但讀不回生效值"
	}
	effective := ScalingLabel(outcome.State.Effective)
	if outcome.SetAttempted && !outcome.Matched {
		return "已要求「" + ScalingLabel(outcome.Requested) + "」，驅動實際套用的是「" + effective + "」"
	}
	return "GPU 縮放：" + effective
}

func effectiveValue(outcome scaling.Outcome) scaling.Value {
	if outcome.ReadBackKnown {
		return outcome.State.Effective
	}
	return outcome.Requested
}

func scalingActionLabel(restore bool) string {
	if restore {
		return "還原 GPU 縮放設定"
	}
	return "變更 GPU 縮放"
}

func scalingWriteMessage(restore bool) string {
	return "正在" + scalingActionLabel(restore) + "…"
}

// ScalingLabel names a scaling value in the words the window prints. It lives here
// rather than in internal/scaling because the driver's raw number is a fact while its
// Traditional Chinese name is this product's presentation of it.
func ScalingLabel(value scaling.Value) string {
	var mode string
	switch value.Mode {
	case scaling.ModeDefault:
		mode = "預設"
	case scaling.ModeFullScreen:
		mode = "全螢幕"
	case scaling.ModeAspectRatio:
		mode = "長寬比"
	case scaling.ModeNoScaling:
		mode = "不縮放"
	case scaling.ModeIntegerScaling:
		mode = "整數倍縮放"
	case scaling.ModeCustomized:
		mode = "自訂"
	default:
		return fmt.Sprintf("未知的縮放設定（Raw=%d）", value.Raw)
	}
	switch value.By {
	case scaling.ByGPU:
		return mode + "（由 GPU 執行）"
	case scaling.ByDisplay:
		return mode + "（由顯示器執行）"
	default:
		return mode
	}
}

// errNoScalingController is what a session built without a GPU-scaling controller
// reports. It is not a driver problem to explain in NVIDIA's terms: this session
// simply has no scaling support wired into it, and the button says exactly that.
var errNoScalingController = fmt.Errorf("%w：未提供 GPU 縮放控制器", scaling.ErrNvapiUnavailable)

// unavailableScaling keeps a nil controller from being a panic waiting for the first
// press. Every method refuses in the same way Probe already describes.
type unavailableScaling struct{}

var _ scaling.Controller = unavailableScaling{}

func (unavailableScaling) Probe() scaling.Availability {
	return scaling.Availability{Reason: errNoScalingController.Error(), Err: errNoScalingController}
}

func (unavailableScaling) Read(domain.MonitorIdentity) (scaling.State, error) {
	return scaling.State{}, errNoScalingController
}

func (unavailableScaling) Apply(_ domain.MonitorIdentity, value scaling.Value) (scaling.Outcome, error) {
	return scaling.Outcome{Requested: value}, errNoScalingController
}

func (unavailableScaling) Restore(_ domain.MonitorIdentity, _ uint32, value scaling.Value) (scaling.Outcome, error) {
	return scaling.Outcome{Requested: value}, errNoScalingController
}

func (unavailableScaling) Close() error { return nil }
