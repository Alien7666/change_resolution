package app

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

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
