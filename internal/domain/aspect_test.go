package domain

import "testing"

// The label is what turns a mode table into something a person can scan for "the
// 4:3 one". It is computed, never matched fuzzily: every row below is the exact
// arithmetic the spec fixes, and the two rules underneath it are the reduced ratio
// and the decimal fallback, in that order.
func TestAspectLabelNamesTheRatiosPeopleRecogniseAndComputesTheRest(t *testing.T) {
	tests := []struct {
		name          string
		width, height uint32
		want          string
	}{
		// The spec's own table, spelled out here because these are the ones a user
		// reads off the picker.
		{"the game mode the tool shipped with", 1920, 1440, "4:3"},
		{"the Mi Monitor's native mode", 2560, 1440, "16:9"},
		{"an ultrawide", 3440, 1440, "21:9"},
		{"an old 5:4 panel", 1280, 1024, "5:4"},
		{"a laptop panel whose ratio is not a round one", 1366, 768, "1.78:1"},
		{"a 16:10 panel", 2560, 1600, "16:10"},
		{"a super-ultrawide", 3840, 1080, "32:9"},

		// 21:9 is marketing, not arithmetic, and the two resolutions sold under that
		// name do not reduce to the same pair. Both spellings are in the table, so
		// both print what the box said.
		{"the other 21:9", 3840, 1620, "21:9"},

		// 16:10 has to be keyed on its reduced form. 2560x1600 reduces to 8:5, and
		// 5 is well inside the denominator limit below, so without the table entry
		// the rule underneath would print a correct but unfamiliar "8:5".
		{"16:10 at another size", 1920, 1200, "16:10"},

		// Below the table: reduce, and print the reduction when its denominator is
		// small enough to read.
		{"a ratio with no common name but a readable reduction", 1280, 768, "5:3"},
		{"a portrait panel", 1080, 1920, "9:16"},

		// The decimal fallback. 2048x1080 reduces to 256:135, which is the shape the
		// spec's prose offers as an example of a printable reduction -- but 135 is
		// far past the denominator limit the same sentence sets, and the limit is
		// what the 1366x768 row above depends on. The limit wins; the example does
		// not survive it.
		{"a ratio whose reduction nobody could read", 2048, 1080, "1.90:1"},

		// No fuzzy matching anywhere: 1360x768 is one pixel-column away from 16:9
		// and must not be labelled 16:9. It reduces to 85:48, which the limit sends
		// to the decimal form.
		{"a ratio that is nearly but not exactly 16:9", 1360, 768, "1.77:1"},

		// A mode the catalogue would have dropped. AspectLabel is exported and takes
		// two numbers, so it answers for one anyway rather than dividing by zero.
		{"a dimension that could not be read", 0, 1440, "—"},
		{"the other dimension that could not be read", 1920, 0, "—"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AspectLabel(tt.width, tt.height); got != tt.want {
				t.Errorf("AspectLabel(%d, %d)=%q, want %q", tt.width, tt.height, got, tt.want)
			}
		})
	}
}

// The native mode is the biggest one the monitor reported, and "biggest" is pixel
// count rather than width so that a tall 4:3 panel is not beaten by a short wide
// one. The tie-breakers exist because the answer has to be the same every time the
// picker is opened, not merely a reasonable one.
func TestNativeModePicksTheLargestModeDeterministically(t *testing.T) {
	tests := []struct {
		name  string
		modes []Mode
		want  Mode
	}{
		{
			// The widest mode is listed first and is not the answer: 3840x1080 is
			// 4147200 pixels against 3440x1440's 4953600. A rule written on width
			// would pick the ultrawide letterbox over the panel's real size.
			name: "the largest pixel count wins, not the first or the widest",
			modes: []Mode{
				{Width: 3840, Height: 1080, RefreshHz: 144, BitsPerPixel: 32},
				{Width: 3440, Height: 1440, RefreshHz: 100, BitsPerPixel: 32},
				{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
			},
			want: Mode{Width: 3440, Height: 1440, RefreshHz: 100, BitsPerPixel: 32},
		},
		{
			// 2560x1440 and 1920x1920 are both 3686400 pixels. Width breaks the tie,
			// so the answer does not depend on which one the driver listed first.
			name: "an exact tie on pixel count breaks on width",
			modes: []Mode{
				{Width: 1920, Height: 1920, RefreshHz: 180, BitsPerPixel: 32},
				{Width: 2560, Height: 1440, RefreshHz: 60, BitsPerPixel: 32},
			},
			want: Mode{Width: 2560, Height: 1440, RefreshHz: 60, BitsPerPixel: 32},
		},
		{
			name: "the highest refresh within that resolution is the one chosen",
			modes: []Mode{
				{Width: 2560, Height: 1440, RefreshHz: 60, BitsPerPixel: 32},
				{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
				{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32},
			},
			want: Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := NativeMode(tt.modes)
			if !ok {
				t.Fatalf("NativeMode(%#v) reported nothing", tt.modes)
			}
			if got != tt.want {
				t.Fatalf("NativeMode=%#v, want %#v", got, tt.want)
			}
		})
	}
}

// An empty catalogue is a real state -- a monitor whose whole reported list was
// filtered away, or one the tool could not enumerate -- and the caller has to be
// able to tell it apart from a mode. Returning a zero Mode with ok true would hand
// the restore path a 0x0 mode to apply.
func TestNativeModeReportsNothingRatherThanAZeroMode(t *testing.T) {
	if mode, ok := NativeMode(nil); ok {
		t.Fatalf("NativeMode(nil)=%#v, want no mode at all", mode)
	}
	if mode, ok := NativeMode([]Mode{}); ok {
		t.Fatalf("NativeMode(empty)=%#v, want no mode at all", mode)
	}
	// A zero-sized entry is not a candidate either: it would win nothing, but it must
	// not be the answer when it is the only entry.
	if mode, ok := NativeMode([]Mode{{RefreshHz: 60, BitsPerPixel: 32}}); ok {
		t.Fatalf("NativeMode(zero-sized)=%#v, want no mode at all", mode)
	}
}

// DeriveFallback is what a restore uses when the profile records no fallback mode of
// its own. It is deliberately the same rule as NativeMode -- the mode to put the
// desktop back to is the monitor's own largest -- and the test says so by asserting
// the two agree on the same catalogue, so a future change to one is a visible change
// to the other rather than a silent divergence.
func TestDeriveFallbackIsTheMonitorsOwnLargestMode(t *testing.T) {
	catalogue := []Mode{
		{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		{Width: 2560, Height: 1440, RefreshHz: 60, BitsPerPixel: 32},
		{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
	}
	want := Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}

	got, ok := DeriveFallback(catalogue)
	if !ok || got != want {
		t.Fatalf("DeriveFallback=%#v ok=%v, want %#v", got, ok, want)
	}
	native, _ := NativeMode(catalogue)
	if got != native {
		t.Fatalf("DeriveFallback=%#v but NativeMode=%#v; the two rules have diverged", got, native)
	}
}

// The caller disables the restore button on this answer. It must never be a guess:
// the tool has no idea what this monitor's native mode is, and applying a
// hard-coded one would change a screen to a mode nobody chose.
func TestDeriveFallbackRefusesToGuessFromAnEmptyCatalogue(t *testing.T) {
	if mode, ok := DeriveFallback(nil); ok {
		t.Fatalf("DeriveFallback(nil)=%#v, want ok=false", mode)
	}
}

// ModeLabel is the one spelling of a mode the whole tool uses, so it is pinned here
// rather than left to whichever caller writes the next status line.
func TestModeLabelWritesAModeTheSameWayEverywhere(t *testing.T) {
	cases := map[Mode]string{
		{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}: "1920 × 1440 @ 180 Hz",
		{Width: 2560, Height: 1440, RefreshHz: 60, BitsPerPixel: 32}:  "2560 × 1440 @ 60 Hz",
		// A mode the tool could not read is said out loud rather than printed as
		// "0 × 0 @ 0 Hz", which reads like a mode the monitor offered.
		{}:                              unknownMode,
		{Width: 1920, BitsPerPixel: 32}: unknownMode,
	}
	for mode, want := range cases {
		if got := ModeLabel(mode); got != want {
			t.Errorf("ModeLabel(%+v) = %q, want %q", mode, got, want)
		}
	}
}
