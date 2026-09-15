//go:build windows

package ui

import (
	"errors"
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

func TestDialogSaveCallsReplaceOnlyAfterOneSuccessfulModelSave(t *testing.T) {
	model, _, mode := dialogFlowFixture(t)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}
	saves := 0
	model.save = func(file config.File) error {
		saves++
		if file.Profile().GameMode != mode {
			t.Fatalf("saved mode = %#v", file.Profile().GameMode)
		}
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
	if saves != 1 || replaces != 1 || !flow.Accepted() {
		t.Fatalf("saves=%d replaces=%d accepted=%v", saves, replaces, flow.Accepted())
	}
}

func TestDialogSaveFailureKeepsTheDraftAndNeverCallsReplace(t *testing.T) {
	model, target, mode := dialogFlowFixture(t)
	if err := model.SelectMonitor(0); err != nil {
		t.Fatalf("SelectMonitor: %v", err)
	}
	if err := model.SelectMode(mode); err != nil {
		t.Fatalf("SelectMode: %v", err)
	}
	wantErr := errors.New("atomic rename failed")
	model.save = func(config.File) error { return wantErr }
	replaces := 0
	flow := newSettingsDialogFlow(model, func(domain.Profile) error {
		replaces++
		return nil
	})
	flow.SetProcessName("game.exe")

	if err := flow.Save(); !errors.Is(err, wantErr) {
		t.Fatalf("Save error = %v", err)
	}
	if replaces != 0 || flow.Accepted() {
		t.Fatalf("failed save replaced=%d accepted=%v", replaces, flow.Accepted())
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
	model.save = func(config.File) error { return nil }
	wantErr := errors.New("session became managed")
	flow := newSettingsDialogFlow(model, func(domain.Profile) error { return wantErr })
	flow.UseManualOnly()

	if err := flow.Save(); !errors.Is(err, wantErr) {
		t.Fatalf("Save error = %v", err)
	}
	if flow.Accepted() || model.Draft().GameMode != mode {
		t.Fatal("replace failure accepted the dialog or discarded its draft")
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
