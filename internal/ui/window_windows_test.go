//go:build windows

package ui

import (
	"errors"
	"fmt"
	"sync"
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

var errUnexpectedModeChange = errors.New("ui tests must never change a display mode")

// stubDisplays answers the read-only calls the refresh path makes. resolveErr is
// swapped mid-test to model a monitor that was asleep when the tool started and
// was switched back to this input afterwards.
type stubDisplays struct {
	mu         sync.Mutex
	resolveErr error
	resolves   int
}

func (d *stubDisplays) ResolveTarget(string) (domain.Target, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resolves++
	if d.resolveErr != nil {
		return domain.Target{}, d.resolveErr
	}
	return domain.Target{DeviceName: `\.\DISPLAY4`, Identity: domain.MonitorIdentity{HardwareID: `MONITOR\XMI27B2`}}, nil
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
	} {
		t.Run(name, func(t *testing.T) {
			w := &window{}
			w.updateAvailability(snapshot)
			if w.unavailableReason == "" {
				t.Fatal("precondition: the 4:3 control was not disabled")
			}

			got := w.availableControls(snapshot)
			if !got.refresh {
				t.Fatal("a latched window offers no way to re-read the display state")
			}
			if got.toggle || got.restore || got.enable {
				t.Fatalf("4:3 commands stayed live while the target is unavailable: %+v", got)
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
	session := app.NewSession(displays, stubProcesses{}, domain.LegacySeedProfile())
	t.Cleanup(func() {
		if err := session.Shutdown(); err != nil {
			t.Errorf("cleanup Shutdown: %v", err)
		}
	})
	w := &window{session: session}

	startup, err := session.Refresh()
	if !errors.Is(err, display.ErrTargetNotFound) {
		t.Fatalf("startup probe error = %v", err)
	}
	w.updateAvailability(startup)
	if w.unavailableReason == "" {
		t.Fatal("precondition: a sleeping monitor did not disable the 4:3 control")
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
		t.Fatal("a monitor that came back left the 4:3 control disabled")
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
