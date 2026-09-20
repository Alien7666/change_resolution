//go:build windows

package ui

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Alien7666/change_resolution/internal/config"
	"github.com/Alien7666/change_resolution/internal/domain"
)

func dialogFlowFixture(t *testing.T) (*settingsModel, domain.Target, domain.Mode) {
	t.Helper()
	target := settingsTarget(`\\.\DISPLAY1`, `\\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")
	mode := domain.Mode{Width: 1920, Height: 1080, RefreshHz: 144, BitsPerPixel: 32}
	model := refreshedSettingsModel(t, &fakeSettingsDisplay{
		targets: []domain.Target{target},
		modes:   map[string][]domain.Mode{target.DeviceName: {mode}},
	})
	return model, target, mode
}

func TestSettingsSectionsUnlockOnlyAfterThePreviousChoiceIsValid(t *testing.T) {
	model, _, mode := dialogFlowFixture(t)
	flow := newSettingsDialogFlow(model, func(domain.Profile) error { return nil })

	if gates := flow.Gates(); gates.Mode || gates.Process || gates.Save {
		t.Fatalf("blank wizard gates = %+v", gates)
	}
	if err := flow.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if gates := flow.Gates(); !gates.Mode || gates.Process || gates.Save {
		t.Fatalf("monitor-only gates = %+v", gates)
	}
	if err := flow.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}
	if gates := flow.Gates(); !gates.Mode || !gates.Process || gates.Save {
		t.Fatalf("mode-selected gates = %+v", gates)
	}
	flow.UseManualOnly()
	if gates := flow.Gates(); !gates.Save {
		t.Fatalf("explicit manual-only choice did not unlock save: %+v", gates)
	}
}

func TestDialogSaveDelegatesOneValidatedProfileToAtomicSaveAndReplace(t *testing.T) {
	model, _, mode := dialogFlowFixture(t)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}
	model.save = func(config.File) error {
		t.Fatal("dialog bypassed Provider.SaveAndReplace and wrote before the ownership guard")
		return nil
	}
	replaces := 0
	flow := newSettingsDialogFlow(model, func(profile domain.Profile) error {
		replaces++
		if profile.GameMode != mode {
			t.Fatalf("replacement mode = %#v", profile.GameMode)
		}
		return nil
	})
	flow.UseManualOnly()

	if err := flow.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if replaces != 1 || !flow.Accepted() {
		t.Fatalf("save-and-replace calls=%d accepted=%v", replaces, flow.Accepted())
	}
}

func TestDialogAtomicSaveAndReplaceFailureKeepsTheDraft(t *testing.T) {
	model, target, mode := dialogFlowFixture(t)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}
	wantErr := errors.New("atomic rename failed")
	model.save = func(config.File) error {
		t.Fatal("dialog wrote outside the atomic callback")
		return nil
	}
	attempts := 0
	flow := newSettingsDialogFlow(model, func(domain.Profile) error {
		attempts++
		return wantErr
	})
	flow.SetProcessName("game.exe")

	if err := flow.Save(); !errors.Is(err, wantErr) {
		t.Fatalf("Save error = %v", err)
	}
	if attempts != 1 || flow.Accepted() {
		t.Fatalf("failed atomic call attempts=%d accepted=%v", attempts, flow.Accepted())
	}
	draft := model.Draft()
	if draft.Monitor.InstancePath != target.Identity.InstancePath || draft.GameMode != mode || draft.ProcessName != "game.exe" {
		t.Fatalf("failed save discarded draft: %#v", draft)
	}
}

func TestDialogReplaceFailureKeepsTheSavedDraftOpen(t *testing.T) {
	model, _, mode := dialogFlowFixture(t)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}
	saves := 0
	model.save = func(config.File) error { saves++; return nil }
	wantErr := errors.New("session became managed")
	flow := newSettingsDialogFlow(model, func(domain.Profile) error { return wantErr })
	flow.UseManualOnly()

	if err := flow.Save(); !errors.Is(err, wantErr) {
		t.Fatalf("Save error = %v", err)
	}
	if saves != 0 || flow.Accepted() || model.Draft().GameMode != mode {
		t.Fatalf("refused atomic save wrote=%d accepted=%v draft=%#v", saves, flow.Accepted(), model.Draft())
	}
}

func TestCancelNeverSavesOrReplaces(t *testing.T) {
	model, _, mode := dialogFlowFixture(t)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}
	saves, replaces := 0, 0
	model.save = func(config.File) error { saves++; return nil }
	flow := newSettingsDialogFlow(model, func(domain.Profile) error { replaces++; return nil })
	flow.UseManualOnly()

	flow.Cancel()
	if saves != 0 || replaces != 0 || flow.Accepted() {
		t.Fatalf("cancel saved=%d replaced=%d accepted=%v", saves, replaces, flow.Accepted())
	}
}

func TestControlRebuildEventsCannotEraseTheDraft(t *testing.T) {
	model, target, mode := dialogFlowFixture(t)
	flow := newSettingsDialogFlow(model, func(domain.Profile) error { return nil })
	if err := flow.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := flow.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}
	flow.SetProcessName("game.exe")

	flow.withRebuild(func() {
		flow.SetProcessName("")
		flow.UseManualOnly()
		_ = flow.SelectMonitor(-1)
	})
	draft := model.Draft()
	if draft.Monitor.InstancePath != target.Identity.InstancePath || draft.GameMode != mode || draft.ProcessName != "game.exe" {
		t.Fatalf("rebuild events changed draft: %#v", draft)
	}
}

// A control the user cannot use and cannot find out why is a dead end. The picker
// can be entirely satisfied while the process choice still holds the save back, and
// that case used to disable the button and say nothing, because the only copy of the
// sentence lived in the handler the disabled button would have called.
func TestBlockedSaveAlwaysCarriesTheSentenceThatUnblocksIt(t *testing.T) {
	model, _, mode := dialogFlowFixture(t)
	flow := newSettingsDialogFlow(model, func(domain.Profile) error { return nil })

	if err := flow.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := flow.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}

	gates := flow.Gates()
	if gates.Save {
		t.Fatalf("save unlocked before any process choice: %+v", gates)
	}
	if gates.Reason != missingProcessChoice {
		t.Fatalf("reason = %q, want %q", gates.Reason, missingProcessChoice)
	}
}

func TestChoosingAProcessClearsTheReasonAndUnlocksSave(t *testing.T) {
	model, _, mode := dialogFlowFixture(t)
	flow := newSettingsDialogFlow(model, func(domain.Profile) error { return nil })
	if err := flow.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := flow.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}

	flow.SetProcessName("notepad++.exe")

	gates := flow.Gates()
	if !gates.Save {
		t.Fatalf("a chosen process did not unlock save: %+v", gates)
	}
	if gates.Reason != "" {
		t.Fatalf("reason = %q, want it cleared once the gate it described is open", gates.Reason)
	}
	if got := model.Draft().ProcessName; got != "notepad++.exe" {
		t.Fatalf("draft process = %q, want the name that was chosen", got)
	}
}

func TestManualOnlyUnlocksSaveAndClearsTheReason(t *testing.T) {
	model, _, mode := dialogFlowFixture(t)
	flow := newSettingsDialogFlow(model, func(domain.Profile) error { return nil })
	if err := flow.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := flow.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}

	flow.UseManualOnly()

	gates := flow.Gates()
	if !gates.Save || gates.Reason != "" {
		t.Fatalf("manual-only gates = %+v, want save with no reason", gates)
	}
	if got := model.Draft().ProcessName; got != "" {
		t.Fatalf("manual-only draft process = %q, want empty", got)
	}
}

// The search box narrows the list of running processes. An empty query is not a
// filter that matches nothing; it is the unfiltered list.
func TestProcessSearchFiltersAndAnEmptyQueryReturnsEverything(t *testing.T) {
	displays := &fakeSettingsDisplay{
		targets: []domain.Target{settingsTarget(`\.\DISPLAY1`, `\?\DISPLAY#ONE#1`, `MONITOR\ONE`, "One")},
		modes: map[string][]domain.Mode{
			`\.\DISPLAY1`: {{Width: 1920, Height: 1080, RefreshHz: 144, BitsPerPixel: 32}},
		},
	}
	lister := &fakeSettingsLister{names: []string{
		"notepad++.exe", "Notepad.exe", "VALORANT-Win64-Shipping.exe", "explorer.exe",
	}}
	model := refreshedSettingsModelWithLister(t, displays, lister)

	if got := len(model.ProcessNames("")); got != 4 {
		t.Fatalf("empty query returned %d names, want all 4", got)
	}
	got := model.ProcessNames("notepad")
	want := []string{"notepad++.exe", "Notepad.exe"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProcessNames(\"notepad\") = %v, want %v (case-insensitive)", got, want)
	}
	if got := model.ProcessNames("valorant"); len(got) != 1 {
		t.Fatalf("ProcessNames(\"valorant\") = %v, want exactly the one match", got)
	}
	if got := model.ProcessNames("nothing-runs-by-this-name"); len(got) != 0 {
		t.Fatalf("a query matching nothing returned %v", got)
	}
}

// A disabled section swallows every click and keystroke without a word, which is how
// an unfinished step above reads as a broken list below. Each locked section names
// the step that unlocks it.
func TestALockedSectionSaysWhichStepUnlocksIt(t *testing.T) {
	tests := map[string]struct {
		gates settingsGates
		want  string
	}{
		"nothing chosen yet":  {gates: settingsGates{}, want: "請先在上方選擇一台顯示器"},
		"monitor but no mode": {gates: settingsGates{Mode: true}, want: "請先在上方選擇顯示模式"},
		"mode chosen":         {gates: settingsGates{Mode: true, Process: true}, want: ""},
		"ready to save":       {gates: settingsGates{Mode: true, Process: true, Save: true}, want: ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := lockedSectionTip(tt.gates); got != tt.want {
				t.Fatalf("lockedSectionTip(%+v) = %q, want %q", tt.gates, got, tt.want)
			}
		})
	}
}

// The status line has to follow the gate. A sentence that outlives the condition it
// described sends the user to fix something that is already fixed.
func TestGateReasonsAreRecognisedSoAStaleOneCanBeCleared(t *testing.T) {
	if isGateReason("") {
		t.Fatal("an empty status was taken for a gate reason")
	}
	if !isGateReason(missingProcessChoice) {
		t.Fatalf("%q was not recognised as a gate reason", missingProcessChoice)
	}
	if isGateReason("寫入設定檔失敗：磁碟已滿") {
		t.Fatal("a message from elsewhere was taken for a gate reason and would be cleared")
	}
	for _, reason := range gateReasons {
		if !isGateReason(reason) {
			t.Fatalf("gateReasons lists %q but isGateReason does not recognise it", reason)
		}
	}
}
