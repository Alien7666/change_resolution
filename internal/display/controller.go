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
	TestMode(target domain.Target, mode domain.Mode) error
	ApplyMode(target domain.Target, mode domain.Mode) error
}

type nativeAPI interface {
	listTargets() ([]domain.Target, error)
	currentMode(deviceName string) (domain.Mode, error)
	changeMode(deviceName string, mode domain.Mode, test bool) error
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

// TestMode runs the CDS_TEST pre-flight. Every rejection is wrapped with
// ErrModeNotSupported so callers can gate on the sentinel instead of on the
// wording of the underlying Win32 diagnostic.
func (c *controller) TestMode(target domain.Target, mode domain.Mode) error {
	if err := c.native.changeMode(target.DeviceName, mode, true); err != nil {
		return fmt.Errorf("%w: %w", ErrModeNotSupported, err)
	}
	return nil
}

func (c *controller) ApplyMode(target domain.Target, mode domain.Mode) error {
	return c.native.changeMode(target.DeviceName, mode, false)
}
