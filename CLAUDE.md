# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Windows-only Go system-tray tool. Manually toggles the Mi Monitor (matched by hardware-ID prefix `MONITOR\XMI27B2`, never a fixed `DISPLAY1` index) between its current mode and a built-in `1920×1440 @ 180 Hz` game mode via `ChangeDisplaySettingsExW`, then restores the saved mode 3 seconds after `VALORANT-Win64-Shipping.exe` disappears from the process list. Startup only reads and displays state; it never changes a display mode on its own.

Design spec: `docs/superpowers/specs/2026-09-13-go-display-tray-design.md`. Implementation plan: `docs/superpowers/plans/2026-09-13-go-display-tray.md`.

## Package layout

- `internal/domain` — `Mode`, `MonitorIdentity`, `MatchLevel`, `Target`, `Profile`, `MaxDimension`, and `LegacySeedProfile()`. Plain data, no Win32 dependency.
- `internal/display` — `Controller` interface (`ResolveTarget`, `CurrentMode`, `TestMode`, `ApplyMode`) plus the Win32 adapter (`EnumDisplayDevicesW`, `EnumDisplaySettingsW`, `ChangeDisplaySettingsExW`). Never sets `CDS_UPDATEREGISTRY`, `DM_POSITION`, or `CDS_SET_PRIMARY`; every change targets only the resolved Mi Monitor device.
- `internal/process` — `Checker` interface and the read-only Toolhelp implementation (`CreateToolhelp32Snapshot` + `Process32First/Next`). Matches only the executable name; never calls `OpenProcess`, reads memory, or inspects windows/command lines.
- `internal/app` — `gameTracker` (pure presence/delay state transitions) and `Session` (serialized `Enable`/`Disable`/`Shutdown`/`Refresh`, background game-presence watcher, `SetOnChange` notifications). This is where the state machine described in the design spec lives, fully covered by fake-backed tests.
- `internal/ui` — Walk main window and notification-area (tray) icon; renders `app.Snapshot` and forwards user actions to `Session`.
- `cmd/resolution-tray` — production composition root (`main_windows.go`), the Common Controls 6 / DPI manifest, and the `go:generate` resource directive.

## Commands

**`go` is not on PATH.** Prepend it for any manual command:

```powershell
$env:PATH = "C:\Program Files\Go\bin;$env:PATH"
```

```powershell
# Reproducible build: go generate → go test ./... → go vet ./... → GUI-subsystem exe
./build.ps1

# Individual commands
go test ./...
go vet ./...
go generate ./cmd/resolution-tray

# Opt-in integration test — only exercises CDS_TEST, never changes the real display
$env:RUN_DISPLAY_INTEGRATION = '1'
go test ./internal/display -run TestWindowsControllerCanTestMiMonitorMode -v
Remove-Item Env:RUN_DISPLAY_INTEGRATION
```

`go build` always targets `windows/amd64` with `CGO_ENABLED=0` and `-ldflags '-H windowsgui -s -w'`, producing `dist/ResolutionTray.exe` (gitignored, local artifact only).

### GitHub Actions

`build.ps1` stays the single source of truth for `go generate` and the link flags — both workflows call it instead of restating them.

- `.github/workflows/ci.yml` — push to any branch, pull requests, `workflow_dispatch`. On `windows-latest`: `gofmt -l ./internal ./cmd` (fails if anything is listed), `go mod tidy -diff`, `go build ./...`, `go vet ./...`, `go test ./...`, `go test -race ./...`, then `./build.ps1` to prove the GUI binary still links. `-race` needs a C toolchain, so CI is the only place the race detector runs.
- `.github/workflows/release.yml` — `v*` tag push, plus `workflow_dispatch` for a dry run that builds and checksums but publishes nothing. On `windows-latest`: `./build.ps1` (generate + full test suite + vet + build), SHA256 checksum, then `softprops/action-gh-release` attaches `dist/ResolutionTray.exe` and `dist/ResolutionTray.exe.sha256` to the release. Job permissions are `contents: write` and nothing else.
- Neither workflow sets `RUN_DISPLAY_INTEGRATION`. CI must never run the opt-in integration tests, which drive the real Win32 display APIs.

## Testing

- `internal/domain`, `internal/display`, `internal/process`: pure/table-driven unit tests plus Win32-struct-layout and fake-adapter tests — no real display or process API calls.
- `internal/app`: `Session` and `gameTracker` behavior is tested entirely against fake `display.Controller` / `process.Checker` implementations and a controllable clock; run with `go test -count=20 ./internal/app` to catch flaky ordering.
- `internal/display/integration_windows_test.go` contains one opt-in test gated on `RUN_DISPLAY_INTEGRATION=1` that calls the real Win32 APIs but only with `CDS_TEST`, so it never mutates the user's actual display.
- No Python tooling remains in the test loop; `go test ./...` is the only required check before building.

## Hard constraints

- Windows `amd64` only; no non-Windows runtime support is needed.
- `CGO_ENABLED=0` for production builds.
- Startup must remain read-only: read and display state, never change a display mode.
- Match the configured monitor through the identity ladder in `domain.MonitorIdentity`: the device interface path first, compared whole and case-insensitively; the hardware ID only as a fallback, only when the model was unique at the moment the user configured it, and only when it matches exactly one attached monitor. Never match by a fixed `DISPLAY1` index, and never take the first of several matches — ambiguity is an honest refusal, not a coin flip.
- The game mode is whatever the user configured. `domain.LegacySeedProfile()` is not that configuration: it exists only to pre-fill the first-run wizard with the original `1920×1440 @ 180 Hz` values, and is never the profile a shipping session runs on.
- Never launch VALORANT, inject or hook code, open its process, read its memory, or modify Riot/Vanguard/game files. Only observe the executable name `VALORANT-Win64-Shipping.exe` via Toolhelp.
- Restore 3 seconds after the game goes from seen/running to absent; manual disable restores immediately and cancels any pending automatic restore.
- Never use `CDS_UPDATEREGISTRY`; always test a target mode (`CDS_TEST`) before applying it (`CDS_TEST`/apply, never combined).
- Never modify the other three displays' modes or positions — only the resolved Mi Monitor target is touched.
- UI strings are in Traditional Chinese (繁體中文); preserve when editing `internal/ui`.

## Legacy Python scripts

The pre-refactor Python/PyInstaller implementation is gone: `res.py`, `res-auto.py`, their `.spec` files and the committed `build/`/`dist/` artifacts were deleted in plan Task 7, and `build/`/`dist/` are now ignored. Do not reintroduce them.

`res-2k.py` and `res-2k.spec` may still exist in the working tree as the user's own untracked local files. They are not part of this project — do not extend, build, or delete them.
