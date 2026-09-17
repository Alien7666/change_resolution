package app

import (
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alien7666/change_resolution/internal/config"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/scaling"
)

type fakeProviderStore struct {
	mu         sync.Mutex
	path       string
	file       config.File
	loadErr    error
	saveErr    error
	backupPath string
	backupErr  error
	saves      int
	backups    int
	saved      []config.File
	opened     []string
	openErr    error
}

func (s *fakeProviderStore) Path() (string, error) { return s.path, nil }

func (s *fakeProviderStore) Load() (config.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file, s.loadErr
}

func (s *fakeProviderStore) Save(file config.File) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.saved = append(s.saved, file)
	if s.saveErr == nil {
		s.file = file
	}
	return s.saveErr
}

func (s *fakeProviderStore) Backup() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.backups++
	return s.backupPath, s.backupErr
}

func (s *fakeProviderStore) OpenFolder(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opened = append(s.opened, path)
	return s.openErr
}

func (s *fakeProviderStore) counts() (saves, backups int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saves, s.backups
}

func (s *fakeProviderStore) lastSaved() config.File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saved[len(s.saved)-1]
}

func providerProfile(label, instancePath string) domain.Profile {
	return domain.Profile{
		Monitor: domain.MonitorIdentity{
			InstancePath:   instancePath,
			HardwareID:     `MONITOR\DEL41A8`,
			ModelWasUnique: true,
			Label:          label,
		},
		GameMode:     domain.Mode{Width: 1920, Height: 1080, RefreshHz: 144, BitsPerPixel: 32},
		FallbackMode: &domain.Mode{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32},
		ProcessName:  "",
		RestoreDelay: 3 * time.Second,
	}
}

func providerDisplay(profile domain.Profile) *fakeDisplay {
	original := *profile.FallbackMode
	return &fakeDisplay{
		target: domain.Target{
			DeviceName: `\\.\DISPLAY3`,
			Identity: domain.MonitorIdentity{
				InstancePath:   profile.Monitor.InstancePath,
				HardwareID:     profile.Monitor.HardwareID,
				ModelWasUnique: true,
				Label:          profile.Monitor.Label,
			},
			MatchedBy: domain.MatchInstancePath,
		},
		current: original,
		modes:   []domain.Mode{original, profile.GameMode},
		layout: domain.Layout{Displays: []domain.DisplayState{{
			DeviceName: `\\.\DISPLAY3`, Mode: original, Primary: true,
		}}},
		fail: make(map[string]error),
	}
}

func waitProvider(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for provider state")
}

func TestProviderStartsUnconfiguredWhenNoConfigFileExists(t *testing.T) {
	profile := providerProfile("first run", `\\?\DISPLAY#OLD`)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, loadErr: fs.ErrNotExist}
	p := newProvider(providerDisplay(profile), stubProviderChecker{}, store)
	t.Cleanup(func() { _ = p.Shutdown() })

	if p.Session() != nil || p.Configured() || !p.Unconfigured() || p.ReadOnly() {
		t.Fatalf("unexpected missing-file state: session=%p configured=%v unconfigured=%v readonly=%v",
			p.Session(), p.Configured(), p.Unconfigured(), p.ReadOnly())
	}
	if got := p.Snapshot().Message; got != "尚未設定" {
		t.Fatalf("message = %q, want 尚未設定", got)
	}
	if p.ConfigError() != nil {
		t.Fatalf("missing file became a configuration error: %v", p.ConfigError())
	}
}

func TestProviderReadsSelectedMonitorScalingWithoutASession(t *testing.T) {
	profile := providerProfile("Dell U4924DW", `\\?\DISPLAY#DEL41A8#selected`)
	displays := providerDisplay(profile)
	scaler := &task15ScalingFake{
		recorder:     &task15Recorder{},
		availability: scaling.Availability{Available: true},
		state: scaling.State{DisplayID: 42, Effective: scaling.Value{
			Raw: 6, Mode: scaling.ModeAspectRatio, By: scaling.ByDisplay,
		}},
	}
	provider := newScalingProvider(displays, stubProviderChecker{}, scaler, &fakeProviderStore{
		path: "config.json", loadErr: fs.ErrNotExist,
	})
	defer func() { _ = provider.Shutdown() }()
	if provider.Session() != nil {
		t.Fatal("precondition: first run unexpectedly has a Session")
	}

	view := provider.ReadScaling(profile.Monitor)
	if !view.Available || !view.Known || view.Effective != scaler.state.Effective {
		t.Fatalf("ReadScaling() = %#v, want selected monitor's measured value", view)
	}
	if len(scaler.calls) != 1 || scaler.calls[0].identity.InstancePath != profile.Monitor.InstancePath {
		t.Fatalf("scaling calls = %#v, want one read for selected identity", scaler.calls)
	}
}

func TestProviderStartsReadOnlyWhenTheConfigFileCannotBeUnderstood(t *testing.T) {
	const path = `C:\scratch\broken-config.json`
	wantErr := errors.New(path + "：設定檔格式錯誤：第 4 行第 7 欄：invalid character")
	store := &fakeProviderStore{path: path, loadErr: wantErr}
	p := newProvider(providerDisplay(providerProfile("unused", "old")), stubProviderChecker{}, store)
	t.Cleanup(func() { _ = p.Shutdown() })

	if p.Session() != nil || p.Configured() || p.Unconfigured() || !p.ReadOnly() {
		t.Fatalf("unexpected rejection state: session=%p configured=%v unconfigured=%v readonly=%v",
			p.Session(), p.Configured(), p.Unconfigured(), p.ReadOnly())
	}
	if p.ConfigPath() != path || !errors.Is(p.ConfigError(), wantErr) {
		t.Fatalf("path/error = %q / %v", p.ConfigPath(), p.ConfigError())
	}
	if got := p.Snapshot().Message; got != wantErr.Error() || !strings.Contains(got, path) {
		t.Fatalf("readonly message = %q, want exact error with full path", got)
	}
}

func TestProviderNeverWritesOnAnyRejectionPath(t *testing.T) {
	for name, loadErr := range map[string]error{
		"absent":    fs.ErrNotExist,
		"malformed": errors.New(`C:\scratch\config.json：設定檔格式錯誤：第 2 行第 1 欄`),
		"newer":     config.ErrNewerVersion,
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeProviderStore{path: `C:\scratch\config.json`, loadErr: loadErr}
			p := newProvider(providerDisplay(providerProfile("unused", "old")), stubProviderChecker{}, store)
			defer p.Shutdown()
			_ = p.Reload()
			if saves, backups := store.counts(); saves != 0 || backups != 0 {
				t.Fatalf("rejection wrote config: saves=%d backups=%d", saves, backups)
			}
		})
	}
}

func TestReplaceShutsTheOldSessionDownAndCarriesTheObserverOver(t *testing.T) {
	first := providerProfile("first", `\\?\DISPLAY#FIRST`)
	second := providerProfile("second", `\\?\DISPLAY#SECOND`)
	displays := providerDisplay(first)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(first)}
	p := newProvider(displays, stubProviderChecker{}, store)
	defer p.Shutdown()

	changes := make(chan Snapshot, 16)
	p.SetOnChange(func(snapshot Snapshot) { changes <- snapshot })
	old := p.Session()
	if err := p.Replace(second); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if p.Session() == nil || p.Session() == old {
		t.Fatal("Replace did not install a fresh session")
	}
	if _, err := old.Refresh(); !errors.Is(err, ErrClosed) {
		t.Fatalf("old Refresh error = %v, want ErrClosed", err)
	}

	waitProvider(t, func() bool {
		for {
			select {
			case snapshot := <-changes:
				if snapshot.Profile.Monitor.Label == "second" {
					return true
				}
			default:
				return false
			}
		}
	})
}

func TestReplaceIsRefusedWhileTheSessionOwnsAnAppliedMode(t *testing.T) {
	first := providerProfile("first", `\\?\DISPLAY#FIRST`)
	displays := providerDisplay(first)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(first)}
	p := newProvider(displays, stubProviderChecker{}, store)
	defer p.Shutdown()
	old := p.Session()
	if err := old.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	err := p.Replace(providerProfile("second", `\\?\DISPLAY#SECOND`))
	if !errors.Is(err, ErrSessionManaged) {
		t.Fatalf("Replace error = %v, want ErrSessionManaged", err)
	}
	if p.Session() != old || !p.Snapshot().Managed {
		t.Fatal("managed replacement discarded the restore entry point")
	}
}

func TestReloadIsRefusedWhileTheSessionOwnsAnAppliedMode(t *testing.T) {
	first := providerProfile("first", `\\?\DISPLAY#FIRST`)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(first)}
	p := newProvider(providerDisplay(first), stubProviderChecker{}, store)
	defer p.Shutdown()
	old := p.Session()
	if err := old.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	store.mu.Lock()
	store.loadErr = errors.New("the edited file is malformed")
	store.mu.Unlock()

	if err := p.Reload(); !errors.Is(err, ErrSessionManaged) {
		t.Fatalf("Reload error = %v, want ErrSessionManaged", err)
	}
	if p.Session() != old || !p.Configured() || !p.Snapshot().Managed {
		t.Fatal("managed reload replaced the owned session or hid its restore entry point")
	}
}

func TestProviderWritesBackTheInstancePathAfterASecondaryKeyRebind(t *testing.T) {
	profile := providerProfile("rebound", `\\?\DISPLAY#OLD`)
	displays := providerDisplay(profile)
	displays.target.Identity.InstancePath = `\\?\DISPLAY#NEW`
	displays.target.MatchedBy = domain.MatchHardwareID
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(profile)}
	p := newProvider(displays, stubProviderChecker{}, store)
	defer p.Shutdown()

	if _, err := p.Session().Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	waitProvider(t, func() bool { saves, _ := store.counts(); return saves == 1 })
	if got := store.lastSaved(); got.Monitor.InstancePath != `\\?\DISPLAY#NEW` || got.Monitor.Label != "rebound" || got.Watch.RestoreDelaySeconds != 3 {
		t.Fatalf("saved config lost data or path: %+v", got)
	}
	if got := p.Snapshot().Message; got != `已以硬體 ID 重新對應到 \\.\DISPLAY3` {
		t.Fatalf("message = %q", got)
	}
	for i := 0; i < 3; i++ {
		if _, err := p.Session().Refresh(); err != nil {
			t.Fatalf("Refresh #%d: %v", i+2, err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if saves, _ := store.counts(); saves != 1 {
		t.Fatalf("secondary rebind saved %d times, want exactly once", saves)
	}
	snapshot := p.Snapshot()
	if got := snapshot.Message; got != `已以硬體 ID 重新對應到 \\.\DISPLAY3` {
		t.Fatalf("message after later refreshes = %q", got)
	}
	if snapshot.Err != nil {
		t.Fatalf("snapshot error after successful rebind = %v", snapshot.Err)
	}
}

func TestProviderReportsASecondaryRebindSaveFailureOnlyOnce(t *testing.T) {
	profile := providerProfile("rebound", `\\?\DISPLAY#OLD`)
	displays := providerDisplay(profile)
	displays.target.Identity.InstancePath = `\\?\DISPLAY#NEW`
	displays.target.MatchedBy = domain.MatchHardwareID
	wantErr := errors.New("disk is read-only")
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(profile), saveErr: wantErr}
	p := newProvider(displays, stubProviderChecker{}, store)
	defer p.Shutdown()

	if _, err := p.Session().Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	waitProvider(t, func() bool { return errors.Is(p.ConfigError(), wantErr) })
	if strings.Contains(p.Snapshot().Message, "已以硬體 ID 重新對應") {
		t.Fatalf("failed save claimed success: %q", p.Snapshot().Message)
	}
	for i := 0; i < 3; i++ {
		_, _ = p.Session().Refresh()
	}
	time.Sleep(20 * time.Millisecond)
	if saves, _ := store.counts(); saves != 1 {
		t.Fatalf("failed secondary rebind retried %d times", saves)
	}
	if err := p.ConfigError(); !errors.Is(err, wantErr) {
		t.Fatalf("ConfigError after later refreshes = %v, want wrapped %v", err, wantErr)
	}
	snapshot := p.Snapshot()
	if !strings.Contains(snapshot.Message, wantErr.Error()) {
		t.Fatalf("message after later refreshes = %q, want it to contain %q", snapshot.Message, wantErr)
	}
	if !errors.Is(snapshot.Err, wantErr) {
		t.Fatalf("snapshot error after later refreshes = %v, want wrapped %v", snapshot.Err, wantErr)
	}
}

func TestProviderRebindSuccessDoesNotHideANewerSessionPhase(t *testing.T) {
	profile := providerProfile("rebound", `\\?\DISPLAY#OLD`)
	displays := providerDisplay(profile)
	displays.target.Identity.InstancePath = `\\?\DISPLAY#NEW`
	displays.target.MatchedBy = domain.MatchHardwareID
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(profile)}
	p := newProvider(displays, stubProviderChecker{}, store)
	defer p.Shutdown()

	if _, err := p.Session().Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	waitProvider(t, func() bool { saves, _ := store.counts(); return saves == 1 })

	const phaseMessage = "正在套用設定的顯示模式"
	p.Session().state(StateApplying, phaseMessage)
	snapshot := p.Snapshot()
	if snapshot.State != StateApplying || snapshot.Message != phaseMessage || snapshot.Err != nil {
		t.Fatalf("working snapshot = %#v, want current applying phase", snapshot)
	}
}

func TestProviderRebindFailureDoesNotHideANewerDisplayError(t *testing.T) {
	profile := providerProfile("rebound", `\\?\DISPLAY#OLD`)
	displays := providerDisplay(profile)
	displays.target.Identity.InstancePath = `\\?\DISPLAY#NEW`
	displays.target.MatchedBy = domain.MatchHardwareID
	diskErr := errors.New("disk is read-only")
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(profile), saveErr: diskErr}
	p := newProvider(displays, stubProviderChecker{}, store)
	defer p.Shutdown()

	if _, err := p.Session().Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	waitProvider(t, func() bool { return errors.Is(p.ConfigError(), diskErr) })

	displayErr := p.Session().fail("讀取顯示器", display.ErrTargetNotFound)
	snapshot := p.Snapshot()
	if snapshot.Message != displayErr.Error() || !errors.Is(snapshot.Err, display.ErrTargetNotFound) {
		t.Fatalf("error snapshot = %#v, want current display error %v", snapshot, displayErr)
	}
	if err := p.ConfigError(); !errors.Is(err, diskErr) {
		t.Fatalf("ConfigError = %v, want original rebind save error %v", err, diskErr)
	}
}

func TestLateSnapshotFromReplacedSessionCannotWriteOldProfile(t *testing.T) {
	first := providerProfile("first", `\\?\DISPLAY#FIRST`)
	second := providerProfile("second", `\\?\DISPLAY#SECOND`)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(first)}
	p := newProvider(providerDisplay(first), stubProviderChecker{}, store)
	defer p.Shutdown()
	p.mu.Lock()
	staleGeneration := p.generation
	p.mu.Unlock()
	if err := p.Replace(second); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	p.observe(staleGeneration, Snapshot{
		Profile: first,
		Target: domain.Target{
			DeviceName: `\\.\DISPLAY9`,
			Identity:   domain.MonitorIdentity{InstancePath: `\\?\DISPLAY#STALE`},
			MatchedBy:  domain.MatchHardwareID,
		},
		MatchedBy: domain.MatchHardwareID,
	})
	if saves, _ := store.counts(); saves != 0 {
		t.Fatalf("stale notification wrote %d configs", saves)
	}
	if got := p.Snapshot().Profile.Monitor.Label; got != "second" {
		t.Fatalf("stale notification replaced the current snapshot with %q", got)
	}
}

func TestResetBacksUpOnlyAfterTheExplicitCall(t *testing.T) {
	wantErr := errors.New("broken configuration")
	store := &fakeProviderStore{
		path: `C:\scratch\config.json`, loadErr: wantErr,
		backupPath: `C:\scratch\config.bad-20260915-120000.json`,
	}
	p := newProvider(providerDisplay(providerProfile("unused", "old")), stubProviderChecker{}, store)
	defer p.Shutdown()
	if _, backups := store.counts(); backups != 0 {
		t.Fatal("provider backed up a rejected file without a user action")
	}

	backup, err := p.Reset()
	if err != nil || backup != store.backupPath {
		t.Fatalf("Reset = %q, %v", backup, err)
	}
	if _, backups := store.counts(); backups != 1 {
		t.Fatalf("backup calls = %d, want 1", backups)
	}
	if !p.Unconfigured() || p.ReadOnly() || !strings.Contains(p.Snapshot().Message, "尚未設定") ||
		!strings.Contains(p.Snapshot().Message, store.backupPath) {
		t.Fatalf("reset did not enter the honest unconfigured state: %+v", p.Snapshot())
	}
}

func TestResetFailureLeavesTheRejectedFileStateVisible(t *testing.T) {
	loadErr := errors.New(`C:\scratch\config.json：設定檔格式錯誤：第 3 行第 2 欄`)
	backupErr := errors.New("access denied")
	store := &fakeProviderStore{
		path: `C:\scratch\config.json`, loadErr: loadErr, backupErr: backupErr,
	}
	p := newProvider(providerDisplay(providerProfile("unused", "old")), stubProviderChecker{}, store)
	defer p.Shutdown()

	if _, err := p.Reset(); !errors.Is(err, backupErr) {
		t.Fatalf("Reset error = %v, want backup failure", err)
	}
	if !p.ReadOnly() || p.Session() != nil {
		t.Fatal("failed reset left the read-only recovery state")
	}
	if got := p.Snapshot().Message; !strings.Contains(got, backupErr.Error()) {
		t.Fatalf("reset failure is not visible: %q", got)
	}
}

func TestOpenConfigFolderUsesTheFullConfiguredPath(t *testing.T) {
	const path = `C:\Users\owner\AppData\Roaming\ResolutionTray\config.json`
	store := &fakeProviderStore{path: path, loadErr: fs.ErrNotExist}
	p := newProvider(providerDisplay(providerProfile("unused", "old")), stubProviderChecker{}, store)
	defer p.Shutdown()

	if err := p.OpenConfigFolder(); err != nil {
		t.Fatalf("OpenConfigFolder: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.opened) != 1 || store.opened[0] != path {
		t.Fatalf("opened = %v, want full config path %q", store.opened, path)
	}
}

func TestObserverCanReplaceWithoutWaitingForTheSessionDispatcher(t *testing.T) {
	first := providerProfile("first", `\\?\DISPLAY#FIRST`)
	second := providerProfile("second", `\\?\DISPLAY#SECOND`)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(first)}
	p := newProvider(providerDisplay(first), stubProviderChecker{}, store)
	defer p.Shutdown()

	done := make(chan error, 1)
	var once sync.Once
	p.SetOnChange(func(Snapshot) {
		once.Do(func() { done <- p.Replace(second) })
	})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("observer Replace: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer Replace deadlocked with the session notification dispatcher")
	}
}

func TestObserverCanShutdownWithoutWaitingForItself(t *testing.T) {
	profile := providerProfile("shutdown", `\\?\DISPLAY#SHUTDOWN`)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(profile)}
	p := newProvider(providerDisplay(profile), stubProviderChecker{}, store)

	done := make(chan error, 1)
	var once sync.Once
	p.SetOnChange(func(Snapshot) {
		once.Do(func() { done <- p.Shutdown() })
	})
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("observer Shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer Shutdown deadlocked")
	}
}

func TestSuccessfulShutdownPermanentlyClosesTheProvider(t *testing.T) {
	profile := providerProfile("closed", `\\?\DISPLAY#CLOSED`)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(profile)}
	p := newProvider(providerDisplay(profile), stubProviderChecker{}, store)
	if err := p.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := p.Replace(providerProfile("replacement", `\\?\DISPLAY#NEW`)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Replace after Shutdown = %v, want ErrClosed", err)
	}
	if err := p.Reload(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Reload after Shutdown = %v, want ErrClosed", err)
	}
	if p.Session() != nil {
		t.Fatal("a closed provider constructed another session")
	}
}

func TestFailedShutdownLeavesTheProviderRetryable(t *testing.T) {
	profile := providerProfile("retry", `\\?\DISPLAY#RETRY`)
	displays := providerDisplay(profile)
	store := &fakeProviderStore{path: `C:\scratch\config.json`, file: config.FromProfile(profile)}
	p := newProvider(displays, stubProviderChecker{}, store)
	if err := p.Session().Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	failure := errors.New("driver refused restore")
	displays.setFailure("apply", failure)
	if err := p.Shutdown(); !errors.Is(err, failure) {
		t.Fatalf("first Shutdown = %v, want restore failure", err)
	}
	if p.Session() == nil || !p.Snapshot().Managed {
		t.Fatal("failed Shutdown discarded the owned session")
	}

	displays.setFailure("apply", nil)
	if err := p.Shutdown(); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
}

type stubProviderChecker struct{}

func (stubProviderChecker) Running(string) (bool, error) { return false, nil }
