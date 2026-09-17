package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Alien7666/change_resolution/internal/config"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/process"
	"github.com/Alien7666/change_resolution/internal/scaling"
)

var (
	// ErrSessionManaged keeps a profile transition from invalidating the saved
	// desktop arrangement that only the current session knows how to restore.
	ErrSessionManaged = errors.New("目前的顯示模式仍由工具管理，請先恢復原始顯示模式再變更設定")

	// ErrResetUnavailable prevents a normal, understood configuration from being
	// moved aside through the recovery-only Reset action.
	ErrResetUnavailable = errors.New("目前沒有讀不懂的設定檔可以重新設定")
)

type providerConfigState uint8

const (
	providerUnconfigured providerConfigState = iota
	providerConfigured
	providerReadOnly
)

// providerStore is the whole configuration boundary used by Provider. The disk
// implementation delegates to config; tests replace it so every startup and
// recovery path is proven without touching the user's real file or opening Explorer.
type providerStore interface {
	Path() (string, error)
	Load() (config.File, error)
	Save(config.File) error
	Backup() (string, error)
	OpenFolder(string) error
}

type diskProviderStore struct{}

func (diskProviderStore) Path() (string, error)       { return config.Path() }
func (diskProviderStore) Load() (config.File, error)  { return config.Load() }
func (diskProviderStore) Save(file config.File) error { return config.Save(file) }
func (diskProviderStore) Backup() (string, error)     { return config.Backup() }
func (diskProviderStore) OpenFolder(path string) error {
	directory := filepath.Dir(path)
	if err := exec.Command("explorer.exe", directory).Run(); err != nil {
		return fmt.Errorf("開啟設定檔資料夾 %s 失敗：%w", directory, err)
	}
	return nil
}

// Provider owns the currently configured Session and the state in which there is
// deliberately no session. It is the UI's one stable reference while Reload and the
// settings flow replace the profile-specific session underneath it.
//
// mu protects state only. No display, file, process, shutdown, or observer call runs
// while it is held. writeMu makes the generation change and a secondary-rebind save
// indivisible, so a late notification from an old session cannot overwrite a newer
// profile. It is never held while Shutdown waits for that old session's dispatcher.
type Provider struct {
	mu      sync.Mutex
	writeMu sync.Mutex
	opMu    sync.Mutex

	displays  display.Controller
	processes process.Checker
	store     providerStore

	// scalings is shared by every session this provider builds and outlives all of
	// them: the NVAPI handle is a process-wide resource, so it is unloaded once, here,
	// when the process ends rather than when a profile changes.
	scalings scaling.Controller

	session    *Session
	state      providerConfigState
	path       string
	configFile config.File
	configErr  error
	snapshot   Snapshot
	observer   func(Snapshot)
	generation uint64

	// A failed automatic rebind write is not retried on every session refresh. The
	// error remains visible until an explicit Reload or Replace starts a fresh state.
	rebindAttempted bool
	rebindMessage   string
	rebindErr       error
	closed          bool

	notify              chan struct{}
	notifyStop          chan struct{}
	notificationsClosed bool
}

// NewProvider loads the user's configuration without writing it and constructs the
// corresponding startup state. A load rejection is data for the UI, not a process
// startup failure, so this constructor always returns a usable provider.
//
// It builds sessions with no GPU-scaling support. That is the honest state for a
// caller that has no controller to give -- the scaling row reports itself unavailable
// and nothing else in the window changes.
func NewProvider(displays display.Controller, processes process.Checker) *Provider {
	return newProvider(displays, processes, diskProviderStore{})
}

// NewProviderWithScaling is the composition root's constructor. The provider hands the
// controller to every session it builds and closes it when the process ends.
func NewProviderWithScaling(
	displays display.Controller,
	processes process.Checker,
	scalings scaling.Controller,
) *Provider {
	return newScalingProvider(displays, processes, scalings, diskProviderStore{})
}

func newProvider(displays display.Controller, processes process.Checker, store providerStore) *Provider {
	return newScalingProvider(displays, processes, nil, store)
}

func newScalingProvider(
	displays display.Controller,
	processes process.Checker,
	scalings scaling.Controller,
	store providerStore,
) *Provider {
	if scalings == nil {
		scalings = unavailableScaling{}
	}
	p := &Provider{
		displays: displays, processes: processes, scalings: scalings, store: store,
		notify: make(chan struct{}, 1), notifyStop: make(chan struct{}),
	}
	go p.dispatchChanges()
	path, pathErr := store.Path()
	p.path = path
	if pathErr != nil {
		p.setInitialRejection(pathErr)
		return p
	}
	file, err := store.Load()
	p.installLoaded(file, err)
	return p
}

// Session returns the session that owns the current profile. It is nil while the
// configuration is absent or read-only; callers must fetch it for every action rather
// than retaining an earlier pointer across Reload or Replace.
func (p *Provider) Session() *Session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.session
}

func (p *Provider) Configured() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == providerConfigured
}

func (p *Provider) Unconfigured() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == providerUnconfigured
}

func (p *Provider) ReadOnly() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == providerReadOnly
}

func (p *Provider) ConfigPath() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.path
}

func (p *Provider) ConfigError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.configErr
}

// ReadScaling is the settings dialog's provider-owned, read-only seam. The selected
// monitor may differ from the active profile, and first run has no Session at all, so
// borrowing Provider.Snapshot().Scaling would misattribute one monitor's value to
// another. This resolves and measures the requested identity without any writes.
func (p *Provider) ReadScaling(identity domain.MonitorIdentity) ScalingSnapshot {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	if p.isClosed() {
		return ScalingSnapshot{Reason: ErrClosed.Error()}
	}
	target, err := p.displays.ResolveTarget(identity)
	if err != nil {
		return ScalingSnapshot{Reason: err.Error()}
	}
	probe := p.scalings.Probe()
	if !probe.Available {
		return ScalingSnapshot{Reason: scalingUnavailableReason(target, probe, nil)}
	}
	state, err := p.scalings.Read(identity)
	if err != nil {
		return ScalingSnapshot{Reason: scalingUnavailableReason(target, probe, err)}
	}
	return ScalingSnapshot{Available: true, Known: true, Effective: state.Effective}
}

func (p *Provider) Snapshot() Snapshot {
	for {
		p.mu.Lock()
		session, generation := p.session, p.generation
		stored := copySnapshot(p.snapshot)
		p.mu.Unlock()
		if session == nil {
			return stored
		}

		// A Session operation updates its own snapshot before its asynchronous observer
		// reaches Provider. Read the live value so the command that just completed cannot
		// render stale Managed/AtGameMode controls. Provider-owned rebind notices remain
		// authoritative because Session has no knowledge of their disk outcome.
		live := copySnapshot(session.Snapshot())
		p.mu.Lock()
		current := generation == p.generation && session == p.session
		configuredPath := p.configFile.Monitor.InstancePath
		rebindMessage, rebindErr := p.rebindMessage, p.rebindErr
		p.mu.Unlock()
		if !current {
			continue
		}
		live.Profile.Monitor.InstancePath = configuredPath
		// The persistence outcome belongs to Provider, but it is only the most useful
		// status while the Session is quietly native. A newer display error or an
		// applying/restoring/scaling phase must keep its own message and error identity.
		if live.State == StateNative && live.Err == nil && (rebindMessage != "" || rebindErr != nil) {
			live.Message, live.Err = rebindMessage, rebindErr
		}
		return live
	}
}

// SetOnChange replaces the UI observer and immediately supplies the current state.
// It survives session replacement because Provider, rather than each Session, owns it.
func (p *Provider) SetOnChange(fn func(Snapshot)) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.observer = fn
	p.mu.Unlock()
	p.signalChange()
}

// Replace swaps an understood profile into a new Session. Persisting a settings
// dialog is deliberately separate: Task 12 saves atomically before calling this
// method, and a save failure must leave the old session running.
func (p *Provider) Replace(profile domain.Profile) error {
	p.opMu.Lock()
	if p.isClosed() {
		p.opMu.Unlock()
		return ErrClosed
	}
	err := p.replace(profile, config.FromProfile(profile))
	p.opMu.Unlock()
	if err == nil {
		p.signalChange()
	}
	return err
}

func (p *Provider) replace(profile domain.Profile, file config.File) error {
	old := p.Session()
	if old != nil && old.Snapshot().Managed {
		return ErrSessionManaged
	}
	if err := p.retire(old); err != nil {
		return err
	}
	p.installSession(profile, file)
	return nil
}

// Reload re-reads config.json. It never aliases Session.Refresh: the latter only
// re-enumerates the configured monitor. If the current session owns an applied mode,
// the loaded result is left unapplied so the existing restore entry point survives.
func (p *Provider) Reload() error {
	p.opMu.Lock()
	if p.isClosed() {
		p.opMu.Unlock()
		return ErrClosed
	}

	file, loadErr := p.store.Load()
	old := p.Session()
	if old != nil && old.Snapshot().Managed {
		p.opMu.Unlock()
		return ErrSessionManaged
	}
	if err := p.retire(old); err != nil {
		p.opMu.Unlock()
		return err
	}
	p.installLoaded(file, loadErr)
	p.opMu.Unlock()
	p.signalChange()
	if errors.Is(loadErr, fs.ErrNotExist) {
		return nil
	}
	return loadErr
}

// Reset is the explicit recovery action for a file Provider could not understand.
// It moves that file aside and only then enters the honest unconfigured state.
func (p *Provider) Reset() (string, error) {
	p.opMu.Lock()
	if p.isClosed() {
		p.opMu.Unlock()
		return "", ErrClosed
	}
	if !p.ReadOnly() {
		p.opMu.Unlock()
		return "", ErrResetUnavailable
	}
	backup, err := p.store.Backup()
	if err != nil {
		wrapped := fmt.Errorf("重新設定失敗：%w", err)
		p.publishConfigError(wrapped)
		p.opMu.Unlock()
		p.signalChange()
		return "", wrapped
	}
	p.installUnconfigured("尚未設定；原設定檔已保留於 " + backup)
	p.opMu.Unlock()
	p.signalChange()
	return backup, nil
}

func (p *Provider) OpenConfigFolder() error {
	path := p.ConfigPath()
	if strings.TrimSpace(path) == "" {
		return errors.New("無法決定設定檔資料夾")
	}
	return p.store.OpenFolder(path)
}

// Shutdown removes the current session from new callers before waiting for its
// watcher and notification dispatcher. A failed restore puts it back, still usable.
func (p *Provider) Shutdown() error {
	p.opMu.Lock()
	if p.isClosed() {
		p.opMu.Unlock()
		return nil
	}
	old := p.Session()
	err := p.retire(old)
	if err == nil {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
	}
	p.opMu.Unlock()
	if err == nil {
		p.stopNotifications()
		// The NVAPI handle is unloaded once the last session has already put back what
		// it owned. A failure to unload is deliberately not returned: it cannot leave
		// the desktop wrong, and Shutdown's error is what decides whether the tool is
		// allowed to exit.
		_ = p.scalings.Close()
	}
	return err
}

func (p *Provider) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

func (p *Provider) retire(old *Session) error {
	if old == nil {
		return nil
	}
	p.writeMu.Lock()
	p.mu.Lock()
	if p.session != old {
		p.mu.Unlock()
		p.writeMu.Unlock()
		return nil
	}
	p.generation++
	p.session = nil
	p.mu.Unlock()
	p.writeMu.Unlock()

	if err := old.Shutdown(); err != nil {
		p.installExisting(old)
		return err
	}
	return nil
}

func (p *Provider) installExisting(session *Session) {
	p.writeMu.Lock()
	p.mu.Lock()
	p.generation++
	generation := p.generation
	p.session = session
	p.snapshot = copySnapshot(session.Snapshot())
	p.mu.Unlock()
	p.writeMu.Unlock()
	session.SetOnChange(func(next Snapshot) { p.observe(generation, next) })
}

func (p *Provider) installLoaded(file config.File, loadErr error) {
	switch {
	case loadErr == nil:
		p.installSession(file.Profile(), file)
	case errors.Is(loadErr, fs.ErrNotExist):
		p.installUnconfigured("尚未設定")
	default:
		p.installReadOnly(loadErr)
	}
}

func (p *Provider) installSession(profile domain.Profile, file config.File) {
	session := NewSession(p.displays, p.processes, p.scalings, profile)
	p.writeMu.Lock()
	p.mu.Lock()
	p.generation++
	generation := p.generation
	p.session = session
	p.state = providerConfigured
	p.configFile = file
	p.configErr = nil
	p.rebindAttempted = false
	p.rebindMessage = ""
	p.rebindErr = nil
	p.snapshot = copySnapshot(session.Snapshot())
	p.mu.Unlock()
	p.writeMu.Unlock()
	session.SetOnChange(func(next Snapshot) { p.observe(generation, next) })
}

func (p *Provider) installUnconfigured(message string) {
	p.writeMu.Lock()
	p.mu.Lock()
	p.generation++
	p.session = nil
	p.state = providerUnconfigured
	p.configFile = config.File{}
	p.configErr = nil
	p.rebindAttempted = false
	p.rebindMessage = ""
	p.rebindErr = nil
	p.snapshot = Snapshot{State: StateNative, Revision: 1, Message: message}
	p.mu.Unlock()
	p.writeMu.Unlock()
}

func (p *Provider) installReadOnly(err error) {
	p.writeMu.Lock()
	p.mu.Lock()
	p.generation++
	p.session = nil
	p.state = providerReadOnly
	p.configFile = config.File{}
	p.configErr = err
	p.rebindAttempted = false
	p.rebindMessage = ""
	p.rebindErr = nil
	p.snapshot = Snapshot{State: StateError, Revision: 1, Message: err.Error(), Err: err}
	p.mu.Unlock()
	p.writeMu.Unlock()
}

func (p *Provider) setInitialRejection(err error) {
	p.state = providerReadOnly
	p.configErr = err
	p.snapshot = Snapshot{State: StateError, Revision: 1, Message: err.Error(), Err: err}
}

// observe accepts notifications only from the currently installed generation. The
// ordinary snapshot is published first; a qualifying secondary match then performs
// its one persistence attempt and publishes the outcome as a second state change.
func (p *Provider) observe(generation uint64, snapshot Snapshot) {
	p.mu.Lock()
	if generation != p.generation || p.session == nil {
		p.mu.Unlock()
		return
	}
	snapshot.Profile.Monitor.InstancePath = p.configFile.Monitor.InstancePath
	p.snapshot = copySnapshot(snapshot)
	p.mu.Unlock()
	p.signalChange()

	p.persistSecondaryRebind(generation, snapshot)
}

func (p *Provider) persistSecondaryRebind(generation uint64, snapshot Snapshot) {
	path := snapshot.Target.Identity.InstancePath
	if snapshot.MatchedBy != domain.MatchHardwareID || strings.TrimSpace(path) == "" {
		return
	}

	p.writeMu.Lock()
	p.mu.Lock()
	if generation != p.generation || p.session == nil || p.state != providerConfigured ||
		p.rebindAttempted || strings.EqualFold(p.configFile.Monitor.InstancePath, path) {
		p.mu.Unlock()
		p.writeMu.Unlock()
		return
	}
	p.rebindAttempted = true
	file := p.configFile
	file.Monitor.InstancePath = path
	p.mu.Unlock()

	err := p.store.Save(file)
	p.mu.Lock()
	if generation != p.generation || p.session == nil {
		p.mu.Unlock()
		p.writeMu.Unlock()
		return
	}
	if err != nil {
		p.configErr = fmt.Errorf("無法記住顯示器的新裝置介面路徑：%w", err)
		p.rebindMessage = p.configErr.Error()
		p.rebindErr = p.configErr
	} else {
		p.configFile = file
		p.configErr = nil
		p.snapshot.Profile.Monitor.InstancePath = path
		p.rebindMessage = "已以硬體 ID 重新對應到 " + snapshot.Target.DeviceName
		p.rebindErr = nil
	}
	p.snapshot.Message = p.rebindMessage
	p.snapshot.Err = p.rebindErr
	p.snapshot.Revision++
	p.mu.Unlock()
	p.writeMu.Unlock()
	p.signalChange()
}

func (p *Provider) publishConfigError(err error) {
	p.mu.Lock()
	p.configErr = err
	p.snapshot.State = StateError
	p.snapshot.Message = err.Error()
	p.snapshot.Err = err
	p.snapshot.Revision++
	p.mu.Unlock()
}

func copySnapshot(snapshot Snapshot) Snapshot {
	snapshot.Profile = snapshot.Profile.Copy()
	return snapshot
}

func (p *Provider) signalChange() {
	p.mu.Lock()
	closed := p.notificationsClosed
	p.mu.Unlock()
	if closed {
		return
	}
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// dispatchChanges isolates the UI observer from Session's own notification
// dispatcher. An observer may synchronously Reload, Replace, or Shutdown without
// making Session.Shutdown wait for the goroutine that is calling it.
func (p *Provider) dispatchChanges() {
	for {
		select {
		case <-p.notifyStop:
			return
		case <-p.notify:
			p.mu.Lock()
			observer, closed := p.observer, p.notificationsClosed
			p.mu.Unlock()
			if observer != nil && !closed {
				observer(p.Snapshot())
			}
		}
	}
}

// stopNotifications deliberately does not join the dispatcher: Shutdown is a legal
// observer action, so joining here could make the dispatcher wait for itself.
func (p *Provider) stopNotifications() {
	p.mu.Lock()
	if !p.notificationsClosed {
		p.notificationsClosed = true
		p.observer = nil
		close(p.notifyStop)
	}
	p.mu.Unlock()
}
