//go:build windows

// Command resolution-tray is the production composition root: it wires the Win32
// display controller and read-only Toolhelp process checker into the configuration
// provider and the Walk tray UI. The provider owns startup loading and can represent
// a missing or rejected file without constructing a seed session.
package main

import (
	"log"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/display"
	processcheck "github.com/Alien7666/change_resolution/internal/process"
	"github.com/Alien7666/change_resolution/internal/ui"
)

func main() {
	displays := display.NewWindowsController()
	processes := processcheck.NewToolhelpChecker()
	provider := app.NewProvider(displays, processes)
	if err := ui.Run(provider, displays, processes); err != nil {
		_ = provider.Shutdown()
		log.Fatal(err)
	}
}
