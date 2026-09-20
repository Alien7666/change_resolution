package display

import (
	"errors"
	"fmt"

	"github.com/Alien7666/change_resolution/internal/domain"
)

// ErrLayoutUnsafe reports that no desktop arrangement could be derived that is safe
// to apply. Every plan is computed in full and validated before a single Win32 call
// is made, so a session that sees this sentinel has changed nothing at all.
var ErrLayoutUnsafe = errors.New("display layout is not safe to apply")

const (
	// maxDimension and maxCoordinate are sanity bounds, far beyond any real desktop.
	// Rejecting anything outside them keeps every later shift inside int32 and turns
	// a display the tool failed to read (a zero-sized mode) into a refusal to act.
	//
	// The dimension bound is domain.MaxDimension rather than a number of this
	// package's own: internal/config has to refuse exactly the same modes when it
	// validates the user's file, and it cannot import internal/display to find out
	// where the line is.
	maxDimension  = domain.MaxDimension
	maxCoordinate = 1 << 24
)

// PlanModeChange derives the arrangement that puts targetDevice into mode while
// keeping the desktop contiguous.
//
// Changing the target's width by delta would otherwise leave a delta-wide dead zone
// the mouse cannot cross, so the displays beyond the edge that moved slide by that
// delta, and the same rule applies downwards for a height change. Which displays
// those are is decided by slidesHorizontally and slidesVertically, not by a larger
// coordinate alone.
// Windows keeps the primary display at the origin, so the whole arrangement is then
// translated to put the primary back at (0,0); when the target is itself the
// primary, that translation is empty and the target never moves.
//
// Only the target's mode is written. Every other display appears in the plan with a
// position and nothing else.
func PlanModeChange(layout domain.Layout, targetDevice string, mode domain.Mode) (domain.LayoutPlan, error) {
	if err := validateLayout(layout); err != nil {
		return domain.LayoutPlan{}, err
	}
	if err := validateMode(mode); err != nil {
		return domain.LayoutPlan{}, fmt.Errorf("requested mode: %w", err)
	}
	target, ok := layout.Find(targetDevice)
	if !ok {
		return domain.LayoutPlan{}, fmt.Errorf("%w: %s is not attached", ErrLayoutUnsafe, targetDevice)
	}

	deltaX := int32(target.Mode.Width) - int32(mode.Width)
	deltaY := int32(target.Mode.Height) - int32(mode.Height)
	arranged := make([]domain.DisplayState, len(layout.Displays))
	for i, display := range layout.Displays {
		next := display
		if display.DeviceName == targetDevice {
			next.Mode = mode
		}
		if slidesHorizontally(display, target) {
			next.Position.X = display.Position.X - deltaX
		}
		if slidesVertically(display, target) {
			next.Position.Y = display.Position.Y - deltaY
		}
		arranged[i] = next
	}

	arranged = anchorPrimary(arranged)
	if err := validateArrangement(arranged); err != nil {
		return domain.LayoutPlan{}, err
	}
	return planFor(arranged, targetDevice), nil
}

// PlanRestore derives the arrangement that puts the desktop back to the saved one.
// Only the target's mode is restored: every other display is moved back to its
// saved coordinate and keeps whatever mode it is running now, because the tool
// never owned those modes and must not revert a change the user made while the game
// mode was active.
//
// Safety is proved against current, not only saved. A neighbour may have changed
// resolution while the target was managed, and applying its old coordinate while
// preserving its new size can otherwise overlap another display. The device set and
// primary role must still be the saved topology too; this planner never guesses how
// a changed desktop should be restored.
func PlanRestore(saved, current domain.Layout, targetDevice string) (domain.LayoutPlan, error) {
	if err := validateLayout(saved); err != nil {
		return domain.LayoutPlan{}, err
	}
	if err := validateLayout(current); err != nil {
		return domain.LayoutPlan{}, err
	}
	if _, ok := saved.Find(targetDevice); !ok {
		return domain.LayoutPlan{}, fmt.Errorf(
			"%w: the saved layout does not contain %s", ErrLayoutUnsafe, targetDevice)
	}
	if len(saved.Displays) != len(current.Displays) {
		return domain.LayoutPlan{}, fmt.Errorf(
			"%w: the attached display set changed from %d to %d displays",
			ErrLayoutUnsafe, len(saved.Displays), len(current.Displays))
	}

	arranged := make([]domain.DisplayState, 0, len(saved.Displays))
	for _, previous := range saved.Displays {
		live, ok := current.Find(previous.DeviceName)
		if !ok {
			return domain.LayoutPlan{}, fmt.Errorf(
				"%w: the saved layout's %s is no longer attached", ErrLayoutUnsafe, previous.DeviceName)
		}
		if live.Primary != previous.Primary {
			return domain.LayoutPlan{}, fmt.Errorf(
				"%w: the primary display changed while the layout was managed", ErrLayoutUnsafe)
		}
		live.Position = previous.Position
		if previous.DeviceName == targetDevice {
			live.Mode = previous.Mode
		}
		arranged = append(arranged, live)
	}
	if err := validateArrangement(arranged); err != nil {
		return domain.LayoutPlan{}, err
	}
	return planFor(arranged, targetDevice), nil
}

// anchorPrimary translates the whole arrangement so the primary display sits at the
// origin, which Windows requires. validateLayout has already established that
// exactly one display is primary.
func anchorPrimary(states []domain.DisplayState) []domain.DisplayState {
	var origin domain.Point
	for _, state := range states {
		if state.Primary {
			origin = state.Position
			break
		}
	}
	if origin == (domain.Point{}) {
		return states
	}
	anchored := make([]domain.DisplayState, len(states))
	for i, state := range states {
		state.Position.X -= origin.X
		state.Position.Y -= origin.Y
		anchored[i] = state
	}
	return anchored
}

func planFor(states []domain.DisplayState, targetDevice string) domain.LayoutPlan {
	changes := make([]domain.LayoutChange, len(states))
	for i, state := range states {
		change := domain.LayoutChange{DeviceName: state.DeviceName, Position: state.Position}
		if state.DeviceName == targetDevice {
			change.Mode, change.SetMode = state.Mode, true
		}
		changes[i] = change
	}
	return domain.LayoutPlan{Changes: changes}
}

// validateLayout rejects anything the tool cannot reason about: a layout it failed
// to read, a device reported twice, a mode that makes no sense, or a desktop
// without exactly one primary display.
func validateLayout(layout domain.Layout) error {
	if len(layout.Displays) == 0 {
		return fmt.Errorf("%w: no attached display was read", ErrLayoutUnsafe)
	}
	seen := make(map[string]struct{}, len(layout.Displays))
	primaries := 0
	for _, display := range layout.Displays {
		if display.DeviceName == "" {
			return fmt.Errorf("%w: a display reported no device name", ErrLayoutUnsafe)
		}
		if _, duplicate := seen[display.DeviceName]; duplicate {
			return fmt.Errorf("%w: %s was read twice", ErrLayoutUnsafe, display.DeviceName)
		}
		seen[display.DeviceName] = struct{}{}
		if err := validateMode(display.Mode); err != nil {
			return fmt.Errorf("%s: %w", display.DeviceName, err)
		}
		if outOfRange(display.Position.X) || outOfRange(display.Position.Y) {
			return fmt.Errorf("%w: %s reported position (%d,%d)",
				ErrLayoutUnsafe, display.DeviceName, display.Position.X, display.Position.Y)
		}
		if display.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		return fmt.Errorf("%w: read %d primary displays, want exactly one", ErrLayoutUnsafe, primaries)
	}
	return nil
}

func validateMode(mode domain.Mode) error {
	if mode.Width == 0 || mode.Height == 0 || mode.Width > maxDimension || mode.Height > maxDimension {
		return fmt.Errorf("%w: unusable resolution %dx%d", ErrLayoutUnsafe, mode.Width, mode.Height)
	}
	return nil
}

// validateArrangement is the last gate before anything is applied: the primary must
// end up at the origin and no two displays may overlap.
func validateArrangement(states []domain.DisplayState) error {
	for _, state := range states {
		if state.Primary && state.Position != (domain.Point{}) {
			return fmt.Errorf("%w: the primary display %s would move to (%d,%d)",
				ErrLayoutUnsafe, state.DeviceName, state.Position.X, state.Position.Y)
		}
	}
	for i := range states {
		for j := i + 1; j < len(states); j++ {
			if overlaps(states[i], states[j]) {
				return fmt.Errorf("%w: %s and %s would overlap",
					ErrLayoutUnsafe, states[i].DeviceName, states[j].DeviceName)
			}
		}
	}
	return nil
}

func overlaps(a, b domain.DisplayState) bool {
	aRight, aBottom := a.Position.X+int32(a.Mode.Width), a.Position.Y+int32(a.Mode.Height)
	bRight, bBottom := b.Position.X+int32(b.Mode.Width), b.Position.Y+int32(b.Mode.Height)
	return a.Position.X < bRight && b.Position.X < aRight &&
		a.Position.Y < bBottom && b.Position.Y < aBottom
}

func outOfRange(coordinate int32) bool {
	return coordinate > maxCoordinate || coordinate < -maxCoordinate
}

// slidesHorizontally reports whether a display has to move when the target's width
// changes, and slidesVertically does the same for its height. Two things must hold,
// and requiring only the first is the defect these replaced.
//
// The display must start at or beyond the edge that moved, because only the space
// past that edge opens or closes. And it must overlap the target's own band in the
// other axis, because a display in a different row is not on the far side of
// anything -- it is simply elsewhere, and sliding it moves it into a display that
// had no reason to expect company.
//
// On the desk this was written for, the monitor above the primary starts at a larger
// X than a monitor in the row below. Narrowing the one above used to drag the one
// below into the primary, and every plan was refused before a single call was made.
func slidesHorizontally(display, target domain.DisplayState) bool {
	if display.DeviceName == target.DeviceName {
		return false
	}
	if display.Position.X < target.Position.X+int32(target.Mode.Width) {
		return false
	}
	return overlap(
		display.Position.Y, display.Position.Y+int32(display.Mode.Height),
		target.Position.Y, target.Position.Y+int32(target.Mode.Height),
	) > 0
}

func slidesVertically(display, target domain.DisplayState) bool {
	if display.DeviceName == target.DeviceName {
		return false
	}
	if display.Position.Y < target.Position.Y+int32(target.Mode.Height) {
		return false
	}
	return overlap(
		display.Position.X, display.Position.X+int32(display.Mode.Width),
		target.Position.X, target.Position.X+int32(target.Mode.Width),
	) > 0
}

// overlap returns how much two half-open intervals share. Zero means they touch at
// a point or a line, which is not enough to make one the far side of the other.
func overlap(aStart, aEnd, bStart, bEnd int32) int32 {
	return min(aEnd, bEnd) - max(aStart, bStart)
}
