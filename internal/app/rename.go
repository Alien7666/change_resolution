package app

import (
	"fmt"

	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
)

// remapSavedNames rewrites a saved arrangement onto the device names the desktop uses
// now. A saved layout records a coordinate per \\.\DISPLAYn, but DISPLAYn is not a
// monitor: an NVAPI write, a driver restart or a moved cable can hand the same screen
// a different number at any time. savedBindings is what keeps the arrangement
// recoverable across that -- it pairs every saved name with the physical monitor that
// owned it, and captureDisplayBindings has already proved that pairing is one-to-one.
//
// Refusing instead of remapping is what left a renumbered desktop with no way back.
// The restore, the mode toggle and the exit all ran the same saved-name comparison, so
// a session whose target had been renumbered could neither put the screens back nor be
// closed, and the window stayed up refusing every button on it.
//
// The refusals that remain are the ones no identity can answer: a saved monitor that
// is no longer attached, a desktop that gained or lost a screen, and two names
// reporting one monitor. Each describes an arrangement that cannot be rebuilt rather
// than one that merely moved.
//
// saved and savedBindings are read, never written. A restore has to stay retryable
// after a failure further down, so this returns new values instead of editing the
// session's own state in place.
func remapSavedNames(
	saved domain.Layout,
	savedBindings map[string]displayBinding,
	current map[string]displayBinding,
) (domain.Layout, map[string]displayBinding, map[string]string, error) {
	// captureDisplayBindings already refuses to build a map where two names share a
	// monitor, so no caller can reach this branch today. It stays because the
	// alternative to checking is a silent wrong answer: without it one of the two
	// names would quietly win and a display would be moved on another's coordinates.
	nameOf := make(map[displayBinding]string, len(current))
	for deviceName, binding := range current {
		if owner, duplicate := nameOf[binding]; duplicate {
			return domain.Layout{}, nil, nil, fmt.Errorf(
				"%w: %s and %s report the same physical monitor",
				display.ErrLayoutUnsafe, owner, deviceName)
		}
		nameOf[binding] = deviceName
	}

	renamed := make(map[string]string, len(savedBindings))
	for deviceName, binding := range savedBindings {
		currentName, attached := nameOf[binding]
		if !attached {
			return domain.Layout{}, nil, nil, fmt.Errorf(
				"%w: the monitor saved as %s is no longer attached",
				display.ErrLayoutUnsafe, deviceName)
		}
		renamed[deviceName] = currentName
	}
	// Checked after the per-monitor lookup above so that a desktop which lost a screen
	// is reported by naming that screen, which the user can act on, rather than by a
	// count they would still have to work back from.
	if len(savedBindings) != len(current) {
		return domain.Layout{}, nil, nil, fmt.Errorf(
			"%w: the desktop had %d monitors when the arrangement was saved and has %d now",
			display.ErrLayoutUnsafe, len(savedBindings), len(current))
	}

	displays := make([]domain.DisplayState, 0, len(saved.Displays))
	for _, state := range saved.Displays {
		currentName, known := renamed[state.DeviceName]
		if !known {
			return domain.Layout{}, nil, nil, fmt.Errorf(
				"%w: the saved arrangement's %s has no captured monitor identity",
				display.ErrLayoutUnsafe, state.DeviceName)
		}
		state.DeviceName = currentName
		displays = append(displays, state)
	}

	bindings := make(map[string]displayBinding, len(savedBindings))
	for deviceName, binding := range savedBindings {
		bindings[renamed[deviceName]] = binding
	}
	return domain.Layout{Displays: displays}, bindings, renamed, nil
}
