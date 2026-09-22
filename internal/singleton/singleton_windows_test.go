//go:build windows

package singleton

import (
	"errors"
	"testing"
)

// The whole contract. A kernel name is used rather than a lock file precisely because
// this stays true across a crash: the name is the operating system's, not a file the
// dead process left behind claiming to still be alive.
func TestASecondAcquireIsRefusedAndTheFirstIsUnaffected(t *testing.T) {
	name := "ResolutionTray.Test.Refused"

	release, err := Acquire(name)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer release()

	second, err := Acquire(name)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire error = %v, want ErrAlreadyRunning", err)
	}
	if second != nil {
		t.Fatal("a refused Acquire handed back a release that would drop the holder's name")
	}
}

// Releasing hands the name back. This is what makes a restart work: the copy that
// exits cleanly must not leave the next one locked out.
func TestReleasingTheNameLetsTheNextCopyStart(t *testing.T) {
	name := "ResolutionTray.Test.Released"

	release, err := Acquire(name)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	release()

	again, err := Acquire(name)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	again()
}

// Two products on one desktop are two names. A refusal that was not about this tool
// would be indistinguishable to the user from the tool being broken.
func TestADifferentNameIsADifferentInstance(t *testing.T) {
	first, err := Acquire("ResolutionTray.Test.NameA")
	if err != nil {
		t.Fatal(err)
	}
	defer first()

	second, err := Acquire("ResolutionTray.Test.NameB")
	if err != nil {
		t.Fatalf("an unrelated name was refused: %v", err)
	}
	second()
}

// ActivateExisting is allowed to find nothing. The other copy is running either way,
// and it may legitimately have no window at this instant, so a caller about to exit
// must not be handed a failure it has nothing to do with.
func TestActivatingAWindowThatIsNotThereIsHarmless(t *testing.T) {
	ActivateExisting("ResolutionTray.Test.NoSuchWindow")
}
