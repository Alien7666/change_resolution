//go:build windows

// Command resolution-tray is the production composition root: it wires the Win32
// display controller, the read-only Toolhelp process checker and the NVAPI scaling
// controller into the configuration provider and the Walk tray UI. The provider owns
// startup loading and can represent a missing or rejected file without constructing a
// seed session.
package main

import (
	"log"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/display"
	processcheck "github.com/Alien7666/change_resolution/internal/process"
	"github.com/Alien7666/change_resolution/internal/scaling"
	"github.com/Alien7666/change_resolution/internal/ui"
)

func main() {
	displays := display.NewWindowsController()
	processes := processcheck.NewToolhelpChecker()
	// The scaling controller resolves monitor identities through the display
	// controller's own ladder rather than importing internal/display, which the two
	// packages deliberately never do in either direction. The domain.Target that
	// crosses this line is consumed into an NVAPI displayId inside internal/scaling and
	// dropped there; no device name ever comes back out.
	//
	// A machine with no NVIDIA driver simply produces a controller whose Probe reports
	// unavailable with a reason. Nothing fails and nothing changes.
	scalings := scaling.NewWindowsController(displays.ResolveTarget)
	provider := app.NewProviderWithScaling(displays, processes, scalings)
	if err := ui.Run(provider, displays, processes); err != nil {
		_ = provider.Shutdown()
		log.Fatal(err)
	}
}
