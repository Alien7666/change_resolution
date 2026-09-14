package display

import (
	"sort"

	"github.com/Alien7666/change_resolution/internal/domain"
)

// The DEVMODEW bits the mode filter reads. They live here rather than beside the
// CDS_* flags because deciding which modes exist is not a Windows-only idea: the
// filter, the dedupe and the sort are pure, are the part most likely to be wrong,
// and are tested without a display. The CDS_* flags stay where they are, with the
// comment recording that CDS_UPDATEREGISTRY is deliberately absent.
//
// dmFields declares which DEVMODEW members the driver actually filled in. A mode
// that does not declare all four of these is not a mode the tool can apply -- the
// numbers beside the undeclared member are whatever was in the buffer.
//
// dmInterlaced is a dmDisplayFlags value, not a dmFields one. It shares the member
// with DM_GRAYSCALE (0x00000001) and is the reason interlaced entries can be told
// apart from progressive ones at all.
const (
	dmBitsPerPel       uint32 = 0x00040000
	dmPelsWidth        uint32 = 0x00080000
	dmPelsHeight       uint32 = 0x00100000
	dmDisplayFrequency uint32 = 0x00400000

	dmInterlaced uint32 = 0x00000002

	requiredModeFields = dmPelsWidth | dmPelsHeight | dmDisplayFrequency | dmBitsPerPel
)

const (
	// applicableBitsPerPixel is the only colour depth this tool writes, so it is also
	// the only one it may offer. Anything else in the catalogue would be a row whose
	// every apply is refused.
	applicableBitsPerPixel uint32 = 32

	// minimumRefreshHz rejects DEVMODE's two spellings of "whatever the hardware
	// defaults to". 0 and 1 are not refresh rates the monitor runs at; showing either
	// would put a number in the picker that the panel never displays.
	minimumRefreshHz uint32 = 2
)

// rawMode is one EnumDisplaySettingsW answer, reduced to the three things deciding
// whether to keep it depends on. It is deliberately not a devMode: the filter is the
// part worth testing, and tying it to a Win32 struct would tie its tests to Windows.
type rawMode struct {
	Mode         domain.Mode
	Fields       uint32
	DisplayFlags uint32
}

// modeKey is the identity of a row in the catalogue. Colour depth is not part of it
// because the filter has already pinned every survivor to 32 bpp, so two entries
// sharing these three values are identical in all four members and it does not
// matter which one survives the dedupe.
type modeKey struct {
	width     uint32
	height    uint32
	refreshHz uint32
}

// catalogue turns what a driver reported into the list a person picks from.
//
// Drivers answer with hundreds of entries, many of which are not modes anyone can
// choose: undeclared members, placeholder refresh rates, interlaced timings, colour
// depths this tool never writes, and one duplicate per dmDisplayFixedOutput scaling
// variant. All of that is dropped silently -- it is how drivers answer, not a fault
// the user could act on -- and what remains is deduplicated and put in the tool's own
// order rather than the driver's.
//
// current is the separate ENUM_CURRENT_SETTINGS read, and it is merged in so the
// list always contains the mode the user is looking at, even when the driver did not
// enumerate it (a mode built in the control panel, or one the driver trimmed). It
// earns no exemption from the filter: a current mode the tool could not apply must
// not become selectable merely by being current.
//
// A catalogue that comes out empty is an answer, not a failure. The caller says "this
// monitor reports no usable mode" and stops there.
func catalogue(raw []rawMode, current domain.Mode) []domain.Mode {
	modes := make([]domain.Mode, 0, len(raw)+1)
	seen := make(map[modeKey]struct{}, len(raw)+1)
	keep := func(mode domain.Mode) {
		key := modeKey{width: mode.Width, height: mode.Height, refreshHz: mode.RefreshHz}
		if _, duplicate := seen[key]; duplicate {
			return
		}
		seen[key] = struct{}{}
		modes = append(modes, mode)
	}

	for _, entry := range raw {
		if entry.Fields&requiredModeFields != requiredModeFields {
			continue
		}
		if entry.DisplayFlags&dmInterlaced != 0 {
			continue
		}
		if !applicableMode(entry.Mode) {
			continue
		}
		keep(entry.Mode)
	}
	if applicableMode(current) {
		keep(current)
	}

	// domain.LargerMode is a total order on the dedupe key, and the dedupe has already
	// made that key unique, so the result does not depend on the order the driver
	// answered in or on the sort being stable. The settings dialog re-enumerates on
	// every open and on every refresh; a list that reshuffled between two reads of one
	// monitor would move the row out from under the user's cursor.
	//
	// Sorting with the same rule domain.NativeMode selects by is what makes the top
	// row of the picker the monitor's native mode, which is the row the "not your
	// native ratio" reminder is measured against.
	sort.Slice(modes, func(i, j int) bool { return domain.LargerMode(modes[i], modes[j]) })
	return modes
}

// applicableMode reports whether the tool could actually apply a mode. It is the
// value half of the filter -- the dmFields and dmDisplayFlags halves are declarations
// about the values, and belong with the entry that carried them.
func applicableMode(mode domain.Mode) bool {
	return mode.Width != 0 &&
		mode.Height != 0 &&
		mode.RefreshHz >= minimumRefreshHz &&
		mode.BitsPerPixel == applicableBitsPerPixel
}
