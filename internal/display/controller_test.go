package display

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
)

type nativeTest struct {
	deviceName string
	mode       domain.Mode
}

type fakeNative struct {
	targets    []domain.Target
	mode       domain.Mode
	layout     domain.Layout
	listErr    error
	currentErr error
	layoutErr  error
	testErr    error
	applyErr   error
	currentFor string
	tests      []nativeTest
	applied    []domain.LayoutPlan
}

func (f *fakeNative) listTargets() ([]domain.Target, error) {
	return f.targets, f.listErr
}

func (f *fakeNative) currentMode(deviceName string) (domain.Mode, error) {
	f.currentFor = deviceName
	return f.mode, f.currentErr
}

func (f *fakeNative) currentLayout() (domain.Layout, error) {
	return f.layout, f.layoutErr
}

func (f *fakeNative) testMode(deviceName string, mode domain.Mode) error {
	f.tests = append(f.tests, nativeTest{deviceName: deviceName, mode: mode})
	return f.testErr
}

func (f *fakeNative) applyLayout(plan domain.LayoutPlan) error {
	f.applied = append(f.applied, plan)
	return f.applyErr
}

// ResolveTarget is the enumeration plus the pure matcher and nothing else: the
// identity reaches resolveIdentity unaltered, and the target that comes back carries
// the rung that matched. The ladder's own behaviour is covered by the
// TestResolveIdentity... tests, which need no adapter at all.
func TestResolveTargetMatchesTheConfiguredIdentity(t *testing.T) {
	api := &fakeNative{targets: []domain.Target{
		{DeviceName: `\.\DISPLAY2`, Identity: domain.MonitorIdentity{HardwareID: `MONITOR\ACR0D0D\0004`}},
		{DeviceName: `\.\DISPLAY1`, Identity: domain.MonitorIdentity{HardwareID: `monitor\xmi27b2\0009`}},
	}}
	c := newController(api)

	got, err := c.ResolveTarget(domain.MonitorIdentity{
		HardwareID: `MONITOR\XMI27B2`, ModelWasUnique: true,
	})
	if err != nil || got.DeviceName != `\.\DISPLAY1` {
		t.Fatalf("target=%#v err=%v", got, err)
	}
	if got.MatchedBy != domain.MatchHardwareID {
		t.Fatalf("MatchedBy=%d, want the rung that answered", got.MatchedBy)
	}
}

func TestResolveTargetReturnsSentinelWhenNoTargetMatches(t *testing.T) {
	api := &fakeNative{targets: []domain.Target{{
		DeviceName: `\.\DISPLAY2`, Identity: domain.MonitorIdentity{HardwareID: `MONITOR\ACR0D0D\0004`},
	}}}
	c := newController(api)

	_, err := c.ResolveTarget(domain.MonitorIdentity{
		HardwareID: `MONITOR\XMI27B2`, ModelWasUnique: true,
	})
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestCurrentModeUsesOnlyResolvedDevice(t *testing.T) {
	mode := domain.Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	api := &fakeNative{mode: mode}
	c := newController(api)
	target := domain.Target{DeviceName: `\.\DISPLAY1`, Identity: domain.MonitorIdentity{HardwareID: `MONITOR\XMI27B2\0009`}}

	got, err := c.CurrentMode(target)
	if err != nil || got != mode {
		t.Fatalf("mode=%#v err=%v", got, err)
	}
	if api.currentFor != target.DeviceName {
		t.Fatalf("device=%q", api.currentFor)
	}
}

func TestCurrentLayoutReportsEveryDisplayItRead(t *testing.T) {
	api := &fakeNative{layout: measuredLayout()}
	c := newController(api)

	got, err := c.CurrentLayout()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, measuredLayout()) {
		t.Fatalf("layout=%#v", got)
	}
}

// The pre-flight is the only call that may name a single device, and it must be the
// resolved target with the requested mode.
func TestTestModeUsesOnlyResolvedDevice(t *testing.T) {
	api := &fakeNative{}
	c := newController(api)
	target := domain.Target{DeviceName: `\.\DISPLAY1`, Identity: domain.MonitorIdentity{HardwareID: `MONITOR\XMI27B2\0009`}}
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}

	if err := c.TestMode(target, mode); err != nil {
		t.Fatal(err)
	}
	want := []nativeTest{{deviceName: target.DeviceName, mode: mode}}
	if !reflect.DeepEqual(api.tests, want) {
		t.Fatalf("tests=%#v", api.tests)
	}
	if len(api.applied) != 0 {
		t.Fatalf("the pre-flight applied something: %#v", api.applied)
	}
}

// The whole arrangement reaches the native layer as one plan. The native layer is
// what chooses the order the displays are applied in, puts back what it already
// changed when one of them fails, and checks the desktop it produced; splitting the
// plan up here would take all three away from it.
func TestApplyLayoutHandsTheWholePlanToTheNativeLayer(t *testing.T) {
	api := &fakeNative{}
	c := newController(api)
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	if err := c.ApplyLayout(plan); err != nil {
		t.Fatal(err)
	}
	if len(api.applied) != 1 || !reflect.DeepEqual(api.applied[0], plan) {
		t.Fatalf("applied=%#v", api.applied)
	}
	if len(api.tests) != 0 {
		t.Fatalf("apply ran a pre-flight of its own: %#v", api.tests)
	}
}

func TestTestModeWrapsPreflightRejectionWithSentinel(t *testing.T) {
	rejection := errors.New(`ChangeDisplaySettingsExW(\.\DISPLAY1): display mode is not supported`)
	api := &fakeNative{testErr: rejection}
	c := newController(api)
	target := domain.Target{DeviceName: `\.\DISPLAY1`, Identity: domain.MonitorIdentity{HardwareID: `MONITOR\XMI27B2\0009`}}
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}

	err := c.TestMode(target, mode)
	if !errors.Is(err, ErrModeNotSupported) {
		t.Fatalf("err=%v does not wrap ErrModeNotSupported", err)
	}
	if !errors.Is(err, rejection) {
		t.Fatalf("err=%v lost the underlying Win32 diagnostic", err)
	}
	if len(api.tests) != 1 {
		t.Fatalf("tests=%#v", api.tests)
	}
}

// A failed apply must stay distinguishable from an unsupported mode: only the
// pre-flight rejection disables the 4:3 control, a one-off apply failure is retryable.
func TestApplyLayoutFailureIsNotReportedAsUnsupportedMode(t *testing.T) {
	failure := errors.New("driver failed the display mode change")
	api := &fakeNative{applyErr: failure}
	c := newController(api)
	plan, err := PlanModeChange(measuredLayout(), `\.\DISPLAY1`, miMonitorGame)
	if err != nil {
		t.Fatal(err)
	}

	err = c.ApplyLayout(plan)
	if !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	if errors.Is(err, ErrModeNotSupported) {
		t.Fatalf("apply failure reported as unsupported mode: %v", err)
	}
}

// The identity fixtures are whole interface paths rather than short stand-ins
// because the primary key is compared whole: against an abbreviated fixture a
// prefix match and an equality match are indistinguishable, and telling those two
// apart is the entire point of this ladder.
//
// The UID... segment names the graphics card output port, so unitA and unitB are two
// physical monitors of one model plugged into two ports.
const (
	unitAPath = `\\?\DISPLAY#XMI27B2#5&2b9d4d4&0&UID4357#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`
	unitBPath = `\\?\DISPLAY#XMI27B2#5&2b9d4d4&0&UID4358#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`
	acerPath  = `\\?\DISPLAY#ACR0D0D#5&2b9d4d4&0&UID4359#{e6f07b5f-ee97-4a90-b076-33f57bf4eaa7}`

	miModelHardwareID   = `MONITOR\XMI27B2`
	acerModelHardwareID = `MONITOR\ACR0D0D`
)

// attachedMonitor is one row of what listTargets reports: the adapter Win32 accepts,
// and the identity read off the monitor behind it. MatchedBy is deliberately absent
// -- enumeration reports hardware facts, and which rung matched is the resolver's
// answer, not the driver's.
func attachedMonitor(deviceName, instancePath, hardwareID string) domain.Target {
	return domain.Target{
		DeviceName: deviceName,
		Identity: domain.MonitorIdentity{
			InstancePath: instancePath,
			HardwareID:   hardwareID,
			Label:        "fixture monitor",
		},
	}
}

// The primary key is the interface path, compared whole and case-insensitively.
// Windows is inconsistent about the case it reports these in, so a case-sensitive
// comparison would lose the monitor for no reason; a prefix comparison would find
// the wrong one, which is why the list carries a monitor whose path merely starts
// with the configured one.
func TestResolveIdentityPrefersTheInstancePathWholeAndCaseInsensitively(t *testing.T) {
	targets := []domain.Target{
		attachedMonitor(`\.\DISPLAY2`, acerPath, `MONITOR\ACR0D0D\0004`),
		attachedMonitor(`\.\DISPLAY1`, strings.ToLower(unitAPath), `MONITOR\XMI27B2\0009`),
		attachedMonitor(`\.\DISPLAY3`, unitAPath+`#EXTRA`, `MONITOR\XMI27B2\0011`),
	}
	// ModelWasUnique is false so that only the primary key can possibly answer this.
	want := domain.MonitorIdentity{InstancePath: unitAPath, HardwareID: miModelHardwareID}

	got, err := resolveIdentity(targets, want)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceName != `\.\DISPLAY1` {
		t.Fatalf("DeviceName=%q, want the monitor whose whole interface path matches", got.DeviceName)
	}
	if got.MatchedBy != domain.MatchInstancePath {
		t.Fatalf("MatchedBy=%d, want MatchInstancePath", got.MatchedBy)
	}
}

// No rung is allowed to take the first match. Duplicate full interface paths should
// not normally come from Windows, but if enumeration ever reports them, differing
// letter case does not make either candidate safer to choose.
func TestResolveIdentityRefusesDuplicateWholeInstancePaths(t *testing.T) {
	targets := []domain.Target{
		attachedMonitor(`\.\DISPLAY1`, strings.ToLower(unitAPath), `MONITOR\XMI27B2\0009`),
		attachedMonitor(`\.\DISPLAY3`, strings.ToUpper(unitAPath), `MONITOR\XMI27B2\0011`),
	}
	want := domain.MonitorIdentity{
		InstancePath:   unitAPath,
		HardwareID:     miModelHardwareID,
		ModelWasUnique: true,
	}

	got, err := resolveIdentity(targets, want)
	if !errors.Is(err, ErrTargetAmbiguous) {
		t.Fatalf("err=%v, want ErrTargetAmbiguous", err)
	}
	if got != (domain.Target{}) {
		t.Fatalf("target=%#v, want nothing resolved", got)
	}
}

// The configured keys are opaque Win32 identifiers. Whole-string equality means
// whitespace is data too: silently trimming a hand-edited value could turn a typo
// into a match and apply a mode to a monitor the stored identity does not name.
func TestResolveIdentityDoesNotNormalizeConfiguredKeys(t *testing.T) {
	for name, want := range map[string]domain.MonitorIdentity{
		"instance path": {
			InstancePath:   unitAPath + " ",
			HardwareID:     miModelHardwareID,
			ModelWasUnique: false, // keep the hardware-ID fallback deliberately disabled
		},
		"hardware id": {
			HardwareID:     miModelHardwareID + " ",
			ModelWasUnique: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			targets := []domain.Target{
				attachedMonitor(`\.\DISPLAY1`, unitAPath, `MONITOR\XMI27B2\0009`),
			}

			got, err := resolveIdentity(targets, want)
			if !errors.Is(err, ErrTargetNotFound) {
				t.Fatalf("target=%#v err=%v, want an exact-key refusal", got, err)
			}
		})
	}
}

// Moving the cable to another port changes the UID segment and therefore the primary
// key. The secondary rung exists for exactly that, and the stored hardware ID is the
// model alone (MONITOR\XMI27B2) while the driver reports the model plus instance
// detail (MONITOR\XMI27B2\0009), so the comparison is on the model portion of both.
func TestResolveIdentityFallsBackToTheHardwareIDWhenItMatchesExactlyOne(t *testing.T) {
	targets := []domain.Target{
		attachedMonitor(`\.\DISPLAY2`, acerPath, `MONITOR\ACR0D0D\0004`),
		attachedMonitor(`\.\DISPLAY3`, unitBPath, `monitor\xmi27b2\0009`),
	}
	want := domain.MonitorIdentity{
		InstancePath:   unitAPath, // the port it used to be plugged into
		HardwareID:     miModelHardwareID,
		ModelWasUnique: true,
	}

	got, err := resolveIdentity(targets, want)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceName != `\.\DISPLAY3` {
		t.Fatalf("DeviceName=%q, want the one monitor of that model", got.DeviceName)
	}
	if got.MatchedBy != domain.MatchHardwareID {
		t.Fatalf("MatchedBy=%d, want MatchHardwareID", got.MatchedBy)
	}
	if got.Identity.InstancePath != unitBPath {
		t.Fatalf("InstancePath=%q, want the path the monitor reports now", got.Identity.InstancePath)
	}
}

// Two monitors of one model, neither of them the configured unit's port: the
// secondary key identifies a model, not a unit, so it cannot choose. Taking the
// first would apply the game mode to whichever screen Windows happened to enumerate
// first. The refusal has to name the candidates, because reselecting is the only way
// out of it and the user needs to know which screens are involved.
func TestResolveIdentityRefusesTheHardwareIDWhenItMatchesTwoMonitors(t *testing.T) {
	targets := []domain.Target{
		attachedMonitor(`\.\DISPLAY1`, unitBPath, `MONITOR\XMI27B2\0009`),
		attachedMonitor(`\.\DISPLAY2`, acerPath, `MONITOR\ACR0D0D\0004`),
		attachedMonitor(`\.\DISPLAY3`, `\\?\DISPLAY#XMI27B2#5&9&0&UID4360#{guid}`, `MONITOR\XMI27B2\0012`),
	}
	want := domain.MonitorIdentity{
		InstancePath:   unitAPath,
		HardwareID:     miModelHardwareID,
		ModelWasUnique: true,
	}

	got, err := resolveIdentity(targets, want)
	if !errors.Is(err, ErrTargetAmbiguous) {
		t.Fatalf("err=%v, want ErrTargetAmbiguous", err)
	}
	if got != (domain.Target{}) {
		t.Fatalf("target=%#v, want nothing resolved", got)
	}
	for _, device := range []string{`\.\DISPLAY1`, `\.\DISPLAY3`} {
		if !strings.Contains(err.Error(), device) {
			t.Errorf("err=%q does not name candidate %s", err, device)
		}
	}
	if strings.Contains(err.Error(), `\.\DISPLAY2`) {
		t.Errorf("err=%q names a monitor of another model", err)
	}
}

// The counter-intuitive rung. The user configured this profile while two monitors of
// the model were attached, so ModelWasUnique was recorded false, and a single hit
// afterwards most likely means the other one is asleep, switched to another input or
// unplugged -- not that this is the one they picked. Applying the game mode to the
// wrong screen is worse than asking them to reselect, so even an unambiguous hit is
// refused, permanently, for this profile.
func TestResolveIdentityDisablesTheHardwareIDRungWhenTheModelWasNotUnique(t *testing.T) {
	targets := []domain.Target{
		attachedMonitor(`\.\DISPLAY1`, unitBPath, `MONITOR\XMI27B2\0009`),
	}
	want := domain.MonitorIdentity{
		InstancePath:   unitAPath, // the twin that is not attached right now
		HardwareID:     miModelHardwareID,
		ModelWasUnique: false,
	}

	got, err := resolveIdentity(targets, want)
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("err=%v, want ErrTargetNotFound", err)
	}
	if got != (domain.Target{}) {
		t.Fatalf("target=%#v, want nothing resolved", got)
	}
	if errors.Is(err, ErrTargetAmbiguous) {
		t.Fatalf("err=%v reports ambiguity for a single hit", err)
	}
}

// With both units of the model live, whole-string equality on the interface path is
// what keeps the mode on the one the user chose. This is the desk the prefix matcher
// silently got wrong.
func TestResolveIdentityPicksOnlyTheChosenUnitWhenTwoOfTheModelAreLive(t *testing.T) {
	targets := []domain.Target{
		attachedMonitor(`\.\DISPLAY1`, unitAPath, `MONITOR\XMI27B2\0009`),
		attachedMonitor(`\.\DISPLAY2`, unitBPath, `MONITOR\XMI27B2\0011`),
	}
	want := domain.MonitorIdentity{
		InstancePath: unitBPath,
		HardwareID:   miModelHardwareID,
	}

	got, err := resolveIdentity(targets, want)
	if err != nil {
		t.Fatal(err)
	}
	if got.DeviceName != `\.\DISPLAY2` || got.Identity.InstancePath != unitBPath {
		t.Fatalf("target=%#v, want only the chosen unit", got)
	}
}

// MatchedBy is how the session can later tell the user that a profile was recovered
// through the secondary key rather than recognised outright, and how Task 11 knows
// there is a new instance path worth writing back. An unresolved list reports
// MatchNone and a resolver that forgot to stamp the level would too, so the
// zero-value case is asserted as well.
func TestResolveIdentityReportsWhichRungMatched(t *testing.T) {
	byPath := []domain.Target{attachedMonitor(`\.\DISPLAY1`, unitAPath, `MONITOR\XMI27B2\0009`)}
	byModel := []domain.Target{attachedMonitor(`\.\DISPLAY1`, unitBPath, `MONITOR\XMI27B2\0009`)}

	for name, tt := range map[string]struct {
		targets []domain.Target
		want    domain.MonitorIdentity
		level   domain.MatchLevel
	}{
		"instance path": {
			targets: byPath,
			want:    domain.MonitorIdentity{InstancePath: unitAPath, HardwareID: miModelHardwareID},
			level:   domain.MatchInstancePath,
		},
		"hardware id": {
			targets: byModel,
			want: domain.MonitorIdentity{
				InstancePath: unitAPath, HardwareID: miModelHardwareID, ModelWasUnique: true,
			},
			level: domain.MatchHardwareID,
		},
		"hardware id with no stored path": {
			targets: byModel,
			want:    domain.MonitorIdentity{HardwareID: miModelHardwareID, ModelWasUnique: true},
			level:   domain.MatchHardwareID,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if tt.targets[0].MatchedBy != domain.MatchNone {
				t.Fatalf("precondition: enumeration stamped a match level")
			}
			got, err := resolveIdentity(tt.targets, tt.want)
			if err != nil {
				t.Fatal(err)
			}
			if got.MatchedBy != tt.level {
				t.Fatalf("MatchedBy=%d, want %d", got.MatchedBy, tt.level)
			}
		})
	}
}

// \\.\DISPLAYn names the adapter, not the monitor, and it is what
// ChangeDisplaySettingsExW takes. Under clone or mirror one adapter drives several
// monitors, so applying the game mode would change all of them. listTargets reports
// one target per monitor, so a second target carrying the same DeviceName is exactly
// that situation, and the honest answer is to refuse and say why rather than change
// a screen the user did not configure.
func TestResolveIdentityRefusesAnAdapterThatStillDrivesAnotherLiveMonitor(t *testing.T) {
	targets := []domain.Target{
		attachedMonitor(`\.\DISPLAY1`, unitAPath, `MONITOR\XMI27B2\0009`),
		attachedMonitor(`\.\DISPLAY1`, acerPath, `MONITOR\ACR0D0D\0004`),
	}
	want := domain.MonitorIdentity{InstancePath: unitAPath, HardwareID: miModelHardwareID}

	got, err := resolveIdentity(targets, want)
	if !errors.Is(err, ErrTargetMirrored) {
		t.Fatalf("err=%v, want ErrTargetMirrored", err)
	}
	if got != (domain.Target{}) {
		t.Fatalf("target=%#v, want nothing resolved", got)
	}
	if !strings.Contains(err.Error(), `\.\DISPLAY1`) {
		t.Errorf("err=%q does not name the shared display device", err)
	}
	if !strings.Contains(err.Error(), acerPath) {
		t.Errorf("err=%q does not name the other monitor on it", err)
	}
}

// Not found is a first-class answer, not a reason to improvise. A monitor that is
// asleep or switched to another input is missing from the list, and the tool's
// response is to disable the controls and keep the profile -- never to rebind to
// whatever is attached, not even when exactly one screen is.
func TestResolveIdentityReturnsNotFoundWithoutGuessing(t *testing.T) {
	for name, targets := range map[string][]domain.Target{
		"one unrelated monitor": {attachedMonitor(`\.\DISPLAY1`, acerPath, `MONITOR\ACR0D0D\0004`)},
		"nothing attached":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			want := domain.MonitorIdentity{
				InstancePath:   unitAPath,
				HardwareID:     miModelHardwareID,
				ModelWasUnique: true,
			}

			got, err := resolveIdentity(targets, want)
			if !errors.Is(err, ErrTargetNotFound) {
				t.Fatalf("err=%v, want ErrTargetNotFound", err)
			}
			if got != (domain.Target{}) {
				t.Fatalf("target=%#v, want nothing resolved", got)
			}
		})
	}
}

// An identity carrying neither key cannot be matched by anything, and the one thing
// it must not do is match everything. The configuration parser refuses such a
// monitor, so this is the resolver's own second line of defence.
func TestResolveIdentityRefusesAnIdentityWithNeitherKey(t *testing.T) {
	targets := []domain.Target{attachedMonitor(`\.\DISPLAY1`, unitAPath, `MONITOR\XMI27B2\0009`)}

	got, err := resolveIdentity(targets, domain.MonitorIdentity{ModelWasUnique: true, Label: "只有名字"})
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("err=%v, want ErrTargetNotFound", err)
	}
	if got != (domain.Target{}) {
		t.Fatalf("target=%#v, want nothing resolved", got)
	}
}
