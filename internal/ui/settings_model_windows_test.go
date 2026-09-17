package ui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/config"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/scaling"
)

func TestWizardPrefillsTheLegacyValuesWhenTheOriginalMonitorIsPresent(t *testing.T) {
	legacy := domain.LegacySeedProfile()
	target := settingsTarget(`\\.\DISPLAY2`, `\\?\DISPLAY#XMI27B2#1`, legacy.Monitor.HardwareID, "Mi Monitor")
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {legacy.GameMode}},
	})

	draft := model.Draft()
	if draft.Monitor.InstancePath != target.Identity.InstancePath || draft.GameMode != legacy.GameMode {
		t.Fatalf("Draft() = %#v, want legacy monitor and game mode", draft)
	}
	if draft.ProcessName != legacy.ProcessName || draft.RestoreDelay != legacy.RestoreDelay {
		t.Fatalf("Draft() watch = %q, %s; want legacy values", draft.ProcessName, draft.RestoreDelay)
	}
	if !model.LegacyPrefilled() {
		t.Fatal("LegacyPrefilled() = false, want true")
	}
}

func TestWizardStartsBlankWhenTheOriginalMonitorIsAbsent(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY2`, `\\?\DISPLAY#OTHER#1`, `MONITOR\OTHER`, "Other")
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}}},
	})

	draft := model.Draft()
	if draft.Monitor.InstancePath != "" || draft.GameMode != (domain.Mode{}) || draft.ProcessName != "" {
		t.Fatalf("Draft() = %#v, want blank first-run selection", draft)
	}
	if model.LegacyPrefilled() {
		t.Fatal("LegacyPrefilled() = true, want false")
	}
}

func TestSelectingAMonitorRecordsItsInstancePathAndWhetherTheModelWasUnique(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY2`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}}},
	})
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}

	draft := model.Draft()
	if draft.Monitor.InstancePath != target.Identity.InstancePath || !draft.Monitor.ModelWasUnique {
		t.Fatalf("selected identity = %#v, want real path and ModelWasUnique", draft.Monitor)
	}
}

func TestTwoMonitorsOfOneModelAppearAsTwoRowsAndSetModelWasUniqueFalse(t *testing.T) {
	first := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#SAME#1`, `MONITOR\SAME`, "Same")
	second := settingsTarget(`\\.\DISPLAY2`, `\\?\DISPLAY#SAME#2`, `MONITOR\SAME`, "Same")
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{first, second},
		modes: map[string][]domain.Mode{
			first.DeviceName:  {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}},
			second.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}},
		},
	})
	if rows := model.MonitorRows(); len(rows) != 2 || rows[0].Target.Identity.InstancePath == rows[1].Target.Identity.InstancePath {
		t.Fatalf("MonitorRows() = %#v, want two separately-addressable rows", rows)
	}
	if err := model.SelectMonitor(1); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if model.Draft().Monitor.ModelWasUnique {
		t.Fatal("ModelWasUnique = true, want false for duplicate hardware model")
	}
}

func TestTheModeTableFiltersByAspectAndDefaultsToTheHighestRefresh(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes: map[string][]domain.Mode{target.DeviceName: {
			{Width: 1920, Height: 1440, RefreshHz: 120, BitsPerPixel: 32},
			{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
			{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32},
		}},
	})
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectResolution(1920, 1440); err != nil {
		t.Fatalf("SelectResolution: %v", err)
	}
	rows := model.ModeRows(aspectFourByThree)
	if len(rows) != 1 || rows[0].HighestRefresh != 180 || !reflect.DeepEqual(rows[0].RefreshRates, []uint32{180, 120}) {
		t.Fatalf("ModeRows(4:3) = %#v, want grouped descending refresh row", rows)
	}
	if got := model.Draft().GameMode; got != (domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}) {
		t.Fatalf("default mode = %#v, want highest full tuple", got)
	}
}

func TestANonNativeChoiceCarriesTheFullScreenScalingReminder(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes: map[string][]domain.Mode{target.DeviceName: {
			{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
			{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32},
		}},
	})
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	rows := model.ModeRows(aspectAll)
	for _, row := range rows {
		if row.Width == 1920 && row.Height == 1440 && !row.FullScreenScalingReminder {
			t.Fatalf("ModeRows() row = %#v, want scaling reminder", row)
		}
	}
}

// The settings dialog can select a monitor other than the session's current target.
// Its note must therefore come from a fresh read for the selected identity, never from
// the snapshot that happened to be on the main window when the dialog opened.
func TestSelectingAnotherMonitorReadsScalingForThatMonitor(t *testing.T) {
	first := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	second := settingsTarget(`\\.\DISPLAY2`, `\\?\DISPLAY#TWO#1`, `MONITOR\TWO`, "Two")
	model := newSettingsModel(&fakeSettingsDisplay{
		targets: []domain.Target{first, second},
		modes: map[string][]domain.Mode{
			first.DeviceName: {
				{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32},
				{Width: 1920, Height: 1440, RefreshHz: 144, BitsPerPixel: 32},
			},
			second.DeviceName: {
				{Width: 3840, Height: 2160, RefreshHz: 120, BitsPerPixel: 32},
				{Width: 1600, Height: 1200, RefreshHz: 120, BitsPerPixel: 32},
			},
		},
	}, &fakeSettingsLister{}, domain.Profile{}, app.ScalingSnapshot{}, true)
	model.readScaling = func(identity domain.MonitorIdentity) app.ScalingSnapshot {
		if identity.InstancePath == second.Identity.InstancePath {
			return app.ScalingSnapshot{Available: true, Known: true, Effective: scaling.Value{
				Raw: 2, Mode: scaling.ModeFullScreen, By: scaling.ByGPU,
			}}
		}
		return app.ScalingSnapshot{Available: true, Known: true, Effective: scaling.Value{
			Raw: 6, Mode: scaling.ModeAspectRatio, By: scaling.ByDisplay,
		}}
	}
	if err := model.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if err := model.SelectMonitor(1); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}

	rows := model.ModeRows(aspectFourByThree)
	if len(rows) != 1 {
		t.Fatalf("ModeRows(4:3) = %#v, want one non-native row", rows)
	}
	note := modeNotes(rows[0], model.scaling)
	if !strings.Contains(note, app.ScalingLabel(app.ScalingFullScreenByGPU())) {
		t.Fatalf("selected monitor note = %q, want second monitor's measured scaling", note)
	}
	if strings.Contains(note, app.ScalingLabel(scaling.Value{Raw: 6, Mode: scaling.ModeAspectRatio, By: scaling.ByDisplay})) {
		t.Fatalf("selected monitor note = %q, still shows the previous monitor's scaling", note)
	}
}

func TestAMonitorWithNoUsableModeIsOfferedWithAReasonNotSilentlyOmitted(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{targets: []domain.Target{target}, modes: map[string][]domain.Mode{target.DeviceName: nil}})
	rows := model.MonitorRows()
	if len(rows) != 1 || rows[0].Reason == "" {
		t.Fatalf("MonitorRows() = %#v, want unusable monitor and reason", rows)
	}
}

func TestMonitorRowsReadCurrentModeAndPrimaryFromTheLayoutSnapshot(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	current := domain.Mode{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32}
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {current}},
		layout: domain.Layout{Displays: []domain.DisplayState{{
			DeviceName: target.DeviceName, Mode: current, Primary: true,
		}}},
	})
	row := model.MonitorRows()[0]
	if !row.Primary || !row.HasCurrentMode || row.CurrentMode != current {
		t.Fatalf("MonitorRows() = %#v, want layout current mode and primary flag", row)
	}
}

func TestSaveWritesOnceAndOnlyAfterValidationThroughTheConfigPackage(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}}},
	})
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectResolution(1920, 1080); err != nil {
		t.Fatalf("SelectResolution: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvPath, path)
	model.SetProcessName(`C:\not-an-executable-name.exe`)
	if _, err := model.Save(); err == nil {
		t.Fatal("Save() error = nil, want config validation rejection")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid Save wrote %s: %v", path, err)
	}

	model.SetProcessName("game.exe")
	calls := 0
	model.save = func(file config.File) error {
		calls++
		if file != config.FromProfile(model.Draft()) {
			t.Fatalf("save file = %#v, want config.FromProfile(Draft())", file)
		}
		return nil
	}
	if _, err := model.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if calls != 1 {
		t.Fatalf("save calls = %d, want 1", calls)
	}
}

func TestAbandoningFirstRunLeavesNoFileBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvPath, path)
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	_ = refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}}},
	})
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opening wizard wrote %s: %v", path, err)
	}
}

func TestSelectingAnotherMonitorClearsItsExplicitFallbackButKeepsDelay(t *testing.T) {
	first := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	second := settingsTarget(`\\.\DISPLAY2`, `\\?\DISPLAY#TWO#1`, `MONITOR\TWO`, "Two")
	fallback := domain.Mode{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32}
	profile := domain.Profile{Monitor: first.Identity, FallbackMode: &fallback, RestoreDelay: 9 * time.Second}
	model := refreshedSettingsModelWithProfile(t, &fakeSettingsDisplay{
		targets: []domain.Target{first, second},
		modes: map[string][]domain.Mode{
			first.DeviceName:  {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}},
			second.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}},
		},
	}, profile)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor same monitor: %v", err)
	}
	if model.Draft().FallbackMode == nil {
		t.Fatal("same-monitor edit cleared fallback")
	}
	if err := model.SelectMonitor(1); err != nil {
		t.Fatalf("SelectMonitor another monitor: %v", err)
	}
	draft := model.Draft()
	if draft.FallbackMode != nil || draft.RestoreDelay != 9*time.Second {
		t.Fatalf("Draft() after monitor change = %#v, want fallback cleared and delay retained", draft)
	}
}

func TestProcessNamesAreCachedAndSearchableWithManualMode(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	lister := &fakeSettingsLister{names: []string{"alpha.exe", "Beta.exe", "gamma.exe"}}
	model := refreshedSettingsModelWithLister(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}}},
	}, lister)
	if got := model.ProcessNames("ET"); !reflect.DeepEqual(got, []string{"Beta.exe"}) {
		t.Fatalf("ProcessNames search = %q, want Beta.exe", got)
	}
	if lister.calls != 1 {
		t.Fatalf("Names calls = %d, want 1 cached read", lister.calls)
	}
	model.SetProcessName("alpha.exe")
	model.UseManualOnly()
	if model.Draft().ProcessName != "" {
		t.Fatalf("manual mode process = %q, want empty", model.Draft().ProcessName)
	}
}

func TestSelectingAMonitorUsesTheDialogLifetimeModeCache(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	displays := &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}}},
	}
	model := refreshedSettingsModel(t, displays)
	if displays.modeCalls != 1 {
		t.Fatalf("EnumModes calls after Refresh = %d, want 1", displays.modeCalls)
	}
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if displays.modeCalls != 1 {
		t.Fatalf("EnumModes calls after selection = %d, want cached result", displays.modeCalls)
	}
}

func TestRefreshFailureInvalidatesSaveReadinessUntilTheDisplayReadSucceeds(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	displays := &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}}},
	}
	model := refreshedSettingsModel(t, displays)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectResolution(1920, 1080); err != nil {
		t.Fatalf("SelectResolution: %v", err)
	}
	if ready, reason := model.SaveReady(); !ready {
		t.Fatalf("SaveReady() = false, %q; want true", reason)
	}

	displays.targetsErr = errors.New("display read failed")
	if err := model.Refresh(); err == nil {
		t.Fatal("Refresh() error = nil, want display read failure")
	}
	if ready, reason := model.SaveReady(); ready || reason == "" {
		t.Fatalf("SaveReady() = %v, %q; want unready reason", ready, reason)
	}
	if model.Draft().Monitor.InstancePath != target.Identity.InstancePath {
		t.Fatal("Refresh failure discarded the user's draft")
	}

	displays.targetsErr = nil
	if err := model.Refresh(); err != nil {
		t.Fatalf("successful Refresh: %v", err)
	}
	if ready, reason := model.SaveReady(); !ready {
		t.Fatalf("SaveReady() after successful Refresh = false, %q", reason)
	}
}

func TestSaveReadinessRejectsMissingOrUnusableSelectedMonitor(t *testing.T) {
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	mode := domain.Mode{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}
	displays := &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {mode}},
	}
	model := refreshedSettingsModel(t, displays)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}

	displays.targets = nil
	if err := model.Refresh(); err != nil {
		t.Fatalf("Refresh missing monitor: %v", err)
	}
	if ready, _ := model.SaveReady(); ready {
		t.Fatal("SaveReady() = true after selected monitor disappeared")
	}

	displays.targets = []domain.Target{target}
	displays.modes[target.DeviceName] = nil
	if err := model.Refresh(); err != nil {
		t.Fatalf("Refresh unusable monitor: %v", err)
	}
	if ready, _ := model.SaveReady(); ready {
		t.Fatal("SaveReady() = true with no usable selected mode")
	}
	if row := model.MonitorRows()[0]; row.Reason == "" {
		t.Fatal("unusable monitor lost its explanation")
	}
}

func refreshedSettingsModel(t *testing.T, displays *fakeSettingsDisplay) *settingsModel {
	t.Helper()
	return refreshedSettingsModelWithProfile(t, displays, domain.Profile{})
}

func refreshedSettingsModelWithProfile(t *testing.T, displays *fakeSettingsDisplay, profile domain.Profile) *settingsModel {
	t.Helper()
	if len(displays.layout.Displays) == 0 {
		for index, target := range displays.targets {
			modes := displays.modes[target.DeviceName]
			current := domain.Mode{}
			if len(modes) > 0 {
				current = modes[0]
			}
			displays.layout.Displays = append(displays.layout.Displays, domain.DisplayState{
				DeviceName: target.DeviceName, Mode: current, Primary: index == 0,
			})
		}
	}
	model := newSettingsModel(displays, &fakeSettingsLister{}, profile, app.ScalingSnapshot{}, true)
	if err := model.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return model
}

func refreshedSettingsModelWithLister(t *testing.T, displays *fakeSettingsDisplay, lister *fakeSettingsLister) *settingsModel {
	t.Helper()
	model := newSettingsModel(displays, lister, domain.Profile{}, app.ScalingSnapshot{}, true)
	if err := model.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return model
}

func settingsTarget(deviceName, instancePath, hardwareID, label string) domain.Target {
	return domain.Target{DeviceName: deviceName, Identity: domain.MonitorIdentity{
		InstancePath: instancePath, HardwareID: hardwareID, Label: label,
	}}
}

type fakeSettingsDisplay struct {
	targets    []domain.Target
	modes      map[string][]domain.Mode
	current    map[string]domain.Mode
	layout     domain.Layout
	modeCalls  int
	targetsErr error
}

func (d *fakeSettingsDisplay) Targets() ([]domain.Target, error) { return d.targets, d.targetsErr }
func (d *fakeSettingsDisplay) ResolveTarget(domain.MonitorIdentity) (domain.Target, error) {
	return domain.Target{}, errors.New("not used")
}
func (d *fakeSettingsDisplay) CurrentMode(target domain.Target) (domain.Mode, error) {
	return d.current[target.DeviceName], nil
}
func (d *fakeSettingsDisplay) EnumModes(target domain.Target) ([]domain.Mode, error) {
	d.modeCalls++
	return d.modes[target.DeviceName], nil
}
func (d *fakeSettingsDisplay) CurrentLayout() (domain.Layout, error) { return d.layout, nil }
func (d *fakeSettingsDisplay) TestMode(domain.Target, domain.Mode) error {
	return errors.New("not used")
}
func (d *fakeSettingsDisplay) ApplyLayout(domain.LayoutPlan) error { return errors.New("not used") }

type fakeSettingsLister struct {
	names []string
	calls int
}

func (l *fakeSettingsLister) Names() ([]string, error) {
	l.calls++
	return append([]string(nil), l.names...), nil
}
