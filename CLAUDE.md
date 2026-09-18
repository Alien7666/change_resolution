# CLAUDE.md

Guidance for contributors working on ResolutionTray, a Windows-only Go system-tray tool for a user-configured display profile and optional NVIDIA GPU-scaling control.

## Design authorities

Read these together before changing behaviour:

1. [Shipped display-tray design](docs/superpowers/specs/2026-09-13-go-display-tray-design.md)
2. [Configurable profile design](docs/superpowers/specs/2026-09-13-configurable-profile-design.md)
3. [NVIDIA GPU-scaling design](docs/superpowers/specs/2026-09-14-gpu-scaling-design.md)
4. [Active configurable-tray plan](docs/superpowers/plans/2026-09-14-configurable-tray.md)

The three specifications define behaviour. The active plan resolves their implementation ordering and constraints. Do not treat old worktree copies under .claude or .superpowers as source code.

## Runtime model

Startup is read-only. A configured session applies the selected display mode only after an explicit user action, saves the current desktop arrangement for that run, and restores it on manual disable, watched-process disappearance after the profile delay, or normal shutdown. **RecoveryPending** means the desktop may have changed but the game mode was not confirmed: retain the original layout and stable bindings, permit only manual restore or normal-shutdown restore, and forbid another apply, scaling write, or profile change.

Monitor identity uses the complete device-interface path, compared case-insensitively. A hardware-ID fallback is allowed only when the model was unique at configuration time and exactly one current monitor matches. A dynamic **\\.\DISPLAYn** is never stored and is never an identity. Missing, ambiguous, or mirrored targets are refusals, never “first match wins.”

## Package layout

- **internal/domain** — plain profile, mode, monitor identity, aspect and fallback derivation data. **LegacySeedProfile()** only pre-fills a first-run dialog; it is not a production session profile.
- **internal/config** — version-1 JSON schema, strict parsing and validation, conversion to and from domain.Profile, and configuration storage. **Path()** uses **%APPDATA%\ResolutionTray\config.json** unless **RESOLUTION_TRAY_CONFIG** supplies the complete file path. **Save()** validates first, then atomically writes the destination path plus **.tmp** in the same directory, syncs, and renames. The default destination therefore uses **config.json.tmp**; an override uses its own path plus **.tmp**. **Load()** and rejected writes do not modify files; **Backup()** runs only after the user explicitly confirms reset.
- **internal/display** — target enumeration/resolution, supported-mode catalogue, current layout, planning, validation and Win32 application. Non-target displays keep their mode fields; their **DM_POSITION** may move so the final desktop arrangement is safe. Do not reintroduce the false rule that positions never change.
- **internal/process** — read-only Toolhelp process-name listing and observation. It never opens a process.
- **internal/scaling** — NVIDIA availability probe, read, validate-then-apply, read-back, and restore. The Controller API is **Probe**, **Read**, **Apply**, **Restore**, and **Close**; it accepts **domain.MonitorIdentity**, never a display target/device name.
- **internal/app** — Session serializes display and scaling operations, owns display/scaling restore state, performs the managed scaling cycle, watches the configured process, and publishes snapshots. Provider owns the replaceable session and configuration-facing lifecycle through **Snapshot**, **ReadScaling**, **SaveAndReplace**, **Replace**, **Reload**, **Reset**, and **Shutdown**. A safe unique secondary identity rebind may persist exactly one updated instancePath per session; it retains the complete loaded file, changes only that path, and leaves the current session usable with a persistent warning if saving fails.
- **internal/ui** — Walk window, tray menu, and shared first-run/settings dialog. Render from app.Snapshot; marshal UI changes through **Synchronize**.
- **cmd/resolution-tray** — production composition root and resource generation directive.

## Configuration and settings

Schema version is exactly **1**. The profile contains monitor identity, game mode, optional fallback mode, process name, and **watch.restoreDelaySeconds**. The default delay is 3 seconds, the accepted range is 0–60 seconds, and it is serialized in config; it is deliberately not a settings-dialog field. An omitted fallback is derived from the monitor's current reported modes, not treated as the panel's native mode.

A missing file is first run. Saving is the only normal creation path; cancel, close, and “later” leave no file. A malformed, unreadable, out-of-range, or newer-version file remains untouched. Preserve the exact file path and underlying error in user-facing failures. Never silently repair, overwrite, or migrate a rejected file. The reset flow may retain a rejected file only after explicit user confirmation, and must report the retained path.

The one exception is a successful, safe secondary identity fallback: Provider may atomically persist the newly resolved instancePath once per session, with all other loaded configuration unchanged. If that save fails, retain the live session and surface the rebind/configuration warning; do not retry automatic writes repeatedly. Ambiguous fallback is a refusal, not a rebind.

Settings are disabled while Session owns an applied display mode, is RecoveryPending, or owns GPU scaling. Recover the display and scaling state before changing the profile. In a normal unmanaged state, NVIDIA scaling is a direct manual action; in a normal managed state it runs the full restore → scaling write → fresh re-apply cycle; RecoveryPending forbids scaling writes.

**SaveAndReplace(profile)** holds one Provider operation gate, checks the replacement guard, saves the new config, then replaces the session. A rejected guard writes no configuration. **Replace** and **Reload** enforce the same guard; none may replace a session with saved recovery state.

## GPU scaling rules

GPU scaling is an explicit manual action. It is not coupled to toggling the display mode or process-driven restore. The first successful owned Apply records the effective value read immediately before that change; it is not a process-startup snapshot. Requested and effective read-back values must remain distinct: a driver-normalised result is successful with a mismatch, not an error.

Apply scaling only on an explicit user request. Save its prior effective value only when the Apply is accepted; restore it on an explicit restore request or normal shutdown. Do not persist GPU scaling. A display restore failure during normal shutdown keeps the app alive and retryable. A scaling restore failure warns once and allows shutdown to finish.

NVIDIA’s “Override the scaling mode set by games and programs” checkbox has no supported API here. Do not promise the tool can read it, set it, or prove black bars disappeared. AMD and Intel remain manual control-panel paths and must not disable display-mode controls.

## Hard constraints

- Windows amd64 only; production builds use CGO_ENABLED=0.
- Startup and first-run discovery are read-only. No preview mode exists.
- Never use **CDS_UPDATEREGISTRY**. Test a display mode with **CDS_TEST** before its separate apply.
- Never use **NV_DISPLAYCONFIG_SAVE_TO_PERSISTENCE**. NVAPI validation uses flag word **0x01**; the formal apply uses exactly **0x00**.
- Call **runtime.KeepAlive** for every backing value or slice passed through every NVAPI syscall.
- Never carry any **\\.\DISPLAYn**, target, layout, plan, or saved arrangement across an NVAPI set. R1 resolves identity and reads fresh layout after the set; R2 consumes and clears the saved arrangement before it.
- In a managed scaling cycle: stop the watcher; restore and consume saved display state; perform the scaling operation; then re-resolve identity, reread layout, replan, and reapply from scratch. The new saved arrangement is built only after the set.
- Do not add a second watcher mechanism. The existing generation cancellation and fresh tracker semantics prevent an automatic restore interleaving with the cycle.
- The shared-recorder fakes in app tests intentionally invalidate display device names after every formal scaling SetAttempted, including a driver-rejected set. Preserve them and extend their assertions for any ordering change.
- UI strings are Traditional Chinese. No UI string may hard-code a particular monitor, mode, aspect ratio, or watched executable.
- Do not launch, inject, hook, open, inspect memory of, or modify files belonging to the configured program.

## Commands

Go is not normally on PATH:

~~~powershell
$env:PATH = "C:\Program Files\Go\bin;$env:PATH"
~~~

~~~powershell
# Full GUI artifact: generate resources → test → vet → dist/ResolutionTray.exe
./build.ps1

# Compile/check packages; this does not create the GUI distribution artifact.
go build ./...
go vet ./...
go test ./...

# Ordering-sensitive packages.
go test -count=20 ./internal/app ./internal/scaling

# Explicit, read-only hardware gates. Both are unset by default.
$env:RUN_DISPLAY_INTEGRATION = '1'
go test ./internal/display -run 'MiMonitor|Integration' -v
Remove-Item Env:RUN_DISPLAY_INTEGRATION

$env:RUN_NVAPI_INTEGRATION = '1'
go test ./internal/scaling -run Integration -v
Remove-Item Env:RUN_NVAPI_INTEGRATION
~~~

CI sets neither integration environment variable. The display gate is limited to CDS_TEST and read-only enumeration; the NVAPI gate is separate. **go test -race ./...** runs in CI only because local development lacks the C toolchain.

## Build and release

**build.ps1** is the supported production build entry and produces **dist/ResolutionTray.exe** with the GUI subsystem and generated resources. Plain **go build** is a compile check, not a release build.

The release workflow creates a GitHub Release only from a pushed **v*** tag. A workflow-dispatch run builds and checksums without publishing. Do not assume a fixed version tag or claim a previously published asset includes changes that exist only on the current branch.
