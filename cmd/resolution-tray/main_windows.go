//go:build windows

// Command resolution-tray is the production composition root: it wires a profile
// to the Win32 display controller, the read-only Toolhelp process checker, the
// application session, and the Walk tray UI. The profile is still the legacy seed
// here; loading the user's saved configuration arrives with the settings dialog.
package main

import (
	"log"

	"github.com/Alien7666/change_resolution/internal/app"
	"github.com/Alien7666/change_resolution/internal/display"
	"github.com/Alien7666/change_resolution/internal/domain"
	processcheck "github.com/Alien7666/change_resolution/internal/process"
	"github.com/Alien7666/change_resolution/internal/ui"
)

func main() {
	session := app.NewSession(
		display.NewWindowsController(),
		processcheck.NewToolhelpChecker(),
		domain.LegacySeedProfile(),
	)
	if err := ui.Run(session); err != nil {
		log.Fatal(err)
	}
}
