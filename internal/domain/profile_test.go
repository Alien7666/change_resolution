package domain

import (
	"math"
	"testing"
	"time"
)

// TestLegacySeedProfileCarriesTheOriginalHardware pins the values the tool shipped
// with. They are no longer what the product runs on: the profile is the user's
// configuration now, and every field below is replaceable from the settings dialog.
// What this test guards is that the first-run wizard pre-fills the original numbers
// rather than an empty form, so the machine this tool was written for keeps working
// without anyone retyping them.
func TestLegacySeedProfileCarriesTheOriginalHardware(t *testing.T) {
	p := LegacySeedProfile()
	if p.Monitor.HardwareID != `MONITOR\XMI27B2` {
		t.Fatalf("Monitor.HardwareID = %q", p.Monitor.HardwareID)
	}
	if p.GameMode != (Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}) {
		t.Fatalf("GameMode = %#v", p.GameMode)
	}
	// The seed keeps the fallback the shipped tool restored to. It is a pointer
	// because a profile may legitimately record none -- the fallback is then derived
	// from what the monitor reports -- and the seed is the one profile that never has
	// to derive anything, because the machine it describes is the one it came from.
	if p.FallbackMode == nil || *p.FallbackMode != (Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}) {
		t.Fatalf("FallbackMode = %#v", p.FallbackMode)
	}
	if p.ProcessName != "VALORANT-Win64-Shipping.exe" || p.RestoreDelay != 3*time.Second {
		t.Fatalf("process/delay = %q/%s", p.ProcessName, p.RestoreDelay)
	}
}

// TestLegacySeedProfileHasNoInstancePathAndClaimsAUniqueModel guards a combination
// that looks like two oversights and is neither.
//
// InstancePath is empty because nobody has ever picked a monitor on this machine —
// the seed is what exists before the wizard has run, and the device interface path
// is only knowable once a real monitor has been selected from a real enumeration.
// ModelWasUnique is true because the original tool only ever ran with a single Mi
// Monitor attached, which is exactly the condition that rung requires.
//
// Together they are what keeps the shipped behaviour alive: with no instance path
// the identity ladder's primary rung cannot match, so resolution falls through to
// the hardware-ID rung, which the true ModelWasUnique leaves enabled — the same
// MONITOR\XMI27B2 match the tool has always made. Filling in the instance path or
// flipping ModelWasUnique to false would silently stop the seed from resolving
// anything at all.
func TestLegacySeedProfileHasNoInstancePathAndClaimsAUniqueModel(t *testing.T) {
	p := LegacySeedProfile()
	if p.Monitor.InstancePath != "" {
		t.Fatalf("Monitor.InstancePath = %q, want empty: the seed predates any monitor selection", p.Monitor.InstancePath)
	}
	if !p.Monitor.ModelWasUnique {
		t.Fatal("Monitor.ModelWasUnique = false, want true: the hardware-ID rung is the seed's only way to resolve")
	}
	if p.Monitor.Label == "" {
		t.Fatal("Monitor.Label is empty: the window has nothing to call the target")
	}
}

// TestMaxDimensionBoundsAModeTheToolWouldRefuse fixes the one dimension bound the
// whole tool shares. internal/display validates every planned arrangement against
// it and internal/config validates the file the user wrote against it, so it lives
// here where neither has to import the other.
func TestMaxDimensionBoundsAModeTheToolWouldRefuse(t *testing.T) {
	if MaxDimension != 1<<16 {
		t.Fatalf("MaxDimension = %d, want %d", MaxDimension, 1<<16)
	}

	cases := []struct {
		name   string
		mode   Mode
		within bool
	}{
		{"the seed's game mode", Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}, true},
		{"the seed's fallback mode", Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}, true},
		{"an 8K monitor", Mode{Width: 7680, Height: 4320, RefreshHz: 60, BitsPerPixel: 32}, true},
		{"the bound itself", Mode{Width: MaxDimension, Height: MaxDimension, RefreshHz: 60, BitsPerPixel: 32}, true},
		{"a width one past the bound", Mode{Width: MaxDimension + 1, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}, false},
		{"a height one past the bound", Mode{Width: 1920, Height: MaxDimension + 1, RefreshHz: 60, BitsPerPixel: 32}, false},
	}
	for _, tc := range cases {
		within := tc.mode.Width <= MaxDimension && tc.mode.Height <= MaxDimension
		if within != tc.within {
			t.Errorf("%s: %dx%d within MaxDimension = %v, want %v",
				tc.name, tc.mode.Width, tc.mode.Height, within, tc.within)
		}
	}

	// The bound is generous enough to accept any mode a real monitor reports, and
	// tight enough that a desktop several such displays wide still fits the int32
	// coordinates Win32 uses — which is the reason it is a bound and not a comment.
	if int64(MaxDimension)*8 > math.MaxInt32 {
		t.Fatalf("MaxDimension = %d leaves no room for a multi-display desktop in int32", MaxDimension)
	}
}

func TestMonitorModelKeyBoundsAndCaseFoldsFullWin32HardwareIDs(t *testing.T) {
	tests := map[string]string{
		`MONITOR\XMI27B2\0009`: `MONITOR\XMI27B2`,
		`monitor\xmi27b2\{4d36e96e-e325-11ce-bfc1-08002be10318}\0011`: `MONITOR\XMI27B2`,
		`MONITOR\DEL41A8`: `MONITOR\DEL41A8`,
	}
	for hardwareID, want := range tests {
		if got := MonitorModelKey(hardwareID); got != want {
			t.Errorf("MonitorModelKey(%q) = %q, want %q", hardwareID, got, want)
		}
	}
}

// A Profile is handed around by value, and the optional fallback is the one field a
// value copy does not separate. Copy is what a caller that publishes a profile to
// another goroutine uses so that neither of them can change the other's restore.
func TestProfileCopyDoesNotShareTheOptionalFallback(t *testing.T) {
	original := LegacySeedProfile()
	copied := original.Copy()
	if copied.FallbackMode == original.FallbackMode {
		t.Fatal("Copy shared the fallback mode pointer")
	}
	if *copied.FallbackMode != *original.FallbackMode {
		t.Fatalf("Copy changed the fallback: %+v", *copied.FallbackMode)
	}
	*copied.FallbackMode = Mode{Width: 640, Height: 480, RefreshHz: 60, BitsPerPixel: 32}
	if original.FallbackMode.Width != 2560 {
		t.Fatalf("changing the copy changed the original: %+v", *original.FallbackMode)
	}

	// A profile that derives its fallback copies just as cleanly; nil is a value, not
	// an absence to be filled in.
	derived := original
	derived.FallbackMode = nil
	if derived.Copy().FallbackMode != nil {
		t.Fatal("Copy invented a fallback for a profile that records none")
	}
}
