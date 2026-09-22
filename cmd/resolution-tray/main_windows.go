//go:build windows

// Command resolution-tray is the production composition root: it wires the Win32
// display controller, the read-only Toolhelp process checker and the NVAPI scaling
// controller into the configuration provider and the Walk tray UI. The provider owns
// startup loading and can represent a missing or rejected file without constructing a
// seed session.
package main

import (
	"errors"
	"log"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/display"
	processcheck "github.com/Alien7666/change_resolution/internal/process"
	"github.com/Alien7666/change_resolution/internal/scaling"
	"github.com/Alien7666/change_resolution/internal/singleton"
	"github.com/Alien7666/change_resolution/internal/ui"
)

// singletonName is this product's name in the session's kernel namespace. It is not
// the window title and must not be derived from one: a name a user could change would
// stop being the same name to the copy that is already running.
const singletonName = "ResolutionTray.SingleInstance"

func main() {
	// Before anything is constructed, and especially before anything reads the
	// desktop. Two copies would each record an "original" arrangement to restore to,
	// and the second one's original would be the first one's changed desktop -- so
	// whichever exits last puts back a layout that was never the user's.
	release, err := singleton.Acquire(singletonName)
	if err != nil {
		if errors.Is(err, singleton.ErrAlreadyRunning) {
			// A tray application is usually hidden, so a silent exit looks exactly
			// like the launcher having done nothing. Show the copy that is running.
			singleton.ActivateExisting(ui.WindowTitle)
			return
		}
		log.Fatal(err)
	}
	defer release()

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
