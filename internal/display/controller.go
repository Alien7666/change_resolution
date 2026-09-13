package display

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Alien7666/change_resolution/internal/domain"
)

var (
	// ErrTargetNotFound reports that no attached monitor matched the requested
	// hardware ID prefix.
	ErrTargetNotFound = errors.New("target display not found")

	// ErrModeNotSupported reports that the CDS_TEST pre-flight refused the mode, so
	// the target cannot be driven at it. Callers use errors.Is to separate an
	// unsupported mode from an apply that merely failed once.
	ErrModeNotSupported = errors.New("display mode not supported")
)

type Controller interface {
	ResolveTarget(hardwareIDPrefix string) (domain.Target, error)
	CurrentMode(target domain.Target) (domain.Mode, error)
	CurrentLayout() (domain.Layout, error)
	TestMode(target domain.Target, mode domain.Mode) error
	ApplyLayout(plan domain.LayoutPlan) error
}

type nativeAPI interface {
	listTargets() ([]domain.Target, error)
	currentMode(deviceName string) (domain.Mode, error)
	currentLayout() (domain.Layout, error)
	testMode(deviceName string, mode domain.Mode) error
	applyLayout(plan domain.LayoutPlan) error
}

type controller struct {
	native nativeAPI
}

func newController(native nativeAPI) *controller {
	return &controller{native: native}
}

func (c *controller) ResolveTarget(prefix string) (domain.Target, error) {
	targets, err := c.native.listTargets()
	if err != nil {
		return domain.Target{}, err
	}
	for _, target := range targets {
		if strings.HasPrefix(strings.ToUpper(target.HardwareID), strings.ToUpper(prefix)) {
			return target, nil
		}
	}
	return domain.Target{}, fmt.Errorf("%w: %s", ErrTargetNotFound, prefix)
}

func (c *controller) CurrentMode(target domain.Target) (domain.Mode, error) {
	return c.native.currentMode(target.DeviceName)
}

// CurrentLayout reads every attached display's mode and desktop position. It is a
// read: planning a contiguous desktop needs the coordinates of the displays the
// tool will move, not just the target's mode.
func (c *controller) CurrentLayout() (domain.Layout, error) {
	return c.native.currentLayout()
}

// TestMode runs the CDS_TEST pre-flight. Every rejection is wrapped with
// ErrModeNotSupported so callers can gate on the sentinel instead of on the
// wording of the underlying Win32 diagnostic.
func (c *controller) TestMode(target domain.Target, mode domain.Mode) error {
	if err := c.native.testMode(target.DeviceName, mode); err != nil {
		return fmt.Errorf("%w: %w", ErrModeNotSupported, err)
	}
	return nil
}

// ApplyLayout applies a whole arrangement as one transaction. Callers build the
// plan with PlanModeChange or PlanRestore, which refuse to produce a plan they
// cannot prove safe, so nothing partial ever reaches the driver.
func (c *controller) ApplyLayout(plan domain.LayoutPlan) error {
	return c.native.applyLayout(plan)
}
