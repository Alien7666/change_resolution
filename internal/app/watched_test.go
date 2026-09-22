package app

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Alien7666/change_resolution/internal/display"
)

const replacementProcess = "OtherGame.exe"

// watchNow points the fixture's own expectation at the process the session is being
// told to watch. f.tick asserts every poll against it, so a watcher left running on
// the old name fails the very next poll instead of passing quietly.
func (f *sessionFixture) watchNow(name string) { f.profile.ProcessName = name }

// The whole point of the feature. The profile names one game because that is the one
// the user plays; on the day they play another, the mode is already applied and the
// screens are already arranged, and nothing about that has to be undone to point the
// watcher somewhere else.
func TestChangingTheWatchedProcessWhileManagedKeepsTheAppliedMode(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	applied := f.desktop()
	f.display.takeCalls()

	if err := f.s.ChangeWatchedProcess(replacementProcess); err != nil {
		t.Fatal(err)
	}

	if calls := f.display.takeCalls(); len(calls) != 0 {
		t.Fatalf("changing the watched process touched the display: %+v", calls)
	}
	if !reflect.DeepEqual(f.desktop(), applied) {
		t.Fatal("changing the watched process changed the desktop")
	}
	got := f.s.Snapshot()
	if !got.Managed || !got.AtGameMode {
		t.Fatalf("snapshot gave up the applied mode: %+v", got)
	}
	if got.Profile.ProcessName != replacementProcess {
		t.Fatalf("snapshot still names %q", got.Profile.ProcessName)
	}
	if got.State != StateWaitingForGame {
		t.Fatalf("state = %q, want the session waiting for the new process", got.State)
	}
	if !strings.Contains(got.Message, replacementProcess) {
		t.Fatalf("message = %q, which never names the process now being watched", got.Message)
	}
}

// The flag that had to be forgotten. gameSeen means "the watched process has been
// seen running", and it was the old process that was seen. Carried over, the first
// poll after the change finds the new game absent, reads that as the game having
// ended, and counts down to a restore the user never asked for -- while they are
// still on their way to launching it.
func TestChangingTheWatchedProcessForgetsThatTheOldOneWasSeen(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})
	if !f.s.gameSeen {
		t.Fatal("the watcher did not record that the old process had been seen")
	}

	if err := f.s.ChangeWatchedProcess(replacementProcess); err != nil {
		t.Fatal(err)
	}
	f.watchNow(replacementProcess)
	if f.s.gameSeen {
		t.Fatal("the seen flag survived a change of process")
	}

	// The new game has not been launched yet. A carried-over flag would arm a restore
	// on this very poll; a forgotten one waits however long it takes.
	if got := f.poll(t, 1, 2, processResult{}); got.State != StateWaitingForGame {
		t.Fatalf("a process that was never seen armed a restore: %+v", got)
	}
	if got := f.poll(t, 1, 300, processResult{}); got.State != StateWaitingForGame || !got.Managed {
		t.Fatalf("the countdown ran anyway: %+v", got)
	}
}

// Seeing the new process arms the automatic restore for it, which is the proof that
// the replacement watcher is a working watcher and not just a stopped one.
func TestTheReplacedWatcherArmsTheRestoreForTheNewProcess(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ChangeWatchedProcess(replacementProcess); err != nil {
		t.Fatal(err)
	}
	f.watchNow(replacementProcess)

	f.poll(t, 1, 1, processResult{running: true})
	if !f.s.gameSeen {
		t.Fatal("seeing the new process did not arm the automatic restore")
	}
	if got := f.poll(t, 1, 2, processResult{}); got.State != StateRestorePending {
		t.Fatalf("the new process ending did not start the countdown: %+v", got)
	}
}

// Emptying the name is a configuration, not a gap: it means "watch nothing, I will
// toggle by hand". The applied mode stays, and nothing is polled.
func TestClearingTheWatchedProcessLeavesAManualOnlySession(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	watchers := f.clock.count()

	if err := f.s.ChangeWatchedProcess("   "); err != nil {
		t.Fatal(err)
	}

	got := f.s.Snapshot()
	if !got.Managed || got.State != StateManualOnly {
		t.Fatalf("snapshot = %+v, want a managed manual-only session", got)
	}
	if got.Profile.ProcessName != "" {
		t.Fatalf("profile still names %q", got.Profile.ProcessName)
	}
	if f.clock.count() != watchers {
		t.Fatalf("watchers = %d, want no replacement started", f.clock.count())
	}
}

// Naming the same process again is a no-op rather than a restart. A restart would
// throw away a seen flag that is still true of that very process, and the countdown
// with it -- so a user who pressed the button twice would silently lose the automatic
// restore they already had armed.
func TestNamingTheSameProcessAgainChangesNothing(t *testing.T) {
	f := newFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})
	watchers := f.clock.count()

	if err := f.s.ChangeWatchedProcess("  " + f.profile.ProcessName + "  "); err != nil {
		t.Fatal(err)
	}
	if !f.s.gameSeen {
		t.Fatal("re-naming the same process forgot that it had been seen")
	}
	if f.clock.count() != watchers {
		t.Fatalf("watchers = %d, want the running one left alone", f.clock.count())
	}
}

// An unmanaged session has no mode to keep and no watcher to replace, but the profile
// it would apply still changes -- otherwise the choice would only stick while the tool
// happened to be managing something.
func TestChangingTheWatchedProcessWhileUnmanagedStillTakesEffect(t *testing.T) {
	f := newFixture(t)

	if err := f.s.ChangeWatchedProcess(replacementProcess); err != nil {
		t.Fatal(err)
	}
	f.watchNow(replacementProcess)
	if got := f.s.Snapshot(); got.Profile.ProcessName != replacementProcess {
		t.Fatalf("snapshot still names %q", got.Profile.ProcessName)
	}
	if f.clock.count() != 0 {
		t.Fatal("an unmanaged session started a watcher")
	}

	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})
	if !f.s.gameSeen {
		t.Fatal("Enable did not watch the process configured before it")
	}
}

// RecoveryPending means the desktop may have changed and the tool could not prove what
// it looks like. Nothing may be reconfigured until that is resolved, and the watched
// process is no exception: arming a restore for a different game against a layout the
// tool is unsure of is exactly the write that state exists to forbid.
func TestChangingTheWatchedProcessIsRefusedWhileRecoveryIsPending(t *testing.T) {
	f := newFixture(t)
	f.display.setFailure("apply", display.ErrLayoutNotVerified)
	if err := f.s.Enable(); !errors.Is(err, display.ErrLayoutNotVerified) {
		t.Fatalf("Enable error = %v, want the uncertain write reported", err)
	}
	if !f.s.recoveryPending {
		t.Fatal("fixture no longer models a pending recovery")
	}
	before := f.s.Snapshot().Profile.ProcessName

	if err := f.s.ChangeWatchedProcess(replacementProcess); !errors.Is(err, ErrDisplayRecoveryPending) {
		t.Fatalf("error = %v, want ErrDisplayRecoveryPending", err)
	}
	if got := f.s.Snapshot().Profile.ProcessName; got != before {
		t.Fatalf("a refused change still rewrote the profile to %q", got)
	}
}
