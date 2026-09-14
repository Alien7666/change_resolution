// Package scaling reads and changes the NVIDIA display-scaling field without
// depending on the display-mode package. Native bindings live in nvapi_windows.go;
// this file contains only decisions that can be exercised against a fake driver.
package scaling

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Alien7666/change_resolution/internal/domain"
)

// Mode is the scaling geometry encoded by NV_SCALING.
type Mode uint8

const (
	ModeUnrecognized Mode = iota
	ModeDefault
	ModeFullScreen
	ModeAspectRatio
	ModeNoScaling
	ModeIntegerScaling
	ModeCustomized
)

// By identifies whether the GPU or the display performs the scaling.
type By uint8

const (
	ByUnknown By = iota
	ByGPU
	ByDisplay
)

// Value preserves the raw driver value alongside its user-facing dimensions.
type Value struct {
	Raw  uint32
	Mode Mode
	By   By
}

const (
	flagValidateOnly      uint32 = 0x01
	flagSaveToPersistence uint32 = 0x02 // never enters flags; declared only so the guard can name it
)

var (
	ErrInvalidFlags              = errors.New("invalid NVAPI display-config flags")
	ErrNvapiUnavailable          = errors.New("NVAPI unavailable")
	ErrNotNvidiaDisplay          = errors.New("target monitor is not driven by NVIDIA")
	ErrScalingTargetNotFound     = errors.New("NVAPI scaling target not found")
	ErrScalingTargetAmbiguous    = errors.New("NVAPI scaling target is ambiguous")
	ErrScalingPayloadDiverged    = errors.New("NVAPI display-config payload diverged")
	ErrIncompatibleStructVersion = errors.New("NVAPI display-config struct version is incompatible")
	ErrControllerClosed          = errors.New("scaling controller is closed")
)

// State is a fact read from the driver. DisplayID is the stable NVAPI key, never a
// GDI display name that a set may invalidate.
type State struct {
	DisplayID uint32
	Effective Value
}

// Outcome keeps the requested value separate from the effective read-back. A
// driver-normalised value is a successful operation with Matched false.
type Outcome struct {
	Requested Value
	State     State
	Matched   bool
}

// Availability reports whether this process can use the scaling controller and,
// when it cannot, carries the actionable cause for the UI.
type Availability struct {
	Available bool
	Reason    string
	Err       error
}

// Controller deliberately accepts only the stable monitor identity. It neither
// accepts nor returns domain.Target: allowing a caller to carry Target.DeviceName
// across an NVAPI set would reintroduce the stale-name hazard this boundary exists
// to prevent.
type Controller interface {
	Probe() Availability
	Read(domain.MonitorIdentity) (State, error)
	Apply(domain.MonitorIdentity, Value) (Outcome, error)
	Close() error
}

type status int32

const (
	statusOK                        status = 0
	statusIncompatibleStructVersion status = -9
)

// nvapi is the boundary Task 14 implements. It owns DLL loading, syscall argument
// layout, backing slices and runtime.KeepAlive; every decision about a payload or
// call sequence stays in this file.
type nvapi interface {
	initialize() status
	readConfig() (*config, status)
	writeConfig(*config, uint32) status
	displayIDByName(string) (uint32, status)
	errorMessage(status) string
	unload() status
}

// targetResolver is injected by the composition root as
// display.Controller.ResolveTarget. The GDI name exists only as a local value long
// enough to be converted to displayId; the controller stores only the latter.
type targetResolver func(domain.MonitorIdentity) (domain.Target, error)

// payloadField is one deterministic line of the native payload. Task 14 supplies
// every native field in this form, flattening raw pointer values to nil/non-nil.
// Numeric fields point into the backing structs so changing Scaling changes the
// exact bytes writeConfig will pass to the driver.
type payloadField struct {
	name   string
	value  string
	number *uint32
}

type configTarget struct {
	displayID    uint32
	scaling      *uint32
	scalingField string
}

// config is the cross-file handoff to Task 14. fields provide the complete,
// deterministic safety view; targets identify the only writable uint32; native
// holds Task 14's typed backing slices so they remain alive through each syscall.
type config struct {
	fields  []payloadField
	targets []configTarget
	native  any
}

type controller struct {
	mu sync.Mutex

	api     nvapi
	resolve targetResolver
	loadErr error

	initialized bool
	closed      bool
	disabled    error
	displayIDs  map[domain.MonitorIdentity]uint32
}

func newController(api nvapi, resolve targetResolver, loadErr error) *controller {
	return &controller{
		api: api, resolve: resolve, loadErr: loadErr,
		displayIDs: make(map[domain.MonitorIdentity]uint32),
	}
}

func (c *controller) Probe() Availability {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ready(); err != nil {
		return Availability{Reason: err.Error(), Err: err}
	}
	return Availability{Available: true}
}

func (c *controller) Read(identity domain.MonitorIdentity) (State, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ready(); err != nil {
		return State{}, err
	}
	cfg, err := c.readConfig()
	if err != nil {
		return State{}, err
	}
	displayID, target, err := c.resolveTarget(cfg, identity)
	if err != nil {
		return State{}, err
	}
	return stateOf(displayID, target)
}

func (c *controller) Apply(identity domain.MonitorIdentity, requested Value) (Outcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ready(); err != nil {
		return Outcome{Requested: requested}, err
	}

	// Every real write starts from a fresh full configuration. No payload from an
	// earlier Read or Apply is retained across a set.
	cfg, err := c.readConfig()
	if err != nil {
		return Outcome{Requested: requested}, err
	}
	displayID, target, err := c.resolveTarget(cfg, identity)
	if err != nil {
		return Outcome{Requested: requested}, err
	}
	beforeState, err := stateOf(displayID, target)
	if err != nil {
		return Outcome{Requested: requested}, err
	}

	// Sending a same-value set is all risk and no result: even an identical NVAPI
	// payload may renumber GDI devices. It is therefore a successful no-op. Task 15
	// compares its pre-read value with the request and takes no scaling ownership.
	if beforeState.Effective.Raw == requested.Raw {
		return Outcome{Requested: requested, State: beforeState, Matched: true}, nil
	}

	before := renderConfig(cfg)
	previous := *target.scaling
	*target.scaling = requested.Raw
	after := renderConfig(cfg)
	if err := requireOnlyScalingChanged(before, after, target.scalingField); err != nil {
		*target.scaling = previous
		wrapped := fmt.Errorf("%w: %v", ErrScalingPayloadDiverged, err)
		c.disabled = wrapped
		return Outcome{Requested: requested, State: beforeState}, wrapped
	}

	if err := checkFlags(flagValidateOnly); err != nil {
		return Outcome{Requested: requested, State: beforeState}, err
	}
	if callStatus := c.api.writeConfig(cfg, flagValidateOnly); callStatus != statusOK {
		return Outcome{Requested: requested, State: beforeState}, c.callError("validate display config", callStatus)
	}
	if err := checkFlags(0); err != nil {
		return Outcome{Requested: requested, State: beforeState}, err
	}
	applyStatus := c.api.writeConfig(cfg, 0)

	// A set can partially take effect even when NVAPI reports failure, and it can
	// invalidate every GDI name. Read back by the stable displayId without resolving
	// or storing another DeviceName.
	readBack, readErr := c.readStateByID(displayID)
	outcome := Outcome{
		Requested: requested,
		State:     readBack,
		Matched:   readErr == nil && readBack.Effective.Raw == requested.Raw,
	}
	if applyStatus != statusOK {
		applyErr := c.callError("apply display config", applyStatus)
		if readErr != nil {
			return outcome, errors.Join(applyErr, fmt.Errorf("read effective scaling after failed apply: %w", readErr))
		}
		return outcome, applyErr
	}
	if readErr != nil {
		return outcome, fmt.Errorf("read effective scaling after apply: %w", readErr)
	}
	return outcome, nil
}

func (c *controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if !c.initialized || c.api == nil {
		return nil
	}
	if callStatus := c.api.unload(); callStatus != statusOK {
		return c.callError("unload NVAPI", callStatus)
	}
	return nil
}

func (c *controller) ready() error {
	if c.closed {
		return ErrControllerClosed
	}
	if c.disabled != nil {
		return c.disabled
	}
	if c.loadErr != nil {
		c.disabled = fmt.Errorf("%w: %v", ErrNvapiUnavailable, c.loadErr)
		return c.disabled
	}
	if c.api == nil {
		c.disabled = fmt.Errorf("%w: no NVAPI implementation", ErrNvapiUnavailable)
		return c.disabled
	}
	if c.initialized {
		return nil
	}
	if callStatus := c.api.initialize(); callStatus != statusOK {
		err := c.callError("initialize NVAPI", callStatus)
		c.disabled = fmt.Errorf("%w: %v", ErrNvapiUnavailable, err)
		return c.disabled
	}
	c.initialized = true
	return nil
}

func (c *controller) readConfig() (*config, error) {
	cfg, callStatus := c.api.readConfig()
	if callStatus != statusOK {
		return nil, c.callError("read display config", callStatus)
	}
	if cfg == nil {
		return nil, errors.New("read display config: NVAPI returned no configuration")
	}
	return cfg, nil
}

func (c *controller) resolveTarget(
	cfg *config,
	identity domain.MonitorIdentity,
) (uint32, *configTarget, error) {
	if cached, ok := c.displayIDs[identity]; ok {
		if target, err := exactlyOneTarget(cfg, cached); err == nil {
			return cached, target, nil
		}
		delete(c.displayIDs, identity)
	}
	if c.resolve == nil {
		return 0, nil, fmt.Errorf("%w: no monitor identity resolver", ErrNotNvidiaDisplay)
	}
	target, err := c.resolve(identity)
	if err != nil {
		return 0, nil, err
	}
	// DeviceName is intentionally local. It is consumed into displayId here and is
	// never stored in controller state or returned from this package.
	displayID, callStatus := c.api.displayIDByName(target.DeviceName)
	if callStatus != statusOK {
		return 0, nil, fmt.Errorf("%w: %v", ErrNotNvidiaDisplay,
			c.callError("resolve NVIDIA display ID", callStatus))
	}
	c.displayIDs[identity] = displayID
	selected, err := exactlyOneTarget(cfg, displayID)
	if err != nil {
		return 0, nil, err
	}
	return displayID, selected, nil
}

func (c *controller) readStateByID(displayID uint32) (State, error) {
	cfg, err := c.readConfig()
	if err != nil {
		return State{}, err
	}
	target, err := exactlyOneTarget(cfg, displayID)
	if err != nil {
		return State{}, err
	}
	return stateOf(displayID, target)
}

func exactlyOneTarget(cfg *config, displayID uint32) (*configTarget, error) {
	var selected *configTarget
	count := 0
	for index := range cfg.targets {
		if cfg.targets[index].displayID == displayID {
			selected = &cfg.targets[index]
			count++
		}
	}
	switch count {
	case 0:
		return nil, fmt.Errorf("%w: displayId %#x", ErrScalingTargetNotFound, displayID)
	case 1:
		return selected, nil
	default:
		return nil, fmt.Errorf("%w: displayId %#x matched %d targets",
			ErrScalingTargetAmbiguous, displayID, count)
	}
}

func stateOf(displayID uint32, target *configTarget) (State, error) {
	if target == nil || target.scaling == nil {
		return State{}, fmt.Errorf("%w: displayId %#x has no scaling field",
			ErrScalingPayloadDiverged, displayID)
	}
	return State{DisplayID: displayID, Effective: decodeValue(*target.scaling)}, nil
}

func renderConfig(cfg *config) []string {
	lines := make([]string, 0, len(cfg.fields))
	for _, field := range cfg.fields {
		value := field.value
		if field.number != nil {
			value = fmt.Sprint(*field.number)
		}
		lines = append(lines, field.name+"="+value)
	}
	sort.Strings(lines)
	return lines
}

func requireOnlyScalingChanged(before, after []string, scalingField string) error {
	if len(before) != len(after) {
		return fmt.Errorf("payload line count changed from %d to %d", len(before), len(after))
	}
	changed := make([]string, 0, 2)
	for index := range before {
		if before[index] != after[index] {
			changed = append(changed, before[index]+" -> "+after[index])
		}
	}
	if len(changed) != 1 {
		return fmt.Errorf("changed %d payload lines: %s", len(changed), strings.Join(changed, "; "))
	}
	wantPrefix := scalingField + "="
	if !strings.HasPrefix(beforeLine(changed[0]), wantPrefix) {
		return fmt.Errorf("changed field is not %s: %s", scalingField, changed[0])
	}
	return nil
}

func beforeLine(change string) string {
	line, _, _ := strings.Cut(change, " -> ")
	return line
}

func (c *controller) callError(operation string, callStatus status) error {
	err := fmt.Errorf("%s: NVAPI status %d: %s", operation, callStatus, c.api.errorMessage(callStatus))
	if callStatus == statusIncompatibleStructVersion {
		err = errors.Join(ErrIncompatibleStructVersion, err)
		c.disabled = err
	}
	return err
}

func decodeValue(raw uint32) Value {
	value := Value{Raw: raw, Mode: ModeUnrecognized, By: ByUnknown}
	switch raw {
	case 0:
		value.Mode = ModeDefault
	case 1:
		value.Mode, value.By = ModeFullScreen, ByDisplay
	case 2:
		value.Mode, value.By = ModeFullScreen, ByGPU
	case 3:
		value.Mode, value.By = ModeNoScaling, ByGPU
	case 5:
		value.Mode, value.By = ModeAspectRatio, ByGPU
	case 6:
		value.Mode, value.By = ModeAspectRatio, ByDisplay
	case 7:
		value.Mode, value.By = ModeNoScaling, ByDisplay
	case 8:
		value.Mode, value.By = ModeIntegerScaling, ByGPU
	case 255:
		value.Mode = ModeCustomized
	}
	return value
}

func checkFlags(flags uint32) error {
	if flags != 0 && flags != flagValidateOnly {
		return fmt.Errorf("%w: %#x", ErrInvalidFlags, flags)
	}
	return nil
}
