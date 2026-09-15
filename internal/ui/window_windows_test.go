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
	if got.refresh || got.toggle || got.restore || got.enable || got.exit {
		t.Fatalf("controls = %+v", got)
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
	for _, state := range []app.State{
		app.StateNative, app.StateApplying, app.StateManualOnly, app.StateWaitingForGame,
		app.StateGameRunning, app.StateRestorePending, app.StateRestoring, app.StateError,
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
