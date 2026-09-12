# Go Display Tray Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Python/PyInstaller resolution scripts with a single Windows Go tray application that manually toggles the Mi Monitor between its current mode and `1920×1440 @ 180 Hz`, then restores the saved mode three seconds after VALORANT closes.

**Architecture:** A small domain package defines profiles and display modes; Windows adapters implement display control and process enumeration; an application session owns the state machine and restoration rules; a Walk view renders the session and notification-area actions. The session depends on interfaces, so all behavior except the final Win32/Walk wiring is covered by deterministic tests.

**Tech Stack:** Go 1.27.x, `github.com/lxn/walk`, `github.com/lxn/win`, `golang.org/x/sys/windows`, Win32 `ChangeDisplaySettingsExW`, Toolhelp process snapshots, PowerShell build script.

**Spec:** `docs/superpowers/specs/2026-09-13-go-display-tray-design.md`

## Global Constraints

- Target only Windows `amd64`; the application does not need non-Windows runtime support.
- Keep production builds at `CGO_ENABLED=0`; Walk's optional CGo message loop and Go's CGo race detector are out of scope.
- On startup, read and display state but do not change any display mode.
- Match the Mi Monitor by hardware-ID prefix `MONITOR\\XMI27B2`, never by a fixed `DISPLAY1` index.
- The first built-in game mode is exactly `1920×1440 @ 180 Hz`, 32 bpp.
- Never launch VALORANT, inject or hook code, open its process, read its memory, or modify Riot/Vanguard/game files.
- Observe only the executable name `VALORANT-Win64-Shipping.exe` through Toolhelp process enumeration.
- Restore three seconds after the game changes from seen/running to absent; manual disable restores immediately.
- Do not use `CDS_UPDATEREGISTRY`; test the target mode before applying it.
- Do not modify the other three displays' modes or positions.
- Build a single GUI-subsystem `.exe` with an embedded Common Controls 6 manifest.
- Delete legacy Python/PyInstaller sources and generated artifacts only after the Go replacement passes tests and builds.

## File Map

- `go.mod`, `go.sum` — Go module and pinned dependencies.
- `.gitignore` — ignores local tools, Python environments, generated resources, and build output.
- `internal/domain/profile.go` — `Mode`, `Target`, `Profile`, and the built-in Mi Monitor profile.
- `internal/display/controller.go` — display-control interface, errors, and Win32-independent matching logic.
- `internal/display/win32_windows.go` — `EnumDisplayDevicesW`, `EnumDisplaySettingsW`, and `ChangeDisplaySettingsExW` adapter.
- `internal/process/checker.go` — process-checking interface and executable-name matching.
- `internal/process/toolhelp_windows.go` — read-only Toolhelp implementation.
- `internal/app/tracker.go` — pure game-presence and delayed-restore state transitions.
- `internal/app/session.go` — serialized enable, disable, monitoring, shutdown, and state notifications.
- `internal/app/session_test.go` — fake-backed application behavior tests.
- `internal/ui/window_windows.go` — Walk main window, tray icon, menu actions, and UI-thread synchronization.
- `cmd/resolution-tray/main_windows.go` — production composition root.
- `cmd/resolution-tray/resolution-tray.manifest` — Common Controls 6 and DPI-awareness manifest.
- `cmd/resolution-tray/generate.go` — resource-generation directive.
- `build.ps1` — reproducible resource generation, tests, and GUI build.
- `README.md` — user workflow, NVIDIA scaling prerequisite, build commands, and safety boundary.
- `CLAUDE.md` — updated project guidance for the Go implementation.

---

### Task 1: Bootstrap the Go module and display profile

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `internal/domain/profile_test.go`
- Create: `internal/domain/profile.go`

**Interfaces:**
- Produces: `domain.Mode`, `domain.Target`, `domain.Profile`, `domain.DefaultProfile()`.

- [x] **Step 1: Install and verify the Go toolchain**

Run:

```powershell
winget install --exact --id GoLang.Go --version 1.27.1 --accept-source-agreements --accept-package-agreements --silent
& 'C:\Program Files\Go\bin\go.exe' version
```

Expected: `go version go1.27.1 windows/amd64`. If `winget` reports the package is already installed, use the existing Go 1.27.x executable.

- [x] **Step 2: Initialize the module and ignore generated content**

Create `go.mod`:

```go
module github.com/Alien7666/change_resolution

go 1.27
```

Create `.gitignore`:

```gitignore
.tools/
.venv/
*.syso
dist/
build/
```

- [x] **Step 3: Write the failing profile test**

Create `internal/domain/profile_test.go`:

```go
package domain

import (
	"testing"
	"time"
)

func TestDefaultProfile(t *testing.T) {
	p := DefaultProfile()
	if p.MonitorHardwareID != `MONITOR\XMI27B2` {
		t.Fatalf("MonitorHardwareID = %q", p.MonitorHardwareID)
	}
	if p.GameMode != (Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}) {
		t.Fatalf("GameMode = %#v", p.GameMode)
	}
	if p.ProcessName != "VALORANT-Win64-Shipping.exe" || p.RestoreDelay != 3*time.Second {
		t.Fatalf("process/delay = %q/%s", p.ProcessName, p.RestoreDelay)
	}
}
```

- [x] **Step 4: Run the test to verify it fails**

Run: `go test ./internal/domain`

Expected: FAIL because `DefaultProfile`, `Mode`, and `Profile` do not exist.

- [x] **Step 5: Implement the profile types**

Create `internal/domain/profile.go`:

```go
package domain

import "time"

type Mode struct {
	Width        uint32
	Height       uint32
	RefreshHz    uint32
	BitsPerPixel uint32
}

type Target struct {
	DeviceName string
	HardwareID string
}

type Profile struct {
	Name               string
	MonitorHardwareID  string
	GameMode           Mode
	FallbackNativeMode Mode
	ProcessName        string
	RestoreDelay       time.Duration
}

func DefaultProfile() Profile {
	return Profile{
		Name:               "Mi Monitor 4:3",
		MonitorHardwareID:  `MONITOR\XMI27B2`,
		GameMode:           Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		FallbackNativeMode: Mode{Width: 2560, Height: 1440, RefreshHz: 180, BitsPerPixel: 32},
		ProcessName:        "VALORANT-Win64-Shipping.exe",
		RestoreDelay:       3 * time.Second,
	}
}
```

- [x] **Step 6: Verify and commit the bootstrap**

Run:

```powershell
go test ./internal/domain
git add go.mod .gitignore internal/domain
git commit -m "feat: bootstrap Go display profile"
```

Expected: tests PASS and the commit contains only module/profile bootstrap files.

---

### Task 2: Implement target-specific Win32 display control

**Files:**
- Create: `internal/display/controller.go`
- Create: `internal/display/controller_test.go`
- Create: `internal/display/win32_windows.go`
- Create: `internal/display/integration_windows_test.go`
- Modify: `go.mod`
- Create: `go.sum`

**Interfaces:**
- Consumes: `domain.Mode`, `domain.Target`.
- Produces: `display.Controller` with `ResolveTarget`, `CurrentMode`, `TestMode`, and `ApplyMode`; `display.NewWindowsController()`.

- [x] **Step 1: Add Windows dependencies**

Run:

```powershell
go get github.com/lxn/win@latest
go get golang.org/x/sys/windows@latest
go mod tidy
```

Expected: `go.mod` and `go.sum` pin `github.com/lxn/win` and `golang.org/x/sys`.

- [x] **Step 2: Write failing controller tests**

Create `internal/display/controller_test.go` with a fake native adapter and these exact cases:

```go
package display

import (
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
)

func TestResolveTargetMatchesHardwareIDPrefixCaseInsensitively(t *testing.T) {
	api := &fakeNative{targets: []domain.Target{
		{DeviceName: `\\.\DISPLAY2`, HardwareID: `MONITOR\ACR0D0D\0004`},
		{DeviceName: `\\.\DISPLAY1`, HardwareID: `monitor\xmi27b2\0009`},
	}}
	c := newController(api)
	got, err := c.ResolveTarget(`MONITOR\XMI27B2`)
	if err != nil || got.DeviceName != `\\.\DISPLAY1` {
		t.Fatalf("target=%#v err=%v", got, err)
	}
}

func TestTestAndApplyUseOnlyResolvedDevice(t *testing.T) {
	api := &fakeNative{}
	c := newController(api)
	target := domain.Target{DeviceName: `\\.\DISPLAY1`, HardwareID: `MONITOR\XMI27B2\0009`}
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	if err := c.TestMode(target, mode); err != nil { t.Fatal(err) }
	if err := c.ApplyMode(target, mode); err != nil { t.Fatal(err) }
	if len(api.changes) != 2 || !api.changes[0].test || api.changes[1].test {
		t.Fatalf("changes=%#v", api.changes)
	}
	for _, change := range api.changes {
		if change.deviceName != target.DeviceName { t.Fatalf("device=%q", change.deviceName) }
	}
}
```

Define `fakeNative`, `nativeChange`, and the `nativeAPI` methods in the same test file so no production test hook is exported.

- [x] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/display`

Expected: FAIL because `newController`, `ResolveTarget`, `TestMode`, and `ApplyMode` do not exist.

- [x] **Step 4: Implement the controller boundary**

Create `internal/display/controller.go` with these public contracts:

```go
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

type controller struct{ native nativeAPI }

func newController(native nativeAPI) *controller { return &controller{native: native} }

func (c *controller) ResolveTarget(prefix string) (domain.Target, error) {
	targets, err := c.native.listTargets()
	if err != nil { return domain.Target{}, err }
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
```

- [x] **Step 5: Implement the Windows adapter**

Create `internal/display/win32_windows.go`. Use `win.EnumDisplayDevices` twice: first for attached desktop adapters, then for their monitor device. Convert UTF-16 fields with `windows.UTF16ToString`. Use `win.EnumDisplaySettings` with `ENUM_CURRENT_SETTINGS` for the current mode. For changes, copy the current `win.DEVMODE`, set `DmPelsWidth`, `DmPelsHeight`, `DmDisplayFrequency`, `DmBitsPerPel`, and set exactly these field flags:

```go
dm.DmFields = win.DM_PELSWIDTH |
	win.DM_PELSHEIGHT |
	win.DM_DISPLAYFREQUENCY |
	win.DM_BITSPERPEL
flags := uint32(win.CDS_FULLSCREEN)
if test { flags = win.CDS_TEST }
result := win.ChangeDisplaySettingsEx(deviceNamePtr, &dm, 0, flags, nil)
if result != win.DISP_CHANGE_SUCCESSFUL {
	return fmt.Errorf("ChangeDisplaySettingsEx(%s): %s", deviceName, describeResult(result))
}
```

Do not set `DM_POSITION`, `CDS_SET_PRIMARY`, or `CDS_UPDATEREGISTRY`. Implement `describeResult` for all documented `DISP_CHANGE_*` values so errors remain actionable without exposing sensitive data.

- [x] **Step 6: Add a safe opt-in integration test**

Create `internal/display/integration_windows_test.go`:

```go
package display

import (
	"os"
	"testing"

	"github.com/Alien7666/change_resolution/internal/domain"
)

func TestWindowsControllerCanTestMiMonitorMode(t *testing.T) {
	if os.Getenv("RUN_DISPLAY_INTEGRATION") != "1" { t.Skip("set RUN_DISPLAY_INTEGRATION=1") }
	c := NewWindowsController()
	target, err := c.ResolveTarget(`MONITOR\XMI27B2`)
	if err != nil { t.Fatal(err) }
	mode := domain.Mode{Width: 1920, Height: 1440, RefreshHz: 180, BitsPerPixel: 32}
	if err := c.TestMode(target, mode); err != nil { t.Fatal(err) }
}
```

This test must use `CDS_TEST` only and must not change the display.

- [x] **Step 7: Verify and commit display control**

Run:

```powershell
go test ./internal/display
$env:RUN_DISPLAY_INTEGRATION='1'; go test ./internal/display -run TestWindowsControllerCanTestMiMonitorMode -v; Remove-Item Env:RUN_DISPLAY_INTEGRATION
git add go.mod go.sum internal/display
git commit -m "feat: control the Mi Monitor through Win32"
```

Expected: unit and safe integration tests PASS; all display changes target the dynamically resolved Mi Monitor device.

---

### Task 3: Implement read-only VALORANT process observation

**Files:**
- Create: `internal/process/checker.go`
- Create: `internal/process/checker_test.go`
- Create: `internal/process/toolhelp_windows.go`

**Interfaces:**
- Produces: `process.Checker` with `Running(executableName string) (bool, error)` and `process.NewToolhelpChecker()`.

- [x] **Step 1: Write the failing name-matching tests**

Create `internal/process/checker_test.go`:

```go
package process

import "testing"

func TestContainsExecutableUsesExactCaseInsensitiveName(t *testing.T) {
	names := []string{"RiotClientServices.exe", "valorant-win64-shipping.EXE"}
	if !containsExecutable(names, "VALORANT-Win64-Shipping.exe") { t.Fatal("expected match") }
	if containsExecutable(names, "VALORANT.exe") { t.Fatal("unexpected partial match") }
}
```

- [x] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/process`

Expected: FAIL because `containsExecutable` does not exist.

- [x] **Step 3: Implement the interface and pure matcher**

Create `internal/process/checker.go`:

```go
package process

import "strings"

type Checker interface {
	Running(executableName string) (bool, error)
}

func containsExecutable(names []string, target string) bool {
	for _, name := range names {
		if strings.EqualFold(name, target) { return true }
	}
	return false
}
```

- [x] **Step 4: Implement Toolhelp enumeration**

Create `internal/process/toolhelp_windows.go` using only:

```go
snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
defer windows.CloseHandle(snapshot)
var entry windows.ProcessEntry32
entry.Size = uint32(unsafe.Sizeof(entry))
```

Iterate with `windows.Process32First` and `windows.Process32Next`, convert `entry.ExeFile[:]` using `windows.UTF16ToString`, and compare with `strings.EqualFold`. Do not call `OpenProcess`, query modules, read memory, enumerate windows, or inspect command lines.

- [x] **Step 5: Verify and commit process observation**

Run:

```powershell
go test ./internal/process
git add internal/process
git commit -m "feat: observe VALORANT process exit safely"
```

Expected: tests PASS and production code contains no `OpenProcess` call.

---

### Task 4: Implement the restoration tracker and application session

**Files:**
- Create: `internal/app/tracker_test.go`
- Create: `internal/app/tracker.go`
- Create: `internal/app/session_test.go`
- Create: `internal/app/session.go`

**Interfaces:**
- Consumes: `domain.Profile`, `display.Controller`, `process.Checker`.
- Produces: `app.Session`, `app.State`, `app.Snapshot`, `Enable`, `Disable`, `Shutdown`, `Refresh`, and `SetOnChange`.

- [x] **Step 1: Write failing tracker tests**

Create `internal/app/tracker_test.go` and cover these exact transitions:

```go
func TestTrackerRestoresOnlyAfterSeenGameHasBeenMissingForDelay(t *testing.T)
func TestTrackerCancelsPendingRestoreWhenGameReturns(t *testing.T)
func TestTrackerNeverRestoresWhenGameWasNeverSeen(t *testing.T)
```

Use a fixed `time.Unix(100, 0)`, feed `Observe(false, now)`, `Observe(true, now)`, then absent observations at `now.Add(2*time.Second)` and `now.Add(3*time.Second)`. Assert `ShouldRestore` is true only for the final observation.

- [x] **Step 2: Run tracker tests to verify they fail**

Run: `go test ./internal/app -run Tracker`

Expected: FAIL because `gameTracker` and `Observe` do not exist.

- [x] **Step 3: Implement the pure tracker**

Create `internal/app/tracker.go`:

```go
package app

import "time"

type trackerResult struct {
	SeenGame     bool
	RestoreAt    time.Time
	ShouldRestore bool
}

type gameTracker struct {
	delay     time.Duration
	seen      bool
	missingAt time.Time
}

func newGameTracker(delay time.Duration) *gameTracker { return &gameTracker{delay: delay} }

func (t *gameTracker) Observe(running bool, now time.Time) trackerResult {
	if running {
		t.seen = true
		t.missingAt = time.Time{}
		return trackerResult{SeenGame: true}
	}
	if !t.seen { return trackerResult{} }
	if t.missingAt.IsZero() { t.missingAt = now }
	restoreAt := t.missingAt.Add(t.delay)
	return trackerResult{SeenGame: true, RestoreAt: restoreAt, ShouldRestore: !now.Before(restoreAt)}
}
```

- [x] **Step 4: Write failing session tests**

Create fake display and checker implementations in `internal/app/session_test.go`. Test:

```go
func TestEnableCapturesCurrentModeTestsThenApplies(t *testing.T)
func TestDisableRestoresCapturedMode(t *testing.T)
func TestEnableFailureDoesNotStartActiveSession(t *testing.T)
func TestShutdownRestoresOnlyWhenSessionAppliedMode(t *testing.T)
func TestManualDisableWinsOverPendingAutomaticRestore(t *testing.T)
func TestDisableUsesFallbackWhenAppStartsInUnmanagedFourByThreeMode(t *testing.T)
```

Assert the fake display call order is exactly `resolve`, `current`, `test`, `apply`; assert restore applies the captured mode rather than the hard-coded fallback.

- [x] **Step 5: Run session tests to verify they fail**

Run: `go test ./internal/app -run 'Enable|Disable|Shutdown|Manual'`

Expected: FAIL because `Session` does not exist.

- [x] **Step 6: Implement the serialized session**

Create `internal/app/session.go` with these contracts:

```go
type State string

const (
	StateNative State = "native"
	StateApplying State = "applying"
	StateWaitingForGame State = "waiting-for-game"
	StateGameRunning State = "game-running"
	StateRestorePending State = "restore-pending"
	StateRestoring State = "restoring"
	StateError State = "error"
)

type Snapshot struct {
	State       State
	Target      domain.Target
	CurrentMode domain.Mode
	FourByThree bool
	Message     string
	Err         error
}

func NewSession(displays display.Controller, processes process.Checker, profile domain.Profile) *Session
func (s *Session) SetOnChange(fn func(Snapshot))
func (s *Session) Snapshot() Snapshot
func (s *Session) Refresh() Snapshot
func (s *Session) Enable() error
func (s *Session) Disable() error
func (s *Session) Shutdown() error
```

Protect mutable state with `sync.Mutex`, serialize display mutations with a separate `sync.Mutex`, and cancel the watcher through `context.CancelFunc`. `Enable` must save the original mode before calling `TestMode` and `ApplyMode`; only a successful apply sets `FourByThree=true` and starts a one-second watcher. `Disable` must cancel the watcher before applying the captured original mode. If the application starts while the display is already in the profile's 4:3 mode and therefore owns no saved mode, manual `Disable` applies `FallbackNativeMode`; `Shutdown` does not apply that fallback because startup must remain read-only. Automatic restore calls the same `Disable` path, so manual and automatic restore cannot race.

`Refresh` resolves the target and reads its current mode without mutating it. It sets `Snapshot.FourByThree` by comparing the current width, height, refresh rate, and bits per pixel with `Profile.GameMode`; it never starts the watcher. This makes an unmanaged 4:3 mode visible and manually recoverable without violating the startup rule.

Watcher errors update the message but do not restore or alter display state. The watcher calls `gameTracker.Observe(running, time.Now())`; when `ShouldRestore` becomes true it calls `Disable` exactly once and exits.

- [x] **Step 7: Verify and commit the application core**

Run:

```powershell
go test -count=20 ./internal/app
go test ./...
git add internal/app
git commit -m "feat: manage automatic display restoration"
```

Expected: all tests PASS under the race detector and normal test run.

---

### Task 5: Build the native window and notification-area UI

**Files:**
- Create: `internal/ui/window_windows.go`
- Create: `cmd/resolution-tray/main_windows.go`
- Create: `cmd/resolution-tray/resolution-tray.manifest`
- Create: `cmd/resolution-tray/generate.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**
- Consumes: `app.Session`, `app.Snapshot`.
- Produces: `ui.Run(session *app.Session) error` and the Windows application entry point.

- [x] **Step 1: Add Walk and the resource generator**

Run:

```powershell
go get github.com/lxn/walk@latest
go get github.com/akavel/rsrc@latest
go mod tidy
```

Expected: dependencies are pinned in `go.mod`/`go.sum`.

- [x] **Step 2: Create the Windows manifest and generation directive**

Create `cmd/resolution-tray/resolution-tray.manifest` with Common Controls 6, `asInvoker`, `PerMonitorV2` DPI awareness, and Windows 10/11 compatibility. Create `cmd/resolution-tray/generate.go`:

```go
//go:build windows

package main

//go:generate go run github.com/akavel/rsrc -manifest resolution-tray.manifest -o rsrc.syso
```

Run: `go generate ./cmd/resolution-tray`

Expected: ignored file `cmd/resolution-tray/rsrc.syso` is generated.

- [x] **Step 3: Implement the Walk UI**

Create `internal/ui/window_windows.go` with a 420×240 fixed-size main window containing:

- title `VALORANT 4:3 顯示工具`;
- target label `Mi Monitor (XMI27B2)`;
- current-mode label;
- checkbox `使用 4:3（1920×1440 @ 180 Hz）`;
- read-only status label;
- buttons `隱藏至系統匣` and `恢復 2K`.

Use `walk.IconApplication()` for the window and tray icon. The checkbox handler runs `session.Enable` or `session.Disable` in a goroutine, disables controls while the operation is in progress, and shows an error dialog on failure. Register `session.SetOnChange` and call `mainWindow.Synchronize(func() { render(snapshot) })` for all background updates.

Create one `walk.NotifyIcon` with tooltip `VALORANT 4:3 顯示工具`. Add actions `顯示主視窗`, `使用 4:3`, `恢復原始解析度`, separator, and `結束`. Window close and minimize events hide the window without ending the message loop. Tray left-click restores and foregrounds the main window. `結束` calls `session.Shutdown`; on restore failure it keeps the application alive and reports the error instead of silently abandoning 4:3.

- [x] **Step 4: Compose the production application**

Create `cmd/resolution-tray/main_windows.go`:

```go
//go:build windows

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
	session := app.NewSession(display.NewWindowsController(), processcheck.NewToolhelpChecker(), domain.DefaultProfile())
	if err := ui.Run(session); err != nil { log.Fatal(err) }
}
```

- [x] **Step 5: Verify UI compilation and commit**

Run:

```powershell
go generate ./cmd/resolution-tray
go test ./...
go vet ./...
go build -trimpath -ldflags '-H windowsgui' -o dist/ResolutionTray.exe ./cmd/resolution-tray
git add go.mod go.sum internal/ui cmd/resolution-tray
git commit -m "feat: add native tray interface"
```

Expected: tests and vet PASS, `dist/ResolutionTray.exe` builds, and starting it shows no console window.

---

### Task 6: Add reproducible build and user documentation

**Files:**
- Create: `build.ps1`
- Create: `README.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Produces: `./build.ps1` and documented user/build workflows.

- [x] **Step 1: Create the build script**

Create `build.ps1` with `$ErrorActionPreference = 'Stop'`, resolve `go.exe` from PATH or `C:\Program Files\Go\bin\go.exe`, then run every Go command through this checked helper:

```powershell
function Invoke-Go {
    param([Parameter(Mandatory)][string[]]$Arguments)
    & $go @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "go $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
    }
}

$env:CGO_ENABLED = '0'
Invoke-Go @('generate', './cmd/resolution-tray')
Invoke-Go @('test', './...')
Invoke-Go @('vet', './...')
New-Item -ItemType Directory -Force -Path 'dist' | Out-Null
Invoke-Go @('build', '-trimpath', '-ldflags', '-H windowsgui -s -w', '-o', 'dist/ResolutionTray.exe', './cmd/resolution-tray')
```

This prevents a failed generate, test, vet, or build command from leaving a release artifact that appears successful.

- [x] **Step 2: Document operation and safety boundaries**

Create `README.md` covering:

- supported environment: Windows 10/11 amd64 and Mi Monitor `XMI27B2`;
- one-time NVIDIA setting: full-screen scaling performed on GPU;
- startup does nothing;
- manual 4:3 on/off workflow;
- automatic restore three seconds after `VALORANT-Win64-Shipping.exe` closes;
- tray actions and normal-exit restoration;
- the unavoidable brief blanking during a physical display-mode change;
- no Riot launcher, injection, memory access, overlay, or game-file changes;
- `go test ./...`, opt-in `CDS_TEST` integration test, and `./build.ps1` commands.

Replace the Python-specific content in `CLAUDE.md` with the same Go architecture and commands, keeping Traditional Chinese UI-string guidance.

- [x] **Step 3: Verify and commit build/documentation**

Run:

```powershell
./build.ps1
Get-Item 'dist/ResolutionTray.exe' | Select-Object Name,Length,LastWriteTime
git add build.ps1 README.md CLAUDE.md
git commit -m "docs: add Go build and usage workflow"
```

Expected: build succeeds and produces a non-empty `ResolutionTray.exe`; documentation matches the implemented behavior.

---

### Task 7: Verify the replacement and remove the Python implementation

**Files:**
- Delete: `res.py`
- Delete: `res-auto.py`
- Delete: `res.spec`
- Delete: `res-auto.spec`
- Delete: `res-2k.py`
- Delete: `res-2k.spec`
- Delete: tracked contents under `build/res/` and `build/res-auto/`
- Delete: local contents under `build/res-2k/`
- Delete: `dist/res.exe`
- Delete: `dist/res-auto.exe`
- Delete: `dist/res-2k.exe`

**Interfaces:**
- Consumes: the tested and built Go replacement.
- Produces: a Go-only source tree; generated `dist/ResolutionTray.exe` remains ignored and local.

- [x] **Step 1: Run the complete automated verification before deletion**

Run:

```powershell
go test -count=20 ./internal/app
go test ./...
go vet ./...
$env:RUN_DISPLAY_INTEGRATION='1'; go test ./internal/display -run TestWindowsControllerCanTestMiMonitorMode -v; Remove-Item Env:RUN_DISPLAY_INTEGRATION
./build.ps1
```

Expected: all tests, vet, safe mode validation, and build PASS.

- [ ] **Step 2: Perform the manual Windows smoke test**

With VALORANT closed:

1. Record all four displays' current width, height, refresh rate, and desktop position.
2. Start `dist/ResolutionTray.exe`; verify startup does not change any display.
3. Enable 4:3; verify only Mi Monitor changes to `1920×1440 @ 180 Hz`.
4. Disable 4:3; verify Mi Monitor returns to the recorded mode and the other three displays remain unchanged.
5. Enable 4:3, start VALORANT manually, close it, and verify restore occurs after three seconds.
6. Enable 4:3 while VALORANT is running, then disable it manually and verify immediate restoration.
7. Minimize and close the main window; verify both hide to the system tray and tray actions remain functional.

Expected: every behavior matches the design spec without the application launching or attaching to VALORANT.

- [x] **Step 3: Remove exact legacy targets**

Use one PowerShell session and this exact target list. Compute every absolute target before deleting anything, reject paths outside the repository, and preserve `.venv/` plus `dist/ResolutionTray.exe`:

```powershell
$repoRoot = [IO.Path]::GetFullPath((Get-Location).Path).TrimEnd([IO.Path]::DirectorySeparatorChar)
$legacyTargets = @(
    'res.py',
    'res-auto.py',
    'res.spec',
    'res-auto.spec',
    'res-2k.py',
    'res-2k.spec',
    'build\res',
    'build\res-auto',
    'build\res-2k',
    'dist\res.exe',
    'dist\res-auto.exe',
    'dist\res-2k.exe'
)
$resolvedTargets = foreach ($relativePath in $legacyTargets) {
    $absolutePath = [IO.Path]::GetFullPath((Join-Path $repoRoot $relativePath))
    if (-not $absolutePath.StartsWith($repoRoot + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing path outside repository: $absolutePath"
    }
    $absolutePath
}
$resolvedTargets | ForEach-Object { Write-Host "Legacy target: $_" }
foreach ($absolutePath in $resolvedTargets) {
    if (Test-Path -LiteralPath $absolutePath) {
        Remove-Item -LiteralPath $absolutePath -Recurse -Force
    }
}
```

- [x] **Step 4: Re-run verification after deletion**

Run:

```powershell
go test -count=20 ./internal/app
go test ./...
go vet ./...
./build.ps1
git status --short
```

Expected: verification still passes; no Python source/spec or legacy executable remains; `dist/` stays ignored.

- [x] **Step 5: Commit the migration cleanup**

Run:

```powershell
git add -A
git diff --cached --check
git diff --cached | Select-String -Pattern 'password|secret|api_key|token' -CaseSensitive:$false
git commit -m "refactor: replace Python resolution scripts with Go tray app"
```

Expected: the staged diff deletes only the enumerated legacy files and includes no generated Go binary or secret.

---

### Task 8: Final review and handoff

**Files:**
- Modify only files required by concrete review findings.

**Interfaces:**
- Validates every interface and behavior produced by Tasks 1–7.

- [x] **Step 1: Inspect the complete branch diff**

Run:

```powershell
git diff main...HEAD --stat
git diff main...HEAD -- . ':(exclude)docs/superpowers/plans/2026-09-13-go-display-tray.md'
```

Check for accidental display-wide changes, fixed `DISPLAY1` assumptions, `CDS_UPDATEREGISTRY`, VALORANT launch code, `OpenProcess`, unbounded goroutines, UI updates outside `Synchronize`, and generated artifacts.

- [x] **Step 2: Run final verification**

Run:

```powershell
go fmt ./...
go test -count=20 ./internal/app
go test ./...
go vet ./...
./build.ps1
git diff --check
git status --short
```

Expected: formatting, tests, race detector, vet, and build PASS; only intended source/documentation changes are present.

- [x] **Step 3: Commit review fixes only when needed**

If Step 1 finds a concrete issue, add a focused regression test, apply the smallest fix, rerun Step 2, and commit with `fix: <specific user-visible behavior>`. If no issue is found, do not create an empty commit.
