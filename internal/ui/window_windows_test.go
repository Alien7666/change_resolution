//go:build windows

package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/config"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/scaling"
)

// preflightRejection mirrors the error app.Session reports when the CDS_TEST
// pre-flight refuses the profile mode, except that the operation label and the
// Win32 diagnostic are deliberately reworded. The latch must key on the sentinel
// alone, so no rewording on either side may silently re-enable the mode toggle.
func preflightRejection() error {
	native := fmt.Errorf("%w: reworded win32 diagnostic", display.ErrModeNotSupported)
	return fmt.Errorf("%s: %w", "reworded operation label", native)
}

func resolvedSnapshot() app.Snapshot {
	return app.Snapshot{
		State:  app.StateNative,
		Target: domain.Target{DeviceName: `\.\DISPLAY4`},
	}
}

func TestUpdateAvailabilityLatchesUnsupportedModeBySentinelNotMessage(t *testing.T) {
	w := &window{}

	w.updateAvailability(app.Snapshot{State: app.StateError, Err: preflightRejection()})
	if w.unavailableReason == "" {
		t.Fatal("unsupported target mode did not disable the mode toggle")
	}
	if !errors.Is(preflightRejection(), display.ErrModeNotSupported) {
		t.Fatal("fixture no longer carries display.ErrModeNotSupported")
	}
}

func TestUpdateAvailabilityLatchesMissingTarget(t *testing.T) {
	w := &window{}

	w.updateAvailability(app.Snapshot{
		State: app.StateError,
		Err:   fmt.Errorf("resolve target: %w: %s", display.ErrTargetNotFound, `MONITOR\XMI27B2`),
	})
	if w.unavailableReason == "" {
		t.Fatal("missing target did not disable the mode toggle")
	}
}

// ambiguousResolve is the error shape resolveIdentity produces when the configured
// identity hits more than one attached monitor. The candidates are in the message
// because the display package has no other way to carry them, and the window has to
// pass them on: reselecting the monitor is the only way out of this latch, and the
// user cannot reselect what the tool will not name.
func ambiguousResolve() error {
	return fmt.Errorf("resolve target: %w: %s 同時命中 %s、%s，請重新選擇顯示器",
		display.ErrTargetAmbiguous, `MONITOR\XMI27B2`, `\\.\DISPLAY1`, `\\.\DISPLAY3`)
}

func mirroredResolve() error {
	return fmt.Errorf("resolve target: %w: %s 同時驅動 %s，工具無法只變更其中一台，請先關閉複製／鏡射顯示",
		display.ErrTargetMirrored, `\\.\DISPLAY1`, `\\.\DISPLAY3`)
}

func TestUpdateAvailabilityLatchesAnAmbiguousTarget(t *testing.T) {
	w := &window{}

	w.updateAvailability(app.Snapshot{State: app.StateError, Err: ambiguousResolve()})
	if w.unavailableReason == "" {
		t.Fatal("an ambiguous target did not disable display mode switching")
	}
	for _, device := range []string{`\\.\DISPLAY1`, `\\.\DISPLAY3`} {
		if !strings.Contains(w.unavailableReason, device) {
			t.Errorf("reason %q does not name candidate %s", w.unavailableReason, device)
		}
	}
	if !strings.Contains(w.unavailableReason, "顯示模式切換") {
		t.Errorf("reason %q does not name the disabled operation neutrally", w.unavailableReason)
	}
	if strings.Contains(w.unavailableReason, "4:3") {
		t.Errorf("reason %q contradicts profiles whose configured mode is not 4:3", w.unavailableReason)
	}
}

// A mirrored adapter is not something re-reading can clear and not something the
// tool can work around, so the reason has to say what the user would have to change:
// the tool cannot drive one of two monitors that share a display device.
func TestUpdateAvailabilityLatchesAMirroredTarget(t *testing.T) {
	w := &window{}

	w.updateAvailability(app.Snapshot{State: app.StateError, Err: mirroredResolve()})
	if w.unavailableReason == "" {
		t.Fatal("a mirrored target did not disable display mode switching")
	}
	if !strings.Contains(w.unavailableReason, "無法只變更其中一台") {
		t.Errorf("reason %q does not explain why a mirrored pair cannot be changed", w.unavailableReason)
	}
	if !strings.Contains(w.unavailableReason, "顯示模式切換") {
		t.Errorf("reason %q does not name the disabled operation neutrally", w.unavailableReason)
	}
	if strings.Contains(w.unavailableReason, "4:3") {
		t.Errorf("reason %q contradicts profiles whose configured mode is not 4:3", w.unavailableReason)
	}
}

func TestUpdateAvailabilityClearsLatchOnCleanRead(t *testing.T) {
	for name, snapshot := range map[string]app.Snapshot{
		"unsupported mode": {State: app.StateError, Err: preflightRejection()},
		"missing target": {State: app.StateError,
			Err: fmt.Errorf("resolve target: %w", display.ErrTargetNotFound)},
		"ambiguous target": {State: app.StateError, Err: ambiguousResolve()},
		"mirrored target":  {State: app.StateError, Err: mirroredResolve()},
	} {
		t.Run(name, func(t *testing.T) {
			w := &window{}
			w.updateAvailability(snapshot)
			if w.unavailableReason == "" {
				t.Fatal("precondition: control was not disabled")
			}

			w.updateAvailability(resolvedSnapshot())
			if w.unavailableReason != "" {
				t.Fatalf("clean read did not clear the latch: %q", w.unavailableReason)
			}
		})
	}
}

// An apply that merely failed once is retryable, so it must neither latch the
// control nor clear a latch that is already set.
func TestUpdateAvailabilityIgnoresUnrelatedFailures(t *testing.T) {
	w := &window{}
	w.updateAvailability(app.Snapshot{
		State: app.StateError,
		Err:   fmt.Errorf("apply display mode: %w", errors.New("driver failed the display mode change")),
	})
	if w.unavailableReason != "" {
		t.Fatalf("retryable apply failure disabled the mode toggle: %q", w.unavailableReason)
	}

	w.updateAvailability(app.Snapshot{State: app.StateError, Err: preflightRejection()})
	w.updateAvailability(app.Snapshot{
		State: app.StateError,
		Err:   errors.New("apply display mode: driver failed the display mode change"),
	})
	if w.unavailableReason == "" {
		t.Fatal("latch was cleared by an unrelated failure instead of a clean read")
	}
}

var errUnexpectedModeChange = errors.New("ui tests must never change a display mode")

// stubDisplays answers the read-only calls the refresh path makes. resolveErr is
// swapped mid-test to model a monitor that was asleep when the tool started and
// was switched back to this input afterwards.
type stubDisplays struct {
	mu         sync.Mutex
	resolveErr error
	resolves   int
}

func (d *stubDisplays) ResolveTarget(domain.MonitorIdentity) (domain.Target, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolves++
	if d.resolveErr != nil {
		return domain.Target{}, d.resolveErr
	}
	return domain.Target{DeviceName: `\.\DISPLAY4`, Identity: domain.MonitorIdentity{HardwareID: `MONITOR\XMI27B2`}}, nil
}

// Targets is on the interface for the settings dialog, which this window does not
// open yet. It answers with the same monitor ResolveTarget does, without counting as
// a resolve.
func (d *stubDisplays) Targets() ([]domain.Target, error) {
	return []domain.Target{{
		DeviceName: `\.\DISPLAY4`,
		Identity: domain.MonitorIdentity{
			HardwareID: `MONITOR\XMI27B2`,
			Label:      "Mi Monitor 27",
		},
	}}, nil
}

// EnumModes is on the interface for the mode picker, which this window does not open
// yet. It answers with a list containing the monitor's current mode, because that is
// the one invariant the catalogue guarantees, and it is a read like the rest of this
// stub: nothing here may change a display.
func (d *stubDisplays) EnumModes(domain.Target) ([]domain.Mode, error) {
	return []domain.Mode{
		{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
	}, nil
}

func (d *stubDisplays) CurrentMode(domain.Target) (domain.Mode, error) {
	return domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}, nil
}

func (d *stubDisplays) CurrentLayout() (domain.Layout, error) {
	return domain.Layout{Displays: []domain.DisplayState{{
		DeviceName: `\.\DISPLAY4`,
		Mode:       domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		Primary:    true,
	}}}, nil
}

func (d *stubDisplays) TestMode(domain.Target, domain.Mode) error { return errUnexpectedModeChange }
func (d *stubDisplays) ApplyLayout(domain.LayoutPlan) error       { return errUnexpectedModeChange }

func (d *stubDisplays) setResolveErr(err error) {
	d.mu.Lock()
	d.resolveErr = err
	d.mu.Unlock()
}

func (d *stubDisplays) resolveCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.resolves
}

type stubProcesses struct{}

func (stubProcesses) Running(string) (bool, error) { return false, nil }
func (stubProcesses) Names() ([]string, error)     { return []string{"game.exe"}, nil }

// A latched window must keep one command live. Enable, Disable and Refresh are the
// only producers of a fresh snapshot, and the first two are disabled here, so
// without a live refresh the latch can only be cleared by restarting the tool.
func TestRefreshStaysAvailableWhileTheTargetIsUnavailable(t *testing.T) {
	for name, snapshot := range map[string]app.Snapshot{
		"missing target": {State: app.StateError,
			Err: fmt.Errorf("resolve target: %w", display.ErrTargetNotFound)},
		"unsupported mode": {State: app.StateError, Err: preflightRejection()},
		"ambiguous target": {State: app.StateError, Err: ambiguousResolve()},
		"mirrored target":  {State: app.StateError, Err: mirroredResolve()},
	} {
		t.Run(name, func(t *testing.T) {
			w := &window{}
			w.updateAvailability(snapshot)
			if w.unavailableReason == "" {
				t.Fatal("precondition: the mode toggle was not disabled")
			}

			got := w.availableControls(snapshot)
			if !got.refresh {
				t.Fatal("a latched window offers no way to re-read the display state")
			}
			if got.toggle || got.restore || got.enable {
				t.Fatalf("mode commands stayed live while the target is unavailable: %+v", got)
			}
			if !got.exit || !got.show || !got.hide {
				t.Fatalf("controls = %+v", got)
			}
		})
	}
}

// One operation at a time: while a session call is in flight no command starts
// another, the new refresh included.
func TestBusyWindowAcceptsNoCommand(t *testing.T) {
	w := &window{busy: true}
	got := w.availableControls(resolvedSnapshot())
	if got.refresh || got.toggle || got.restore || got.enable || got.settings || got.exit {
		t.Fatalf("controls = %+v", got)
	}
}

func TestSettingsEntryIsDisabledOnlyWhileManagedBusyOrReadOnly(t *testing.T) {
	snapshot := resolvedSnapshot()
	w := &window{configState: configStateConfigured}
	if got := w.availableControls(snapshot); !got.settings {
		t.Fatalf("idle configured controls = %+v", got)
	}

	snapshot.Managed = true
	if got := w.availableControls(snapshot); got.settings {
		t.Fatalf("managed controls = %+v", got)
	}
	if got := settingsDisabledReason(snapshot); got != "請先恢復原始解析度再變更設定" {
		t.Fatalf("managed reason = %q", got)
	}

	w.busy = true
	snapshot.Managed = false
	if got := w.availableControls(snapshot); got.settings {
		t.Fatalf("busy controls = %+v", got)
	}

	w.busy = false
	w.configState = configStateUnconfigured
	if got := w.availableControls(snapshot); !got.settings {
		t.Fatalf("first-run controls = %+v", got)
	}
	w.configState = configStateReadOnly
	if got := w.availableControls(snapshot); got.settings {
		t.Fatalf("read-only controls = %+v", got)
	}
}

func TestFirstRunSettingsUsesABlankDraftInTheSharedDialog(t *testing.T) {
	displays := &stubDisplays{}
	t.Setenv(config.EnvPath, filepath.Join(t.TempDir(), "missing", "config.json"))
	provider := app.NewProvider(displays, stubProcesses{})
	t.Cleanup(func() { _ = provider.Shutdown() })

	opened := 0
	w := &window{
		provider:  provider,
		displays:  displays,
		processes: stubProcesses{},
		openSettings: func(model *settingsModel, replace func(domain.Profile) error) (bool, error) {
			opened++
			if !model.firstRun {
				t.Fatal("absent configuration did not open first-run mode")
			}
			draft := model.Draft()
			if draft.Monitor.InstancePath != "" || draft.Monitor.HardwareID != "" || draft.GameMode != (domain.Mode{}) || draft.ProcessName != "" {
				t.Fatalf("first-run draft inherited a configured profile: %#v", draft)
			}
			if replace == nil {
				t.Fatal("shared dialog was not given Provider.Replace")
			}
			return false, nil
		},
	}

	if !shouldOpenFirstRun(provider) {
		t.Fatal("missing configuration did not schedule first-run settings")
	}
	accepted, err := w.showSettings(true)
	if err != nil || accepted || opened != 1 {
		t.Fatalf("accepted=%v opened=%d err=%v", accepted, opened, err)
	}
}

func TestResetCancellationDoesNotBackupOrOpenSettings(t *testing.T) {
	backups, opened := 0, 0
	accepted, backupPath, err := resetAndOpenSettings(
		func() bool { return false },
		func() (string, error) { backups++; return `C:\scratch\config.bad.json`, nil },
		func(string) (bool, error) { opened++; return false, nil },
	)
	if err != nil || accepted || backupPath != "" || backups != 0 || opened != 0 {
		t.Fatalf("accepted=%v path=%q backups=%d opened=%d err=%v", accepted, backupPath, backups, opened, err)
	}
}

func TestConfirmedResetPassesTheBackupPathBeforeOpeningFirstRunSettings(t *testing.T) {
	const wantPath = `C:\Users\owner\AppData\Roaming\ResolutionTray\config.bad-20260916.json`
	order := []string{}
	accepted, backupPath, err := resetAndOpenSettings(
		func() bool { order = append(order, "confirm"); return true },
		func() (string, error) { order = append(order, "backup"); return wantPath, nil },
		func(path string) (bool, error) {
			order = append(order, "settings:"+path)
			return true, nil
		},
	)
	if err != nil || !accepted || backupPath != wantPath {
		t.Fatalf("accepted=%v path=%q err=%v", accepted, backupPath, err)
	}
	wantOrder := []string{"confirm", "backup", "settings:" + wantPath}
	if strings.Join(order, "|") != strings.Join(wantOrder, "|") {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
}

// The refresh command has to reach the session, and the read it performs has to
// clear a latch that a monitor set while it was unreachable.
func TestRefreshCommandReReadsTheDisplayAndClearsTheLatch(t *testing.T) {
	displays := &stubDisplays{
		resolveErr: fmt.Errorf("%w: %s", display.ErrTargetNotFound, `MONITOR\XMI27B2`),
	}
	t.Setenv(config.EnvPath, filepath.Join(t.TempDir(), "config.json"))
	provider := app.NewProvider(displays, stubProcesses{})
	if err := provider.Replace(domain.LegacySeedProfile()); err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	session := provider.Session()
	t.Cleanup(func() {
		if err := provider.Shutdown(); err != nil {
			t.Errorf("cleanup Shutdown: %v", err)
		}
	})
	w := &window{provider: provider}

	startup, err := session.Refresh()
	if !errors.Is(err, display.ErrTargetNotFound) {
		t.Fatalf("startup probe error = %v", err)
	}
	w.updateAvailability(startup)
	if w.unavailableReason == "" {
		t.Fatal("precondition: a sleeping monitor did not disable the mode toggle")
	}
	before := displays.resolveCount()

	displays.setResolveErr(nil) // the monitor came back
	if err := w.refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := displays.resolveCount(); got <= before {
		t.Fatalf("resolve calls = %d, want the refresh command to re-read the display", got)
	}

	w.updateAvailability(session.Snapshot())
	if w.unavailableReason != "" {
		t.Fatalf("refresh left the window latched: %q", w.unavailableReason)
	}
	if got := w.availableControls(session.Snapshot()); !got.toggle {
		t.Fatal("a monitor that came back left the mode toggle disabled")
	}
}

func TestUnconfiguredWindowKeepsOnlyConfigurationRecoveryActionsLive(t *testing.T) {
	w := &window{configState: configStateUnconfigured}
	got := w.availableControls(app.Snapshot{Message: "尚未設定"})
	if got.toggle || got.restore || got.enable || got.reset {
		t.Fatalf("unconfigured state exposed a mutation: %+v", got)
	}
	if !got.refresh || !got.openFolder || !got.exit || !got.show || !got.hide {
		t.Fatalf("unconfigured state hid a recovery action: %+v", got)
	}
}

func TestReadOnlyWindowOffersExplicitResetWithoutEnablingDisplayChanges(t *testing.T) {
	w := &window{configState: configStateReadOnly}
	got := w.availableControls(app.Snapshot{State: app.StateError, Err: errors.New("bad config")})
	if got.toggle || got.restore || got.enable {
		t.Fatalf("read-only state exposed a display mutation: %+v", got)
	}
	if !got.refresh || !got.openFolder || !got.reset {
		t.Fatalf("read-only state hid a recovery action: %+v", got)
	}
}

func TestProviderStateTextKeepsTheExactConfigurationErrorAndPath(t *testing.T) {
	const exact = `C:\Users\owner\AppData\Roaming\ResolutionTray\config.json：設定檔格式錯誤：第 4 行第 7 欄`
	w := &window{configState: configStateReadOnly}
	got := w.statusText(app.Snapshot{State: app.StateError, Message: exact, Err: errors.New(exact)})
	if !strings.Contains(got, exact) {
		t.Fatalf("status %q lost the exact configuration error", got)
	}
}

func TestReloadCommandLoadsAConfigWhenNoSessionExists(t *testing.T) {
	profile := domain.LegacySeedProfile()
	displays := &stubDisplays{}
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvPath, path)
	provider := app.NewProvider(displays, stubProcesses{})
	t.Cleanup(func() {
		if err := provider.Shutdown(); err != nil {
			t.Errorf("cleanup Shutdown: %v", err)
		}
	})
	if provider.Session() != nil {
		t.Fatal("precondition: missing configuration constructed a session")
	}
	if err := config.Save(config.FromProfile(profile)); err != nil {
		t.Fatalf("write scratch config: %v", err)
	}

	w := &window{provider: provider, configState: configStateUnconfigured}
	if err := w.refresh(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if provider.Session() == nil || !provider.Configured() {
		t.Fatal("reload did not construct a session from the new config")
	}
	if displays.resolveCount() == 0 {
		t.Fatal("newly loaded session was not given its read-only display probe")
	}
}

func TestQueuedProviderNotificationRendersTheCurrentSession(t *testing.T) {
	t.Setenv(config.EnvPath, filepath.Join(t.TempDir(), "config.json"))
	provider := app.NewProvider(&stubDisplays{}, stubProcesses{})
	t.Cleanup(func() { _ = provider.Shutdown() })
	first := domain.LegacySeedProfile()
	first.Monitor.Label = "first"
	if err := provider.Replace(first); err != nil {
		t.Fatalf("first Replace: %v", err)
	}
	stale := provider.Snapshot()
	second := first.Copy()
	second.Monitor.Label = "second"
	if err := provider.Replace(second); err != nil {
		t.Fatalf("second Replace: %v", err)
	}

	if got := snapshotForRender(provider, stale).Profile.Monitor.Label; got != "second" {
		t.Fatalf("queued old notification rendered %q", got)
	}
}

func TestReplacingTheSessionResetsAutomaticFailureDeduplication(t *testing.T) {
	t.Setenv(config.EnvPath, filepath.Join(t.TempDir(), "config.json"))
	provider := app.NewProvider(&stubDisplays{}, stubProcesses{})
	t.Cleanup(func() { _ = provider.Shutdown() })
	first := domain.LegacySeedProfile()
	if err := provider.Replace(first); err != nil {
		t.Fatalf("first Replace: %v", err)
	}
	w := &window{
		provider:                    provider,
		observedSession:             provider.Session(),
		reportedAutoRestoreFailures: 7,
	}
	second := first.Copy()
	second.Monitor.Label = "second"
	if err := provider.Replace(second); err != nil {
		t.Fatalf("second Replace: %v", err)
	}

	w.syncConfigState()
	if w.reportedAutoRestoreFailures != 0 {
		t.Fatalf("new session inherited automatic failure count %d", w.reportedAutoRestoreFailures)
	}
}

// The watcher restores without being asked, so its failures need a notification of
// their own: exactly one per failure, however often the state is re-rendered.
func TestAutomaticRestoreFailureIsAnnouncedOncePerOccurrence(t *testing.T) {
	w := &window{}
	failed := app.Snapshot{
		State:               app.StateError,
		Managed:             true,
		AutoRestoreFailures: 1,
		Err:                 errors.New("apply display mode: driver refused the mode change"),
	}

	announced := w.takeAutoRestoreFailure(failed)
	if announced == nil {
		t.Fatal("a failed automatic restore raised no notification")
	}
	if !errors.Is(announced, failed.Err) {
		t.Fatalf("notification lost the failure: %v", announced)
	}
	if err := w.takeAutoRestoreFailure(failed); err != nil {
		t.Fatalf("the same failure was announced twice: %v", err)
	}
	// Every later poll re-renders the same failed snapshot.
	for revision := uint64(1); revision <= 3; revision++ {
		poll := failed
		poll.Revision = revision
		if err := w.takeAutoRestoreFailure(poll); err != nil {
			t.Fatalf("a later poll re-announced the same failure: %v", err)
		}
	}

	next := failed
	next.AutoRestoreFailures = 2
	if err := w.takeAutoRestoreFailure(next); err == nil {
		t.Fatal("a second failed automatic restore raised no notification")
	}
}

// runOperation already reports a failure the user asked for, so the same failure
// must not also arrive as a notification.
func TestUserTriggeredFailureIsNotAnnouncedAsAutomatic(t *testing.T) {
	w := &window{}
	manual := app.Snapshot{
		State:   app.StateError,
		Managed: true,
		Err:     errors.New("apply display mode: driver refused the mode change"),
	}
	if err := w.takeAutoRestoreFailure(manual); err != nil {
		t.Fatalf("a user-triggered failure would be reported twice: %v", err)
	}
}

// --- Task 10: every window string is generated from the snapshot -------------

// seedProfileSnapshot is the shipped configuration arriving as data instead of as
// constants. Everything the window used to hard-code is in here, so a literal left
// behind in the window still looks right against this fixture -- which is exactly why
// widescreenSnapshot exists beside it.
func seedProfileSnapshot() app.Snapshot {
	profile := domain.LegacySeedProfile()
	return app.Snapshot{
		State:         app.StateNative,
		Target:        domain.Target{DeviceName: `\\.\DISPLAY4`},
		Profile:       profile,
		FallbackMode:  *profile.FallbackMode,
		FallbackKnown: true,
	}
}

// widescreenSnapshot shares nothing with the seed: another monitor, a 32:9 game mode,
// another watched process, another delay, and a fallback that was derived from the
// monitor rather than written into the profile. Every seed literal is a forbidden
// substring of anything this profile renders.
func widescreenSnapshot() app.Snapshot {
	return app.Snapshot{
		State:  app.StateNative,
		Target: domain.Target{DeviceName: `\\.\DISPLAY2`, MatchedBy: domain.MatchInstancePath},
		Profile: domain.Profile{
			Monitor: domain.MonitorIdentity{
				InstancePath:   `\\?\DISPLAY#DEL41A8#5&1f2e3d4c&0&UID4353`,
				HardwareID:     `MONITOR\DEL41A8`,
				ModelWasUnique: true,
				Label:          "DELL U4924DW",
			},
			GameMode:     domain.Mode{Width: 3840, Height: 1080, RefreshHz: 144, BitsPerPixel: 32},
			ProcessName:  "cs2.exe",
			RestoreDelay: 12 * time.Second,
		},
		FallbackMode:  domain.Mode{Width: 5120, Height: 2160, RefreshHz: 120, BitsPerPixel: 32},
		FallbackKnown: true,
	}
}

// seedLiterals are the strings the window used to contain. None of them may come out
// of a renderer fed a profile that does not mention them.
var seedLiterals = []string{"1920", "1440", "2560", "180", "Mi Monitor", "XMI27B2", "VALORANT", "4:3"}

func TestTargetTextUsesTheConfiguredLabelAndTheResolvedDevice(t *testing.T) {
	snapshot := widescreenSnapshot()

	got := targetText(snapshot)
	if !strings.Contains(got, snapshot.Profile.Monitor.Label) {
		t.Errorf("target text %q does not name the configured monitor", got)
	}
	if !strings.Contains(got, `\\.\DISPLAY2`) {
		t.Errorf("target text %q does not name the device the monitor resolved to", got)
	}

	snapshot.Target.DeviceName = ""
	got = targetText(snapshot)
	if !strings.Contains(got, "尚未找到") {
		t.Errorf("unresolved target text %q does not say the monitor was not found", got)
	}
	if strings.Contains(got, "DISPLAY2") {
		t.Errorf("unresolved target text %q still names a device", got)
	}
}

// A profile whose monitor offers no friendly name still has to render something a
// person can match against their hardware. The window never invents one: it falls
// through to the keys the profile is actually matched on.
func TestTargetTextFallsBackToTheIdentityWhenThereIsNoLabel(t *testing.T) {
	snapshot := widescreenSnapshot()
	snapshot.Profile.Monitor.Label = "   "

	if got := monitorLabel(snapshot); got != snapshot.Profile.Monitor.InstancePath {
		t.Errorf("monitorLabel = %q, want the instance path", got)
	}

	snapshot.Profile.Monitor.InstancePath = ""
	if got := monitorLabel(snapshot); got != snapshot.Profile.Monitor.HardwareID {
		t.Errorf("monitorLabel = %q, want the hardware ID", got)
	}

	snapshot.Profile.Monitor.HardwareID = ""
	if got := monitorLabel(snapshot); got == "" {
		t.Error("a profile with no identity strings rendered an empty monitor name")
	}
}

// A label is legitimately allowed to be a whole device interface path when the monitor
// reports nothing friendlier, and the window is a fixed size. The line is shortened in
// the middle -- the part that is the constant device class GUID -- and the untouched
// string stays reachable as the tooltip, so nothing the user needs is destroyed.
func TestTargetTextShortensAnOverlongLabelAndKeepsItInTheTooltip(t *testing.T) {
	const full = `MONITOR\XMI27B2\{4d36e96e-e325-11ce-bfc1-08002be10318}\0009`
	snapshot := seedProfileSnapshot()
	snapshot.Profile.Monitor.Label = full

	line := targetText(snapshot)
	if strings.Contains(line, full) {
		t.Errorf("target line %q was not shortened", line)
	}
	if !strings.Contains(line, "…") {
		t.Errorf("target line %q does not mark the elision", line)
	}
	if !strings.Contains(line, `MONITOR\XMI`) {
		t.Errorf("target line %q dropped the head of the label", line)
	}
	if !strings.Contains(line, `0009`) {
		t.Errorf("target line %q dropped the tail of the label, which is the port", line)
	}

	tip := targetTooltip(snapshot)
	if !strings.Contains(tip, full) {
		t.Errorf("tooltip %q does not carry the whole label", tip)
	}
	if !strings.Contains(tip, `\\.\DISPLAY4`) {
		t.Errorf("tooltip %q does not carry the resolved device", tip)
	}
}

func TestToggleTextPrintsTheConfiguredModeAndItsAspect(t *testing.T) {
	for name, tc := range map[string]struct {
		snapshot app.Snapshot
		mode     string
		aspect   string
	}{
		"seed":       {seedProfileSnapshot(), "1920 × 1440 @ 180 Hz", "4:3"},
		"widescreen": {widescreenSnapshot(), "3840 × 1080 @ 144 Hz", "32:9"},
	} {
		t.Run(name, func(t *testing.T) {
			got := toggleText(tc.snapshot)
			if !strings.Contains(got, tc.mode) {
				t.Errorf("toggle text %q does not print the configured mode %q", got, tc.mode)
			}
			if !strings.Contains(got, tc.aspect) {
				t.Errorf("toggle text %q does not print the configured aspect %q", got, tc.aspect)
			}
		})
	}
}

// The caption is fixed and carries no numbers, because the mode it applies is not a
// constant any more; the numbers live in the tooltip, which is re-rendered from every
// snapshot.
func TestRestoreButtonTextPrintsTheModeItWillApply(t *testing.T) {
	if strings.ContainsAny(restoreButtonText, "0123456789") {
		t.Errorf("restore caption %q still names a specific mode", restoreButtonText)
	}

	snapshot := widescreenSnapshot()
	tip := restoreTooltip(snapshot)
	if want := domain.ModeLabel(snapshot.FallbackMode); !strings.Contains(tip, want) {
		t.Errorf("restore tooltip %q does not print the mode it will apply (%q)", tip, want)
	}

	// A session that owns the desktop restores the arrangement it recorded, not the
	// derived fallback, so the tooltip must not promise a mode the click will not apply.
	snapshot.Managed = true
	owned := restoreTooltip(snapshot)
	if strings.Contains(owned, domain.ModeLabel(snapshot.FallbackMode)) {
		t.Errorf("owned restore tooltip %q promises the fallback mode", owned)
	}
	if owned == "" {
		t.Error("owned restore tooltip says nothing")
	}
}

func TestRestoreButtonIsDisabledWhenTheFallbackCannotBeDerived(t *testing.T) {
	snapshot := widescreenSnapshot()
	snapshot.FallbackMode, snapshot.FallbackKnown = domain.Mode{}, false
	snapshot.FallbackReason = "無法決定要恢復的顯示模式：顯示器沒有回報任何這個工具能套用的顯示模式"

	w := &window{}
	w.updateAvailability(snapshot)
	if w.unavailableReason != "" {
		t.Fatalf("precondition: an underivable fallback latched the window: %q", w.unavailableReason)
	}

	got := w.availableControls(snapshot)
	if got.restore {
		t.Error("restore stayed live with no mode to restore to")
	}
	// The toggle applies the configured mode and does not consult the fallback, so an
	// underivable fallback must not take it away too.
	if !got.toggle || !got.enable {
		t.Errorf("an underivable fallback disabled the apply controls: %+v", got)
	}

	if tip := restoreTooltip(snapshot); !strings.Contains(tip, snapshot.FallbackReason) {
		t.Errorf("restore tooltip %q does not explain why the button is disabled", tip)
	}
}

// The session tells the two derivation failures apart on purpose -- one is a monitor
// that stopped answering, the other a monitor that answered with nothing usable -- and
// the window passes the distinction through rather than flattening it into one
// sentence of its own.
func TestRestoreTooltipKeepsTheTwoFallbackFailuresApart(t *testing.T) {
	enumerationFailed := "無法決定要恢復的顯示模式：讀取顯示器支援的顯示模式失敗：EnumDisplaySettingsW 失敗"
	nothingUsable := "無法決定要恢復的顯示模式：顯示器沒有回報任何這個工具能套用的顯示模式"

	snapshot := widescreenSnapshot()
	snapshot.FallbackKnown, snapshot.FallbackMode = false, domain.Mode{}

	snapshot.FallbackReason = enumerationFailed
	first := restoreTooltip(snapshot)
	snapshot.FallbackReason = nothingUsable
	second := restoreTooltip(snapshot)

	if first == second {
		t.Fatalf("both derivation failures render as %q", first)
	}
	if !strings.Contains(first, enumerationFailed) || !strings.Contains(second, nothingUsable) {
		t.Errorf("tooltips lost the session's reasons: %q / %q", first, second)
	}
}

// A session that already owns the desktop has a saved arrangement to put back and does
// not consult the fallback at all. Disabling its restore would leave the user holding
// the applied mode with no way out but exiting the tool.
func TestRestoreStaysAvailableWhileTheSessionOwnsTheDesktop(t *testing.T) {
	snapshot := widescreenSnapshot()
	snapshot.State, snapshot.Managed, snapshot.AtGameMode = app.StateWaitingForGame, true, true
	snapshot.FallbackMode, snapshot.FallbackKnown = domain.Mode{}, false
	snapshot.FallbackReason = "無法決定要恢復的顯示模式：顯示器沒有回報任何這個工具能套用的顯示模式"

	w := &window{}
	w.updateAvailability(snapshot)

	if got := w.availableControls(snapshot); !got.restore {
		t.Fatalf("an owned desktop could not be restored: %+v", got)
	}
}

func TestAutoRestoreTextSaysUnsetWhenNoProcessIsConfigured(t *testing.T) {
	snapshot := widescreenSnapshot()
	snapshot.Profile.ProcessName = "   "

	got := autoRestoreText(snapshot)
	if !strings.Contains(got, "未設定") {
		t.Errorf("auto-restore text %q does not say the watch is unset", got)
	}
	if !strings.Contains(got, "不會自動恢復") {
		t.Errorf("auto-restore text %q does not say what that means", got)
	}
	if strings.Contains(got, "cs2.exe") || strings.Contains(got, "12") {
		t.Errorf("auto-restore text %q names a watch that is not configured", got)
	}
}

func TestAutoRestoreTextNamesTheWatchedProcessAndItsDelay(t *testing.T) {
	got := autoRestoreText(widescreenSnapshot())
	if !strings.Contains(got, "cs2.exe") {
		t.Errorf("auto-restore text %q does not name the watched process", got)
	}
	if !strings.Contains(got, "12") {
		t.Errorf("auto-restore text %q does not print the configured delay", got)
	}

	// A zero delay is a legal configuration and "結束後 0 秒" is not a sentence.
	immediate := widescreenSnapshot()
	immediate.Profile.RestoreDelay = 0
	if got := autoRestoreText(immediate); strings.Contains(got, "0 秒") {
		t.Errorf("a zero delay rendered as %q", got)
	}
}

func TestUnavailableReasonNamesTheConfiguredMonitorAndMode(t *testing.T) {
	snapshot := widescreenSnapshot()

	notFound := snapshot
	notFound.State, notFound.Err = app.StateError,
		fmt.Errorf("resolve target: %w: %s", display.ErrTargetNotFound, `MONITOR\DEL41A8`)
	w := &window{}
	w.updateAvailability(notFound)
	if !strings.Contains(w.unavailableReason, "DELL U4924DW") {
		t.Errorf("missing-target reason %q does not name the configured monitor", w.unavailableReason)
	}

	unsupported := snapshot
	unsupported.State, unsupported.Err = app.StateError, preflightRejection()
	w = &window{}
	w.updateAvailability(unsupported)
	if want := domain.ModeLabel(snapshot.Profile.GameMode); !strings.Contains(w.unavailableReason, want) {
		t.Errorf("unsupported-mode reason %q does not name the configured mode %q", w.unavailableReason, want)
	}
	for _, literal := range seedLiterals {
		if strings.Contains(w.unavailableReason, literal) {
			t.Errorf("unsupported-mode reason %q still carries the seed literal %q", w.unavailableReason, literal)
		}
	}
}

// StateManualOnly was added with the profile and parked in stateText's default branch
// so the build stayed green. A state rendered as its own identifier is a raw
// "manual-only" on screen.
func TestStateTextNamesTheManualOnlyState(t *testing.T) {
	snapshot := widescreenSnapshot()
	snapshot.State, snapshot.AtGameMode, snapshot.Managed = app.StateManualOnly, true, true
	snapshot.Profile.ProcessName = ""

	got := stateText(snapshot)
	if got == string(app.StateManualOnly) {
		t.Fatalf("StateManualOnly still falls through to the default branch: %q", got)
	}
	if !strings.Contains(got, domain.ModeLabel(snapshot.Profile.GameMode)) {
		t.Errorf("manual-only text %q does not say which mode is applied", got)
	}
	if !strings.Contains(got, "不會自動恢復") {
		t.Errorf("manual-only text %q does not say nothing will restore it", got)
	}
}

// Every state the session can publish has a sentence. The identifier leaking through
// the default branch is the failure this pins.
func TestStateTextCoversEveryState(t *testing.T) {
	states := []app.State{
		app.StateNative, app.StateApplying, app.StateManualOnly, app.StateWaitingForGame,
		app.StateGameRunning, app.StateRestorePending, app.StateRestoring, app.StateError,
		app.StateScalingCycle,
	}
	for _, state := range states {
		snapshot := widescreenSnapshot()
		snapshot.State = state
		got := stateText(snapshot)
		if got == "" || got == string(state) {
			t.Errorf("state %q renders as %q", state, got)
		}
	}
}

// internal/display's arrangement planner writes its refusals in Win32 device names,
// because it has no access to monitor labels. The window cannot label the other
// screens -- it never sees their identities -- but it does know which device name the
// configured monitor resolved to, and says so.
func TestStatusTextNamesTheConfiguredMonitorBesideItsDeviceName(t *testing.T) {
	snapshot := widescreenSnapshot()
	snapshot.State = app.StateError
	snapshot.Message = `plan display layout: display layout is not safe to apply: \\.\DISPLAY2 is not attached`

	w := &window{}
	got := w.statusText(snapshot)
	if !strings.Contains(got, "DELL U4924DW") {
		t.Errorf("status %q leaves the device name untranslated", got)
	}

	// A message about no device at all gets no note.
	quiet := widescreenSnapshot()
	quiet.Message = "尚未套用 3840 × 1080 @ 144 Hz"
	if got := (&window{}).statusText(quiet); strings.Contains(got, "目前是") {
		t.Errorf("status %q annotated a message that names no device", got)
	}
}

// The one test that would have caught the constants: render everything against a
// profile that mentions none of the shipped hardware and look for it anyway.
func TestNoRenderedStringCarriesTheSeedHardware(t *testing.T) {
	snapshot := widescreenSnapshot()

	rendered := map[string]string{
		"target":      targetText(snapshot),
		"tooltip":     targetTooltip(snapshot),
		"mode":        modeText(snapshot),
		"toggle":      toggleText(snapshot),
		"autoRestore": autoRestoreText(snapshot),
		"restoreTip":  restoreTooltip(snapshot),
		"trayEnable":  trayEnableText(snapshot),
		// The tray tip opens with the product name, which is branding rather than a
		// description of the configured mode -- it is also the README's heading, so
		// renaming it is a documentation decision and not this task's. Only the part
		// the profile generates is scanned.
		"trayTooltip": strings.TrimPrefix(trayTooltip(snapshot), windowTitle),
	}

	// Task 16 added a whole row. A renderer missing from this map is a renderer this
	// guard silently stops covering, so every one of them is listed here.
	scaled := scalingReadySnapshot()
	scaled.Scaling.Requested, scaled.Scaling.RequestedKnown = app.ScalingFullScreenByGPU(), true
	scaled.Scaling.Owned, scaled.Scaling.Saved = true, scalingAspectByDisplay()
	scaled.Managed, scaled.AtGameMode = true, true
	rendered["scalingRow"] = scalingText(scaled)
	rendered["scalingButton"] = scalingButtonText(scaled)
	rendered["scalingNote"] = scalingNoteText(scaled)
	rendered["scalingReminder"] = scalingAspectReminder(scaled)
	rendered["scalingMismatch"] = scalingMismatchNote(scaled)
	rendered["scalingCost"] = scalingManagedCostText(scaled)
	rendered["scalingExit"] = exitScalingWarning(scaled.Scaling)
	cycling := scaled
	cycling.State, cycling.Message = app.StateScalingCycle, "正在變更 GPU 縮放：寫入縮放設定…"
	rendered["scalingCycleRow"] = scalingText(cycling)
	cycleWindow := &window{}
	cycleWindow.updateScalingAvailability(cycling)
	rendered["scalingCycleBeside"] = cycleWindow.scalingButtonNote(cycling)
	rendered["scalingCycleAdvice"] = cycleWindow.scalingAdviceText(cycling)
	for name, view := range map[string]app.ScalingSnapshot{
		"scalingLatch:vendor":   {Reason: "偵測到 AMD Radeon RX 7800 XT，這個功能只支援 NVIDIA。"},
		"scalingLatch:unprobed": {},
		"scalingLatch:none":     {Available: true, Known: true, Effective: scalingAspectByDisplay()},
	} {
		latched := scaled
		latched.Scaling = view
		w := &window{}
		w.updateScalingAvailability(latched)
		rendered[name] = w.scalingUnavailableReason
		rendered["beside:"+name] = w.scalingButtonNote(latched)
		rendered["advice:"+name] = w.scalingAdviceText(latched)
	}
	unresolved := scaled
	unresolved.State = app.StateError
	unresolved.Err = fmt.Errorf("resolve target: %w", display.ErrTargetNotFound)
	unresolvedWindow := &window{}
	unresolvedWindow.updateScalingAvailability(unresolved)
	rendered["scalingLatch:target"] = unresolvedWindow.scalingUnavailableReason
	for _, state := range []app.State{
		app.StateNative, app.StateApplying, app.StateManualOnly, app.StateWaitingForGame,
		app.StateGameRunning, app.StateRestorePending, app.StateRestoring, app.StateError,
		app.StateScalingCycle,
	} {
		stated := snapshot
		stated.State = state
		rendered["state:"+string(state)] = stateText(stated)
	}
	for _, sentinel := range []error{
		display.ErrTargetNotFound, display.ErrModeNotSupported,
		display.ErrTargetAmbiguous, display.ErrTargetMirrored,
	} {
		latched := snapshot
		latched.State, latched.Err = app.StateError, fmt.Errorf("resolve target: %w", sentinel)
		w := &window{}
		w.updateAvailability(latched)
		rendered["latch:"+sentinel.Error()] = w.unavailableReason
	}

	for where, text := range rendered {
		for _, literal := range seedLiterals {
			if strings.Contains(text, literal) {
				t.Errorf("%s renders %q, which carries the seed literal %q", where, text, literal)
			}
		}
	}
}

func TestTrayTooltipNamesTheConfiguredMonitorAndMode(t *testing.T) {
	snapshot := widescreenSnapshot()
	got := trayTooltip(snapshot)
	if !strings.Contains(got, "DELL U4924DW") {
		t.Errorf("tray tooltip %q does not name the configured monitor", got)
	}
	if want := domain.ModeLabel(snapshot.Profile.GameMode); !strings.Contains(got, want) {
		t.Errorf("tray tooltip %q does not name the configured mode", got)
	}
	// Shell_NotifyIcon copies into a fixed 128-wchar field; an overlong tip loses its
	// terminator and renders as whatever follows it in memory.
	long := snapshot
	long.Profile.Monitor.Label = strings.Repeat("顯", 300)
	if got := len([]rune(trayTooltip(long))); got >= 128 {
		t.Errorf("tray tooltip is %d runes, which overruns NOTIFYICONDATA.szTip", got)
	}
}

// --- Task 16: GPU scaling is a second, independent availability group ---------

// scalingAspectByDisplay is the value the design was measured against: aspect-ratio
// scaling performed by the monitor, which is the setting that leaves black bars on a
// mode whose shape is not the panel's. Raw 6 is the number the driver reported.
func scalingAspectByDisplay() scaling.Value {
	return scaling.Value{Raw: 6, Mode: scaling.ModeAspectRatio, By: scaling.ByDisplay}
}

// scalingReadySnapshot is a window whose display half is healthy and whose driver
// answered: the baseline both isolation tests break in one direction only.
func scalingReadySnapshot() app.Snapshot {
	snapshot := widescreenSnapshot()
	snapshot.NativeMode, snapshot.NativeKnown = snapshot.FallbackMode, snapshot.FallbackKnown
	snapshot.Scaling = app.ScalingSnapshot{
		Available: true,
		Known:     true,
		Effective: scalingAspectByDisplay(),
	}
	return snapshot
}

// The bug this task exists to remove: one latched unavailableReason meant any NVAPI
// failure took the configured-mode toggle with it, and the mode toggle has nothing to
// do with the GPU scaling API.
func TestScalingUnavailabilityLeavesTheFourByThreeToggleUsable(t *testing.T) {
	const vendor = "偵測到 AMD Radeon RX 7800 XT，這個功能只支援 NVIDIA。請到該顯示卡的控制台手動設定全螢幕縮放。"
	snapshot := widescreenSnapshot()
	snapshot.Scaling = app.ScalingSnapshot{Reason: vendor}

	w := &window{}
	w.updateAvailability(snapshot)
	w.updateScalingAvailability(snapshot)

	if w.unavailableReason != "" {
		t.Fatalf("a missing GPU-scaling driver latched the display half: %q", w.unavailableReason)
	}
	if w.scalingUnavailableReason != vendor {
		t.Fatalf("scaling reason = %q, want the vendor text the session reported", w.scalingUnavailableReason)
	}
	got := w.availableControls(snapshot)
	if !got.toggle || !got.enable || !got.restore {
		t.Fatalf("an NVAPI failure disabled the display-mode controls: %+v", got)
	}
	if got.scalingApply || got.scalingRestore {
		t.Fatalf("an unavailable scaling controller left its own button live: %+v", got)
	}
	if !strings.Contains(scalingText(snapshot), "AMD Radeon RX 7800 XT") {
		t.Errorf("scaling row %q does not name the adapter that was detected", scalingText(snapshot))
	}
	// The reason is on screen under the button, not only in its tooltip, and it is the
	// first thing there: a user who cannot press the button needs it before anything else.
	if advice := w.scalingAdviceText(snapshot); !strings.HasPrefix(advice, vendor) {
		t.Errorf("advice block %q does not open with the reason the button is disabled", advice)
	}
}

// The other direction. The two halves own one sentence each: neither may be written
// with the other's words, and a display failure that is only about a display mode --
// the monitor reports no such mode -- leaves the scaling button entirely alone.
func TestTargetUnavailabilityLeavesTheScalingButtonReasonIntact(t *testing.T) {
	missing := scalingReadySnapshot()
	missing.State = app.StateError
	missing.Err = fmt.Errorf("resolve target: %w: %s", display.ErrTargetNotFound, `MONITOR\DEL41A8`)

	w := &window{}
	w.updateAvailability(missing)
	w.updateScalingAvailability(missing)

	if !strings.Contains(w.unavailableReason, "DELL U4924DW") {
		t.Fatalf("display latch %q lost the configured monitor", w.unavailableReason)
	}
	if w.scalingUnavailableReason == "" {
		t.Fatal("a monitor the tool cannot resolve left the scaling button live")
	}
	if w.scalingUnavailableReason == w.unavailableReason {
		t.Fatalf("both halves are rendering one shared sentence: %q", w.unavailableReason)
	}
	if !strings.Contains(w.scalingUnavailableReason, "DELL U4924DW") {
		t.Errorf("scaling reason %q does not name the monitor it is about", w.scalingUnavailableReason)
	}
	if strings.Contains(w.scalingUnavailableReason, "顯示模式切換") {
		t.Errorf("the scaling reason %q describes the display half's disabled command", w.scalingUnavailableReason)
	}
	if strings.Contains(w.unavailableReason, "GPU 縮放") {
		t.Errorf("the display reason %q describes the scaling half", w.unavailableReason)
	}

	// A mode the monitor does not report is purely a display-side refusal. It disables
	// the mode toggle and must not reach the scaling button at all.
	unsupported := scalingReadySnapshot()
	unsupported.State, unsupported.Err = app.StateError, preflightRejection()
	w = &window{}
	w.updateAvailability(unsupported)
	w.updateScalingAvailability(unsupported)
	if w.unavailableReason == "" {
		t.Fatal("precondition: the unsupported mode did not disable the mode toggle")
	}
	if w.scalingUnavailableReason != "" {
		t.Fatalf("an unsupported display mode disabled the scaling button: %q", w.scalingUnavailableReason)
	}
	got := w.availableControls(unsupported)
	if got.toggle || got.enable {
		t.Fatalf("precondition: the mode commands stayed live: %+v", got)
	}
	if !got.scalingApply {
		t.Fatalf("an unsupported display mode took the scaling button away: %+v", got)
	}
}

func TestScalingButtonTextSwitchesToRestoreAndPrintsTheValueItWillRestore(t *testing.T) {
	fresh := scalingReadySnapshot()
	if got := scalingButtonText(fresh); got != scalingApplyText {
		t.Fatalf("untouched scaling button = %q, want %q", got, scalingApplyText)
	}

	owned := scalingReadySnapshot()
	owned.Scaling.Effective = app.ScalingFullScreenByGPU()
	owned.Scaling.Owned, owned.Scaling.Saved = true, scalingAspectByDisplay()

	got := scalingButtonText(owned)
	if !strings.Contains(got, "還原") {
		t.Errorf("owned scaling button %q does not offer a restore", got)
	}
	if want := app.ScalingLabel(scalingAspectByDisplay()); !strings.Contains(got, want) {
		t.Errorf("owned scaling button %q does not print the value it will restore (%q)", got, want)
	}
}

// The tool reports what took effect. A driver that stored something other than the
// written value was measured on real hardware, so the line the user reads must be the
// read-back and never the request.
func TestScalingLineShowsTheEffectiveValueNotTheRequestedOne(t *testing.T) {
	snapshot := scalingReadySnapshot()
	snapshot.Scaling.Requested, snapshot.Scaling.RequestedKnown = app.ScalingFullScreenByGPU(), true
	snapshot.Scaling.Matched = false

	line := scalingText(snapshot)
	if want := app.ScalingLabel(scalingAspectByDisplay()); !strings.Contains(line, want) {
		t.Fatalf("scaling line %q does not print the effective value %q", line, want)
	}
	if strings.Contains(line, "全螢幕") {
		t.Fatalf("scaling line %q prints the value that was asked for", line)
	}
}

// A normalised write is not a failure: nothing broke and nothing needs undoing. Both
// values are printed, and nothing in the row is styled or worded as an error.
func TestAMismatchedReadBackIsShownAsBothValuesAndIsNotAnError(t *testing.T) {
	snapshot := scalingReadySnapshot()
	snapshot.Scaling.Requested, snapshot.Scaling.RequestedKnown = app.ScalingFullScreenByGPU(), true
	snapshot.Scaling.Matched = false
	snapshot.Scaling.Owned, snapshot.Scaling.Saved = true, scalingAspectByDisplay()

	note := scalingNoteText(snapshot)
	for _, want := range []string{
		app.ScalingLabel(app.ScalingFullScreenByGPU()),
		app.ScalingLabel(scalingAspectByDisplay()),
	} {
		if !strings.Contains(note, want) {
			t.Errorf("scaling note %q does not print %q", note, want)
		}
	}
	// The sentence about the normalised value itself must read as a statement of fact.
	// The permanent override line below it legitimately says what the tool cannot do,
	// so the wording check is aimed at the mismatch sentence rather than the block.
	mismatch := scalingMismatchNote(snapshot)
	if mismatch == "" {
		t.Fatal("a driver that stored a different value said nothing")
	}
	for _, forbidden := range []string{"失敗", "錯誤", "無法"} {
		if strings.Contains(mismatch, forbidden) {
			t.Errorf("mismatch note %q words a normalised value as a failure (%q)", mismatch, forbidden)
		}
	}
	if strings.Contains(scalingText(snapshot), "失敗") {
		t.Errorf("scaling row %q words a normalised value as a failure", scalingText(snapshot))
	}

	w := &window{}
	w.updateScalingAvailability(snapshot)
	if w.scalingUnavailableReason != "" {
		t.Errorf("a normalised value disabled the scaling button: %q", w.scalingUnavailableReason)
	}
}

// The profile design promised a static reminder. With a read path it becomes measured,
// and the honesty line about the one checkbox NVAPI does not expose sits beside it
// permanently rather than as a note to fix later.
func TestNonNativeChoiceShowsTheMeasuredScalingWarning(t *testing.T) {
	snapshot := scalingReadySnapshot()

	measured := scalingAspectReminder(snapshot)
	if want := domain.AspectLabel(snapshot.Profile.GameMode.Width, snapshot.Profile.GameMode.Height); !strings.Contains(measured, want) {
		t.Errorf("reminder %q does not name the configured mode's shape %q", measured, want)
	}
	if want := app.ScalingLabel(scalingAspectByDisplay()); !strings.Contains(measured, want) {
		t.Errorf("reminder %q does not print the scaling value that was read back", measured)
	}
	if !strings.Contains(measured, "黑邊") {
		t.Errorf("reminder %q does not say what the user will see", measured)
	}

	// Full-screen scaling is stated as a fact and nothing more. The tool cannot read
	// whether a game overrode it, so it never claims the bars are gone.
	fullScreen := scalingReadySnapshot()
	fullScreen.Scaling.Effective = app.ScalingFullScreenByGPU()
	if got := scalingAspectReminder(fullScreen); strings.Contains(got, "黑邊") {
		t.Errorf("full-screen reminder %q still promises black bars", got)
	}
	if got := scalingAspectReminder(fullScreen); !strings.Contains(got, app.ScalingLabel(app.ScalingFullScreenByGPU())) {
		t.Errorf("full-screen reminder %q does not print the value that was read back", got)
	}

	// No read path, no measurement: the promised static wording, with no value invented.
	unread := scalingReadySnapshot()
	unread.Scaling = app.ScalingSnapshot{}
	static := scalingAspectReminder(unread)
	if static == "" || strings.Contains(static, "（由") {
		t.Errorf("unread reminder %q is not the static wording", static)
	}

	// A mode whose shape matches the panel's enumerated native mode needs no reminder.
	native := scalingReadySnapshot()
	native.NativeMode = domain.Mode{Width: 1280, Height: 360, RefreshHz: 60, BitsPerPixel: 32}
	if got := scalingAspectReminder(native); got != "" {
		t.Errorf("a same-shape mode produced a reminder: %q", got)
	}

	note := scalingNoteText(snapshot)
	if !strings.Contains(note, scalingOverrideNote) {
		t.Errorf("scaling note %q drops the override-checkbox line", note)
	}
	if !strings.Contains(scalingOverrideNote, "工具無法代為設定") {
		t.Errorf("override line %q does not say the tool cannot set it", scalingOverrideNote)
	}

	// The same measurement reaches the settings dialog's 備註 column.
	row := modeRow{Width: 1920, Height: 1440, Aspect: "4:3", FullScreenScalingReminder: true}
	if got := modeNotes(row, snapshot.Scaling); !strings.Contains(got, app.ScalingLabel(scalingAspectByDisplay())) {
		t.Errorf("settings note %q is still the static reminder", got)
	}
	if got := modeNotes(row, app.ScalingSnapshot{}); strings.Contains(got, "（由") {
		t.Errorf("settings note %q invented a scaling value it never read", got)
	}
}

func TestScalingAspectReminderUsesPanelNativeNotExplicitFallback(t *testing.T) {
	snapshot := scalingReadySnapshot()
	snapshot.Profile.GameMode = domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	snapshot.FallbackMode = domain.Mode{Width: 1600, Height: 1200, RefreshHz: 60, BitsPerPixel: 32}
	snapshot.FallbackKnown = true
	snapshot.NativeMode = domain.Mode{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32}
	snapshot.NativeKnown = true
	if got := scalingAspectReminder(snapshot); got == "" || !strings.Contains(got, "4:3") {
		t.Fatalf("native 16:9 with explicit 4:3 fallback suppressed reminder: %q", got)
	}

	snapshot.FallbackMode = domain.Mode{Width: 2560, Height: 1440, RefreshHz: 60, BitsPerPixel: 32}
	snapshot.NativeMode = domain.Mode{Width: 1600, Height: 1200, RefreshHz: 60, BitsPerPixel: 32}
	if got := scalingAspectReminder(snapshot); got != "" {
		t.Fatalf("native 4:3 with explicit 16:9 fallback produced false warning: %q", got)
	}
}

func TestScalingAspectReminderStaysHonestWhenNativeModeIsUnknown(t *testing.T) {
	snapshot := scalingReadySnapshot()
	snapshot.NativeMode, snapshot.NativeKnown = domain.Mode{}, false
	if got := scalingAspectReminder(snapshot); got != "" {
		t.Fatalf("unknown native mode produced a guessed reminder: %q", got)
	}
}

// The only moment a user sees the black bars is while the configured mode is applied,
// so a scaling button disabled exactly then is a button that does not exist.
func TestScalingButtonStaysEnabledWhileTheSessionOwnsAnAppliedMode(t *testing.T) {
	snapshot := scalingReadySnapshot()
	snapshot.State, snapshot.Managed, snapshot.AtGameMode = app.StateWaitingForGame, true, true

	w := &window{}
	w.updateAvailability(snapshot)
	w.updateScalingAvailability(snapshot)

	got := w.availableControls(snapshot)
	if !got.scalingApply {
		t.Fatalf("an owned display mode disabled the scaling button: %+v", got)
	}

	owned := snapshot
	owned.Scaling.Owned, owned.Scaling.Saved = true, scalingAspectByDisplay()
	if got := w.availableControls(owned); !got.scalingRestore || got.scalingApply {
		t.Fatalf("an owned scaling value did not switch the button to restore: %+v", got)
	}
}

func TestScalingButtonWarnsThatTheScreensChangeModeTwiceMoreWhileManaged(t *testing.T) {
	idle := scalingReadySnapshot()
	w := &window{}
	w.updateScalingAvailability(idle)
	if got := w.scalingButtonNote(idle); got != "" {
		t.Fatalf("an unmanaged session warned about a cycle it will not run: %q", got)
	}

	managed := idle
	managed.State, managed.Managed, managed.AtGameMode = app.StateWaitingForGame, true, true
	got := w.scalingButtonNote(managed)
	for _, want := range []string{"恢復原始排列", "縮放", "再切回"} {
		if !strings.Contains(got, want) {
			t.Errorf("managed warning %q does not contain %q", got, want)
		}
	}
	if want := domain.ModeLabel(managed.Profile.GameMode); !strings.Contains(got, want) {
		t.Errorf("managed warning %q does not name the mode it will put back (%q)", got, want)
	}
}

// opMu already makes a second click merely queue. Disabling is about not offering a
// command against a desktop that is in the middle of changing.
func TestEveryMutatingControlIsDisabledDuringTheScalingCycle(t *testing.T) {
	snapshot := scalingReadySnapshot()
	snapshot.State = app.StateScalingCycle
	snapshot.Managed, snapshot.AtGameMode = true, true
	snapshot.Message = "正在變更 GPU 縮放：寫入縮放設定…"

	w := &window{}
	w.updateAvailability(snapshot)
	w.updateScalingAvailability(snapshot)

	got := w.availableControls(snapshot)
	if got.toggle || got.restore || got.enable || got.settings || got.reset ||
		got.scalingApply || got.scalingRestore || got.exit {
		t.Fatalf("a mutating control stayed live during the cycle: %+v", got)
	}
	// Hiding and showing the window change nothing on the desktop and stay available.
	if !got.show || !got.hide {
		t.Fatalf("the cycle took away a control that changes nothing: %+v", got)
	}

	// Managed is held true for the whole cycle on purpose, so nothing flickers to the
	// unapplied rendering at the one moment pressing it would mean nothing.
	if !snapshot.Managed {
		t.Fatal("fixture no longer models the masked Managed flag")
	}
	if got := scalingText(snapshot); !strings.Contains(got, "寫入縮放設定") {
		t.Errorf("scaling row %q does not name the phase the cycle is on", got)
	}
	if got := (&window{}).statusText(snapshot); !strings.Contains(got, snapshot.Message) {
		t.Errorf("status %q does not name the phase the cycle is on", got)
	}
	if got := stateText(snapshot); got == string(app.StateScalingCycle) {
		t.Errorf("StateScalingCycle falls through to the default branch: %q", got)
	}
}

func TestRequiredScalingDetailsRemainVisibleOutsideTheTooltip(t *testing.T) {
	snapshot := scalingReadySnapshot()
	snapshot.Scaling.Requested = app.ScalingFullScreenByGPU()
	snapshot.Scaling.RequestedKnown = true
	snapshot.Scaling.Matched = false

	details := fitText(scalingDetailsText(snapshot), reasonLineBudget, detailLineLimit)
	continuousDetails := strings.ReplaceAll(details, "\n", "")
	for _, want := range []string{
		app.ScalingLabel(app.ScalingFullScreenByGPU()),
		app.ScalingLabel(scalingAspectByDisplay()),
	} {
		if !strings.Contains(continuousDetails, want) {
			t.Fatalf("visible scaling details %q lost %q", details, want)
		}
	}
	if got := fitText(scalingOverrideText(), reasonLineBudget, overrideLineLimit); strings.ReplaceAll(got, "\n", "") != scalingOverrideNote {
		t.Fatalf("visible override line = %q, want full honesty line", got)
	}
}

func TestHiddenExitWarningKeepsTheTrayAliveLongEnoughToBeDelivered(t *testing.T) {
	if got := exitNoticeLifetime("warning", false); got <= 0 {
		t.Fatalf("hidden exit warning lifetime = %s, want a positive tray lifetime", got)
	}
	if got := exitNoticeLifetime("warning", true); got != 0 {
		t.Fatalf("visible warning lifetime = %s, want synchronous dialog path", got)
	}
	if got := exitNoticeLifetime("", false); got != 0 {
		t.Fatalf("successful exit lifetime = %s, want immediate exit", got)
	}
}

// The asymmetry is deliberate: a profile change voids what the saved arrangement
// means, while a scaling change only threatens the device names inside it.
func TestSettingsStaysDisabledWhileManagedEvenThoughScalingDoesNot(t *testing.T) {
	snapshot := scalingReadySnapshot()
	snapshot.State, snapshot.Managed, snapshot.AtGameMode = app.StateWaitingForGame, true, true

	w := &window{configState: configStateConfigured}
	w.updateScalingAvailability(snapshot)

	got := w.availableControls(snapshot)
	if got.settings {
		t.Fatalf("settings stayed live while the session owned a mode: %+v", got)
	}
	if !got.scalingApply {
		t.Fatalf("the scaling button was disabled for the settings dialog's reason: %+v", got)
	}
	if reason := settingsDisabledReason(snapshot); reason != settingsManagedText {
		t.Fatalf("settings reason = %q", reason)
	}
}

// Each of the cycle's three failure exits says what failed and what the desktop is
// doing now. The window is the one place the user reads them, so it must pass them
// through without rewriting or truncating them.
func TestScalingCycleFailureMessagesReachTheStatusLine(t *testing.T) {
	messages := map[string]string{
		"restore failed":  "恢復原始顯示排列失敗，GPU 縮放未變更：driver refused the mode change",
		"scaling failed":  "變更 GPU 縮放失敗，桌面已恢復為原始排列，工具不再管理顯示模式：NVAPI_ERROR",
		"re-apply failed": "GPU 縮放已變更為「全螢幕（由 GPU 執行）」，但重新套用 3840 × 1080 @ 144 Hz 失敗，桌面維持在原始排列：driver refused the mode change",
	}
	for name, message := range messages {
		t.Run(name, func(t *testing.T) {
			snapshot := scalingReadySnapshot()
			snapshot.State, snapshot.Message = app.StateError, message
			snapshot.Err = errors.New(message)

			if got := (&window{}).statusText(snapshot); !strings.Contains(got, message) {
				t.Fatalf("status %q lost the cycle's exit wording", got)
			}
		})
	}
}

// A previous smoke test found a long rejection widening the fixed window. Nothing the
// driver can say may do that: the label is wrapped to a bounded width before it
// reaches Walk, and the untouched text stays reachable as the tooltip.
func TestALongScalingRejectionIsWrappedInsteadOfWideningTheWindow(t *testing.T) {
	const long = "NVAPI_INCOMPATIBLE_STRUCT_VERSION (-9)：這個驅動版本的結構約定與工具不同，" +
		"已停用 GPU 縮放功能到本次執行結束，不會用猜出來的版本重試。"

	fitted := fitText(long, reasonLineBudget, reasonLineLimit)
	for _, line := range strings.Split(fitted, "\n") {
		if len([]rune(line)) > reasonLineBudget {
			t.Fatalf("line %q is %d runes, which is wider than the fixed window allows", line, len([]rune(line)))
		}
	}
	if strings.Count(fitted, "\n")+1 > reasonLineLimit {
		t.Fatalf("fitted text runs to %d lines", strings.Count(fitted, "\n")+1)
	}
	if !strings.HasPrefix(long, strings.SplitN(fitted, "\n", 2)[0]) {
		t.Fatalf("wrapping rewrote the first line: %q", fitted)
	}

	// Every label that can be handed driver text is bounded the same way.
	for name, limit := range map[string]int{"advice": adviceLineLimit, "status": statusLineLimit} {
		folded := fitText(strings.Repeat(long, 4), reasonLineBudget, limit)
		if strings.Count(folded, "\n")+1 > limit {
			t.Errorf("%s label runs to %d lines", name, strings.Count(folded, "\n")+1)
		}
		for _, line := range strings.Split(folded, "\n") {
			if len([]rune(line)) > reasonLineBudget+1 {
				t.Errorf("%s line %q is %d runes wide", name, line, len([]rune(line)))
			}
		}
	}

	// A short reason is left exactly as it is.
	if got := fitText("找不到 NVIDIA 驅動", reasonLineBudget, reasonLineLimit); got != "找不到 NVIDIA 驅動" {
		t.Fatalf("a short reason was rewritten: %q", got)
	}
	if got := fitText("", reasonLineBudget, reasonLineLimit); got != "" {
		t.Fatalf("an empty reason rendered as %q", got)
	}
}

// fakeScalingController answers the session without NVAPI. Restore is the only call
// that can be made to fail, because the exit path is the one place a scaling failure
// is deliberately swallowed.
type fakeScalingController struct {
	mu         sync.Mutex
	effective  scaling.Value
	restoreErr error
	restores   int
}

func (f *fakeScalingController) Probe() scaling.Availability {
	return scaling.Availability{Available: true}
}

func (f *fakeScalingController) Read(domain.MonitorIdentity) (scaling.State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return scaling.State{DisplayID: 1, Effective: f.effective}, nil
}

func (f *fakeScalingController) Apply(_ domain.MonitorIdentity, value scaling.Value) (scaling.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	previous := scaling.State{DisplayID: 1, Effective: f.effective}
	f.effective = value
	return scaling.Outcome{
		Requested: value, Previous: previous,
		State:        scaling.State{DisplayID: 1, Effective: value},
		SetAttempted: true, Applied: true, ReadBackKnown: true, Matched: true,
	}, nil
}

func (f *fakeScalingController) Restore(_ domain.MonitorIdentity, _ uint32, value scaling.Value) (scaling.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restores++
	if f.restoreErr != nil {
		return scaling.Outcome{Requested: value, SetAttempted: true}, f.restoreErr
	}
	previous := scaling.State{DisplayID: 1, Effective: f.effective}
	f.effective = value
	return scaling.Outcome{
		Requested: value, Previous: previous,
		State:        scaling.State{DisplayID: 1, Effective: value},
		SetAttempted: true, Applied: true, ReadBackKnown: true, Matched: true,
	}, nil
}

func (f *fakeScalingController) Close() error { return nil }

// Session.Shutdown returns nil when only the scaling restore failed, by design: a tool
// that cannot be closed is worse than a runtime-only scaling value left changed. The
// signal is therefore structural -- the row still says this run owns a change -- and
// the window has to read it from the session it just retired, because Provider stops
// observing that session before it shuts it down.
func TestExitReportsAScalingRestoreThatShutdownDeliberatelySwallowed(t *testing.T) {
	t.Setenv(config.EnvPath, filepath.Join(t.TempDir(), "config.json"))
	controller := &fakeScalingController{effective: scalingAspectByDisplay()}
	provider := app.NewProviderWithScaling(&stubDisplays{}, stubProcesses{}, controller)
	t.Cleanup(func() { _ = provider.Shutdown() })
	if err := provider.Replace(domain.LegacySeedProfile()); err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	session := provider.Session()
	if session == nil {
		t.Fatal("precondition: no session to exercise")
	}
	if err := session.ApplyGPUScaling(); err != nil {
		t.Fatalf("ApplyGPUScaling: %v", err)
	}
	if !session.Snapshot().Scaling.Owned {
		t.Fatal("precondition: the successful write was not booked")
	}
	controller.mu.Lock()
	controller.restoreErr = errors.New("NVAPI_ERROR")
	controller.mu.Unlock()

	if err := provider.Shutdown(); err != nil {
		t.Fatalf("a failed scaling restore blocked the exit: %v", err)
	}
	after := shutdownScaling(provider, session)
	if !after.Owned {
		t.Fatal("a failed exit restore left no signal for the window to report")
	}
	warning := exitScalingWarning(after)
	if warning == "" {
		t.Fatal("the window says nothing about a scaling value it could not put back")
	}
	if want := app.ScalingLabel(scalingAspectByDisplay()); !strings.Contains(warning, want) {
		t.Errorf("exit warning %q does not name the value it failed to restore (%q)", warning, want)
	}
	if !strings.Contains(warning, "重新開機") {
		t.Errorf("exit warning %q does not say how the value goes away on its own", warning)
	}
}

// The same hook must stay quiet when the restore worked, which is the case the
// Provider's own cached snapshot cannot tell apart: it stops observing the session
// before Shutdown runs, so its copy still says the value is owned.
func TestExitSaysNothingWhenTheScalingRestoreSucceeded(t *testing.T) {
	t.Setenv(config.EnvPath, filepath.Join(t.TempDir(), "config.json"))
	controller := &fakeScalingController{effective: scalingAspectByDisplay()}
	provider := app.NewProviderWithScaling(&stubDisplays{}, stubProcesses{}, controller)
	t.Cleanup(func() { _ = provider.Shutdown() })
	if err := provider.Replace(domain.LegacySeedProfile()); err != nil {
		t.Fatalf("configure provider: %v", err)
	}
	session := provider.Session()
	if err := session.ApplyGPUScaling(); err != nil {
		t.Fatalf("ApplyGPUScaling: %v", err)
	}
	if err := provider.Shutdown(); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if controller.restores == 0 {
		t.Fatal("precondition: the exit path never attempted a restore")
	}
	if got := exitScalingWarning(shutdownScaling(provider, session)); got != "" {
		t.Fatalf("a successful exit restore still warned: %q", got)
	}
}
