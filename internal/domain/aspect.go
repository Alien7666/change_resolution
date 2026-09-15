package domain

import "fmt"

// conventionalAspects maps a fully reduced width:height pair to the way the ratio is
// actually written and sold. Every key is the reduced form, which is the whole point
// of the map: 2560x1600 reduces to 8:5, and nobody calls a monitor an 8:5, so the
// entry that makes it print "16:10" has to be keyed on 8:5 rather than on the name.
//
// 21:9 is the clearest case of why this is a table and not arithmetic. It is a
// marketing name covering two genuinely different ratios -- 3440x1440 reduces to
// 43:18 and 3840x1620 to 64:27 -- so both spellings are listed and both print what
// the box said.
//
// Nothing here is fuzzy. A ratio one pixel-column away from a known one (1360x768,
// which reduces to 85:48) is not in the table and is not rounded into it; it falls
// through to the rules in AspectLabel and prints an honest number.
var conventionalAspects = map[[2]uint32]string{
	{4, 3}:   "4:3",
	{5, 4}:   "5:4",
	{3, 2}:   "3:2",
	{8, 5}:   "16:10",
	{16, 9}:  "16:9",
	{64, 27}: "21:9",
	{43, 18}: "21:9",
	{32, 9}:  "32:9",
}

// aspectDenominatorLimit is where a reduced ratio stops being something a person can
// read. "5:3" is a ratio; "683:384" is two numbers. Past this bound the decimal form
// carries the same information in a shape that can be compared at a glance, which is
// what the column is for.
const aspectDenominatorLimit = 32

// unknownAspect is what a dimension the tool could not read gets. The catalogue
// filters zero-sized modes out long before the picker, so this is a guard rather
// than a case the user is expected to meet -- but AspectLabel takes two numbers, and
// answering is better than dividing by zero.
const unknownAspect = "—"

// AspectLabel names the shape of a mode: the thing a user is actually choosing when
// they pick a resolution, and the thing the "this is not your native ratio, you will
// need full-screen scaling" reminder is computed from.
//
// The rule is fixed and has no tolerance in it:
//
//  1. reduce width:height by their greatest common divisor;
//  2. if the reduced pair is one people have a name for, use the name;
//  3. otherwise print the reduced pair when its denominator is small enough to read;
//  4. otherwise print two decimals of width/height against 1.
//
// Rule 4 is what 1366x768 gets: it reduces to 683:384, and "1.78:1" is the honest
// answer where the reduction is noise.
func AspectLabel(width, height uint32) string {
	if width == 0 || height == 0 {
		return unknownAspect
	}
	divisor := greatestCommonDivisor(width, height)
	numerator, denominator := width/divisor, height/divisor
	if name, ok := conventionalAspects[[2]uint32{numerator, denominator}]; ok {
		return name
	}
	if denominator <= aspectDenominatorLimit {
		return fmt.Sprintf("%d:%d", numerator, denominator)
	}
	return fmt.Sprintf("%.2f:1", float64(width)/float64(height))
}

// unknownMode is what a mode the tool could not read is called. Like unknownAspect it
// is a guard rather than an expected case: a zero-sized mode never reaches a picker.
const unknownMode = "未知的顯示模式"

// ModeLabel is how a display mode is written for a person: the one spelling the whole
// tool uses, so the mode named in a status line, in a button and in an error message
// is recognisably the same mode.
//
// Bit depth is deliberately absent. It is always 32 and saying so in every sentence
// would bury the numbers that differ; a caller with room for it -- the window's
// "current mode" line -- appends it.
func ModeLabel(mode Mode) string {
	if mode.Width == 0 || mode.Height == 0 {
		return unknownMode
	}
	return fmt.Sprintf("%d × %d @ %d Hz", mode.Width, mode.Height, mode.RefreshHz)
}

func greatestCommonDivisor(a, b uint32) uint32 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// NativeMode reports the largest mode in a catalogue, which is the tool's definition
// of a monitor's native mode: the panel's own resolution is the biggest one its EDID
// offers, and the tool has no other source for it.
//
// "Largest" is pixel count rather than width, so a 1920x1440 4:3 mode outranks a
// 2048x1080 one that is wider but has fewer pixels in it. The two tie-breakers -- width, then refresh --
// exist so the answer is the same on every enumeration rather than merely a
// reasonable one: this feeds a restore, and a restore that picked a different mode
// depending on the order the driver answered in would be a different desktop each
// time.
//
// A zero-sized entry is never the answer. ok is false for an empty catalogue, which
// the caller must treat as "this monitor reports nothing usable" rather than
// substituting a mode of its own.
func NativeMode(modes []Mode) (Mode, bool) {
	var native Mode
	found := false
	for _, mode := range modes {
		if mode.Width == 0 || mode.Height == 0 {
			continue
		}
		if !found || LargerMode(mode, native) {
			native, found = mode, true
		}
	}
	return native, found
}

// LargerMode is the one ordering the tool has for display modes: biggest first,
// where "biggest" is pixel count so that a tall 4:3 mode is not judged smaller than a
// short wide one, with width and then refresh rate breaking the ties.
//
// It is exported because two places need the same answer and a second copy of it
// would eventually disagree with the first. The mode picker sorts with it, so the
// monitor's native mode is the row at the top; NativeMode selects with it, so the
// mode a restore falls back to is that same row. If those two ever parted company,
// the picker would point at one mode as native while a restore applied another.
//
// The order is total on (width, height, refresh), which is also the catalogue's
// dedupe key, so sorting with it is deterministic without being stable.
func LargerMode(candidate, incumbent Mode) bool {
	candidatePixels := uint64(candidate.Width) * uint64(candidate.Height)
	incumbentPixels := uint64(incumbent.Width) * uint64(incumbent.Height)
	switch {
	case candidatePixels != incumbentPixels:
		return candidatePixels > incumbentPixels
	case candidate.Width != incumbent.Width:
		return candidate.Width > incumbent.Width
	default:
		return candidate.RefreshHz > incumbent.RefreshHz
	}
}

// DeriveFallback is the mode a restore puts the desktop back to when the profile
// records no fallback of its own: the monitor's own native mode, derived from what
// it reports rather than remembered from a machine this tool was once written for.
//
// It is deliberately NativeMode and nothing more. The name exists separately because
// the two answer different questions -- "what is this panel" versus "what should we
// restore to" -- and a call site reading DeriveFallback says which one it is asking.
// They coincide today, and a test asserts they still do, so a future divergence has
// to be written down rather than drifted into.
//
// ok is false for an empty catalogue. The caller disables the restore rather than
// guessing: this tool never applies a mode nobody chose.
func DeriveFallback(modes []Mode) (Mode, bool) {
	return NativeMode(modes)
}
