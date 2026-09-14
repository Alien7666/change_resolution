package display

import (
	"reflect"
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
)

// allModeFields is what a driver declares when it means "these four members are
// real". Tests that are not about a missing declaration use it so the thing under
// test is the one thing the case is named for.
const allModeFields = dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel

// good builds an entry a clean driver would report.
func good(width, height, refresh uint32) rawMode {
	return rawMode{
		Mode:   domain.Mode{Width: width, Height: height, RefreshHz: refresh, BitsPerPixel: 32},
		Fields: allModeFields,
	}
}

// The DEVMODEW bits the filter reads. The four field bits already have a Win32 ABI
// test of their own; DM_INTERLACED arrived with this file and gets its value pinned
// here, because a wrong value would not fail -- it would quietly stop dropping
// interlaced modes and let one into a picker.
func TestModeFlagsMatchWin32(t *testing.T) {
	for name, got := range map[string]uint32{
		"DM_BITSPERPEL":       dmBitsPerPel,
		"DM_PELSWIDTH":        dmPelsWidth,
		"DM_PELSHEIGHT":       dmPelsHeight,
		"DM_DISPLAYFREQUENCY": dmDisplayFrequency,
		"DM_INTERLACED":       dmInterlaced,
	} {
		want := map[string]uint32{
			"DM_BITSPERPEL": 0x00040000, "DM_PELSWIDTH": 0x00080000,
			"DM_PELSHEIGHT": 0x00100000, "DM_DISPLAYFREQUENCY": 0x00400000,
			"DM_INTERLACED": 0x00000002,
		}[name]
		if got != want {
			t.Errorf("%s=%#x, want %#x", name, got, want)
		}
	}
}

// Every row here is one way the driver's list is dirty. The rule underneath all of
// them is the same: this catalogue is the list the user picks a mode to *apply*
// from, so anything the tool would refuse to apply must not be in it. Dropping is
// silent on purpose -- a driver listing hundreds of variants is normal, not a fault
// the user can act on.
func TestCatalogueDropsEveryModeTheToolCouldNotApply(t *testing.T) {
	tests := []struct {
		name string
		raw  rawMode
	}{
		{
			name: "the width was never declared, so the number beside it means nothing",
			raw: rawMode{
				Mode:   domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
				Fields: allModeFields &^ dmPelsWidth,
			},
		},
		{
			name: "the height was never declared",
			raw: rawMode{
				Mode:   domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
				Fields: allModeFields &^ dmPelsHeight,
			},
		},
		{
			name: "the refresh rate was never declared",
			raw: rawMode{
				Mode:   domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
				Fields: allModeFields &^ dmDisplayFrequency,
			},
		},
		{
			name: "the colour depth was never declared",
			raw: rawMode{
				Mode:   domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
				Fields: allModeFields &^ dmBitsPerPel,
			},
		},
		{name: "zero width", raw: good(0, 1440, 180)},
		{name: "zero height", raw: good(1920, 0, 180)},
		{
			// 0 and 1 are DEVMODE's two spellings of "whatever the hardware
			// defaults to". Offering either as a choice would print a refresh rate
			// the monitor will not run.
			name: "a refresh rate of 0, which means hardware default",
			raw:  good(1920, 1440, 0),
		},
		{name: "a refresh rate of 1, which also means hardware default", raw: good(1920, 1440, 1)},
		{
			name: "an interlaced mode, which this tool never applies",
			raw: rawMode{
				Mode:         domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
				Fields:       allModeFields,
				DisplayFlags: dmInterlaced,
			},
		},
		{
			name: "16-bit colour, which this tool never applies",
			raw: rawMode{
				Mode:   domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 16},
				Fields: allModeFields,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := catalogue([]rawMode{tt.raw}, domain.Mode{})
			if len(got) != 0 {
				t.Fatalf("catalogue=%#v, want the entry dropped", got)
			}
		})
	}
}

// A monitor whose whole list filtered away is a state the settings flow has to be
// able to describe ("this monitor reports no usable mode"). It is not an error
// here: nothing failed, and an error would make the caller say the enumeration
// broke when it worked and found nothing.
func TestCatalogueReturnsAnEmptyListRatherThanAnError(t *testing.T) {
	got := catalogue([]rawMode{good(1920, 1440, 1), {Mode: miMonitorGame}}, domain.Mode{})
	if len(got) != 0 {
		t.Fatalf("catalogue=%#v, want nothing", got)
	}
}

// The driver lists one entry per dmDisplayFixedOutput scaling variant, so the same
// (width, height, refresh) comes back several times. Colour depth is already pinned
// to 32 by the filter, which is why those three values are the whole key: two
// survivors sharing them are identical in all four members.
func TestCatalogueCollapsesDuplicateResolutionRefreshPairs(t *testing.T) {
	raw := []rawMode{
		good(1920, 1440, 180),
		good(1920, 1440, 180),
		good(1920, 1440, 60),
		good(1920, 1440, 180),
	}

	got := catalogue(raw, domain.Mode{})
	want := []domain.Mode{
		{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		{Width: 1920, Height: 1440, RefreshHz: 60, BitsPerPixel: 32},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalogue=%#v, want %#v", got, want)
	}
}

// The order is the tool's, never the driver's. Largest first is what a person
// scanning for a resolution expects, and pixel count rather than width is what keeps
// a tall 4:3 mode above a short wide one.
func TestCatalogueOrdersByPixelCountThenWidthThenRefresh(t *testing.T) {
	raw := []rawMode{
		good(1920, 1440, 60),
		good(2560, 1440, 180),
		good(1920, 1920, 60),  // 3686400 pixels, exactly as many as 2560x1440
		good(1920, 1440, 180), // 2764800 pixels, and 1920x1920 is wider than nothing else here
		good(3440, 1440, 100),
		good(2560, 1440, 60),
	}

	got := catalogue(raw, domain.Mode{})
	want := []domain.Mode{
		{Width: 3440, Height: 1440, RefreshHz: 100, BitsPerPixel: 32}, // 4953600
		{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}, // 3686400, wider
		{Width: 2560, Height: 1440, RefreshHz: 60, BitsPerPixel: 32},
		{Width: 1920, Height: 1920, RefreshHz: 60, BitsPerPixel: 32}, // 3686400, narrower
		{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		{Width: 1920, Height: 1440, RefreshHz: 60, BitsPerPixel: 32},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalogue=%#v,\nwant %#v", got, want)
	}
}

// The settings dialog re-enumerates on every open and on every refresh, and the
// driver is under no obligation to answer in the same order twice. A list that
// reshuffles between two reads of the same monitor would move the row under the
// user's cursor, so the sort has to be a total order on the dedupe key rather than a
// stable pass over whatever arrived.
func TestCatalogueOrderDoesNotDependOnTheOrderTheDriverAnswered(t *testing.T) {
	forwards := []rawMode{
		good(2560, 1440, 60), good(2560, 1440, 180),
		good(1920, 1440, 180), good(1920, 1440, 60),
	}
	backwards := make([]rawMode, 0, len(forwards))
	for i := len(forwards) - 1; i >= 0; i-- {
		backwards = append(backwards, forwards[i])
	}

	if got, want := catalogue(backwards, domain.Mode{}), catalogue(forwards, domain.Mode{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("a reversed driver list produced %#v,\nwant the same catalogue %#v", got, want)
	}
}

// A user running a mode the driver does not enumerate -- one built in the control
// panel, or one the driver trimmed -- would otherwise open the picker and not find
// the mode on the screen in front of them. The read that supplies it is a separate
// ENUM_CURRENT_SETTINGS call, so it arrives outside the enumerated list and has to
// be put in its sorted place rather than appended at the end.
func TestCatalogueCarriesTheCurrentModeTheDriverDidNotEnumerate(t *testing.T) {
	raw := []rawMode{good(2560, 1440, 180), good(1280, 1024, 60)}
	current := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 165, BitsPerPixel: 32}

	got := catalogue(raw, current)
	want := []domain.Mode{
		{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		current,
		{Width: 1280, Height: 1024, RefreshHz: 60, BitsPerPixel: 32},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalogue=%#v,\nwant %#v", got, want)
	}
}

// The usual case: the driver did enumerate it. The current mode must then be one
// row, not two, and the dedupe key is what makes that true without the caller
// having to check first.
func TestCatalogueDoesNotRepeatACurrentModeTheDriverAlreadyListed(t *testing.T) {
	got := catalogue([]rawMode{good(2560, 1440, 180), good(1920, 1440, 180)}, miMonitorNative)
	want := []domain.Mode{miMonitorNative, miMonitorGame}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalogue=%#v, want %#v", got, want)
	}
}

// The current mode is injected because the user is looking at it, not because it is
// exempt from the rules. A mode this tool could not apply -- an unreadable one, or
// one at a colour depth it never writes -- must not become selectable just by being
// current, because selecting it would save a profile whose every apply is refused.
func TestCatalogueDoesNotInjectACurrentModeTheToolCouldNotApply(t *testing.T) {
	enumerated := []rawMode{good(2560, 1440, 180)}
	for name, current := range map[string]domain.Mode{
		"a current mode that could not be read": {},
		"a current mode at a colour depth we never write": {
			Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 16,
		},
		"a current mode whose refresh rate is the hardware default": {
			Width: 1920, Height: 1440, RefreshHz: 1, BitsPerPixel: 32,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := catalogue(enumerated, current)
			want := []domain.Mode{miMonitorNative}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("catalogue=%#v, want %#v", got, want)
			}
		})
	}
}

// The top row of the catalogue is the monitor's native mode, and both the picker and
// the restore depend on that being true: the picker measures its "not your native
// ratio, you will need full-screen scaling" reminder against it, and a restore with
// no configured fallback applies it. They agree because they are one rule --
// domain.LargerMode -- rather than two that happen to match, and this is the
// assertion that keeps them one.
func TestCatalogueLeadsWithTheModeTheRestoreWouldFallBackTo(t *testing.T) {
	raw := []rawMode{
		good(1920, 1440, 180),
		good(2560, 1440, 60),
		good(1280, 1024, 75),
		good(2560, 1440, 180),
	}

	modes := catalogue(raw, domain.Mode{})
	fallback, ok := domain.DeriveFallback(modes)
	if !ok {
		t.Fatal("a non-empty catalogue derived no fallback")
	}
	if modes[0] != fallback {
		t.Fatalf("the catalogue leads with %#v but the restore would apply %#v", modes[0], fallback)
	}
}
