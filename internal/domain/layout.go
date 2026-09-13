package domain

// Point is a top-left desktop coordinate in the virtual screen space, matching the
// Win32 POINTL that DEVMODEW.dmPosition carries.
type Point struct {
	X int32
	Y int32
}

// DisplayState is one attached display's mode and desktop position exactly as the
// driver reports them.
type DisplayState struct {
	DeviceName string
	Mode       Mode
	Position   Point
	Primary    bool
}

// Layout is the whole desktop arrangement at one moment. Changing the target's
// width moves every other display, so the state a session has to save is the full
// arrangement, not the target's mode alone.
type Layout struct {
	Displays []DisplayState
}

// Find reports the saved state of deviceName. Callers use the boolean to abort
// rather than guess when a device they expected is not part of the layout.
func (l Layout) Find(deviceName string) (DisplayState, bool) {
	for _, display := range l.Displays {
		if display.DeviceName == deviceName {
			return display, true
		}
	}
	return DisplayState{}, false
}

// LayoutChange is one display's part of a layout change. Mode is written only when
// SetMode is true, so a display the tool does not target can only ever be moved: its
// resolution, refresh rate and colour depth are never in the write set.
type LayoutChange struct {
	DeviceName string
	Position   Point
	Mode       Mode
	SetMode    bool
}

// LayoutPlan is the whole arrangement to be applied, not a list of independent
// edits. It is planned and validated as a unit, and the layer that applies it is
// responsible for ordering the displays so no intermediate desktop overlaps and for
// putting back what it already changed if one of them fails.
type LayoutPlan struct {
	Changes []LayoutChange
}
