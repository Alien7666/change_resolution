package scaling

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
)

func TestScalingValueDecodesTheModeAndTheDeviceThatScales(t *testing.T) {
	tests := []struct {
		raw  uint32
		mode Mode
		by   By
	}{
		{raw: 0, mode: ModeDefault, by: ByUnknown},
		{raw: 1, mode: ModeFullScreen, by: ByDisplay},
		{raw: 2, mode: ModeFullScreen, by: ByGPU},
		{raw: 3, mode: ModeNoScaling, by: ByGPU},
		{raw: 5, mode: ModeAspectRatio, by: ByGPU},
		{raw: 6, mode: ModeAspectRatio, by: ByDisplay},
		{raw: 7, mode: ModeNoScaling, by: ByDisplay},
		{raw: 8, mode: ModeIntegerScaling, by: ByGPU},
		{raw: 255, mode: ModeCustomized, by: ByUnknown},
		{raw: 4, mode: ModeUnrecognized, by: ByUnknown},
		{raw: 99, mode: ModeUnrecognized, by: ByUnknown},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.raw), func(t *testing.T) {
			got := decodeValue(tt.raw)
			if got != (Value{Raw: tt.raw, Mode: tt.mode, By: tt.by}) {
				t.Fatalf("decodeValue(%d) = %#v", tt.raw, got)
			}
		})
	}
}

func TestSaveToPersistenceAppearsOnlyInItsConstantDeclaration(t *testing.T) {
	source, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	const declaration = "flagSaveToPersistence uint32 = 0x02"
	if got := strings.Count(string(source), "0x02"); got != 1 {
		t.Fatalf("0x02 appears %d times, want exactly the named declaration", got)
	}
	if !strings.Contains(string(source), declaration) {
		t.Fatalf("the sole 0x02 is not the persistence-flag declaration")
	}
}

type writeCall struct {
	flags   uint32
	payload []string
}

type fakeNVAPI struct {
	initializeStatus   status
	initializeStatuses []status
	readStatuses       []status
	writeStatuses      []status
	displayID          uint32
	displayIDs         []uint32
	displayIDStatus    status
	displayIDStatuses  []status
	unloadStatus       status
	configs            []*config

	initializeCalls  int
	readCalls        int
	displayNameCalls []string
	writes           []writeCall
	unloadCalls      int
}

func (f *fakeNVAPI) initialize() status {
	index := f.initializeCalls
	f.initializeCalls++
	if index < len(f.initializeStatuses) {
		return f.initializeStatuses[index]
	}
	return f.initializeStatus
}

func (f *fakeNVAPI) readConfig() (*config, status) {
	index := f.readCalls
	f.readCalls++
	if status := statusAt(f.readStatuses, index); status != statusOK {
		return nil, status
	}
	if len(f.configs) == 0 {
		return nil, statusOK
	}
	if index >= len(f.configs) {
		index = len(f.configs) - 1
	}
	return f.configs[index], statusOK
}

func (f *fakeNVAPI) writeConfig(cfg *config, flags uint32) status {
	f.writes = append(f.writes, writeCall{flags: flags, payload: renderConfig(cfg)})
	return statusAt(f.writeStatuses, len(f.writes)-1)
}

func (f *fakeNVAPI) displayIDByName(name string) (uint32, status) {
	f.displayNameCalls = append(f.displayNameCalls, name)
	index := len(f.displayNameCalls) - 1
	displayID := f.displayID
	if index < len(f.displayIDs) {
		displayID = f.displayIDs[index]
	}
	callStatus := f.displayIDStatus
	if index < len(f.displayIDStatuses) {
		callStatus = f.displayIDStatuses[index]
	}
	return displayID, callStatus
}

func (f *fakeNVAPI) errorMessage(value status) string { return fmt.Sprintf("fake status %d", value) }

func (f *fakeNVAPI) unload() status {
	f.unloadCalls++
	return f.unloadStatus
}

func statusAt(statuses []status, index int) status {
	if index < len(statuses) {
		return statuses[index]
	}
	return statusOK
}

type targetSpec struct {
	displayID uint32
	scaling   uint32
}

func fakeConfig(specs ...targetSpec) *config {
	cfg := &config{}
	for index, spec := range specs {
		scaling := new(uint32)
		*scaling = spec.scaling
		name := fmt.Sprintf("paths[0].targets[%d].details.scaling", index)
		cfg.targets = append(cfg.targets, configTarget{
			displayID:    spec.displayID,
			scaling:      scaling,
			scalingField: name,
		})
		cfg.fields = append(cfg.fields,
			payloadField{name: fmt.Sprintf("paths[0].targets[%d].displayId", index), value: fmt.Sprint(spec.displayID)},
			payloadField{name: name, number: scaling},
		)
	}
	cfg.fields = append(cfg.fields,
		payloadField{name: "paths[0].flags", value: "17"},
		payloadField{name: "paths[0].targetInfo", value: "non-nil"},
	)
	return cfg
}

func resolverFor(name string, calls *[]domain.MonitorIdentity) targetResolver {
	return func(identity domain.MonitorIdentity) (domain.Target, error) {
		*calls = append(*calls, identity)
		return domain.Target{DeviceName: name, Identity: identity}, nil
	}
}

func TestExactlyOneTargetOrNotFoundOrAmbiguous(t *testing.T) {
	tests := []struct {
		name    string
		config  *config
		wantRaw uint32
		wantErr error
	}{
		{name: "none", config: fakeConfig(targetSpec{displayID: 7, scaling: 6}), wantErr: ErrScalingTargetNotFound},
		{name: "one", config: fakeConfig(targetSpec{displayID: 42, scaling: 6}), wantRaw: 6},
		{name: "several", config: fakeConfig(
			targetSpec{displayID: 42, scaling: 6},
			targetSpec{displayID: 42, scaling: 2},
		), wantErr: ErrScalingTargetAmbiguous},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeNVAPI{configs: []*config{tt.config}, displayID: 42}
			var resolved []domain.MonitorIdentity
			controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)
			identity := domain.MonitorIdentity{InstancePath: "unit-42"}

			state, err := controller.Read(identity)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Read error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && state != (State{DisplayID: 42, Effective: decodeValue(tt.wantRaw)}) {
				t.Fatalf("Read state = %#v", state)
			}
			if tt.wantErr == ErrScalingTargetAmbiguous && state.Effective.Raw != 0 {
				t.Fatalf("ambiguous lookup returned the first target: %#v", state)
			}
		})
	}
}

func TestReadReusesOnlyACachedDisplayIDThatStillNamesExactlyOneTarget(t *testing.T) {
	api := &fakeNVAPI{
		displayID: 42,
		configs: []*config{
			fakeConfig(targetSpec{displayID: 42, scaling: 6}),
			fakeConfig(targetSpec{displayID: 42, scaling: 2}),
		},
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)
	identity := domain.MonitorIdentity{InstancePath: "unit-42"}

	if _, err := controller.Read(identity); err != nil {
		t.Fatal(err)
	}
	state, err := controller.Read(identity)
	if err != nil {
		t.Fatal(err)
	}
	if state.Effective.Raw != 2 {
		t.Fatalf("second Read effective = %#v", state.Effective)
	}
	if len(resolved) != 1 || len(api.displayNameCalls) != 1 {
		t.Fatalf("cache re-resolved: resolver=%d displayIDByName=%d", len(resolved), len(api.displayNameCalls))
	}
}

func TestReadDropsAnInvalidCachedIDAndResolvesTheCurrentGDINameAgain(t *testing.T) {
	api := &fakeNVAPI{
		displayIDs: []uint32{42, 77},
		configs: []*config{
			fakeConfig(targetSpec{displayID: 42, scaling: 6}),
			fakeConfig(targetSpec{displayID: 77, scaling: 2}),
		},
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)
	identity := domain.MonitorIdentity{InstancePath: "unit-42"}

	if _, err := controller.Read(identity); err != nil {
		t.Fatal(err)
	}
	state, err := controller.Read(identity)
	if err != nil {
		t.Fatal(err)
	}
	if state.DisplayID != 77 || state.Effective.Raw != 2 {
		t.Fatalf("second Read state = %#v", state)
	}
	if len(resolved) != 2 || len(api.displayNameCalls) != 2 {
		t.Fatalf("stale cache was retained: resolver=%d displayIDByName=%d", len(resolved), len(api.displayNameCalls))
	}
}

func TestDisplayNameThatNVAPICannotResolveIsNotAnNvidiaDisplay(t *testing.T) {
	api := &fakeNVAPI{
		configs:         []*config{fakeConfig(targetSpec{displayID: 42, scaling: 6})},
		displayIDStatus: -8,
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY9`, &resolved), nil)

	_, err := controller.Read(domain.MonitorIdentity{InstancePath: "unit-42"})
	if !errors.Is(err, ErrNotNvidiaDisplay) {
		t.Fatalf("Read error = %v", err)
	}
	if !reflect.DeepEqual(api.displayNameCalls, []string{`\\.\DISPLAY9`}) {
		t.Fatalf("displayIDByName calls = %#v", api.displayNameCalls)
	}
}

func TestPayloadDivergenceAbortsBeforeAnyWrite(t *testing.T) {
	cfg := fakeConfig(targetSpec{displayID: 42, scaling: 6})
	// This models a broken layout map: the field advertised as Scaling aliases a
	// second payload field. A write would therefore change two lines.
	cfg.fields = append(cfg.fields, payloadField{
		name: "paths[0].targets[0].details.rotation", number: cfg.targets[0].scaling,
	})
	api := &fakeNVAPI{configs: []*config{cfg}, displayID: 42}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	outcome, err := controller.Apply(domain.MonitorIdentity{InstancePath: "unit-42"}, decodeValue(2))
	if !errors.Is(err, ErrScalingPayloadDiverged) {
		t.Fatalf("Apply error = %v", err)
	}
	if len(api.writes) != 0 {
		t.Fatalf("divergent payload reached NVAPI: %#v", api.writes)
	}
	if outcome.Previous.Effective != decodeValue(6) || outcome.State != outcome.Previous ||
		outcome.SetAttempted || outcome.Applied || !outcome.ReadBackKnown {
		t.Fatalf("divergence outcome = %#v", outcome)
	}
	if availability := controller.Probe(); availability.Available || !errors.Is(availability.Err, ErrScalingPayloadDiverged) {
		t.Fatalf("Probe after divergence = %#v", availability)
	}
}

func TestWriteSequenceIsValidateThenApplyAndNothingElse(t *testing.T) {
	initial := fakeConfig(targetSpec{displayID: 42, scaling: 6})
	readBack := fakeConfig(targetSpec{displayID: 42, scaling: 2})
	api := &fakeNVAPI{configs: []*config{initial, readBack}, displayID: 42}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	outcome, err := controller.Apply(domain.MonitorIdentity{InstancePath: "unit-42"}, decodeValue(2))
	if err != nil {
		t.Fatal(err)
	}
	if got := []uint32{api.writes[0].flags, api.writes[1].flags}; !reflect.DeepEqual(got, []uint32{flagValidateOnly, 0}) {
		t.Fatalf("write flags = %#v", got)
	}
	if len(api.writes) != 2 || api.readCalls != 2 {
		t.Fatalf("writes=%d reads=%d", len(api.writes), api.readCalls)
	}
	if outcome != (Outcome{
		Requested:     decodeValue(2),
		Previous:      State{DisplayID: 42, Effective: decodeValue(6)},
		State:         State{DisplayID: 42, Effective: decodeValue(2)},
		SetAttempted:  true,
		Applied:       true,
		ReadBackKnown: true,
		Matched:       true,
	}) {
		t.Fatalf("outcome = %#v", outcome)
	}
	if len(resolved) != 1 || len(api.displayNameCalls) != 1 {
		t.Fatalf("GDI name crossed the set: resolver=%d displayIDByName=%d", len(resolved), len(api.displayNameCalls))
	}
}

func TestValidateFailureMeansNoApply(t *testing.T) {
	api := &fakeNVAPI{
		configs:       []*config{fakeConfig(targetSpec{displayID: 42, scaling: 6})},
		displayID:     42,
		writeStatuses: []status{-5},
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	outcome, err := controller.Apply(domain.MonitorIdentity{InstancePath: "unit-42"}, decodeValue(2))
	if err == nil {
		t.Fatal("Apply succeeded after validation failed")
	}
	if len(api.writes) != 1 || api.writes[0].flags != flagValidateOnly {
		t.Fatalf("writes = %#v", api.writes)
	}
	if api.readCalls != 1 {
		t.Fatalf("validation failure performed %d reads", api.readCalls)
	}
	if outcome.Previous.Effective != decodeValue(6) || outcome.State != outcome.Previous ||
		outcome.SetAttempted || outcome.Applied || !outcome.ReadBackKnown {
		t.Fatalf("validation outcome = %#v", outcome)
	}
}

func TestANormalisedReadBackIsNotAnError(t *testing.T) {
	api := &fakeNVAPI{
		configs: []*config{
			fakeConfig(targetSpec{displayID: 42, scaling: 2}),
			fakeConfig(targetSpec{displayID: 42, scaling: 1}),
		},
		displayID: 42,
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	outcome, err := controller.Apply(domain.MonitorIdentity{InstancePath: "unit-42"}, decodeValue(6))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Matched || outcome.State.Effective != decodeValue(1) || outcome.Previous.Effective != decodeValue(2) ||
		outcome.Requested != decodeValue(6) || !outcome.SetAttempted || !outcome.Applied || !outcome.ReadBackKnown {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestAFailedApplyStillRereadsTheEffectiveValue(t *testing.T) {
	api := &fakeNVAPI{
		configs: []*config{
			fakeConfig(targetSpec{displayID: 42, scaling: 6}),
			fakeConfig(targetSpec{displayID: 42, scaling: 1}),
		},
		displayID:     42,
		writeStatuses: []status{statusOK, -5},
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	outcome, err := controller.Apply(domain.MonitorIdentity{InstancePath: "unit-42"}, decodeValue(2))
	if err == nil {
		t.Fatal("Apply succeeded despite the formal set failure")
	}
	if api.readCalls != 2 || outcome.Previous.Effective != decodeValue(6) || outcome.State.Effective != decodeValue(1) ||
		!outcome.SetAttempted || outcome.Applied || !outcome.ReadBackKnown {
		t.Fatalf("reads=%d outcome=%#v", api.readCalls, outcome)
	}
}

func TestASuccessfulSetWhoseReadBackFailsStillReportsTheSetFacts(t *testing.T) {
	api := &fakeNVAPI{
		configs:       []*config{fakeConfig(targetSpec{displayID: 42, scaling: 6})},
		displayID:     42,
		readStatuses:  []status{statusOK, -5},
		writeStatuses: []status{statusOK, statusOK},
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	outcome, err := controller.Apply(domain.MonitorIdentity{InstancePath: "unit-42"}, decodeValue(2))
	if err == nil {
		t.Fatal("Apply succeeded despite the read-back failure")
	}
	if outcome.Previous != (State{DisplayID: 42, Effective: decodeValue(6)}) ||
		outcome.State != (State{}) || !outcome.SetAttempted || !outcome.Applied || outcome.ReadBackKnown || outcome.Matched {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestApplyingTheEffectiveValueIsANoOp(t *testing.T) {
	api := &fakeNVAPI{configs: []*config{fakeConfig(targetSpec{displayID: 42, scaling: 2})}, displayID: 42}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	outcome, err := controller.Apply(domain.MonitorIdentity{InstancePath: "unit-42"}, decodeValue(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(api.writes) != 0 || api.readCalls != 1 {
		t.Fatalf("same-value request wrote=%d read=%d", len(api.writes), api.readCalls)
	}
	if !outcome.Matched || outcome.Previous != outcome.State || outcome.State.Effective != decodeValue(2) ||
		outcome.SetAttempted || outcome.Applied || !outcome.ReadBackKnown {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestRestoreRejectsAChangedDisplayIDBeforeAnyWrite(t *testing.T) {
	api := &fakeNVAPI{
		configs:   []*config{fakeConfig(targetSpec{displayID: 77, scaling: 2})},
		displayID: 77,
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY7`, &resolved), nil)

	outcome, err := controller.Restore(domain.MonitorIdentity{InstancePath: "unit-42"}, 42, decodeValue(6))
	if !errors.Is(err, ErrScalingTargetChanged) {
		t.Fatalf("Restore error = %v", err)
	}
	if len(api.writes) != 0 || outcome.SetAttempted || outcome.Applied || outcome.ReadBackKnown {
		t.Fatalf("mismatched restore wrote or claimed state: writes=%d outcome=%#v", len(api.writes), outcome)
	}
	if !reflect.DeepEqual(api.displayNameCalls, []string{`\\.\DISPLAY7`}) {
		t.Fatalf("displayIDByName calls = %#v", api.displayNameCalls)
	}
}

func TestRestoreForcesAFreshIdentityResolutionInsteadOfTrustingTheCache(t *testing.T) {
	api := &fakeNVAPI{
		configs: []*config{
			fakeConfig(targetSpec{displayID: 42, scaling: 2}),
			fakeConfig(targetSpec{displayID: 42, scaling: 2}),
		},
		displayIDs: []uint32{42, 77},
	}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)
	identity := domain.MonitorIdentity{InstancePath: "unit-42"}

	if _, err := controller.Read(identity); err != nil {
		t.Fatal(err)
	}
	_, err := controller.Restore(identity, 42, decodeValue(6))
	if !errors.Is(err, ErrScalingTargetChanged) {
		t.Fatalf("Restore error = %v", err)
	}
	if len(resolved) != 2 || len(api.displayNameCalls) != 2 || len(api.writes) != 0 {
		t.Fatalf("restore trusted cache: resolve=%d names=%d writes=%d", len(resolved), len(api.displayNameCalls), len(api.writes))
	}
}

func TestRestoreNoOpIsAKnownSuccessfulOutcome(t *testing.T) {
	api := &fakeNVAPI{configs: []*config{fakeConfig(targetSpec{displayID: 42, scaling: 6})}, displayID: 42}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	outcome, err := controller.Restore(domain.MonitorIdentity{InstancePath: "unit-42"}, 42, decodeValue(6))
	if err != nil {
		t.Fatal(err)
	}
	want := Outcome{
		Requested:     decodeValue(6),
		Previous:      State{DisplayID: 42, Effective: decodeValue(6)},
		State:         State{DisplayID: 42, Effective: decodeValue(6)},
		ReadBackKnown: true,
		Matched:       true,
	}
	if outcome != want || len(api.writes) != 0 {
		t.Fatalf("outcome=%#v writes=%d", outcome, len(api.writes))
	}
}

func TestRestoreAppliesOnlyAfterTheFreshIdentityMatchesTheOwnedDisplayID(t *testing.T) {
	initial := fakeConfig(targetSpec{displayID: 42, scaling: 2})
	readBack := fakeConfig(targetSpec{displayID: 42, scaling: 6})
	api := &fakeNVAPI{configs: []*config{initial, readBack}, displayID: 42}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY9`, &resolved), nil)

	outcome, err := controller.Restore(domain.MonitorIdentity{InstancePath: "unit-42"}, 42, decodeValue(6))
	if err != nil {
		t.Fatal(err)
	}
	if got := []uint32{api.writes[0].flags, api.writes[1].flags}; !reflect.DeepEqual(got, []uint32{flagValidateOnly, 0}) {
		t.Fatalf("write flags = %#v", got)
	}
	want := Outcome{
		Requested:     decodeValue(6),
		Previous:      State{DisplayID: 42, Effective: decodeValue(2)},
		State:         State{DisplayID: 42, Effective: decodeValue(6)},
		SetAttempted:  true,
		Applied:       true,
		ReadBackKnown: true,
		Matched:       true,
	}
	if outcome != want || len(resolved) != 1 || len(api.displayNameCalls) != 1 {
		t.Fatalf("outcome=%#v resolve=%d names=%d", outcome, len(resolved), len(api.displayNameCalls))
	}
}

type loaderStep struct {
	api nvapi
	err error
}

func sequencedLoader(steps []loaderStep, calls *int) nvapiLoader {
	return func() (nvapi, error) {
		index := *calls
		*calls = *calls + 1
		if index >= len(steps) {
			index = len(steps) - 1
		}
		return steps[index].api, steps[index].err
	}
}

func TestProbeRetriesATransientLoadFailure(t *testing.T) {
	api := &fakeNVAPI{}
	loadCalls := 0
	var resolved []domain.MonitorIdentity
	controller := newLoadableController(sequencedLoader([]loaderStep{
		{err: errors.New("nvapi64.dll missing")},
		{api: api},
	}, &loadCalls), resolverFor(`\\.\DISPLAY1`, &resolved))

	if availability := controller.Probe(); availability.Available || !errors.Is(availability.Err, ErrNvapiUnavailable) {
		t.Fatalf("first Probe = %#v", availability)
	}
	if availability := controller.Probe(); !availability.Available || availability.Err != nil {
		t.Fatalf("second Probe = %#v", availability)
	}
	if loadCalls != 2 || api.initializeCalls != 1 {
		t.Fatalf("loads=%d initialize=%d", loadCalls, api.initializeCalls)
	}
}

func TestProbeRetriesATransientInitializationFailure(t *testing.T) {
	api := &fakeNVAPI{initializeStatuses: []status{-5, statusOK}}
	loadCalls := 0
	var resolved []domain.MonitorIdentity
	controller := newLoadableController(sequencedLoader([]loaderStep{{api: api}}, &loadCalls), resolverFor(`\\.\DISPLAY1`, &resolved))

	if availability := controller.Probe(); availability.Available || !errors.Is(availability.Err, ErrNvapiUnavailable) {
		t.Fatalf("first Probe = %#v", availability)
	}
	if availability := controller.Probe(); !availability.Available || availability.Err != nil {
		t.Fatalf("second Probe = %#v", availability)
	}
	if loadCalls != 1 || api.initializeCalls != 2 {
		t.Fatalf("loads=%d initialize=%d", loadCalls, api.initializeCalls)
	}
}

func TestCloseAfterALoadFailurePreventsAnyRetry(t *testing.T) {
	loadCalls := 0
	var resolved []domain.MonitorIdentity
	controller := newLoadableController(sequencedLoader([]loaderStep{{err: errors.New("missing")}}, &loadCalls), resolverFor(`\\.\DISPLAY1`, &resolved))

	if availability := controller.Probe(); availability.Available {
		t.Fatalf("Probe = %#v", availability)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if availability := controller.Probe(); availability.Available || !errors.Is(availability.Err, ErrControllerClosed) {
		t.Fatalf("Probe after Close = %#v", availability)
	}
	if loadCalls != 1 {
		t.Fatalf("load calls = %d", loadCalls)
	}
}

func TestUnavailableNvapiCallsNothing(t *testing.T) {
	api := &fakeNVAPI{}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), errors.New("nvapi64.dll missing"))

	availability := controller.Probe()
	if availability.Available || !errors.Is(availability.Err, ErrNvapiUnavailable) {
		t.Fatalf("Probe = %#v", availability)
	}
	if _, err := controller.Read(domain.MonitorIdentity{InstancePath: "unit-42"}); !errors.Is(err, ErrNvapiUnavailable) {
		t.Fatalf("Read error = %v", err)
	}
	if _, err := controller.Apply(domain.MonitorIdentity{InstancePath: "unit-42"}, decodeValue(2)); !errors.Is(err, ErrNvapiUnavailable) {
		t.Fatalf("Apply error = %v", err)
	}
	if api.initializeCalls != 0 || api.readCalls != 0 || len(api.writes) != 0 || len(resolved) != 0 {
		t.Fatalf("unavailable controller called api: init=%d read=%d writes=%d resolve=%d",
			api.initializeCalls, api.readCalls, len(api.writes), len(resolved))
	}
}

func TestProbeInitializesOnceAndCloseUnloadsOnce(t *testing.T) {
	api := &fakeNVAPI{}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	if availability := controller.Probe(); !availability.Available || availability.Err != nil {
		t.Fatalf("Probe = %#v", availability)
	}
	if availability := controller.Probe(); !availability.Available {
		t.Fatalf("second Probe = %#v", availability)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if api.initializeCalls != 1 || api.unloadCalls != 1 {
		t.Fatalf("initialize=%d unload=%d", api.initializeCalls, api.unloadCalls)
	}
}

func TestInitializationFailureIsUnavailableWithoutHardDisablingTheController(t *testing.T) {
	api := &fakeNVAPI{initializeStatus: -5}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	availability := controller.Probe()
	if availability.Available || !errors.Is(availability.Err, ErrNvapiUnavailable) {
		t.Fatalf("Probe = %#v", availability)
	}
	if _, err := controller.Read(domain.MonitorIdentity{InstancePath: "unit-42"}); !errors.Is(err, ErrNvapiUnavailable) {
		t.Fatalf("Read error = %v", err)
	}
	if api.initializeCalls != 2 || api.readCalls != 0 || len(resolved) != 0 {
		t.Fatalf("init=%d read=%d resolve=%d", api.initializeCalls, api.readCalls, len(resolved))
	}
}

func TestIncompatibleStructVersionDisablesFurtherCallsForThisRun(t *testing.T) {
	api := &fakeNVAPI{readStatuses: []status{statusIncompatibleStructVersion}}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	_, err := controller.Read(domain.MonitorIdentity{InstancePath: "unit-42"})
	if !errors.Is(err, ErrIncompatibleStructVersion) {
		t.Fatalf("Read error = %v", err)
	}
	availability := controller.Probe()
	if availability.Available || !errors.Is(availability.Err, ErrIncompatibleStructVersion) {
		t.Fatalf("Probe = %#v", availability)
	}
	if api.initializeCalls != 1 || api.readCalls != 1 || len(resolved) != 0 {
		t.Fatalf("disabled controller called again: init=%d read=%d resolve=%d",
			api.initializeCalls, api.readCalls, len(resolved))
	}
}

func TestCloseWithoutInitializationCallsNothing(t *testing.T) {
	api := &fakeNVAPI{}
	var resolved []domain.MonitorIdentity
	controller := newController(api, resolverFor(`\\.\DISPLAY1`, &resolved), nil)

	if err := controller.Close(); err != nil {
		t.Fatal(err)
	}
	if api.initializeCalls != 0 || api.unloadCalls != 0 {
		t.Fatalf("initialize=%d unload=%d", api.initializeCalls, api.unloadCalls)
	}
	if availability := controller.Probe(); availability.Available || !errors.Is(availability.Err, ErrControllerClosed) {
		t.Fatalf("Probe after Close = %#v", availability)
	}
}

func TestCheckFlagsAcceptsOnlyZeroAndValidateOnly(t *testing.T) {
	for _, flags := range []uint32{0, flagValidateOnly} {
		if err := checkFlags(flags); err != nil {
			t.Errorf("checkFlags(%#x) = %v, want nil", flags, err)
		}
	}

	for _, flags := range []uint32{flagSaveToPersistence, 0x04, 0x08, 0x10, 0xffffffff} {
		if err := checkFlags(flags); !errors.Is(err, ErrInvalidFlags) {
			t.Errorf("checkFlags(%#x) = %v, want ErrInvalidFlags", flags, err)
		}
	}
}
