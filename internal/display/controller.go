package display

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Alien7666/change_resolution/internal/domain"
)

var ErrTargetNotFound = errors.New("target display not found")

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

func (c *controller) TestMode(target domain.Target, mode domain.Mode) error {
	return c.native.changeMode(target.DeviceName, mode, true)
}

func (c *controller) ApplyMode(target domain.Target, mode domain.Mode) error {
	return c.native.changeMode(target.DeviceName, mode, false)
}
