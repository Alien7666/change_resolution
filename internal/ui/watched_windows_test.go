package ui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/config"
)

// The button exists because the settings dialog cannot be pressed at the moment the
// question is asked. A button that went off for the same reason settings does would be
// a button that does not exist.
func TestTheWatchedProcessButtonStaysLiveWhileAModeIsApplied(t *testing.T) {
	w := &window{configState: configStateConfigured}
	snapshot := app.Snapshot{Managed: true, AtGameMode: true, State: app.StateWaitingForGame}

	got := w.availableControls(snapshot)
	if !got.watched {
		t.Fatal("an applied mode disabled the button for changing the watched process")
	}
	if got.settings {
		t.Fatal("fixture no longer models the settings button being disabled while managed")
	}
	if reason := watchedButtonReason(snapshot); reason != "" {
		t.Fatalf("a live button carries the reason %q", reason)
	}
}

// The one refusal that survives. RecoveryPending means the tool cannot describe the
// desktop, and nothing may be reconfigured against a desktop in that state.
func TestTheWatchedProcessButtonIsOffWhileRecoveryIsPending(t *testing.T) {
	w := &window{configState: configStateConfigured}
	snapshot := app.Snapshot{Managed: true, RecoveryPending: true}

	if w.availableControls(snapshot).watched {
		t.Fatal("a pending display recovery left the button live")
	}
	if reason := watchedButtonReason(snapshot); !strings.Contains(reason, "恢復") {
		t.Fatalf("reason = %q, which never names what has to happen first", reason)
	}
}

// Two controls can disagree, and the checkbox wins. A user who ticked "watch nothing"
// has said so in the plainest way the dialog offers; a name left behind it in the edit
// box is a leftover, not a second opinion.
func TestTheManualOnlyCheckboxOutranksALeftoverName(t *testing.T) {
	name, err := watchedResult("SomethingElse.exe", true)
	if err != nil {
		t.Fatal(err)
	}
	if name != "" {
		t.Fatalf("result = %q, want the manual-only choice honoured", name)
	}
}

// Pressing 套用 with nothing chosen is the case the settings dialog used to answer
// with a silently disabled button. Here it answers with the sentence that unblocks it.
func TestSavingWithNoChoiceSaysWhatToDo(t *testing.T) {
	_, err := watchedResult("   ", false)
	if err == nil {
		t.Fatal("an empty choice was accepted as a process name")
	}
	if !strings.Contains(err.Error(), watchedManualText) {
		t.Fatalf("error = %v, which never names the checkbox that would unblock it", err)
	}
}

// The same rule the config file is held to, applied before a live session hears the
// name. Toolhelp reports bare image names, so a path would look configured and then
// never match.
func TestAPathShapedNameIsRefusedBeforeItCanBeApplied(t *testing.T) {
	_, err := watchedResult(`C:\Games\Other\Game.exe`, false)
	if !errors.Is(err, config.ErrOutOfRange) {
		t.Fatalf("error = %v, want config.ErrOutOfRange", err)
	}
}

func TestAChosenNameIsTrimmedAndKept(t *testing.T) {
	name, err := watchedResult("  OtherGame.exe  ", false)
	if err != nil {
		t.Fatal(err)
	}
	if name != "OtherGame.exe" {
		t.Fatalf("result = %q", name)
	}
}

// The search is a substring match, case-folded, over the names that were read. An
// empty query is every name rather than none.
func TestTheWatchedProcessSearchFiltersWithoutCase(t *testing.T) {
	list := watchedList{names: []string{"VALORANT-Win64-Shipping.exe", "chrome.exe", "Steam.exe"}}

	if got := list.matching("  "); !reflect.DeepEqual(got, list.names) {
		t.Fatalf("an empty query returned %v", got)
	}
	if got := list.matching("EXE"); len(got) != 3 {
		t.Fatalf("a case-folded query returned %v", got)
	}
	if got := list.matching("valor"); !reflect.DeepEqual(got, []string{"VALORANT-Win64-Shipping.exe"}) {
		t.Fatalf("query result = %v", got)
	}
	if got := list.matching("nothing-matches-this"); len(got) != 0 {
		t.Fatalf("query result = %v, want nothing", got)
	}
}

// A process list that could not be read is not a refusal: the dialog says so and still
// takes a typed name, which is the only thing that works when Toolhelp will not answer.
func TestAnUnreadableProcessListStillExplainsItself(t *testing.T) {
	list := watchedList{err: errors.New("Toolhelp refused")}

	tip := processTipText("", len(list.matching("")), len(list.names), list.err)
	if !strings.Contains(tip, "Toolhelp refused") || !strings.Contains(tip, "手動輸入") {
		t.Fatalf("tip = %q, want the cause and the way round it", tip)
	}
	if name, err := watchedResult("OtherGame.exe", false); err != nil || name != "OtherGame.exe" {
		t.Fatalf("a typed name was refused when the list was unreadable: %q %v", name, err)
	}
}
