package app

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
)

func instance(path string) displayBinding {
	return displayBinding{kind: bindingInstancePath, value: path}
}

// The case that left a desktop with no way back. An NVAPI write handed the configured
// monitor a different DISPLAYn while the session was managing its mode, and every
// route out -- the toggle, the restore button and the exit -- compared the saved name
// against the new one and refused. The arrangement was never unrecoverable: the saved
// identities say which screen each coordinate belongs to.
func TestRemapSavedNamesFollowsAMonitorThatWasRenumbered(t *testing.T) {
	saved := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY8`, Mode: fixtureTop, Position: domain.Point{X: 2556, Y: -1080}},
		{DeviceName: `\.\DISPLAY1`, Mode: fixtureNative, Primary: true},
	}}
	savedBindings := map[string]displayBinding{
		`\.\DISPLAY8`: instance("mi-2461w"),
		`\.\DISPLAY1`: instance("mi-monitor"),
	}
	current := map[string]displayBinding{
		`\.\DISPLAY5`: instance("mi-2461w"),
		`\.\DISPLAY1`: instance("mi-monitor"),
	}

	layout, bindings, renamed, err := remapSavedNames(saved, savedBindings, current)
	if err != nil {
		t.Fatalf("a renumbered monitor was refused: %v", err)
	}

	wantLayout := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY5`, Mode: fixtureTop, Position: domain.Point{X: 2556, Y: -1080}},
		{DeviceName: `\.\DISPLAY1`, Mode: fixtureNative, Primary: true},
	}}
	if !reflect.DeepEqual(layout, wantLayout) {
		t.Fatalf("remapped layout = %+v, want %+v", layout, wantLayout)
	}
	if !reflect.DeepEqual(bindings, current) {
		t.Fatalf("remapped bindings = %+v, want %+v", bindings, current)
	}
	if renamed[`\.\DISPLAY8`] != `\.\DISPLAY5` {
		t.Fatalf("renamed[DISPLAY8] = %q, want DISPLAY5", renamed[`\.\DISPLAY8`])
	}
	if renamed[`\.\DISPLAY1`] != `\.\DISPLAY1` {
		t.Fatalf("a monitor that kept its name was renamed to %q", renamed[`\.\DISPLAY1`])
	}
}

// The saved layout is the session's own state and a failed restore has to be
// retryable, so remapping reads it and returns a new one rather than editing it.
func TestRemapSavedNamesLeavesTheLayoutItWasGivenAlone(t *testing.T) {
	saved := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY8`, Mode: fixtureTop},
	}}
	savedBindings := map[string]displayBinding{`\.\DISPLAY8`: instance("mi-2461w")}
	current := map[string]displayBinding{`\.\DISPLAY5`: instance("mi-2461w")}

	if _, _, _, err := remapSavedNames(saved, savedBindings, current); err != nil {
		t.Fatal(err)
	}
	if saved.Displays[0].DeviceName != `\.\DISPLAY8` {
		t.Fatalf("remapping rewrote the caller's layout to %q", saved.Displays[0].DeviceName)
	}
	if savedBindings[`\.\DISPLAY8`] != instance("mi-2461w") {
		t.Fatal("remapping rewrote the caller's bindings")
	}
}

// Two screens trading numbers is the same problem as one screen changing its own:
// each coordinate still names a physical monitor that is still attached, so the
// arrangement can be rebuilt exactly.
func TestRemapSavedNamesFollowsTwoMonitorsThatExchangedNames(t *testing.T) {
	saved := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY2`, Mode: fixtureSide, Position: domain.Point{X: 1920}},
		{DeviceName: `\.\DISPLAY5`, Mode: fixtureTop, Position: domain.Point{X: 1922, Y: -1080}},
	}}
	savedBindings := map[string]displayBinding{
		`\.\DISPLAY2`: instance("right"),
		`\.\DISPLAY5`: instance("top"),
	}
	current := map[string]displayBinding{
		`\.\DISPLAY2`: instance("top"),
		`\.\DISPLAY5`: instance("right"),
	}

	layout, _, renamed, err := remapSavedNames(saved, savedBindings, current)
	if err != nil {
		t.Fatalf("an exchanged pair was refused: %v", err)
	}
	want := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY5`, Mode: fixtureSide, Position: domain.Point{X: 1920}},
		{DeviceName: `\.\DISPLAY2`, Mode: fixtureTop, Position: domain.Point{X: 1922, Y: -1080}},
	}}
	if !reflect.DeepEqual(layout, want) {
		t.Fatalf("remapped layout = %+v, want %+v", layout, want)
	}
	if renamed[`\.\DISPLAY2`] != `\.\DISPLAY5` || renamed[`\.\DISPLAY5`] != `\.\DISPLAY2` {
		t.Fatalf("renamed = %+v, want the pair exchanged", renamed)
	}
}

// Every refusal this function is still allowed to make. A coordinate saved for a
// screen that is not there describes an arrangement that cannot be rebuilt, and no
// identity can be guessed from a name.
func TestRemapSavedNamesRefusesADesktopItCannotRebuild(t *testing.T) {
	saved := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY8`, Mode: fixtureTop},
		{DeviceName: `\.\DISPLAY1`, Mode: fixtureNative, Primary: true},
	}}
	savedBindings := map[string]displayBinding{
		`\.\DISPLAY8`: instance("mi-2461w"),
		`\.\DISPLAY1`: instance("mi-monitor"),
	}

	tests := map[string]map[string]displayBinding{
		"a saved monitor was unplugged": {
			`\.\DISPLAY1`: instance("mi-monitor"),
		},
		"a monitor was added": {
			`\.\DISPLAY8`: instance("mi-2461w"),
			`\.\DISPLAY1`: instance("mi-monitor"),
			`\.\DISPLAY9`: instance("newcomer"),
		},
		"a saved monitor was replaced by another": {
			`\.\DISPLAY5`: instance("someone-else"),
			`\.\DISPLAY1`: instance("mi-monitor"),
		},
		// Two names reporting one monitor necessarily means a saved monitor is absent
		// once the counts agree, so this lands on the missing-monitor refusal rather
		// than on the duplicate check. The duplicate check is an invariant guard that
		// captureDisplayBindings makes unreachable; nothing here can drive it.
		"a monitor answers to a name another one already used": {
			`\.\DISPLAY5`: instance("mi-monitor"),
			`\.\DISPLAY1`: instance("mi-monitor"),
		},
	}

	for name, current := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, _, err := remapSavedNames(saved, savedBindings, current)
			if !errors.Is(err, display.ErrLayoutUnsafe) {
				t.Fatalf("error = %v, want display.ErrLayoutUnsafe", err)
			}
		})
	}
}

// A saved layout carrying a display the bindings never captured cannot be remapped.
// captureDisplayBindings rejects that pairing when the layout is saved, so reaching it
// here means the two halves of the session's own state have drifted apart.
func TestRemapSavedNamesRefusesALayoutItHasNoIdentityFor(t *testing.T) {
	saved := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY8`, Mode: fixtureTop},
		{DeviceName: `\.\DISPLAY1`, Mode: fixtureNative, Primary: true},
	}}
	savedBindings := map[string]displayBinding{`\.\DISPLAY8`: instance("mi-2461w")}
	current := map[string]displayBinding{`\.\DISPLAY5`: instance("mi-2461w")}

	_, _, _, err := remapSavedNames(saved, savedBindings, current)
	if !errors.Is(err, display.ErrLayoutUnsafe) {
		t.Fatalf("error = %v, want display.ErrLayoutUnsafe", err)
	}
}
