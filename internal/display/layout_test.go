package display

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
)

var (
	miMonitorNative = domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	miMonitorGame   = domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	sideMode        = domain.Mode{Width: 2560, Height: 1440, RefreshHz: 144, BitsPerPixel: 32}
	topMode         = domain.Mode{Width: 1920, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}
)

// measuredLayout is the real four monitor desktop this tool runs on, with the Mi
// Monitor still at its native width and every display where the smoke test found
// it. DISPLAY1 is both the target and the primary, so nothing may move it.
func measuredLayout() domain.Layout {
	return domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY1`, Mode: miMonitorNative, Position: domain.Point{X: 0, Y: 0}, Primary: true},
		{DeviceName: `\.\DISPLAY2`, Mode: sideMode, Position: domain.Point{X: 2560, Y: 0}},
		{DeviceName: `\.\DISPLAY5`, Mode: topMode, Position: domain.Point{X: 2562, Y: -1080}},
		{DeviceName: `\.\DISPLAY3`, Mode: topMode, Position: domain.Point{X: 642, Y: -1080}},
	}}
}

func positions(t *testing.T, plan domain.LayoutPlan) map[string]domain.Point {
	t.Helper()
	result := make(map[string]domain.Point, len(plan.Changes))
	for _, change := range plan.Changes {
		result[change.DeviceName] = change.Position
	}
	return result
}

// The defect this planner exists for: dropping the Mi Monitor from 2560 to 1920
// wide used to leave a 640 px dead zone the mouse could not cross. The display
// beyond the target's right edge closes that gap by exactly the width delta.
//
// The two monitors in the row above do not move. They sit alongside the target
// rather than beyond the edge that moved, so no gap opens between them and it and
// nothing about their own arrangement changed. Sliding them as well was what let a
// monitor in one row be dragged into a monitor in another.
func TestPlanModeChangeClosesTheGapOnTheMeasuredDesktop(t *testing.T) {
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.Point{
		`\.\DISPLAY1`: {X: 0, Y: 0},
		`\.\DISPLAY2`: {X: 1920, Y: 0},
		`\.\DISPLAY5`: {X: 2562, Y: -1080},
		`\.\DISPLAY3`: {X: 642, Y: -1080},
	}
	if got := positions(t, plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("positions = %+v, want %+v", got, want)
	}
}

// Position is the only field a display that is not the target may ever give up.
func TestPlanModeChangeWritesAModeOnlyForTheTarget(t *testing.T) {
	layout := measuredLayout()
	plan, err := PlanModeChange(layout, `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != len(layout.Displays) {
		t.Fatalf("plan covers %d displays, want %d", len(plan.Changes), len(layout.Displays))
	}
	for _, change := range plan.Changes {
		target := change.DeviceName == `\.\DISPLAY1`
		if change.SetMode != target {
			t.Fatalf("%s: SetMode = %v", change.DeviceName, change.SetMode)
		}
		if !target && change.Mode != (domain.Mode{}) {
			t.Fatalf("%s carries a mode into the transaction: %+v", change.DeviceName, change.Mode)
		}
		if target && change.Mode != miMonitorGame {
			t.Fatalf("target mode = %+v", change.Mode)
		}
	}
}

// Windows keeps the primary display at the origin. When the target sits to the left
// of the primary the raw shift would drag the primary off it, so the arrangement is
// translated back and the neighbours absorb the delta instead.
func TestPlanModeChangeNeverMovesThePrimaryDisplay(t *testing.T) {
	layout := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY9`, Mode: miMonitorNative, Position: domain.Point{X: -2560, Y: 0}},
		{DeviceName: `\.\DISPLAY1`, Mode: sideMode, Position: domain.Point{X: 0, Y: 0}, Primary: true},
	}}
	plan, err := PlanModeChange(layout, `\.\DISPLAY9`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.Point{
		`\.\DISPLAY9`: {X: -1920, Y: 0},
		`\.\DISPLAY1`: {X: 0, Y: 0},
	}
	if got := positions(t, plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("positions = %+v, want %+v", got, want)
	}
}

// Growing the target back is the same rule with the opposite sign, which is what
// keeps the fallback restore from parking the wider mode on top of its neighbour.
func TestPlanModeChangeReopensTheGapWhenTheTargetGrows(t *testing.T) {
	narrowed, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}
	widened, err := PlanModeChange(layoutFrom(narrowed, measuredLayout()), `\.\DISPLAY1`, miMonitorNative)
	if err != nil {
		t.Fatal(err)
	}
	want := positions(t, planFor(measuredLayout().Displays, `\.\DISPLAY1`))
	if got := positions(t, widened); !reflect.DeepEqual(got, want) {
		t.Fatalf("positions = %+v, want %+v", got, want)
	}
}

// A height change moves the displays below the target by the same reasoning.
func TestPlanModeChangeClosesAVerticalGap(t *testing.T) {
	layout := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY1`, Mode: miMonitorNative, Position: domain.Point{X: 0, Y: 0}, Primary: true},
		{DeviceName: `\.\DISPLAY3`, Mode: topMode, Position: domain.Point{X: 0, Y: -1080}},
		{DeviceName: `\.\DISPLAY2`, Mode: topMode, Position: domain.Point{X: 0, Y: 1440}},
	}}
	plan, err := PlanModeChange(layout, `\.\DISPLAY1`, topMode)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.Point{
		`\.\DISPLAY1`: {X: 0, Y: 0},
		`\.\DISPLAY3`: {X: 0, Y: -1080},
		`\.\DISPLAY2`: {X: 0, Y: 1080},
	}
	if got := positions(t, plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("positions = %+v, want %+v", got, want)
	}
}

// layoutFrom replays a plan onto the layout it came from, so a test can ask what
// the desktop looks like after the plan was applied.
func layoutFrom(plan domain.LayoutPlan, before domain.Layout) domain.Layout {
	displays := make([]domain.DisplayState, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		state, _ := before.Find(change.DeviceName)
		state.Position = change.Position
		if change.SetMode {
			state.Mode = change.Mode
		}
		displays = append(displays, state)
	}
	return domain.Layout{Displays: displays}
}

// Anything the planner cannot prove safe has to come back as a refusal. A caller
// that sees ErrLayoutUnsafe has changed nothing, which is the whole point of
// computing and validating the arrangement before the first Win32 call.
func TestPlanModeChangeRefusesLayoutsItCannotMakeSafe(t *testing.T) {
	withDisplays := func(mutate func([]domain.DisplayState)) domain.Layout {
		layout := measuredLayout()
		mutate(layout.Displays)
		return layout
	}
	tests := map[string]struct {
		layout domain.Layout
		target string
	}{
		"no display was read": {layout: domain.Layout{}, target: `\.\DISPLAY1`},
		"target is not attached": {
			layout: measuredLayout(), target: `\.\DISPLAY8`,
		},
		"a display was not read": {
			layout: withDisplays(func(d []domain.DisplayState) { d[2].Mode = domain.Mode{} }),
			target: `\.\DISPLAY1`,
		},
		"a display reported no name": {
			layout: withDisplays(func(d []domain.DisplayState) { d[1].DeviceName = "" }),
			target: `\.\DISPLAY1`,
		},
		"a display was read twice": {
			layout: withDisplays(func(d []domain.DisplayState) { d[3].DeviceName = d[2].DeviceName }),
			target: `\.\DISPLAY1`,
		},
		"no primary display": {
			layout: withDisplays(func(d []domain.DisplayState) { d[0].Primary = false }),
			target: `\.\DISPLAY1`,
		},
		"two primary displays": {
			layout: withDisplays(func(d []domain.DisplayState) { d[1].Primary = true }),
			target: `\.\DISPLAY1`,
		},
		// Two displays in the row above the target already overlap each other.
		// Neither sits beyond the edge the target is moving, so no shift the plan
		// could make separates them, and the whole plan is refused.
		"displays would overlap": {
			layout: withDisplays(func(d []domain.DisplayState) { d[2].Position.X = 2000 }),
			target: `\.\DISPLAY1`,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			plan, err := PlanModeChange(tt.layout, tt.target, miMonitorGame)
			if !errors.Is(err, ErrLayoutUnsafe) {
				t.Fatalf("err = %v, want ErrLayoutUnsafe", err)
			}
			if len(plan.Changes) != 0 {
				t.Fatalf("a refused plan still carries changes: %+v", plan)
			}
		})
	}
}

// An unusable game mode must be refused as loudly as an unusable layout.
func TestPlanModeChangeRefusesAnUnusableMode(t *testing.T) {
	if _, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, domain.Mode{}); !errors.Is(err, ErrLayoutUnsafe) {
		t.Fatalf("err = %v, want ErrLayoutUnsafe", err)
	}
}

// Restoring has to reproduce the saved coordinates exactly, and it may rewrite the
// mode of the target only: a display whose resolution the user changed while the
// game mode was active keeps that change.
func TestPlanRestoreReproducesTheSavedArrangement(t *testing.T) {
	saved := measuredLayout()
	plan, err := PlanRestore(saved, saved, `\.\DISPLAY1`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.Point{
		`\.\DISPLAY1`: {X: 0, Y: 0},
		`\.\DISPLAY2`: {X: 2560, Y: 0},
		`\.\DISPLAY5`: {X: 2562, Y: -1080},
		`\.\DISPLAY3`: {X: 642, Y: -1080},
	}
	if got := positions(t, plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("positions = %+v, want %+v", got, want)
	}
	for _, change := range plan.Changes {
		target := change.DeviceName == `\.\DISPLAY1`
		if change.SetMode != target {
			t.Fatalf("%s: SetMode = %v", change.DeviceName, change.SetMode)
		}
		if target && change.Mode != miMonitorNative {
			t.Fatalf("restored target mode = %+v", change.Mode)
		}
	}
}

// A round trip is the property the user actually cares about: enable then restore
// puts every display back where it started.
func TestPlanModeChangeThenPlanRestoreIsIdentity(t *testing.T) {
	before := measuredLayout()
	applied, err := PlanModeChange(before, `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := PlanRestore(before, layoutFrom(applied, before), `\.\DISPLAY1`)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(positions(t, applied), positions(t, restored)) {
		t.Fatal("the game mode plan and the restore plan cannot be the same arrangement")
	}
	if !reflect.DeepEqual(layoutFrom(restored, layoutFrom(applied, before)), before) {
		t.Fatalf("round trip = %+v, want %+v", layoutFrom(restored, layoutFrom(applied, before)), before)
	}
}

// The saved arrangement belongs to the displays that existed when it was captured.
// If the target is no longer one of them the coordinates are stale, and applying
// them would move displays onto each other.
func TestPlanRestoreRefusesASavedLayoutWithoutTheTarget(t *testing.T) {
	saved := measuredLayout()
	plan, err := PlanRestore(saved, saved, `\.\DISPLAY7`)
	if !errors.Is(err, ErrLayoutUnsafe) {
		t.Fatalf("err = %v, want ErrLayoutUnsafe", err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("a refused plan still carries changes: %+v", plan)
	}
}

// A restore writes saved positions but deliberately preserves every non-target
// display's current mode. Safety therefore has to be proved with those current sizes,
// not with the smaller sizes in the saved snapshot.
func TestPlanRestoreRefusesSavedPositionsThatOverlapACurrentNeighbourMode(t *testing.T) {
	mode100 := domain.Mode{Width: 100, Height: 100, RefreshHz: 60, BitsPerPixel: 32}
	mode200 := domain.Mode{Width: 200, Height: 100, RefreshHz: 60, BitsPerPixel: 32}
	saved := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY1`, Mode: mode100, Position: domain.Point{}, Primary: true},
		{DeviceName: `\.\DISPLAY2`, Mode: mode100, Position: domain.Point{X: 100}},
		{DeviceName: `\.\DISPLAY3`, Mode: mode100, Position: domain.Point{X: 200}},
	}}
	current := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY1`, Mode: mode100, Position: domain.Point{}, Primary: true},
		{DeviceName: `\.\DISPLAY2`, Mode: mode200, Position: domain.Point{X: 100}},
		{DeviceName: `\.\DISPLAY3`, Mode: mode100, Position: domain.Point{X: 300}},
	}}

	plan, err := PlanRestore(saved, current, `\.\DISPLAY1`)
	if !errors.Is(err, ErrLayoutUnsafe) {
		t.Fatalf("err = %v, want ErrLayoutUnsafe", err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("unsafe restore returned plan = %+v", plan)
	}
}

func TestPlanRestoreRefusesAChangedDeviceSet(t *testing.T) {
	saved := measuredLayout()
	current := measuredLayout()
	current.Displays = append(current.Displays, domain.DisplayState{
		DeviceName: `\.\DISPLAY9`,
		Mode:       sideMode,
		Position:   domain.Point{X: 6000},
	})

	plan, err := PlanRestore(saved, current, `\.\DISPLAY1`)
	if !errors.Is(err, ErrLayoutUnsafe) {
		t.Fatalf("err = %v, want ErrLayoutUnsafe", err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("changed topology returned plan = %+v", plan)
	}
}

func TestPlanRestoreRefusesAChangedPrimaryDisplay(t *testing.T) {
	saved := measuredLayout()
	current := measuredLayout()
	for i := range current.Displays {
		current.Displays[i].Primary = current.Displays[i].DeviceName == `\.\DISPLAY2`
	}

	plan, err := PlanRestore(saved, current, `\.\DISPLAY1`)
	if !errors.Is(err, ErrLayoutUnsafe) {
		t.Fatalf("err = %v, want ErrLayoutUnsafe", err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("changed primary returned plan = %+v", plan)
	}
}

// The growing direction's fixtures. Every mode here is larger than the target's
// current one in at least one axis, which is the half of the planner no user could
// reach until the mode picker existed.
var (
	miMonitorWide = domain.Mode{Width: 3840, Height: 1440, RefreshHz: 120, BitsPerPixel: 32}
	ultrawideMode = domain.Mode{Width: 2560, Height: 1080, RefreshHz: 120, BitsPerPixel: 32}
	portraitMode  = domain.Mode{Width: 1440, Height: 2560, RefreshHz: 60, BitsPerPixel: 32}
	// mixedAxisMode is wider and shorter than the Mi Monitor's native mode, which
	// is the shape of most of the modes a picker will offer.
	mixedAxisMode = domain.Mode{Width: 3440, Height: 1080, RefreshHz: 120, BitsPerPixel: 32}
)

// assertNoOverlaps holds an arrangement to the property the planner exists to
// guarantee. A grow test that only compared coordinates would still pass if two
// displays landed on each other, and landing two displays on each other is exactly
// what Windows answers by repacking the whole desktop.
func assertNoOverlaps(t *testing.T, states []domain.DisplayState) {
	t.Helper()
	for i := range states {
		for j := i + 1; j < len(states); j++ {
			if overlaps(states[i], states[j]) {
				t.Fatalf("%s %+v and %s %+v overlap",
					states[i].DeviceName, states[i], states[j].DeviceName, states[j])
			}
		}
	}
}

// The mirror of the gap-closing case, and the one no user could reach until a mode
// picker existed: a target that gets wider needs the space the display beyond its
// right edge is standing in, so that display moves outward by the width delta.
// The row above is alongside the target, not beyond the edge that moved, and stays.
// Growing past the mode the monitor started in is not the same as restoring to it --
// the restore has a saved arrangement to return to, this has nothing but the rule.
func TestPlanModeChangePushesNeighboursOutwardWhenTheTargetGrows(t *testing.T) {
	layout := measuredLayout()
	plan, err := PlanModeChange(layout, `\.\DISPLAY1`, miMonitorWide)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.Point{
		`\.\DISPLAY1`: {X: 0, Y: 0},
		`\.\DISPLAY2`: {X: 3840, Y: 0},
		`\.\DISPLAY5`: {X: 2562, Y: -1080},
		`\.\DISPLAY3`: {X: 642, Y: -1080},
	}
	if got := positions(t, plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("positions = %+v, want %+v", got, want)
	}
	assertNoOverlaps(t, layoutFrom(plan, layout).Displays)
}

// Windows keeps the primary at the origin, so a growing target that is not the
// primary is planned in two moves: the neighbours (the primary among them) are
// pushed outward, and the whole arrangement is then translated back onto the
// origin. The translation's sign is the part that has never run on hardware -- get
// it backwards and the target lands on top of the primary instead of beside it.
func TestPlanModeChangeAnchorsThePrimaryWhenANonPrimaryTargetGrows(t *testing.T) {
	tests := map[string]struct {
		layout domain.Layout
		mode   domain.Mode
		want   map[string]domain.Point
	}{
		"target grows to the left of the primary": {
			layout: domain.Layout{Displays: []domain.DisplayState{
				{DeviceName: `\.\DISPLAY9`, Mode: miMonitorGame, Position: domain.Point{X: -1920, Y: 0}},
				{DeviceName: `\.\DISPLAY1`, Mode: sideMode, Position: domain.Point{X: 0, Y: 0}, Primary: true},
			}},
			mode: miMonitorNative,
			want: map[string]domain.Point{
				`\.\DISPLAY9`: {X: -2560, Y: 0},
				`\.\DISPLAY1`: {X: 0, Y: 0},
			},
		},
		"target grows above the primary": {
			layout: domain.Layout{Displays: []domain.DisplayState{
				{DeviceName: `\.\DISPLAY9`, Mode: topMode, Position: domain.Point{X: 0, Y: -1080}},
				{DeviceName: `\.\DISPLAY1`, Mode: miMonitorGame, Position: domain.Point{X: 0, Y: 0}, Primary: true},
			}},
			mode: miMonitorGame,
			want: map[string]domain.Point{
				`\.\DISPLAY9`: {X: 0, Y: -1440},
				`\.\DISPLAY1`: {X: 0, Y: 0},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			plan, err := PlanModeChange(tt.layout, `\.\DISPLAY9`, tt.mode)
			if err != nil {
				t.Fatal(err)
			}
			if got := positions(t, plan); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("positions = %+v, want %+v", got, tt.want)
			}
			assertNoOverlaps(t, layoutFrom(plan, tt.layout).Displays)
		})
	}
}

// A picker lists every mode the monitor reports, and plenty of them are wider and
// shorter than the one it is running: 1920x1440 to 2560x1080 grows one axis while
// it frees the other. The two axes are planned independently and must stay that
// way -- the displays to the right move outward by the width delta in the same plan
// that moves the display below inward by the height delta.
func TestPlanModeChangeHandlesATargetThatGrowsInOneAxisAndShrinksInTheOther(t *testing.T) {
	layout := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY1`, Mode: miMonitorGame, Position: domain.Point{}, Primary: true},
		{DeviceName: `\.\DISPLAY2`, Mode: topMode, Position: domain.Point{X: 1920, Y: 0}},
		{DeviceName: `\.\DISPLAY6`, Mode: topMode, Position: domain.Point{X: 0, Y: 1440}},
	}}
	plan, err := PlanModeChange(layout, `\.\DISPLAY1`, ultrawideMode)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]domain.Point{
		`\.\DISPLAY1`: {X: 0, Y: 0},
		`\.\DISPLAY2`: {X: 2560, Y: 0},
		`\.\DISPLAY6`: {X: 0, Y: 1080},
	}
	if got := positions(t, plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("positions = %+v, want %+v", got, want)
	}
	assertNoOverlaps(t, layoutFrom(plan, layout).Displays)
}

// "No safe arrangement" stops being theoretical the moment the user picks the mode:
// a shrinking axis closes the gaps the other displays stand in, and two of them can
// close onto each other. The refusal is correct, but a refusal the user cannot act
// on is only half of one -- the message has to say which two displays collided, not
// merely that the desktop cannot be arranged.
//
// A purely growing target cannot produce this: with both deltas outward no pair's
// separation ever decreases. It takes a mode that gives an axis back.
func TestValidateArrangementNamesBothDisplaysThatWouldOverlap(t *testing.T) {
	// DISPLAY2 stands beyond the target's right edge and overlaps its band, so the
	// narrowing target pulls it left. DISPLAY6 is below that band and stays where it
	// is. They clear each other now and close onto each other afterwards, which is
	// the shape no ordering can rescue.
	layout := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY1`, Mode: miMonitorNative, Position: domain.Point{}, Primary: true},
		{DeviceName: `\.\DISPLAY2`, Mode: portraitMode, Position: domain.Point{X: 2560, Y: 0}},
		{DeviceName: `\.\DISPLAY6`, Mode: topMode, Position: domain.Point{X: 640, Y: 1500}},
	}}
	plan, err := PlanModeChange(layout, `\.\DISPLAY1`, miMonitorGame)
	if !errors.Is(err, ErrLayoutUnsafe) {
		t.Fatalf("err = %v, want ErrLayoutUnsafe", err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("a refused plan still carries changes: %+v", plan)
	}
	for _, device := range []string{`\.\DISPLAY2`, `\.\DISPLAY6`} {
		if !strings.Contains(err.Error(), device) {
			t.Fatalf("err = %q, which never names %s", err, device)
		}
	}
	if strings.Contains(err.Error(), `\.\DISPLAY1`) {
		t.Fatalf("err = %q names a display that is not part of the collision", err)
	}
}

// The defect found by picking the second monitor: the target is in the row above
// the primary, and the display beside the primary sits at a larger X than it. A
// rule that only compares coordinates pulls that display left, into the primary,
// and every plan is refused with a message naming two displays the user never
// touched. These are the real coordinates from the desk it was found on.
func TestPlanModeChangeLeavesAnotherRowAloneWhenTheTargetIsNotThePrimary(t *testing.T) {
	desktop := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY1`, Mode: miMonitorNative, Position: domain.Point{X: 0, Y: 0}, Primary: true},
		{DeviceName: `\.\DISPLAY2`, Mode: domain.Mode{Width: 3840, Height: 2160, RefreshHz: 160, BitsPerPixel: 32},
			Position: domain.Point{X: 2560, Y: 0}},
		{DeviceName: `\.\DISPLAY3`, Mode: topMode, Position: domain.Point{X: 636, Y: -1080}},
		{DeviceName: `\.\DISPLAY5`, Mode: topMode, Position: domain.Point{X: 2556, Y: -1080}},
	}}
	narrower := domain.Mode{Width: 1440, Height: 1080, RefreshHz: 60, BitsPerPixel: 32}

	plan, err := PlanModeChange(desktop, `\.\DISPLAY5`, narrower)
	if err != nil {
		t.Fatalf("planning a mode for the top-right monitor was refused: %v", err)
	}

	got := positions(t, plan)
	for _, device := range []string{`\.\DISPLAY1`, `\.\DISPLAY2`, `\.\DISPLAY3`} {
		was, _ := desktop.Find(device)
		if got[device] != was.Position {
			t.Fatalf("%s moved from %+v to %+v; only the target changed size",
				device, was.Position, got[device])
		}
	}
	if got[`\.\DISPLAY5`] != (domain.Point{X: 2556, Y: -1080}) {
		t.Fatalf("the target moved to %+v; nothing stands beyond the edge it gave back", got[`\.\DISPLAY5`])
	}
	assertNoOverlaps(t, layoutFrom(plan, desktop).Displays)
}

// The other half of the rule. A display can sit beyond the edge that moved and
// still have nothing to do with the target, because it stands in another band
// entirely. Both conditions are needed: either one alone drags a display that was
// never in the way.
func TestPlanModeChangeOnlySlidesDisplaysInTheTargetsOwnBand(t *testing.T) {
	desktop := domain.Layout{Displays: []domain.DisplayState{
		{DeviceName: `\.\DISPLAY1`, Mode: miMonitorNative, Position: domain.Point{X: 0, Y: 0}, Primary: true},
		// Beyond the target's right edge and level with it: this one slides.
		{DeviceName: `\.\DISPLAY2`, Mode: sideMode, Position: domain.Point{X: 2560, Y: 0}},
		// Beyond the same edge, but a whole row above: nothing opens beside it.
		{DeviceName: `\.\DISPLAY8`, Mode: topMode, Position: domain.Point{X: 2560, Y: -1080}},
	}}

	plan, err := PlanModeChange(desktop, `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	got := positions(t, plan)
	if got[`\.\DISPLAY2`] != (domain.Point{X: 1920, Y: 0}) {
		t.Fatalf("the display level with the target is at %+v, want it to close the 640px gap", got[`\.\DISPLAY2`])
	}
	if got[`\.\DISPLAY8`] != (domain.Point{X: 2560, Y: -1080}) {
		t.Fatalf("the display a row above moved to %+v; no gap opened beside it", got[`\.\DISPLAY8`])
	}
	assertNoOverlaps(t, layoutFrom(plan, desktop).Displays)
}
