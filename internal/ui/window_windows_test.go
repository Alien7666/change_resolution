//go:build windows

package ui

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
)

// preflightRejection mirrors the error app.Session reports when the CDS_TEST
// pre-flight refuses the profile mode, except that the operation label and the
// Win32 diagnostic are deliberately reworded. The latch must key on the sentinel
// alone, so no rewording on either side may silently re-enable the 4:3 control.
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
		t.Fatal("unsupported target mode did not disable the 4:3 control")
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
		t.Fatal("missing target did not disable the 4:3 control")
	}
}

func TestUpdateAvailabilityClearsLatchOnCleanRead(t *testing.T) {
	for name, snapshot := range map[string]app.Snapshot{
		"unsupported mode": {State: app.StateError, Err: preflightRejection()},
		"missing target": {State: app.StateError,
			Err: fmt.Errorf("resolve target: %w", display.ErrTargetNotFound)},
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
		t.Fatalf("retryable apply failure disabled the 4:3 control: %q", w.unavailableReason)
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
