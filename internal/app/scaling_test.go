package app

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	"github.com/Alien7666/change_resolution/internal/scaling"
)

var errTask15StaleDisplayName = errors.New("display device name no longer exists")

// task15Recorder is shared by both sides of the integration fake. It makes the
// cross-subsystem order visible without teaching either fake about a Session.
type task15Recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *task15Recorder) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *task15Recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := append([]string(nil), r.events...)
	r.events = nil
	return events
}

// task15DisplayFake wraps the established session fake with the hazard that matters
// to Task 15: once NVAPI has invalidated GDI names, any Target or LayoutPlan retained
// from before that set is rejected instead of accidentally changing another screen.
type task15DisplayFake struct {
	base       *fakeDisplay
	recorder   *task15Recorder
	generation uint32
}

var _ display.Controller = (*task15DisplayFake)(nil)

func (d *task15DisplayFake) Targets() ([]domain.Target, error) {
	d.recorder.record("display.targets")
	return d.base.Targets()
}

func (d *task15DisplayFake) ResolveTarget(identity domain.MonitorIdentity) (domain.Target, error) {
	d.recorder.record("display.resolve")
	return d.base.ResolveTarget(identity)
}

func (d *task15DisplayFake) CurrentMode(target domain.Target) (domain.Mode, error) {
	d.recorder.record("display.current")
	if err := d.requireCurrentName(target.DeviceName); err != nil {
		return domain.Mode{}, err
	}
	return d.base.CurrentMode(target)
}

func (d *task15DisplayFake) EnumModes(target domain.Target) ([]domain.Mode, error) {
	d.recorder.record("display.modes")
	if err := d.requireCurrentName(target.DeviceName); err != nil {
		return nil, err
	}
	return d.base.EnumModes(target)
}

func (d *task15DisplayFake) CurrentLayout() (domain.Layout, error) {
	d.recorder.record("display.layout")
	return d.base.CurrentLayout()
}

func (d *task15DisplayFake) TestMode(target domain.Target, mode domain.Mode) error {
	d.recorder.record("display.test")
	if err := d.requireCurrentName(target.DeviceName); err != nil {
		return err
	}
	return d.base.TestMode(target, mode)
}

func (d *task15DisplayFake) ApplyLayout(plan domain.LayoutPlan) error {
	d.recorder.record("display.apply")
	for _, change := range plan.Changes {
		if err := d.requireCurrentName(change.DeviceName); err != nil {
			return err
		}
	}
	return d.base.ApplyLayout(plan)
}

func (d *task15DisplayFake) requireCurrentName(deviceName string) error {
	d.base.mu.Lock()
	defer d.base.mu.Unlock()
	for _, state := range d.base.layout.Displays {
		if state.DeviceName == deviceName {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", errTask15StaleDisplayName, deviceName)
}

// renameAll models the documented NVAPI side effect. Layout and Targets move to new
// GDI names together, while the physical MonitorIdentity carried by each target is
// copied untouched. Repeated sets rename repeatedly, so no earlier generation is safe.
func (d *task15DisplayFake) renameAll() {
	d.base.mu.Lock()
	defer d.base.mu.Unlock()

	d.generation++
	names := make(map[string]string, len(d.base.layout.Displays))
	next := uint32(700) + d.generation*100
	nameFor := func(old string) string {
		if renamed, ok := names[old]; ok {
			return renamed
		}
		renamed := fmt.Sprintf(`\.\DISPLAY%d`, next)
		next++
		names[old] = renamed
		return renamed
	}

	displays := append([]domain.DisplayState(nil), d.base.layout.Displays...)
	for i := range displays {
		displays[i].DeviceName = nameFor(displays[i].DeviceName)
	}
	targets := append([]domain.Target(nil), d.base.targets...)
	for i := range targets {
		targets[i].DeviceName = nameFor(targets[i].DeviceName)
	}
	d.base.target.DeviceName = nameFor(d.base.target.DeviceName)
	d.base.layout = domain.Layout{Displays: displays}
	d.base.targets = targets
	d.recorder.record("display.renumber")
}

type task15ScalingResult struct {
	outcome scaling.Outcome
	err     error
}

type task15ScalingCall struct {
	operation         string
	identity          domain.MonitorIdentity
	value             scaling.Value
	expectedDisplayID uint32
}

// task15ScalingFake scripts controller outcomes but owns the renumbering rule. Tests
// cannot forget to invalidate GDI names on one call site: Apply and Restore both pass
// through finishSet, and every formal set attempt invalidates them even on an error.
type task15ScalingFake struct {
	mu sync.Mutex

	recorder *task15Recorder
	displays *task15DisplayFake

	availability scaling.Availability
	state        scaling.State
	readErr      error
	closeErr     error
	closed       bool

	applyResults   []task15ScalingResult
	restoreResults []task15ScalingResult
	calls          []task15ScalingCall
}

var _ scaling.Controller = (*task15ScalingFake)(nil)

func (s *task15ScalingFake) Probe() scaling.Availability {
	s.recorder.record("scaling.probe")
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.availability
}

func (s *task15ScalingFake) Read(identity domain.MonitorIdentity) (scaling.State, error) {
	s.recorder.record("scaling.read")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, task15ScalingCall{operation: "read", identity: identity})
	return s.state, s.readErr
}

func (s *task15ScalingFake) Apply(identity domain.MonitorIdentity, value scaling.Value) (scaling.Outcome, error) {
	s.recorder.record("scaling.apply")
	result := s.nextResult("apply", identity, 0, value)
	s.finishSet(result.outcome)
	return result.outcome, result.err
}

func (s *task15ScalingFake) Restore(
	identity domain.MonitorIdentity,
	expectedDisplayID uint32,
	value scaling.Value,
) (scaling.Outcome, error) {
	s.recorder.record("scaling.restore")
	result := s.nextResult("restore", identity, expectedDisplayID, value)
	s.finishSet(result.outcome)
	return result.outcome, result.err
}

func (s *task15ScalingFake) Close() error {
	s.recorder.record("scaling.close")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.closeErr
}

func (s *task15ScalingFake) nextResult(
	operation string,
	identity domain.MonitorIdentity,
	expectedDisplayID uint32,
	value scaling.Value,
) task15ScalingResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, task15ScalingCall{
		operation: operation, identity: identity, value: value, expectedDisplayID: expectedDisplayID,
	})
	var result task15ScalingResult
	if operation == "restore" && len(s.restoreResults) != 0 {
		result, s.restoreResults = s.restoreResults[0], s.restoreResults[1:]
		return result
	}
	if operation == "apply" && len(s.applyResults) != 0 {
		result, s.applyResults = s.applyResults[0], s.applyResults[1:]
		return result
	}
	result.outcome = scaling.Outcome{
		Requested:     value,
		Previous:      s.state,
		State:         s.state,
		ReadBackKnown: true,
		Matched:       s.state.Effective == value,
	}
	return result
}

func (s *task15ScalingFake) finishSet(outcome scaling.Outcome) {
	if !outcome.SetAttempted {
		return
	}
	s.displays.renameAll()
}

func (s *task15ScalingFake) queueApply(outcome scaling.Outcome, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyResults = append(s.applyResults, task15ScalingResult{outcome: outcome, err: err})
}

func (s *task15ScalingFake) queueRestore(outcome scaling.Outcome, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restoreResults = append(s.restoreResults, task15ScalingResult{outcome: outcome, err: err})
}

type task15FakeRig struct {
	recorder  *task15Recorder
	displays  *task15DisplayFake
	scaling   *task15ScalingFake
	identity  domain.MonitorIdentity
	oldTarget domain.Target
}

func newTask15FakeRig(t *testing.T) *task15FakeRig {
	t.Helper()
	profile := domain.LegacySeedProfile()
	layout := fixtureLayout(fixtureNative)
	targets := make([]domain.Target, len(layout.Displays))
	for i, state := range layout.Displays {
		targets[i] = domain.Target{
			DeviceName: state.DeviceName,
			Identity: domain.MonitorIdentity{
				InstancePath: "task15-instance:" + state.DeviceName,
				HardwareID:   "MONITOR\\TASK15\\" + state.DeviceName,
			},
		}
	}
	target, ok := targetByDevice(targets, targetDevice)
	if !ok {
		t.Fatal("Task 15 fake layout has no configured target")
	}
	target.Identity.HardwareID = profile.Monitor.HardwareID
	for i := range targets {
		if targets[i].DeviceName == target.DeviceName {
			targets[i] = target
		}
	}
	recorder := &task15Recorder{}
	base := &fakeDisplay{
		target: target, targets: targets, current: fixtureNative,
		modes: []domain.Mode{fixtureNative}, layout: layout, fail: make(map[string]error),
	}
	displays := &task15DisplayFake{base: base, recorder: recorder}
	scalings := &task15ScalingFake{
		recorder:     recorder,
		displays:     displays,
		availability: scaling.Availability{Available: true},
		state: scaling.State{
			DisplayID: 41,
			Effective: scaling.Value{Raw: 2, Mode: scaling.ModeAspectRatio, By: scaling.ByGPU},
		},
	}
	return &task15FakeRig{
		recorder: recorder, displays: displays, scaling: scalings,
		identity: profile.Monitor, oldTarget: target,
	}
}

func task15SetOutcome(state scaling.State, requested scaling.Value, applied bool) scaling.Outcome {
	return scaling.Outcome{
		Requested: requested, Previous: state, State: state,
		SetAttempted: true, Applied: applied, ReadBackKnown: true,
		Matched: state.Effective == requested,
	}
}

func TestTask15ScalingFakesRenameAfterEveryActualApplyAndRestore(t *testing.T) {
	tests := map[string]func(*task15FakeRig, scaling.Value) error{
		"apply": func(rig *task15FakeRig, value scaling.Value) error {
			rig.scaling.queueApply(task15SetOutcome(rig.scaling.state, value, true), nil)
			_, err := rig.scaling.Apply(rig.identity, value)
			return err
		},
		"restore": func(rig *task15FakeRig, value scaling.Value) error {
			rig.scaling.queueRestore(task15SetOutcome(rig.scaling.state, value, true), nil)
			_, err := rig.scaling.Restore(rig.identity, rig.scaling.state.DisplayID, value)
			return err
		},
	}
	for name, set := range tests {
		t.Run(name, func(t *testing.T) {
			rig := newTask15FakeRig(t)
			before, err := rig.displays.Targets()
			if err != nil {
				t.Fatal(err)
			}
			rig.recorder.take()
			requested := scaling.Value{Raw: 1, Mode: scaling.ModeFullScreen, By: scaling.ByGPU}
			if err := set(rig, requested); err != nil {
				t.Fatal(err)
			}
			after, err := rig.displays.Targets()
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("target count changed from %d to %d", len(before), len(after))
			}
			for i := range before {
				if after[i].DeviceName == before[i].DeviceName {
					t.Fatalf("target %d kept stale device name %s", i, before[i].DeviceName)
				}
				if after[i].Identity != before[i].Identity {
					t.Fatalf("target %d physical identity changed: before=%+v after=%+v", i, before[i], after[i])
				}
			}
			if _, err := rig.displays.CurrentMode(rig.oldTarget); !errors.Is(err, errTask15StaleDisplayName) {
				t.Fatalf("stale CurrentMode error = %v", err)
			}
		})
	}
}

func TestTask15ScalingFakeRenamesAfterAFormalSetFailure(t *testing.T) {
	rig := newTask15FakeRig(t)
	setErr := errors.New("formal NVAPI set failed")
	requested := scaling.Value{Raw: 1, Mode: scaling.ModeFullScreen, By: scaling.ByGPU}
	rig.scaling.queueApply(task15SetOutcome(rig.scaling.state, requested, false), setErr)

	outcome, err := rig.scaling.Apply(rig.identity, requested)
	if !errors.Is(err, setErr) || !outcome.SetAttempted || outcome.Applied {
		t.Fatalf("Apply = (%+v, %v), want attempted failed set", outcome, err)
	}
	if _, err := rig.displays.CurrentMode(rig.oldTarget); !errors.Is(err, errTask15StaleDisplayName) {
		t.Fatalf("stale CurrentMode error = %v", err)
	}
}

func TestTask15ScalingFakeNoOpDoesNotRename(t *testing.T) {
	for _, operation := range []string{"apply", "restore"} {
		t.Run(operation, func(t *testing.T) {
			rig := newTask15FakeRig(t)
			value := rig.scaling.state.Effective
			var err error
			if operation == "apply" {
				_, err = rig.scaling.Apply(rig.identity, value)
			} else {
				_, err = rig.scaling.Restore(rig.identity, rig.scaling.state.DisplayID, value)
			}
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := rig.displays.ResolveTarget(rig.identity)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.DeviceName != rig.oldTarget.DeviceName {
				t.Fatalf("no-op renamed %s to %s", rig.oldTarget.DeviceName, fresh.DeviceName)
			}
			for _, event := range rig.recorder.take() {
				if event == "display.renumber" {
					t.Fatal("no-op scaling set renumbered the display fake")
				}
			}
		})
	}
}

func TestTask15DisplayFakeRejectsEveryStaleNamedOperation(t *testing.T) {
	rig := newTask15FakeRig(t)
	requested := scaling.Value{Raw: 1, Mode: scaling.ModeFullScreen, By: scaling.ByGPU}
	rig.scaling.queueApply(task15SetOutcome(rig.scaling.state, requested, true), nil)
	if _, err := rig.scaling.Apply(rig.identity, requested); err != nil {
		t.Fatal(err)
	}
	stalePlan, err := display.PlanModeChange(fixtureLayout(fixtureNative), rig.oldTarget.DeviceName, fixtureNative)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func() error{
		"CurrentMode": func() error {
			_, err := rig.displays.CurrentMode(rig.oldTarget)
			return err
		},
		"EnumModes": func() error {
			_, err := rig.displays.EnumModes(rig.oldTarget)
			return err
		},
		"TestMode": func() error {
			return rig.displays.TestMode(rig.oldTarget, fixtureNative)
		},
		"ApplyLayout": func() error {
			return rig.displays.ApplyLayout(stalePlan)
		},
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, errTask15StaleDisplayName) {
				t.Fatalf("error = %v, want errTask15StaleDisplayName", err)
			}
		})
	}
}

func TestTask15RecorderSeesSetThenRenumberBeforeTheNextDisplayRead(t *testing.T) {
	rig := newTask15FakeRig(t)
	requested := scaling.Value{Raw: 1, Mode: scaling.ModeFullScreen, By: scaling.ByGPU}
	rig.scaling.queueApply(task15SetOutcome(rig.scaling.state, requested, true), nil)
	if _, err := rig.scaling.Apply(rig.identity, requested); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.displays.ResolveTarget(rig.identity); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.displays.CurrentLayout(); err != nil {
		t.Fatal(err)
	}
	want := []string{"scaling.apply", "display.renumber", "display.resolve", "display.layout"}
	if got := rig.recorder.take(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Task 15 step 2: the ordering, isolation and cycle tests, driven through a real
// Session wired to the shared recorder above.
// ---------------------------------------------------------------------------

func (s *task15ScalingFake) currentState() scaling.State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *task15ScalingFake) setState(state scaling.State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}

func (s *task15ScalingFake) takeCalls() []task15ScalingCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := append([]task15ScalingCall(nil), s.calls...)
	s.calls = nil
	return calls
}

func (r *task15FakeRig) currentTargetName() string {
	r.displays.base.mu.Lock()
	defer r.displays.base.mu.Unlock()
	return r.displays.base.target.DeviceName
}

// newTask15Wiring puts the hazard fakes between a real Session and the desktop the
// session fixture assembled. Both sides share one recorder, so the cross-subsystem
// order is something a test reads rather than infers. The starting effective value is
// deliberately not the one the apply button writes, so every apply in these tests is a
// real set and therefore a real renumber.
func newTask15Wiring(base *fakeDisplay, identity domain.MonitorIdentity) *task15FakeRig {
	recorder := &task15Recorder{}
	displays := &task15DisplayFake{base: base, recorder: recorder}
	scalings := &task15ScalingFake{
		recorder:     recorder,
		displays:     displays,
		availability: scaling.Availability{Available: true},
		state: scaling.State{
			DisplayID: 41,
			Effective: scaling.Value{Raw: 6, Mode: scaling.ModeAspectRatio, By: scaling.ByDisplay},
		},
	}
	base.mu.Lock()
	target := base.target
	base.mu.Unlock()
	return &task15FakeRig{
		recorder: recorder, displays: displays, scaling: scalings,
		identity: identity, oldTarget: target,
	}
}

type scalingFixture struct {
	*sessionFixture
	rig *task15FakeRig
}

func newScalingFixture(t *testing.T) *scalingFixture {
	t.Helper()
	profile := domain.LegacySeedProfile()
	var rig *task15FakeRig
	f := newFixtureWrapped(t, profile, fixtureNative, fixtureLayout(fixtureNative),
		func(base *fakeDisplay) (display.Controller, scaling.Controller) {
			rig = newTask15Wiring(base, profile.Monitor)
			return rig.displays, rig.scaling
		})
	return &scalingFixture{sessionFixture: f, rig: rig}
}

// queueApplied scripts the driver accepting a write and reporting the new value back,
// which is also what makes the fake renumber every display.
func (f *scalingFixture) queueApplied(requested scaling.Value) {
	previous := f.rig.scaling.currentState()
	after := scaling.State{DisplayID: previous.DisplayID, Effective: requested}
	f.rig.scaling.queueApply(scaling.Outcome{
		Requested: requested, Previous: previous, State: after,
		SetAttempted: true, Applied: true, ReadBackKnown: true, Matched: true,
	}, nil)
	f.rig.scaling.setState(after)
}

func (f *scalingFixture) queueRestored(requested scaling.Value) {
	previous := f.rig.scaling.currentState()
	after := scaling.State{DisplayID: previous.DisplayID, Effective: requested}
	f.rig.scaling.queueRestore(scaling.Outcome{
		Requested: requested, Previous: previous, State: after,
		SetAttempted: true, Applied: true, ReadBackKnown: true, Matched: true,
	}, nil)
	f.rig.scaling.setState(after)
}

// queueSetFailure scripts a formal set the driver rejected. SetAttempted stays true
// because the call really went out: the fake renumbers on it exactly as the hardware
// may, and the read-back is still authoritative.
func (f *scalingFixture) queueSetFailure(restore bool, requested scaling.Value, err error) {
	previous := f.rig.scaling.currentState()
	outcome := scaling.Outcome{
		Requested: requested, Previous: previous, State: previous,
		SetAttempted: true, Applied: false, ReadBackKnown: true,
	}
	if restore {
		f.rig.scaling.queueRestore(outcome, err)
		return
	}
	f.rig.scaling.queueApply(outcome, err)
}

// failAfterFirstApply lets the cycle's display restore through and then breaks the
// re-apply, which is the only way to reach the cycle's third failure point.
func (f *scalingFixture) failAfterFirstApply(err error) {
	f.display.mu.Lock()
	defer f.display.mu.Unlock()
	f.display.hook = func(operation string) {
		if operation != "apply" {
			return
		}
		f.display.mu.Lock()
		f.display.fail["test"] = err
		f.display.hook = nil
		f.display.mu.Unlock()
	}
}

// signalOnFirstApply closes a channel the moment the cycle's first display apply
// starts, which is how a test knows opMu is held and the cycle is under way.
func (f *scalingFixture) signalOnFirstApply() chan struct{} {
	started := make(chan struct{})
	var once sync.Once
	f.display.mu.Lock()
	defer f.display.mu.Unlock()
	f.display.hook = func(operation string) {
		if operation == "apply" {
			once.Do(func() { close(started) })
		}
	}
	return started
}

func (f *scalingFixture) displayApplies() int {
	applies := 0
	for _, call := range f.display.takeCalls() {
		if call.operation == "apply" {
			applies++
		}
	}
	return applies
}

func indexOfEvent(events []string, name string) int {
	for i, event := range events {
		if event == name {
			return i
		}
	}
	return -1
}

func lastIndexOfEvent(events []string, name string) int {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i] == name {
			return i
		}
	}
	return -1
}

func withoutEvents(events []string, dropped ...string) []string {
	kept := make([]string, 0, len(events))
	for _, event := range events {
		skip := false
		for _, name := range dropped {
			if event == name {
				skip = true
				break
			}
		}
		if !skip {
			kept = append(kept, event)
		}
	}
	return kept
}

func requireNoScalingEvent(t *testing.T, events []string) {
	t.Helper()
	for _, event := range events {
		if strings.HasPrefix(event, "scaling.") {
			t.Fatalf("the scaling controller was reached at all: %v", events)
		}
	}
}

// sameArrangement compares two desktops by what the user sees -- each display's mode
// and position, in order. Device names are excluded on purpose: a set that failed may
// still have renumbered them, and the arrangement is what the ruling is about.
func sameArrangement(got, want domain.Layout) bool {
	if len(got.Displays) != len(want.Displays) {
		return false
	}
	for i := range got.Displays {
		if got.Displays[i].Mode != want.Displays[i].Mode {
			return false
		}
		if got.Displays[i].Position != want.Displays[i].Position {
			return false
		}
	}
	return true
}

// cycleEvents is the whole of one managed scaling cycle as the shared recorder sees
// it: the display restore, the one set, the renumber it causes, and the re-apply that
// reads everything again afterwards.
func cycleEvents(set string) []string {
	return []string{
		"display.resolve", "display.layout", "display.targets", "display.test", "display.apply",
		set, "display.renumber",
		"display.resolve", "display.layout", "display.targets", "display.test", "display.apply",
	}
}

// R1: the display side's view of the desktop is built strictly after the last set.
func TestEnableResolvesTheTargetAfterAnyScalingSetNotBefore(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	renamed := f.rig.currentTargetName()
	if renamed == f.rig.oldTarget.DeviceName {
		t.Fatal("the scaling fake did not renumber the desktop")
	}
	f.display.takeCalls()

	if err := f.s.Enable(); err != nil {
		t.Fatalf("Enable after a scaling set: %v", err)
	}
	events := f.rig.recorder.take()
	set := indexOfEvent(events, "scaling.apply")
	if set < 0 {
		t.Fatalf("no set recorded: %v", events)
	}
	if first := indexOfEvent(events, "display.resolve"); first < set {
		t.Fatalf("a target was resolved at %d, before the set at %d: %v", first, set, events)
	}
	for _, call := range f.display.takeCalls() {
		if call.operation == "test" && call.target.DeviceName != renamed {
			t.Fatalf("Enable tested %s, want the post-set name %s", call.target.DeviceName, renamed)
		}
		if call.operation != "apply" {
			continue
		}
		for _, change := range call.plan.Changes {
			if change.DeviceName == f.rig.oldTarget.DeviceName {
				t.Fatalf("Enable applied a pre-set device name: %+v", call.plan)
			}
		}
	}
}

// R2: leaving is the mirror of entering, so the arrangement is back before the set.
func TestRestoreAppliesTheDisplayLayoutBeforeAnyScalingRestore(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.rig.recorder.take()

	f.queueRestored(f.s.scalingSaved)
	if err := f.s.RestoreGPUScaling(); err != nil {
		t.Fatal(err)
	}
	events := f.rig.recorder.take()
	set, apply := indexOfEvent(events, "scaling.restore"), indexOfEvent(events, "display.apply")
	if set < 0 || apply < 0 || apply > set {
		t.Fatalf("the display restore did not precede the scaling restore: %v", events)
	}
}

func TestShutdownRestoresScalingOnlyAfterTheDisplayRestoreSucceeded(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.rig.recorder.take()

	f.queueRestored(f.s.scalingSaved)
	if err := f.s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	events := f.rig.recorder.take()
	set, apply := indexOfEvent(events, "scaling.restore"), indexOfEvent(events, "display.apply")
	if set < 0 || apply < 0 || apply > set {
		t.Fatalf("Shutdown restored scaling before the display: %v", events)
	}
	if f.s.scalingOwned {
		t.Fatal("a successful exit restore must release scaling ownership")
	}
}

func TestAFailedDisplayRestoreSkipsTheScalingRestoreEntirely(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("display restore failed")
	f.display.setFailure("apply", failure)
	f.rig.recorder.take()
	f.rig.scaling.takeCalls()

	if err := f.s.Shutdown(); !errors.Is(err, failure) {
		t.Fatalf("Shutdown error = %v", err)
	}
	requireNoScalingEvent(t, f.rig.recorder.take())
	if calls := f.rig.scaling.takeCalls(); len(calls) != 0 {
		t.Fatalf("scaling calls = %+v", calls)
	}
	if !f.s.scalingOwned {
		t.Fatal("a skipped restore must keep scaling ownership")
	}
	if !f.s.managed {
		t.Fatal("a failed display restore must keep display ownership")
	}
}

// R3's other half: a set only ever happens at a workflow boundary, never while a plan
// whose rollback state is keyed by device name is live.
func TestNoScalingSetHappensInsideAnApplyLayout(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.queueRestored(f.s.scalingSaved)
	if err := f.s.RestoreGPUScaling(); err != nil {
		t.Fatal(err)
	}
	events := f.rig.recorder.take()
	if indexOfEvent(events, "scaling.restore") < 0 {
		t.Fatalf("the cycle did not run: %v", events)
	}
	for i, event := range events {
		if event != "display.layout" {
			continue
		}
		for j := i + 1; j < len(events); j++ {
			if events[j] == "display.apply" {
				break
			}
			if events[j] == "scaling.apply" || events[j] == "scaling.restore" {
				t.Fatalf("a set happened between a layout read and the apply planned from it: %v", events)
			}
		}
	}
}

func TestScalingFailuresNeverChangeManagedSavedOrToggleAvailability(t *testing.T) {
	f := newScalingFixture(t)
	failure := errors.New("NVAPI set failed")
	f.queueSetFailure(false, ScalingFullScreenByGPU(), failure)

	if err := f.s.ApplyGPUScaling(); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if f.s.managed || len(f.s.saved.Displays) != 0 {
		t.Fatalf("a scaling failure touched display ownership: managed=%v saved=%+v", f.s.managed, f.s.saved)
	}
	if f.s.scalingOwned {
		t.Fatal("a failed set must not take scaling ownership")
	}
	if err := f.s.Enable(); err != nil {
		t.Fatalf("the mode toggle was not usable after a scaling failure: %v", err)
	}
	if got := f.s.Snapshot(); !got.Managed || !got.AtGameMode {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestScalingUnavailableReasonNamesKnownAdapterAndVendorControlPanel(t *testing.T) {
	target := domain.Target{
		Identity:            domain.MonitorIdentity{Label: "Dell U4924DW"},
		AdapterDeviceString: "AMD Radeon RX 7800 XT",
	}
	reason := scalingUnavailableReason(target, scaling.Availability{
		Reason: "NVAPI unavailable: load nvapi64.dll",
		Err:    errors.Join(scaling.ErrNvapiUnavailable, scaling.ErrNvapiDLLUnavailable),
	}, nil)
	for _, want := range []string{"AMD Radeon RX 7800 XT", "AMD Software"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason = %q, want %q", reason, want)
		}
	}
}

func TestScalingUnavailableReasonNamesMonitorOutsideNVIDIAPath(t *testing.T) {
	target := domain.Target{Identity: domain.MonitorIdentity{Label: "Dell U4924DW"}}
	reason := scalingUnavailableReason(target, scaling.Availability{Available: true}, scaling.ErrScalingTargetNotFound)
	if !strings.Contains(reason, "Dell U4924DW") || !strings.Contains(reason, "NVIDIA") {
		t.Fatalf("reason = %q, want monitor and NVIDIA path", reason)
	}
}

func TestScalingUnavailableReasonExplainsMissingDriverInterface(t *testing.T) {
	reason := scalingUnavailableReason(domain.Target{}, scaling.Availability{
		Err: scaling.ErrNvapiInterfaceUnavailable,
	}, nil)
	if reason != "這個 NVIDIA 驅動版本不提供需要的介面。" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestScalingUnavailableReasonPreservesInitializeDiagnostic(t *testing.T) {
	target := domain.Target{AdapterDeviceString: "NVIDIA GeForce RTX 3070"}
	const diagnostic = "NVAPI unavailable: initialize NVAPI: NVAPI status -6: NVAPI API not initialized"
	reason := scalingUnavailableReason(target, scaling.Availability{
		Reason: diagnostic,
		Err:    scaling.ErrNvapiUnavailable,
	}, nil)
	if reason != diagnostic {
		t.Fatalf("reason = %q, want initialize diagnostic preserved", reason)
	}
}

func TestDisplayFailuresNeverChangeScalingOwnershipOrSavedValue(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	owned, saved, id := f.s.scalingOwned, f.s.scalingSaved, f.s.scalingID
	if !owned {
		t.Fatal("the successful set did not take ownership")
	}

	failure := errors.New("display apply failed")
	f.display.setFailure("apply", failure)
	if err := f.s.Enable(); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if f.s.scalingOwned != owned || f.s.scalingSaved != saved || f.s.scalingID != id {
		t.Fatalf("a display failure moved scaling ownership: owned=%v saved=%+v id=%d",
			f.s.scalingOwned, f.s.scalingSaved, f.s.scalingID)
	}
	if got := f.s.Snapshot(); !got.Scaling.Owned || got.Scaling.Saved != saved {
		t.Fatalf("snapshot scaling = %+v", got.Scaling)
	}
}

func TestScalingRestoreFailureDoesNotBlockShutdown(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("NVAPI restore failed")
	f.queueSetFailure(true, f.s.scalingSaved, failure)

	if err := f.s.Shutdown(); err != nil {
		t.Fatalf("a scaling restore failure blocked the exit: %v", err)
	}
	got := f.s.Snapshot()
	if !got.Scaling.Owned {
		t.Fatal("an unrestored value must stay owned, which is how the UI knows to say so")
	}
	if got.Err == nil || !strings.Contains(got.Message, "GPU 縮放") {
		t.Fatalf("snapshot = %+v", got)
	}
	if err := f.s.Enable(); !errors.Is(err, ErrClosed) {
		t.Fatalf("the session did not close: %v", err)
	}
}

func TestSnapshotDeviceNameIsRefreshedAfterAScalingSet(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	renamed := f.rig.currentTargetName()
	if got := f.s.Snapshot(); got.Target.DeviceName != renamed {
		t.Fatalf("snapshot target = %s, want the post-set name %s", got.Target.DeviceName, renamed)
	}
}

// R5, happy path.
func TestScalingWhileManagedRestoresThenSetsThenReappliesInThatOrder(t *testing.T) {
	f := newScalingFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.rig.recorder.take()
	f.display.takeCalls()

	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	got := withoutEvents(f.rig.recorder.take(), "scaling.probe")
	if want := cycleEvents("scaling.apply"); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if applies := f.displayApplies(); applies != 2 {
		t.Fatalf("the desktop changed %d times, want restore + re-apply", applies)
	}
	if !f.s.scalingOwned || !f.s.managed {
		t.Fatalf("cycle end: scalingOwned=%v managed=%v", f.s.scalingOwned, f.s.managed)
	}
	if snapshot := f.s.Snapshot(); !snapshot.Managed || !snapshot.AtGameMode ||
		snapshot.State != StateWaitingForGame || snapshot.Err != nil {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

// R5 step 4 is a redo, not a reuse -- and this is the path that makes R1 non-vacuous.
func TestTheCycleReresolvesTheTargetAndLayoutAfterTheScalingSet(t *testing.T) {
	f := newScalingFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	targetBefore := f.rig.currentTargetName()
	layoutBefore := f.desktop()
	f.rig.recorder.take()
	f.display.takeCalls()

	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	events := f.rig.recorder.take()
	set := indexOfEvent(events, "scaling.apply")
	if set < 0 {
		t.Fatalf("no set recorded: %v", events)
	}
	if lastIndexOfEvent(events, "display.resolve") < set || lastIndexOfEvent(events, "display.layout") < set {
		t.Fatalf("neither the target nor the layout was read again after the set: %v", events)
	}

	calls := f.display.takeCalls()
	if len(calls) != 10 {
		t.Fatalf("calls = %d: %+v", len(calls), calls)
	}
	for _, call := range calls[5:] {
		if call.operation == "test" && call.target.DeviceName == targetBefore {
			t.Fatalf("the re-apply reused the pre-set target %s", targetBefore)
		}
		if call.operation != "apply" {
			continue
		}
		for _, change := range call.plan.Changes {
			if _, stale := layoutBefore.Find(change.DeviceName); stale {
				t.Fatalf("the re-apply reused the pre-set layout name %s", change.DeviceName)
			}
		}
	}
}

func TestTheCycleRebuildsSavedFromTheFreshLayoutNotTheConsumedOne(t *testing.T) {
	f := newScalingFixture(t)
	original := f.desktop()
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	consumed := f.s.saved

	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	rebuilt := f.s.saved
	if len(rebuilt.Displays) != len(consumed.Displays) {
		t.Fatalf("saved = %+v, want %d displays", rebuilt, len(consumed.Displays))
	}
	for _, state := range rebuilt.Displays {
		if _, stale := consumed.Find(state.DeviceName); stale {
			t.Fatalf("saved still carries the pre-set name %s", state.DeviceName)
		}
	}
	if !sameArrangement(rebuilt, original) {
		t.Fatalf("saved = %+v, want the arrangement the session started from %+v", rebuilt, original)
	}
	if err := f.s.Disable(); err != nil {
		t.Fatalf("the rebuilt saved layout could not be restored: %v", err)
	}
	if got := f.desktop(); !sameArrangement(got, original) {
		t.Fatalf("restored desktop = %+v, want %+v", got, original)
	}
}

func TestTheScalingRestoreButtonRunsTheSameCycleAsTheApplyButton(t *testing.T) {
	f := newScalingFixture(t)
	f.queueApplied(ScalingFullScreenByGPU())
	if err := f.s.ApplyGPUScaling(); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.rig.recorder.take()
	f.display.takeCalls()

	f.queueRestored(f.s.scalingSaved)
	if err := f.s.RestoreGPUScaling(); err != nil {
		t.Fatal(err)
	}
	got := withoutEvents(f.rig.recorder.take(), "scaling.probe")
	if want := cycleEvents("scaling.restore"); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if applies := f.displayApplies(); applies != 2 {
		t.Fatalf("the desktop changed %d times, want restore + re-apply", applies)
	}
	if f.s.scalingOwned {
		t.Fatal("the restore button must release scaling ownership")
	}
	if !f.s.managed {
		t.Fatal("the cycle must end owning the mode again")
	}
}

// Failure point 1: the display restore fails, so nothing may reach NVAPI at all.
func TestACycleWhoseDisplayRestoreFailsNeverReachesNVAPI(t *testing.T) {
	f := newScalingFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	savedBefore := f.s.saved
	watchers := f.clock.count()
	failure := errors.New("display restore failed")
	f.display.setFailure("apply", failure)
	f.rig.recorder.take()
	f.rig.scaling.takeCalls()

	if err := f.s.ApplyGPUScaling(); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	requireNoScalingEvent(t, f.rig.recorder.take())
	if calls := f.rig.scaling.takeCalls(); len(calls) != 0 {
		t.Fatalf("scaling calls = %+v", calls)
	}
	if !f.s.managed || !reflect.DeepEqual(f.s.saved, savedBefore) {
		t.Fatalf("ownership was not kept for a retry: managed=%v saved=%+v", f.s.managed, f.s.saved)
	}
	if f.s.scalingOwned {
		t.Fatal("scaling ownership changed on a path that never called NVAPI")
	}
	if f.clock.count() != watchers+1 {
		t.Fatalf("watchers = %d, want the stopped one put back", f.clock.count())
	}
	got := f.s.Snapshot()
	if got.State != StateError || !got.Managed {
		t.Fatalf("snapshot = %+v", got)
	}
	if !strings.Contains(got.Message, "GPU 縮放未變更") {
		t.Fatalf("the message does not say scaling was untouched: %q", got.Message)
	}
	f.display.setFailure("apply", nil)
	if err := f.s.Disable(); err != nil {
		t.Fatalf("the toggle was not usable afterwards: %v", err)
	}
}

// Failure point 2: the set fails, so the user is left on the restored arrangement.
func TestACycleWhoseScalingSetFailsLeavesTheDesktopRestoredAndUnowned(t *testing.T) {
	f := newScalingFixture(t)
	original := f.desktop()
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()
	f.rig.scaling.takeCalls()
	failure := errors.New("NVAPI set failed")
	f.queueSetFailure(false, ScalingFullScreenByGPU(), failure)

	if err := f.s.ApplyGPUScaling(); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if applies := f.displayApplies(); applies != 1 {
		t.Fatalf("display applies = %d, want only the restore", applies)
	}
	calls := f.rig.scaling.takeCalls()
	if len(calls) != 1 || calls[0].operation != "apply" {
		t.Fatalf("scaling calls = %+v, want exactly one set", calls)
	}
	if f.s.managed || len(f.s.saved.Displays) != 0 {
		t.Fatalf("display ownership was not released: managed=%v saved=%+v", f.s.managed, f.s.saved)
	}
	if f.s.scalingOwned {
		t.Fatal("a failed set must not take scaling ownership")
	}
	if got := f.desktop(); !sameArrangement(got, original) {
		t.Fatalf("desktop = %+v, want the original arrangement %+v", got, original)
	}
	if got := f.s.Snapshot(); got.State != StateError || got.Managed {
		t.Fatalf("snapshot = %+v", got)
	}
	if err := f.s.Enable(); err != nil {
		t.Fatalf("the toggle was not usable afterwards: %v", err)
	}
}

// Failure point 3: the write happened and the mode did not, and neither is faked.
func TestACycleWhoseReapplyFailsKeepsTheScalingResultAndOwnsNoDisplayMode(t *testing.T) {
	f := newScalingFixture(t)
	original := f.desktop()
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.display.takeCalls()
	previous := f.rig.scaling.currentState()
	failure := errors.New("re-apply failed")
	f.failAfterFirstApply(failure)
	f.queueApplied(ScalingFullScreenByGPU())

	if err := f.s.ApplyGPUScaling(); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if !f.s.scalingOwned {
		t.Fatal("the write that really happened was rolled back")
	}
	if f.s.scalingSaved != previous.Effective || f.s.scalingID != previous.DisplayID {
		t.Fatalf("booked saved=%+v id=%d, want %+v / %d",
			f.s.scalingSaved, f.s.scalingID, previous.Effective, previous.DisplayID)
	}
	if f.s.managed || len(f.s.saved.Displays) != 0 {
		t.Fatalf("half ownership was taken: managed=%v saved=%+v", f.s.managed, f.s.saved)
	}
	if applies := f.displayApplies(); applies != 1 {
		t.Fatalf("display applies = %d, want only the restore", applies)
	}
	if got := f.desktop(); !sameArrangement(got, original) {
		t.Fatalf("desktop = %+v, want the original arrangement %+v", got, original)
	}
	got := f.s.Snapshot()
	if got.State != StateError || got.Managed || !got.Scaling.Owned {
		t.Fatalf("snapshot = %+v", got)
	}
	if !strings.Contains(got.Message, "GPU 縮放已變更") || !strings.Contains(got.Message, "重新套用") {
		t.Fatalf("the message does not state both halves: %q", got.Message)
	}
	f.display.setFailure("test", nil)
	if err := f.s.Enable(); err != nil {
		t.Fatalf("the retry is an ordinary Enable and it failed: %v", err)
	}
}

// The generation counter, not the managed check, is what stops the poll: by the time it
// reaches the gate the cycle has finished and s.managed is true again, so only the
// generation can refuse it. A third desktop change would be that refusal missing.
func TestTheWatcherCannotRestoreWhileTheCycleIsRunning(t *testing.T) {
	f := newScalingFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})
	f.poll(t, 0, 2, processResult{})
	if got := f.s.Snapshot(); got.State != StateRestorePending {
		t.Fatalf("snapshot = %+v", got)
	}

	// Wake the poll that comes due mid-cycle. It blocks inside the process check until
	// the cycle is already holding opMu, which is the "already waiting at the gate"
	// case the generation check exists for.
	f.clock.tick(0, time.Unix(200, 0))
	if name := waitValue(t, f.checker.called); name != f.profile.ProcessName {
		t.Fatalf("watched process = %q", name)
	}
	started := f.signalOnFirstApply()
	f.display.takeCalls()
	f.queueApplied(ScalingFullScreenByGPU())

	done := make(chan error, 1)
	go func() { done <- f.s.ApplyGPUScaling() }()
	waitValue(t, started)
	f.checker.results <- processResult{}
	if err := waitValue(t, done); err != nil {
		t.Fatal(err)
	}

	if applies := f.displayApplies(); applies != 2 {
		t.Fatalf("the desktop changed %d times, want exactly restore + re-apply", applies)
	}
	if !f.s.managed {
		t.Fatal("the pending restore fired and took the mode away")
	}
	if got := f.s.Snapshot(); !got.Managed || !got.AtGameMode {
		t.Fatalf("snapshot = %+v", got)
	}
	if !f.clock.tickers[0].isStopped() {
		t.Fatal("the stopped watcher was not joined")
	}
	if f.clock.count() != 2 {
		t.Fatalf("watchers = %d, want the cycle to have re-armed exactly one", f.clock.count())
	}
}

func TestAGameThatEndsDuringTheCycleStillRestoresOnAFullDelayAfterwards(t *testing.T) {
	// The negative that makes the mechanism visible: a replacement tracker inherits the
	// seen flag and nothing else. A carried-over countdown would restore seconds after
	// the user asked to be back in the game mode, and a carried-over fired would never
	// arm again at all.
	seeded := newGameTracker(3*time.Second, true)
	if !seeded.seen || seeded.missing || !seeded.missingAt.IsZero() || seeded.fired {
		t.Fatalf("seeded tracker = %+v", *seeded)
	}

	f := newScalingFixture(t)
	if err := f.s.Enable(); err != nil {
		t.Fatal(err)
	}
	f.poll(t, 0, 1, processResult{running: true})
	if !f.s.gameSeen {
		t.Fatal("the watcher did not record that the game had been seen")
	}

	f.clock.tick(0, time.Unix(200, 0))
	waitValue(t, f.checker.called)
	started := f.signalOnFirstApply()
	f.queueApplied(ScalingFullScreenByGPU())
	done := make(chan error, 1)
	go func() { done <- f.s.ApplyGPUScaling() }()
	waitValue(t, started)
	// The game ends during the cycle. This observation is discarded, countdown and all.
	f.checker.results <- processResult{}
	if err := waitValue(t, done); err != nil {
		t.Fatal(err)
	}
	if !f.s.gameSeen {
		t.Fatal("the cycle dropped the seen flag")
	}
	if f.clock.count() != 2 {
		t.Fatalf("watchers = %d", f.clock.count())
	}

	// The first post-cycle poll that finds the process absent opens a full delay from
	// that instant, not from anything that happened before the cycle.
	if got := f.poll(t, 1, 300, processResult{}); got.State != StateRestorePending {
		t.Fatalf("the seeded tracker did not arm: %+v", got)
	}
	f.display.takeCalls()
	if got := f.poll(t, 1, 302, processResult{}); got.State != StateRestorePending {
		t.Fatalf("restored early: %+v", got)
	}
	if applies := f.displayApplies(); applies != 0 {
		t.Fatalf("the desktop changed %d times before the delay elapsed", applies)
	}
	if got := f.poll(t, 1, 303, processResult{}); got.Managed || got.State != StateNative {
		t.Fatalf("the automatic restore did not happen at the full delay: %+v", got)
	}
	if f.s.managed {
		t.Fatal("the session still owns the mode")
	}
}
