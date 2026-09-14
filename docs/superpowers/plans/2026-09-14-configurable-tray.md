# Configurable Tray Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the built-in Mi Monitor profile into a configuration the user owns — target monitor, target display mode and watched process — persisted to `%APPDATA%\ResolutionTray\config.json`, and add a manual NVIDIA GPU-scaling control so the full-screen-scaling prerequisite stops being a thing the user has to do by hand in the NVIDIA control panel.

**Architecture:** The domain gains a stable monitor identity and a profile whose every field comes from the user. A new `internal/config` package parses, validates and atomically writes that profile with no Win32 involved. `internal/display` learns to enumerate monitor identities and supported modes, and its `ResolveTarget` changes from "first hardware-ID prefix match" to "exactly one identity match or an honest error". A new `internal/scaling` package sits beside `internal/display` — neither imports the other — and `app.Session` is the single place that holds both and decides the order they run in. The UI renders every string from the snapshot, and gains one modal that serves as both the first-run wizard and the settings dialog.

**Tech Stack:** Go 1.27.x, `github.com/lxn/walk`, `github.com/lxn/win`, `golang.org/x/sys/windows`, Win32 `ChangeDisplaySettingsExW` / `EnumDisplaySettingsW` / `EnumDisplayDevicesW` / `QueryDisplayConfig`, `nvapi64.dll` via `nvapi_QueryInterface`, Toolhelp process snapshots, PowerShell build script.

**Specs:**
- `docs/superpowers/specs/2026-09-13-go-display-tray-design.md` (shipped behaviour; every promise in it stays true)
- `docs/superpowers/specs/2026-09-13-configurable-profile-design.md`
- `docs/superpowers/specs/2026-09-14-gpu-scaling-design.md`

## Global Constraints

Everything the shipped plan constrained still holds. These are the ones this plan adds or re-words.

- **`go` is not on PATH.** Prepend it once per shell: `$env:PATH = "C:\Program Files\Go\bin;$env:PATH"`.
- **The working tree is green at every commit.** Every task ends with `gofmt -l ./internal ./cmd` listing nothing, `go build ./...`, `go vet ./...` and `go test ./...` all passing. `go test -count=20 ./internal/app` is run additionally by every task that touches `Session` (Tasks 1, 8, 11, 15) because that package's ordering is concurrency-sensitive. `-race` is **not** run locally — the development machine has no C toolchain — it runs only in CI, which is the reason CI exists.
- **The tool still works after every task.** No task may leave a dead menu item, a dialog that saves into nothing, or a state the user cannot get out of. Where an intermediate state is unavoidable it is an honest, explained, read-only state.
- **Startup stays read-only**, including first run. Detecting monitors, enumerating modes, listing process names and reading GPU scaling are all reads. Nothing in the wizard applies a display mode, and there is no preview.
- **Never `CDS_UPDATEREGISTRY`, never `NV_DISPLAYCONFIG_SAVE_TO_PERSISTENCE`.** Both are the same mistake in two APIs. `CDS_TEST` before every apply; `VALIDATE_ONLY` (`0x01`) before every NVAPI set; the NVAPI apply flag word is exactly `0x00`.
- **Never take the first of several matches.** Monitor identity, NVAPI target selection and adapter resolution all fail loudly on ambiguity rather than pick one.
- **A scaling change while the session owns a display mode runs a full cycle; it is not refused.** The two specs look contradictory here and an implementer will hit it alone, so the resolution is recorded in both places. The scaling spec's R2 guarantees "no NVAPI set between `saved` being built and being cleared", while the first version's scaling button is independent of the game-mode toggle and is therefore reachable at any moment. Refusing the button while `Session.managed` is true was the cheap reconciliation and it is **rejected**: the moment a user notices black bars is precisely while the game mode is applied, so a button disabled exactly then is a button that does not exist. The rule is the scaling spec's **R5** — one operation under one acquisition of `opMu`: `stopWatcher()` → the whole display restore (R2) → the NVAPI set → the whole re-apply (R1, re-resolving identity, layout and plan from scratch and rebuilding `saved` from that fresh layout) → release `opMu` → join the watcher. No device name crosses the set, because `saved` is consumed and cleared before it and rebuilt after it, so R2 stays true verbatim — it just holds in two stretches instead of one. Both scaling buttons, apply and restore, behave identically. The accepted cost is that the screens change mode twice more than they otherwise would. `Shutdown` is unchanged and stays a different shape: leave-only, restoring `scalingSaved` instead of applying a requested value, never re-applying anything, and never blocked by a scaling failure.
- **The cycle's three failure points are decided, not left to the implementer.** Display restore fails → abort before **any** NVAPI call (`saved` is still live, and a set there is exactly what R2 forbids), keep `managed` and `saved` so the user can retry, call `ensureWatcher()`, and say both that the restore failed and that scaling was not changed. Scaling set fails → do **not** re-apply the game mode; leave the user on the restored arrangement, unowned, and re-read the effective scaling value as the existing rule already requires. Re-apply fails → book the scaling write that succeeded exactly as a non-cycle press would (ownership taken on an apply, released on a restore — never rolled back, or exit could not undo it) and take **no** display ownership (`managed` stays false, `saved` stays empty); the toggle stays usable and a retry is one ordinary `Enable`. The principle underneath both rulings: the cycle never holds a mode it did not successfully apply, and never holds an arrangement it did not successfully save.
- **The game watcher cannot act during the cycle, and the mechanism is the existing one.** `stopWatcher()` increments the generation *before* cancelling, so a poll already waiting at `opMu` finds `generation != s.generation` and returns before `tracker.Observe` is ever reached — which is precisely how a delayed restore that comes due mid-cycle is prevented from interleaving. `s.managed` legitimately goes false between the restore and the re-apply, and `observeGame`'s existing `!s.managed` check makes that a second, independent stop rather than a "the game ended, restore now" signal. `opMu` is never released between the cycle's steps. Do not invent a fourth mechanism. **The cycle must not swallow a pending automatic restore, either.** The watcher it re-arms gets a brand-new `gameTracker` *object* — `missing`, `missingAt` and `fired` always start at zero, which is exactly why the tracker is not preserved wholesale — but its `seen` flag is seeded from a new session-level `gameSeen bool` that lives and dies with `managed`. A game that ends during the cycle is therefore still "seen" afterwards, so the first post-cycle poll that finds the process absent opens a **fresh, full-length** delay and restores normally. The shipped promise (restore after the watched process goes from present to absent) holds across a cycle unchanged; no half-elapsed countdown survives it.
- **`設定…` stays disabled while `Session.managed` is true.** The profile spec's rule is untouched by the cycle, and the asymmetry is deliberate rather than inconsistent: changing the profile voids what `saved` *means* — it was recorded around one target monitor and one target mode, and no ordering repairs that — while changing scaling only threatens the device *names* inside it, and names are re-derivable by consuming `saved` before the set and rebuilding it after.
- **Scaling failures never touch the display side.** They must not change the toggle's availability, block `Shutdown`, or themselves change `managed` or `saved`. The reverse holds too. The cycle looks like an exception and is not: on that path `managed` and `saved` are changed by the *successful display restore* in step 2, never by the scaling call that failed afterwards.
- **CI never sets `RUN_DISPLAY_INTEGRATION` and never sets `RUN_NVAPI_INTEGRATION`.** The second is a new, separate gate: `RUN_DISPLAY_INTEGRATION` promises "`CDS_TEST` and read-only enumeration only", and reusing it for NVAPI would empty that promise. Neither workflow file needs editing — `go test ./...` picks up new packages on its own. If a workflow edit ever looks necessary, that is a signal the gating is wrong, not that the workflow is.
- **Tests never read or write the user's real config.** `RESOLUTION_TRAY_CONFIG` overrides the whole path; every config test sets it to a `t.TempDir()` path.
- **UI strings stay Traditional Chinese (繁體中文)**, and none of them may be a literal that contradicts the configuration: no hard-coded `1920×1440`, no hard-coded `Mi Monitor`, no hard-coded `4:3` in a place that describes the configured mode.
- **`runtime.KeepAlive` is a review checklist item, not folklore.** NVAPI stores raw pointers in struct fields; the GC does not see them and `go vet`'s unsafeptr check cannot help. Every `nvapi` syscall keeps every backing slice alive across the call.
- **Deferred, deliberately:** the countdown-confirm state (`ConfirmPending`) is out of scope for this release (profile spec 已定案); custom driver-synthesised resolutions are out of scope; AMD and Intel scaling are out of scope; multiple profiles are out of scope (`version: 1` stays a single profile).

## File Map

- `internal/domain/profile.go` — `Mode`, `MonitorIdentity`, `Target`, `Profile`, `LegacySeedProfile`, shared dimension bound.
- `internal/domain/aspect.go` — aspect-ratio labelling, native-mode detection, fallback derivation (pure).
- `internal/config/config.go` — file schema, `Parse`, validation, conversion to `domain.Profile`.
- `internal/config/store.go` — `Path` (with `RESOLUTION_TRAY_CONFIG`), `Load`, atomic `Save`, `Backup`.
- `internal/display/controller.go` — `Controller` (identity `ResolveTarget`, `EnumModes`), sentinels, pure identity matching.
- `internal/display/modes.go` — build-tag-free mode filter/dedupe/sort plus the `DM_*` field bits it needs.
- `internal/display/layout.go` — arrangement planning, hardened for the growing direction.
- `internal/display/win32_windows.go` — `EnumDisplayDevicesW` (twice per monitor), `EnumDisplaySettingsW` (current and enumeration), `ChangeDisplaySettingsExW`.
- `internal/display/ccd_windows.go` — read-only `QueryDisplayConfig` friendly names with a silent fallback ladder.
- `internal/display/fake_win32_windows_test.go` — the fake desktop, split out of `integration_windows_test.go`.
- `internal/process/checker.go` — `Checker` plus `Lister` (deduped, sorted executable names).
- `internal/app/session.go` — profile-driven session, scaling ownership, the ordering rules.
- `internal/app/provider.go` — the replaceable session the UI holds.
- `internal/scaling/controller.go` — pure target selection, diffing, validate-then-apply, read-back.
- `internal/scaling/nvapi_windows.go` — `nvapi64.dll` loading, `nvapi_QueryInterface`, the three-pass config.
- `internal/ui/window_windows.go` — main window, rendered entirely from the snapshot.
- `internal/ui/settings_windows.go` — the settings dialog, which is also the first-run wizard.
- `cmd/resolution-tray/main_windows.go` — load config → provider → UI.
- `CLAUDE.md`, `README.md` — corrected guidance and user documentation.

---

### Task 1: Generalize the domain and demote the built-in profile

**Files:**
- Modify: `internal/domain/profile.go`
- Modify: `internal/domain/profile_test.go`
- Modify: `internal/display/layout.go` (use the shared bound)
- Modify: `internal/app/session.go`, `internal/app/session_test.go` (field renames only)
- Modify: `cmd/resolution-tray/main_windows.go`
- Modify: `CLAUDE.md`

**Interfaces:**
- Produces: `domain.MonitorIdentity`, `domain.MatchLevel`, the generalized `domain.Profile`, `domain.LegacySeedProfile()`, `domain.MaxDimension`.
- Consumes: nothing new. No Win32, no I/O.

- [x] **Step 1: Rewrite the domain test around identity and the seed**

Replace `internal/domain/profile_test.go`. It currently asserts that the built-in values *are* the product's configuration; after this task they are only the wizard's pre-fill, and the test must say so. Cover:

```go
func TestLegacySeedProfileCarriesTheOriginalHardware(t *testing.T)   // MONITOR\XMI27B2, 1920x1440@180, VALORANT, 3s
func TestLegacySeedProfileHasNoInstancePathAndClaimsAUniqueModel(t *testing.T)
func TestMaxDimensionBoundsAModeTheToolWouldRefuse(t *testing.T)
```

The second test is the load-bearing one: the seed has an empty `InstancePath` because nobody has ever picked a monitor on this machine, and `ModelWasUnique: true` because the original tool only ever ran with one Mi Monitor attached. That combination is exactly what keeps the shipped behaviour working between Task 5 and Task 12 — the identity ladder falls through to the hardware-ID rung. Say that in a comment; it is not obvious and it will be deleted by someone who thinks the empty field is an oversight.

- [x] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/domain`

Expected: FAIL — `LegacySeedProfile`, `MonitorIdentity` and `MaxDimension` do not exist.

- [x] **Step 3: Implement the domain types**

```go
type MonitorIdentity struct {
	InstancePath   string // primary key: the device interface path, compared whole and case-insensitively
	HardwareID     string // secondary key: MONITOR\XMI27B2 — identifies a model, not a unit
	ModelWasUnique bool   // false permanently disables the secondary key for this profile
	Label          string // display only; never compared
}

type MatchLevel uint8

const (
	MatchNone MatchLevel = iota
	MatchInstancePath
	MatchHardwareID
)

type Target struct {
	DeviceName string          // \\.\DISPLAYn — valid only for the read that produced it
	Identity   MonitorIdentity
	MatchedBy  MatchLevel
}

type Profile struct {
	Name               string
	Monitor            MonitorIdentity
	GameMode           Mode
	FallbackNativeMode Mode // becomes optional-and-derived in Task 8
	ProcessName        string
	RestoreDelay       time.Duration
}

func LegacySeedProfile() Profile // the old DefaultProfile values, renamed

const MaxDimension = 1 << 16
```

`Target.HardwareID` moves under `Identity`; keep `DeviceName` where it is. `display/layout.go`'s unexported `maxDimension` becomes a reference to `domain.MaxDimension` so `internal/config` can validate against the same bound in Task 2 without importing `internal/display`.

- [x] **Step 4: Fix the call sites that stop compiling**

`session.go` reads `s.profile.MonitorHardwareID` in `readCurrent`, `readLayout` and `restoreSaved`; it becomes `s.profile.Monitor.HardwareID`. `ResolveTarget` still takes a prefix string until Task 5 — do not change its signature here. `session_test.go`'s `fixtureLayout` and `newFixture` call `domain.DefaultProfile()`; they become `domain.LegacySeedProfile()`. `main_windows.go` likewise. Nothing else changes behaviour: the tool after this task is byte-for-byte the same tool.

- [x] **Step 5: Correct the two CLAUDE.md bullets that now contradict the specs**

`CLAUDE.md`'s "Hard constraints" list still says the game mode is exactly `1920×1440` and the monitor is exactly `MONITOR\XMI27B2`. Both specs replace those. An implementing agent reading that list mid-plan will obey it and undo this work, so fix it now rather than in the documentation task:

- Replace the hardware-ID bullet with: match the configured monitor through the identity ladder — device interface path first, hardware ID only when the model was unique when the user configured it — never by a fixed `DISPLAY1` index and never by taking the first of several matches.
- Replace the game-mode bullet with: the game mode is whatever the user configured; `domain.LegacySeedProfile()` exists only to pre-fill the first-run wizard with the original `1920×1440 @ 180 Hz` values and is never the profile a shipping session runs on.

Leave the rest of `CLAUDE.md` alone; the full refresh (package layout, new commands, new env var) is Task 17.

- [x] **Step 6: Verify and commit the domain change**

```powershell
$env:PATH = "C:\Program Files\Go\bin;$env:PATH"
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
go test -count=20 ./internal/app
git add internal/domain internal/display/layout.go internal/app cmd CLAUDE.md
git commit -m "refactor: make the display profile a user-owned configuration"
```

Expected: everything passes, the binary behaves identically, and no file still mentions `DefaultProfile`.

---

### Task 2: Parse, validate and atomically persist the configuration

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/config/store.go`
- Create: `internal/config/store_test.go`

**Interfaces:**
- Consumes: `domain.Profile`, `domain.Mode`, `domain.MonitorIdentity`, `domain.MaxDimension`.
- Produces: `config.Parse`, `config.File`, `config.Path`, `config.Load`, `config.Save`, `config.Backup`, and the rejection sentinels.

This task is pure Go and file I/O into a temp directory. It touches no Win32, no display, no UI, and nothing else in the tree depends on it yet — it is deliberately sequenced early because it is the one large chunk of this release that can be finished without fighting the display layer.

- [x] **Step 1: Write the failing parse/validation table**

Create `internal/config/config_test.go` as one table-driven test plus a few named ones. The spec's rejection table is the test list; every row is a case:

- valid file → the exact `domain.Profile` it converts to, including `RestoreDelay` from `restoreDelaySeconds`;
- unknown keys (`"_comment"`) are ignored and do **not** fail;
- missing `version`, non-integer `version` → rejected as corruption, not as an old file;
- `version` greater than `config.Version` → `ErrNewerVersion`, and the caller is told never to write;
- malformed JSON → the error carries line and column, converted from `json.SyntaxError.Offset`;
- missing `monitor` or `gameMode` → rejected, naming the field;
- negative or fractional numbers → rejected (decode into unsigned integer types so `-1` and `1.5` are natural decode errors, never silent truncation);
- width/height of 0 or greater than `domain.MaxDimension` → rejected;
- `refreshHz` of 0, 1, or greater than 1000 → rejected (0 and 1 mean "hardware default" in DEVMODE, not a user choice);
- `bitsPerPixel` other than 32 → rejected; absent → 32;
- `restoreDelaySeconds` outside 0–60 → rejected, **not clamped**; assert explicitly that the returned value is an error and not a clamped config;
- `processName` absent, empty or whitespace → **accepted**, meaning "watch nothing, manual only";
- `processName` containing `\` `/` `:` `<` `>` `"` `?` `*` `|` or a control character, or equal to `.` or `..`, or longer than 255 → rejects the whole file (Toolhelp reports bare image names and the matcher compares for equality, so a path-shaped value could never fire — it would look configured and silently never work);
- `processName` without `.exe` → accepted.

- [x] **Step 2: Write the failing store test**

Create `internal/config/store_test.go`:

- `Path()` returns `os.UserConfigDir()/ResolutionTray/config.json`, and returns `RESOLUTION_TRAY_CONFIG` verbatim when it is set (assert with `t.Setenv`);
- `Save` writes through `config.json.tmp` in the **same directory**, `Sync`s, then `os.Rename`s — assert the temp file does not survive and the final content round-trips through `Parse`;
- a pre-existing config survives a `Save` whose rename never happens (simulate by making `Save` fail on a read-only target directory, or by asserting the temp-then-rename call order through a small injected `fileOps` seam) — the old file must still parse afterwards;
- **every rejection path writes zero bytes.** Point a rejecting `Load` at a directory and assert no file was created, no `.tmp` was created, and the original file's mod-time is unchanged. This assertion is the whole point of the "no partial loading" rule and it must be explicit;
- `Backup` renames an unreadable file to `config.bad-<timestamp>.json` and returns the new path, and does so only when called.

- [x] **Step 3: Run both tests to verify they fail**

Run: `go test ./internal/config`

Expected: FAIL — the package does not exist.

- [x] **Step 4: Implement the schema and validation**

```go
const Version = 1

type File struct {
	Version   int       `json:"version"`
	Monitor   Monitor   `json:"monitor"`
	GameMode  Mode      `json:"gameMode"`
	Fallback  *Mode     `json:"fallbackMode"`
	Watch     Watch     `json:"watch"`
}

func Parse(data []byte) (File, error)
func (f File) Profile() domain.Profile

var (
	ErrMalformed    = errors.New("設定檔格式錯誤")
	ErrNewerVersion = errors.New("設定檔是較新版本寫的")
	ErrMissingField = errors.New("設定檔缺少必要欄位")
	ErrOutOfRange   = errors.New("設定檔的值超出範圍")
)
```

Decode with `json.Decoder` so the syntax-error offset is available; convert the offset to line and column by counting newlines in the input up to it. Numeric fields decode into `uint32` / `uint64`. Validation returns on the first failure with a message naming the field — a config file is a boundary and is treated as untrusted input, but the user is the author, so the message has to tell them which line to fix.

- [x] **Step 5: Implement the store**

`Path()` prefers `RESOLUTION_TRAY_CONFIG` (the whole path, not a directory), otherwise `os.UserConfigDir()` + `ResolutionTray` + `config.json`. `Save` creates the directory, writes `config.json.tmp` beside the target, `Sync`s, closes, then renames — same directory is a correctness requirement, a cross-volume rename is not atomic. Never truncate in place. `Load` reads, `Parse`s, and returns; it writes nothing on any path, including success.

Write the JSON with two-space indentation, UTF-8 without BOM, LF line endings, `version` first.

- [x] **Step 6: Verify and commit the configuration package**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
git add internal/config
git commit -m "feat: read and write a validated profile configuration"
```

Expected: all tests pass; `git status --short` shows no stray file under a temp path; nothing outside `internal/config` changed.

---

### Task 3: Enumerate the device interface path and keep the monitor's own names

**Files:**
- Create: `internal/display/fake_win32_windows_test.go` (moved out of `integration_windows_test.go`)
- Modify: `internal/display/integration_windows_test.go`
- Modify: `internal/display/win32_windows.go`
- Modify: `internal/display/controller.go`

**Interfaces:**
- Produces: `domain.Target.Identity` populated from Win32 — `InstancePath`, `HardwareID`, `Label`.
- Consumes: `EnumDisplayDevicesW` with and without `EDD_GET_DEVICE_INTERFACE_NAME`.

`listTargets` has to start reporting the device interface path before an identity can be stored anywhere, so this task comes before the config is ever written from a picker and before matching changes.

- [x] **Step 1: Split and reshape the fake desktop before adding to it**

`internal/display/integration_windows_test.go` is 841 lines and holds both the fake `user32` and every test that uses it; the file name also no longer describes what is in it. Move `fakeWin32`, `win32Call`, `enumSettingsCall`, `fakeDesktop`, `measuredDesktop`, `newDisplayDevice` and `copyUTF16` into `fake_win32_windows_test.go`, unchanged in behaviour. Then reshape the monitor side: `monitors` becomes keyed by adapter name *and* by whether the caller asked for the interface name, because those two calls return different content in the same `DeviceID` field. Keep every existing test passing with no assertion edits — this step is a pure move plus one field, and `go test ./internal/display` proves it.

- [x] **Step 2: Write the failing identity enumeration tests**

In `integration_windows_test.go`:

```go
func TestWindowsNativeReadsTheDeviceInterfacePathAndTheHardwareID(t *testing.T)
func TestWindowsNativeKeepsTheMonitorsOwnDeviceStringAsALabel(t *testing.T)
func TestWindowsNativeReportsOneTargetPerMonitorNotPerAdapter(t *testing.T)
```

The third is the one that matters later: a cloned/mirrored adapter drives two monitors, and the only way Task 5 can detect that is if `listTargets` returns two targets carrying the same `DeviceName`. Assert exactly that shape here.

- [x] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/display`

Expected: FAIL — targets carry no identity.

- [x] **Step 4: Implement the double enumeration**

`EDD_GET_DEVICE_INTERFACE_NAME` is `0x00000001`. The flag **replaces** the content of `DISPLAY_DEVICEW.DeviceID`: with the flag it holds the interface path (`\\?\DISPLAY#XMI27B2#5&2b9d4d4&0&UID4357#{e6f07b5f-...}`), without it the hardware ID (`MONITOR\XMI27B2\0009`). Both are needed, so each monitor index is read twice:

```go
plain   := n.api.enumDisplayDevices(&adapter.DeviceName[0], monitorIndex, &monitor, 0)
withIfc := n.api.enumDisplayDevices(&adapter.DeviceName[0], monitorIndex, &monitorIfc, eddGetDeviceInterfaceName)
```

`Identity.HardwareID` comes from the first, `Identity.InstancePath` from the second, `Identity.Label` from `DeviceString` of either. A failed second call is not fatal: leave `InstancePath` empty and let the ladder fall through — a monitor with no interface path is still selectable by hardware ID.

- [x] **Step 5: Verify and commit identity enumeration**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
git add internal/display
git commit -m "feat: read each monitor's device interface path"
```

Expected: all tests pass; `ResolveTarget` still matches on the hardware-ID prefix and the tool behaves exactly as before.

---

### Task 4: Friendly names through the CCD API, read-only with a quiet fallback

**Files:**
- Create: `internal/display/ccd_windows.go`
- Create: `internal/display/ccd_windows_test.go`
- Modify: `internal/display/win32_windows.go`
- Modify: `internal/display/integration_windows_test.go`

**Interfaces:**
- Produces: a friendly-name lookup used to fill `Identity.Label`, with the ladder *CCD target name → `DeviceString` → hardware ID*.

`EnumDisplayDevicesW` frequently answers "Generic PnP Monitor", which is useless in a list where picking the wrong row applies a mode to the wrong screen. This is an independent, read-only addition: if any part of it fails, the label silently falls back and nothing else notices.

- [ ] **Step 1: Write the failing name-ladder tests**

The ladder itself is pure and gets a table test: a CCD name wins; an empty CCD name falls back to `DeviceString`; a `DeviceString` that is empty or equal to a known-useless placeholder falls back to the hardware ID; a CCD call that errors falls back without surfacing an error anywhere. Add a struct-size assertion test for each CCD struct the binding declares — take the sizes from the Windows SDK headers, do not guess them, and let the opt-in integration test in Step 4 be the thing that proves them against the real API.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/display`

- [ ] **Step 3: Implement the CCD binding behind a seam**

`GetDisplayConfigBufferSizes` → `QueryDisplayConfig` → `DisplayConfigGetDeviceInfo(DISPLAYCONFIG_DEVICE_INFO_GET_TARGET_NAME)`, all from `user32.dll` through `windows.NewLazySystemDLL` exactly as this file already loads `user32`. Put the lookup behind a small interface (`friendlyNamer`) so the fake desktop can supply names and so a machine where the call is unavailable is a normal test case, not an untested branch. This code is read-only — it must never call any of the `DisplayConfigSetDeviceInfo` / `SetDisplayConfig` family.

- [ ] **Step 4: Extend the opt-in integration test**

Add a case to the existing `RUN_DISPLAY_INTEGRATION`-gated test that resolves friendly names for every attached monitor and logs them. It is a read; it stays inside that gate's promise. CI still never sets the gate.

- [ ] **Step 5: Verify and commit friendly names**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
$env:RUN_DISPLAY_INTEGRATION='1'; go test ./internal/display -run 'Integration|MiMonitor' -v; Remove-Item Env:RUN_DISPLAY_INTEGRATION
git add internal/display
git commit -m "feat: name monitors the way the user recognises them"
```

Expected: unit tests pass; the opt-in read reports real monitor names; no setter API appears anywhere in the new file.

---

### Task 5: Resolve the target by identity, not by prefix

**Files:**
- Modify: `internal/display/controller.go`
- Modify: `internal/display/controller_test.go`
- Modify: `internal/display/integration_windows_test.go`
- Modify: `internal/app/session.go`, `internal/app/session_test.go`
- Modify: `internal/ui/window_windows.go`, `internal/ui/window_windows_test.go`

**Interfaces:**
- Produces: `ResolveTarget(domain.MonitorIdentity) (domain.Target, error)`, the pure `resolveIdentity`, `ErrTargetAmbiguous`, `ErrTargetMirrored`, and `Target.MatchedBy`.

**This is the largest single code change in the plan, and the step text should not pretend otherwise.** `ResolveTarget` is on the `Controller` interface, so this one signature touches: the real controller, the fake `nativeAPI` in `controller_test.go`, the fake `user32` desktop, `Session.readCurrent`, `Session.readLayout`, `Session.restoreSaved`, the `fakeDisplay` in `session_test.go`, the `stubDisplays` in `window_windows_test.go`, and the `ResolveTarget` string literal in every test that passes `MONITOR\XMI27B2`. Expect to touch roughly a dozen test functions across three packages, all mechanically. Do the pure matcher first so the mechanical part has something correct to lean on.

- [x] **Step 1: Write the failing identity-matching tests**

`resolveIdentity(targets []domain.Target, want domain.MonitorIdentity) (domain.Target, error)` is pure and takes a fake monitor list. Cover, from the spec's ladder:

```go
func TestResolveIdentityPrefersTheInstancePathWholeAndCaseInsensitively(t *testing.T)
func TestResolveIdentityFallsBackToTheHardwareIDWhenItMatchesExactlyOne(t *testing.T)
func TestResolveIdentityRefusesTheHardwareIDWhenItMatchesTwoMonitors(t *testing.T)   // ErrTargetAmbiguous
func TestResolveIdentityDisablesTheHardwareIDRungWhenTheModelWasNotUnique(t *testing.T)
func TestResolveIdentityPicksOnlyTheChosenUnitWhenTwoOfTheModelAreLive(t *testing.T)
func TestResolveIdentityReportsWhichRungMatched(t *testing.T)                        // MatchedBy
func TestResolveIdentityRefusesAnAdapterThatStillDrivesAnotherLiveMonitor(t *testing.T) // ErrTargetMirrored
func TestResolveIdentityReturnsNotFoundWithoutGuessing(t *testing.T)
```

The `ModelWasUnique == false` case is the one with a counter-intuitive rule: even a single hardware-ID hit is refused, because with two of the model configured, one hit most likely means the other is asleep or unplugged, not that this is the one the user wanted. Put the reasoning in the test comment.

- [x] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/display -run ResolveIdentity`

- [x] **Step 3: Implement the ladder**

```go
var (
	ErrTargetAmbiguous = errors.New("設定的顯示器同時命中多台")
	ErrTargetMirrored  = errors.New("解析出的顯示裝置同時驅動另一台顯示器")
)

func resolveIdentity(targets []domain.Target, want domain.MonitorIdentity) (domain.Target, error)
```

Instance path: whole-string `strings.EqualFold`, never a prefix. Hardware ID: whole-string `EqualFold` on the model portion, only when `want.ModelWasUnique`, and only when exactly one candidate matches. Mirror check: after a match, if any *other* target in the list carries the same `DeviceName`, refuse with `ErrTargetMirrored` — the tool cannot change one of two monitors sharing an adapter, and saying so is better than changing both.

- [x] **Step 4: Change the interface and fix every call site**

`Controller.ResolveTarget` takes `domain.MonitorIdentity`. `Session` passes `s.profile.Monitor`. The fakes take an identity and answer from a list rather than echoing a prefix — `fakeDisplay.ResolveTarget` in `session_test.go` currently records `domain.Target{HardwareID: prefix}`, which must become the identity it was asked for. `stubDisplays` in the UI test likewise. Run `go build ./... && go test ./...` repeatedly and fix compilation breakage in one pass rather than guessing which files are affected.

- [x] **Step 5: Surface the new sentinels in the UI latch**

`ui.updateAvailability` latches on `ErrTargetNotFound` and `ErrModeNotSupported`. Add `ErrTargetAmbiguous` and `ErrTargetMirrored` with their own reasons (the ambiguity message lists the candidates' `\\.\DISPLAYn` and asks the user to reselect; the mirror message explains the tool cannot change one of a mirrored pair). Add a UI test per sentinel, mirroring `TestUpdateAvailabilityLatchesMissingTarget`.

- [x] **Step 6: Verify and commit identity resolution**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
go test -count=20 ./internal/app
git add internal/display internal/app internal/ui
git commit -m "feat: resolve the target monitor by a stable identity"
```

Expected: all tests pass. On the author's machine the seed profile (empty instance path, `ModelWasUnique: true`) still resolves the Mi Monitor through the hardware-ID rung, so the tool's behaviour is unchanged — verify this by running the opt-in integration test, which uses the real adapter list.

---

### Task 6: Enumerate supported modes and build the catalogue

**Files:**
- Create: `internal/display/modes.go`
- Create: `internal/display/modes_test.go`
- Create: `internal/domain/aspect.go`
- Create: `internal/domain/aspect_test.go`
- Modify: `internal/display/controller.go`, `internal/display/win32_windows.go`
- Modify: `internal/display/integration_windows_test.go`

**Interfaces:**
- Produces: `Controller.EnumModes(target) ([]domain.Mode, error)`, the pure `catalogue`, `domain.AspectLabel`, `domain.NativeMode`, `domain.DeriveFallback`.

No picker can exist before this: the wizard's mode table, the "native ratio" reminder and the derived fallback mode all read from here.

- [ ] **Step 1: Write the failing catalogue tests**

The filter/dedupe/sort is pure. So it can live in a file with no build tag, define a tag-free raw form and move the four `DM_*` field bits plus `DM_INTERLACED` into `modes.go` (leave the `CDS_*` flags and their `CDS_UPDATEREGISTRY`-is-deliberately-absent comment exactly where they are, and keep `TestWindowsFlagsMatchWin32AndExcludeUpdateRegistry` passing):

```go
type rawMode struct {
	Mode         domain.Mode
	Fields       uint32
	DisplayFlags uint32
}

func catalogue(raw []rawMode, current domain.Mode) []domain.Mode
```

Cases: a mode missing any of the four required `dmFields` bits is dropped; zero width or height is dropped; `refreshHz` of 0 or 1 is dropped; `DM_INTERLACED` is dropped; non-32 bpp is dropped; duplicates on `(width, height, refresh)` collapse to one; the ordering is pixel count descending, then width descending, then refresh descending within a resolution; **the current mode is appended and marked when the driver did not enumerate it**; and an input that filters down to nothing returns an empty catalogue rather than an error, so the caller can say "this monitor reports no usable mode".

- [ ] **Step 2: Write the failing aspect tests**

`internal/domain/aspect_test.go`, table-driven and exactly the spec's examples: `1920×1440 → 4:3`, `2560×1440 → 16:9`, `3440×1440 → 21:9`, `1280×1024 → 5:4`, `1366×768 → 1.78:1`, `2560×1600 → 16:10`, `3840×1080 → 32:9`. Plus `DeriveFallback`: the largest pixel count wins, ties break on width, and the highest refresh within that resolution is chosen; an empty catalogue returns `ok == false` so the caller disables the button instead of guessing.

- [ ] **Step 3: Run both to verify they fail**

Run: `go test ./internal/display ./internal/domain`

- [ ] **Step 4: Implement the catalogue, the aspect label and the enumeration**

`AspectLabel` reduces by `gcd`, maps the known table (`4:3`, `5:4`, `3:2`, `16:10`, `16:9`, `64:27`→`21:9`, `43:18`→`21:9`, `32:9`), then prints `a:b` when the reduced denominator is ≤ 32, and otherwise a two-decimal `1.78:1`. No fuzzy matching anywhere.

The Windows side calls `EnumDisplaySettingsW(device, i, &dm)` from `i = 0` until it returns `FALSE`, **zeroing the `DEVMODEW` and re-setting `dmSize` before every call** — the same reason `loadCurrentMode` re-asserts `DmSize` today: do not assume GDI preserved the caller's buffer size. Convert each result to `rawMode` and hand the batch to `catalogue` along with a separate `ENUM_CURRENT_SETTINGS` read. This is purely a read and is safe at any time, including during first run.

- [ ] **Step 5: Extend the fake desktop and the opt-in test**

The fake `user32` answers indexed `enumDisplaySettings` calls from a per-device mode list, including a deliberately dirty one (an interlaced entry, a 1 Hz entry, a 16-bit entry, a duplicate) so the filter is exercised through the real adapter, not only through `catalogue` directly. Add an opt-in case under `RUN_DISPLAY_INTEGRATION` that enumerates the real target's modes and logs the catalogue — still a read.

- [ ] **Step 6: Verify and commit mode enumeration**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
git add internal/display internal/domain
git commit -m "feat: enumerate the modes a monitor actually reports"
```

---

### Task 7: Make the growing direction safe before any picker can reach it

**Files:**
- Modify: `internal/display/layout_test.go`
- Modify: `internal/display/integration_windows_test.go`
- Modify: `internal/app/session_test.go`
- Modify: `internal/display/layout.go` (only if a test proves a defect)

**Interfaces:**
- Validates: `PlanModeChange`'s shift direction and primary anchoring when the target grows, `validateArrangement`'s message quality, `orderForApply`'s grow branch, and the whole `Session.Enable` path with a larger mode.

The shipped tool only ever narrowed the target: `2560 → 1920`. `orderForApply`'s `growsMode` branch and the outward-shift half of `PlanModeChange` have unit tests but **have never run on real hardware**, because no user-reachable code path could produce them. Task 12 hands the user a list of every mode the monitor reports, most of which are larger than the current one. This task closes that gap before the list exists rather than after.

- [ ] **Step 1: Write the failing (or newly-load-bearing) growth tests**

`internal/display/layout_test.go`:

```go
func TestPlanModeChangePushesNeighboursOutwardWhenTheTargetGrows(t *testing.T)
func TestPlanModeChangeAnchorsThePrimaryWhenANonPrimaryTargetGrows(t *testing.T)
func TestPlanModeChangeHandlesATargetThatGrowsInOneAxisAndShrinksInTheOther(t *testing.T)
func TestValidateArrangementNamesBothDisplaysThatWouldOverlap(t *testing.T)
```

The mixed-axis case is the one most likely to be wrong: `growsMode` is the OR of both axes, so `1920×1440 → 2560×1080` counts as growing and must be applied last even though it frees vertical space. The overlap-message test is not a nicety — the spec notes that "no safe arrangement" moves from a theoretical error to one users will actually see, and a message that does not name the two colliding monitors is unactionable.

`internal/display/integration_windows_test.go`: extend the existing grow-order test so the plan order and the required apply order disagree (use `offsetTargetLayout`, where the target is in the middle of the read order), proving the ordering comes from the rule and not from list position.

`internal/app/session_test.go`: every session test today shrinks. Add:

```go
func TestEnableAppliesALargerGameModeWithoutOverlappingTheNeighbours(t *testing.T)
func TestEnableAbortsWhenALargerModeHasNoSafeArrangement(t *testing.T)
func TestDisableReturnsTheDesktopFromALargerGameMode(t *testing.T)
```

Parameterise `fixtureLayout` so the fixture can be built around a game mode wider than the native one, rather than assuming the target only ever narrows.

- [ ] **Step 2: Run them and record what actually fails**

Run: `go test ./internal/display ./internal/app`

Some of these will pass immediately — that is a legitimate outcome and means the branch was already correct. Do not weaken a test to make it fail. Record in the commit message which ones failed and which were confirmations.

- [ ] **Step 3: Fix only what a failing test proves**

Change `layout.go` only where a test demonstrates a defect. The most likely candidates are the overlap message (currently correct but terse) and the anchoring translation's sign when a non-primary target grows.

- [ ] **Step 4: Verify and commit the growth coverage**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
go test -count=20 ./internal/app
git add internal/display internal/app
git commit -m "test: cover the growing direction before users can choose it"
```

Expected: the grow path is covered end to end from `Session.Enable` down to the Win32 call order. The remaining risk is hardware-only and is recorded in the manual verification section at the end of this plan.

---

### Task 8: Generalize the session to the configured profile

**Files:**
- Modify: `internal/app/session.go`
- Modify: `internal/app/session_test.go`
- Modify: `internal/domain/profile.go`
- Modify: `internal/ui/window_windows.go` (field rename only)

**Interfaces:**
- Produces: `Snapshot.AtGameMode`, `Snapshot.Profile`, `Snapshot.FallbackMode` / `FallbackKnown` / `FallbackReason`, `Snapshot.MatchedBy`; an optional `Profile.FallbackMode *domain.Mode`; a session that watches no process when none is configured.

- [ ] **Step 1: Write the failing generalization tests**

```go
func TestSnapshotReportsTheConfiguredModeNotAFourByThreeAssumption(t *testing.T)
func TestDisableDerivesTheFallbackFromTheEnumeratedModesWhenNoneIsConfigured(t *testing.T)
func TestDisableRefusesAndExplainsWhenTheFallbackCannotBeDerived(t *testing.T)
func TestEnableStartsNoWatcherWhenNoProcessIsConfigured(t *testing.T)
func TestManualDisableStillWorksWithNoProcessConfigured(t *testing.T)
func TestRestoreNamesTheNeighbourThatIsNoLongerInTheSavedLayout(t *testing.T)
func TestSnapshotCarriesTheProfileSoTheUINeverInventsAString(t *testing.T)
```

The fallback-derivation test needs `EnumModes` on the fake display controller (Task 6 put it on the interface). The "no watcher" test asserts the process checker is never called and that `Snapshot` says so, because a user who left the field empty must not believe automatic restore is armed.

`TestRestoreNamesTheNeighbour...` is the scaling spec's tension #2, fixed early while the restore path is being edited anyway: `restoreSaved` only checks that the *target* is in the saved layout, so a renamed *neighbour* passes that check and then fails deep inside `stageChanges` with a message nobody can act on. Check every device in the saved layout up front and name the missing one.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/app`

- [ ] **Step 3: Make the fallback optional and derived**

`Profile.FallbackNativeMode Mode` becomes `Profile.FallbackMode *Mode`. When it is nil, `restoreFallback` calls `EnumModes` and `domain.DeriveFallback`; when derivation fails (enumeration error, or a catalogue that filtered down to nothing) the restore is refused with a message saying why, and `Snapshot.FallbackKnown` is false so the UI disables the button instead of offering a guess. Never fall back to a hard-coded `2560×1440`.

- [ ] **Step 4: Rename the 4:3 concept and publish the profile**

`Snapshot.FourByThree` → `Snapshot.AtGameMode`; `Session.observed` compares against `s.profile.GameMode` as it already does, but nothing downstream may assume a ratio. Add `Snapshot.Profile domain.Profile` (a value copy, taken under `mu`) so the UI can render every string from the snapshot in Task 10 without reaching into the session. Add `Snapshot.MatchedBy` from the resolved target so Task 11 can tell the user a rebind happened. Replace the session's own Chinese messages that name `4:3` or `2K` with wording generated from the profile's mode.

- [ ] **Step 5: Verify and commit the generalized session**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
go test -count=20 ./internal/app
git add internal/app internal/domain internal/ui
git commit -m "feat: drive the session from the configured profile"
```

Expected: all tests pass, including 20 repeats of the app package; no occurrence of `FourByThree` remains; `grep -rn '1920\|2560\|4:3' internal/app` returns only test fixtures.

---

### Task 9: List running process names for the wizard

**Files:**
- Modify: `internal/process/checker.go`
- Modify: `internal/process/checker_test.go`
- Modify: `internal/process/toolhelp_windows.go`

**Interfaces:**
- Produces: `process.Lister` with `Names() ([]string, error)`, and the pure dedupe/sort the wizard shows.

Small and self-contained, but the wizard cannot offer a process picker without it, and doing it here keeps Task 12 focused on the dialog.

- [x] **Step 1: Write the failing dedupe/sort test**

`uniqueSortedNames([]string) []string`: case-insensitive dedupe keeping the first spelling seen, sorted case-insensitively, empty entries dropped. Table-driven; no Win32.

- [x] **Step 2: Run it to verify it fails**

Run: `go test ./internal/process`

- [x] **Step 3: Implement `Lister` on the existing snapshot walk**

```go
type Lister interface {
	Names() ([]string, error)
}
```

`toolhelpChecker` gains `Names`, reusing the same `CreateToolhelp32Snapshot` + `Process32First/Next` walk that `Running` uses, collecting `entry.ExeFile` and nothing else. **No new API may appear in this file** — no `OpenProcess`, no module enumeration, no command lines, no paths. Listing names is exactly as safe as matching one, and the wizard showing names rather than paths is also what makes a path-shaped `processName` nearly impossible to produce from the UI.

- [x] **Step 4: Verify and commit the lister**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
git add internal/process
git commit -m "feat: list running executable names for the settings picker"
```

Expected: tests pass; `grep -n OpenProcess internal/process` returns nothing.

---

### Task 10: Render the main window from the profile

**Files:**
- Modify: `internal/ui/window_windows.go`
- Modify: `internal/ui/window_windows_test.go`

**Interfaces:**
- Consumes: `app.Snapshot` (now carrying the profile, the derived fallback and the match level).
- Produces: a window with no hard-coded hardware or mode strings.

- [ ] **Step 1: Write the failing text-generation tests**

The rendering helpers are pure functions of a snapshot and are already testable without Walk (`targetText`, `modeText`, `statusText` exist today). Add:

```go
func TestTargetTextUsesTheConfiguredLabelAndTheResolvedDevice(t *testing.T)
func TestToggleTextPrintsTheConfiguredModeAndItsAspect(t *testing.T)
func TestRestoreButtonTextPrintsTheModeItWillApply(t *testing.T)
func TestRestoreButtonIsDisabledWhenTheFallbackCannotBeDerived(t *testing.T)
func TestAutoRestoreTextSaysUnsetWhenNoProcessIsConfigured(t *testing.T)
func TestUnavailableReasonNamesTheConfiguredMonitorAndMode(t *testing.T)
```

The last one closes the spec's complaint that `updateAvailability` hard-codes `1920×1440 @ 180 Hz` in the "mode not supported" latch message.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/ui`

- [ ] **Step 3: Delete the constants and generate the strings**

Remove `monitorName`, `toggleText` and `restoreText` as constants. Every one of them becomes a function of the snapshot:

- 目標螢幕：`<Profile.Monitor.Label>` → `\\.\DISPLAYn`, or `（尚未找到）`;
- 目前模式：`2560 × 1440 @ 180 Hz（32 bpp）`;
- toggle: `使用 1920 × 1440 @ 180 Hz（4:3）` — mode and aspect both from the profile;
- 自動恢復：`VALORANT-Win64-Shipping.exe，結束後 3 秒` or `未設定（不會自動恢復）`;
- restore button: `恢復原始解析度`, tooltip printing the exact mode it will apply.

Grow the window to roughly `460×280` for the extra line, per the profile spec. Do not add the settings button yet — Task 12 adds it together with the dialog it opens. If the four buttons do not fit at the fixed width, move 隱藏至系統匣 into the tray menu, which the spec already authorises (closing and minimising already hide).

- [ ] **Step 4: Verify and commit the generated rendering**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
./build.ps1
git add internal/ui
git commit -m "feat: render every window string from the profile"
```

Expected: `grep -n '1920\|2560\|XMI27B2\|Mi Monitor' internal/ui/*.go` returns only test fixtures; the built binary shows the same information as before, worded from the seed profile.

---

### Task 11: Load the configuration at startup behind a replaceable session

**Files:**
- Create: `internal/app/provider.go`
- Create: `internal/app/provider_test.go`
- Modify: `internal/ui/window_windows.go`, `internal/ui/window_windows_test.go`
- Modify: `cmd/resolution-tray/main_windows.go`

**Interfaces:**
- Produces: `app.Provider` with `Session()`, `Configured()`, `Unconfigured()`, `Replace(domain.Profile)`, `SetOnChange(func(Snapshot))`; `ui.Run(*app.Provider) error`.

The profile spec's open question 9 settles this: a profile change rebuilds the session rather than mutating it, because the saved layout, the ownership flag and the watcher goroutine all belong to one profile. That requires the UI to hold an indirection instead of a `*app.Session`, and this task installs the indirection before there is anything that replaces it.

- [ ] **Step 1: Write the failing provider tests**

```go
func TestProviderStartsUnconfiguredWhenNoConfigFileExists(t *testing.T)
func TestProviderStartsReadOnlyWhenTheConfigFileCannotBeUnderstood(t *testing.T)
func TestProviderNeverWritesOnAnyRejectionPath(t *testing.T)
func TestReplaceShutsTheOldSessionDownAndCarriesTheObserverOver(t *testing.T)
func TestReplaceIsRefusedWhileTheSessionOwnsAnAppliedMode(t *testing.T)
func TestProviderWritesBackTheInstancePathAfterASecondaryKeyRebind(t *testing.T)
```

The last one is the profile spec's decided open question 4: when the primary key misses and the hardware-ID rung matched unambiguously, persist the new `instancePath` and say so in the status line (`已以硬體 ID 重新對應到 \\.\DISPLAY3`). This does not contradict "never rewrite a file you could not understand" — that rule is about parse failures, and this is a successful parse plus one unambiguous rebind. Assert it writes exactly once, not once per refresh.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/app`

- [ ] **Step 3: Implement the provider**

`Replace` refuses with a clear error while `managed` is true, otherwise shuts the current session down (which restores nothing, because nothing is owned), builds a new one from the new profile, and re-registers the observer so the UI's `SetOnChange` survives a replacement and `ui.Run` never has to re-subscribe.

- [ ] **Step 4: Make startup configuration-driven, with an honest unconfigured state**

`main_windows.go` becomes: resolve the config path → `Load` → build a provider. Three outcomes:

- **loaded** — exactly today's tool, on the user's profile;
- **absent** — the unconfigured state: every mutating control disabled, the status text says 尚未設定, and the two live exits are 重新讀取 and 開啟設定檔所在資料夾;
- **unreadable / malformed / newer version** — the read-only state: the full path, the exact error (with line and column for a syntax error), and the same two exits, plus 重新設定 which is offered but which, until Task 12 exists, only backs the bad file up and returns to the unconfigured state.

Nothing writes on the second or third path. There is no 設定… menu item yet, because the dialog does not exist — an unconfigured tool that explains itself is coherent; a menu item that opens nothing is not.

- [ ] **Step 5: Verify and commit the provider**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
go test -count=20 ./internal/app
./build.ps1
git add internal/app internal/ui cmd
git commit -m "feat: load the profile from configuration at startup"
```

Expected: with `RESOLUTION_TRAY_CONFIG` pointed at a hand-written valid file, the built binary behaves exactly as before; with it pointed at a missing file, the binary starts, changes no display, and says it is not configured.

---

### Task 12: The settings dialog, which is also the first-run wizard

**Files:**
- Create: `internal/ui/settings_windows.go`
- Create: `internal/ui/settings_windows_test.go`
- Modify: `internal/ui/window_windows.go`
- Modify: `cmd/resolution-tray/main_windows.go`

**Interfaces:**
- Consumes: `display.Controller` (monitor list, `EnumModes`), `process.Lister`, `config.Save`, `domain.LegacySeedProfile`, `app.Provider.Replace`.
- Produces: one modal used for both first run and later edits, and the 設定… entry points.

One dialog, one validation path, one save path. The first-run wizard differs from the settings dialog only in its title and step hints.

- [ ] **Step 1: Write the failing dialog-logic tests**

Everything decidable without Walk gets a test; the Walk plumbing does not:

```go
func TestWizardPrefillsTheLegacyValuesWhenTheOriginalMonitorIsPresent(t *testing.T)
func TestWizardStartsBlankWhenTheOriginalMonitorIsAbsent(t *testing.T)
func TestSelectingAMonitorRecordsItsInstancePathAndWhetherTheModelWasUnique(t *testing.T)
func TestTwoMonitorsOfOneModelAppearAsTwoRowsAndSetModelWasUniqueFalse(t *testing.T)
func TestTheModeTableFiltersByAspectAndDefaultsToTheHighestRefresh(t *testing.T)
func TestANonNativeChoiceCarriesTheFullScreenScalingReminder(t *testing.T)
func TestAMonitorWithNoUsableModeIsOfferedWithAReasonNotSilentlyOmitted(t *testing.T)
func TestSaveWritesOnceAndOnlyAfterValidationThroughTheConfigPackage(t *testing.T)
func TestAbandoningFirstRunLeavesNoFileBehind(t *testing.T)
```

`TestSaveWrites...` must assert the dialog validates by calling `internal/config`, not by re-implementing the rules — two validation paths that disagree is the failure mode this test exists to prevent.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/ui`

- [ ] **Step 3: Build the three sections**

Modal, resizable (it contains a table), about `560×460`:

1. **Monitor** — friendly name, `\\.\DISPLAYn`, current mode, whether it is primary. Two monitors of one model are two rows with their own interface paths; picking one records `ModelWasUnique: false`.
2. **Mode** — the Task 6 catalogue as a table of 解析度｜比例｜最高刷新率｜備註 with an aspect filter (全部 / 4:3 / 16:9 / 16:10 / 其他). Selecting a resolution reveals its refresh-rate dropdown, defaulting to the highest and showing how many exist. What is saved is always the full `(width, height, refresh, 32)` tuple, never "resolution plus whatever refresh is highest", because the latter re-resolves differently after a driver update. 備註 carries 目前模式, 原生模式, and the non-native reminder.
3. **Process** — deduped, sorted names from `process.Lister` with a search box, a free-text field, and an explicit 不觀察任何程序（只用手動切換） option.

Each section unlocks the next. Nothing in the dialog applies a mode, and there is no preview. Re-enumerate on open and on 重新整理; cache for the dialog's lifetime; invalidate after the tool itself applies or restores a mode.

- [ ] **Step 4: Wire the entry points and the save**

Add 設定… to the main window and to the tray menu above the separator. **Both are disabled whenever the session owns an applied mode**, with the reason beside them (請先恢復原始解析度再變更設定) — changing the target monitor or mode mid-session would strand the saved arrangement. This rule is **not** relaxed by the GPU-scaling cycle in Task 15, and the asymmetry is deliberate: a profile change voids what `saved` *means* (it was recorded around one target monitor and one target mode, and no ordering repairs that), while a scaling change only threatens the device *names* inside it, which the cycle re-derives by consuming `saved` before the NVAPI set and rebuilding it from a fresh layout after. Save runs `config.Save` (atomic) and then `Provider.Replace`; a failed write keeps the dialog open with the user's input intact and shows the error, and the previous config file is necessarily untouched because the write is temp-then-rename.

Startup: absent config → open the dialog as 初次設定, pre-filled from `LegacySeedProfile()` when `MONITOR\XMI27B2` is attached and reports `1920×1440 @ 180 Hz`, with a line reading 偵測到既有設定，確認後儲存. 稍後再設定 leaves no file and the next start is a first run again. Also complete the 重新設定 exit from Task 11: back the unreadable file up as `config.bad-<timestamp>.json` and open the same dialog, only after the user confirms.

- [ ] **Step 5: Verify and commit the dialog**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
./build.ps1
git add internal/ui cmd
git commit -m "feat: configure the monitor, mode and watched process from the tool"
```

Expected: a first run on a machine with no config produces a working config with one save click; abandoning it writes nothing; 設定… is disabled while 4:3 is applied.

---

### Task 13: The scaling package's decision layer, on a fake NVAPI

**Files:**
- Create: `internal/scaling/controller.go`
- Create: `internal/scaling/controller_test.go`

**Interfaces:**
- Produces: `scaling.Controller` (`Probe`, `Read`, `Apply`, `Close`), `scaling.Value`, `scaling.State`, `scaling.Outcome`, the internal `nvapi` seam, `checkFlags`, and the error sentinels.

Every judgement lives above the seam and every piece of pointer bookkeeping lives below it. This task is the whole of the former, is fully covered by a fake, and runs on any machine — including a CI runner with no NVIDIA GPU.

- [ ] **Step 1: Write the failing decision tests**

From the scaling spec's "不需要 GPU" list:

```go
func TestScalingValueDecodesTheModeAndTheDeviceThatScales(t *testing.T) // 2, 6, 1, 8, 255, and 4 => unrecognised
func TestCheckFlagsAcceptsOnlyZeroAndValidateOnly(t *testing.T)
func TestSaveToPersistenceAppearsOnlyInItsConstantDeclaration(t *testing.T)
func TestExactlyOneTargetOrNotFoundOrAmbiguous(t *testing.T)
func TestPayloadDivergenceAbortsBeforeAnyWrite(t *testing.T)
func TestWriteSequenceIsValidateThenApplyAndNothingElse(t *testing.T)
func TestValidateFailureMeansNoApply(t *testing.T)
func TestANormalisedReadBackIsNotAnError(t *testing.T)  // wrote 6, read 1 => Matched:false
func TestAFailedApplyStillRereadsTheEffectiveValue(t *testing.T)
func TestUnavailableNvapiCallsNothing(t *testing.T)
```

`TestSaveToPersistence...` scans the package's own source for `0x02` and asserts it appears on exactly the constant-declaration line — the same trick as the existing `TestWindowsFlagsMatchWin32AndExcludeUpdateRegistry`, which is the template. `TestUnavailableNvapiCallsNothing` is the path CI actually reaches end to end, so it is not optional. `4` must decode as *unrecognised*, never as a neighbouring known value.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/scaling`

- [ ] **Step 3: Implement the decision layer**

The spec's type sketch, verbatim in shape:

```go
type Value struct { Raw uint32; Mode Mode; By By }
type State struct { DisplayID uint32; Effective Value }
type Outcome struct { Requested Value; State State; Matched bool }

type Controller interface {
	Probe() Availability
	Read(domain.MonitorIdentity) (State, error)
	Apply(domain.MonitorIdentity, Value) (Outcome, error)
	Close() error
}

const (
	flagValidateOnly      uint32 = 0x01
	flagSaveToPersistence uint32 = 0x02 // never enters flags; declared only so the guard can name it
)
```

`Controller` deliberately neither takes nor returns a `domain.Target`: that is the structural half of the ordering rule, and a later refactor that "simplifies" it by passing a target would silently reintroduce the stale-name hazard. Write that in a comment on the interface.

The apply sequence is fixed: re-read the whole config, select exactly one target, change exactly one `uint32`, render the payload before and after into a deterministic string (pointers flattened to `nil`/`non-nil`), require the diff to be exactly that one scaling line, `VALIDATE_ONLY`, apply with flags exactly `0`, then re-read and report the value that came back. **The UI never shows a requested value as if it were effective**; `Outcome.State` is the only source.

- [ ] **Step 4: Verify and commit the decision layer**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
go test -count=20 ./internal/scaling
git add internal/scaling
git commit -m "feat: decide GPU scaling changes above the NVAPI seam"
```

Expected: full coverage of every branch above the seam with no GPU present anywhere in the loop.

---

### Task 14: Bind `nvapi64.dll` below the seam

**Files:**
- Create: `internal/scaling/nvapi_windows.go`
- Create: `internal/scaling/nvapi_windows_test.go`
- Create: `internal/scaling/integration_windows_test.go`

**Interfaces:**
- Produces: `scaling.NewWindowsController()`, the `nvapi` implementation, the struct/version constants.

- [ ] **Step 1: Write the failing layout assertions**

These are pure Go layout facts on amd64 and are the highest-value test in the whole scaling feature, because they are what a wrong struct offset would break and they need no GPU. Guard on `runtime.GOARCH == "amd64"` and assert the measured sizes from the spec: `pathInfo` 48, `advTargetInfo` 128, `targetInfo` 24, `sourceMode` 32, `timing` 96, `timingExt` 64; then assert the version stamps computed from them are `0x00020030` (`NV_DISPLAYCONFIG_PATH_INFO_VER2`) and `0x00010080` (`NV_DISPLAYCONFIG_PATH_ADVANCED_TARGET_INFO_VER1`).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/scaling`

- [ ] **Step 3: Implement the binding**

`windows.NewLazySystemDLL("nvapi64.dll")` — `LOAD_LIBRARY_SEARCH_SYSTEM32`, the same way this repo loads `user32.dll`. This is a security requirement, not a style choice: the tool ships as a single portable exe users run out of `Downloads`, which is the textbook DLL-planting location, and the default search order would let a same-named file beside the exe take over. Resolve `nvapi_QueryInterface`, then the six interface ids from NVIDIA's own `nvapi_interface.h`: `NvAPI_Initialize` `0x0150E828`, `NvAPI_DISP_GetDisplayConfig` `0x11ABCCF8`, `NvAPI_DISP_SetDisplayConfig` `0x5D8CF8DE`, `NvAPI_DISP_GetDisplayIdByDisplayName` `0xAE457190`, `NvAPI_GetErrorMessage` `0x6C2D048C`, `NvAPI_Unload` `0xD22BDD7E`.

The three-pass config read: count → allocate paths with the version stamped on each → allocate target and advanced-target arrays, stamp each advanced target, bind the pointers, **re-stamp the path version because pass 2 overwrote it** → read. Zero and re-stamp on every pass; do not assume the driver preserved anything.

Two things deserve comments in the file because they are unlike anything else in this repo:

- `NvAPI_DISP_GetDisplayIdByDisplayName` takes an **ANSI** byte string (`\\.\DISPLAY1` plus a NUL), the only non-UTF-16 Win32-style string boundary in the project;
- the path structs hold **raw pointers in struct fields**, which the GC does not trace and `go vet`'s unsafeptr check cannot see. Every syscall is bracketed with `runtime.KeepAlive` over every backing slice.

Initialize once per process, `NvAPI_Unload` only from `Close()`. Cache `MonitorIdentity → displayId` but re-validate on every use: the freshly read config must contain exactly one target with that id, or the cache is dropped and the name resolved again.

- [ ] **Step 4: Add the opt-in, read-only integration test**

Gate on the **new** `RUN_NVAPI_INTEGRATION=1`. Never reuse `RUN_DISPLAY_INTEGRATION`. Contents: resolve a displayId, run the three-pass read, and a **negative control** that deliberately stamps a wrong version and asserts `NVAPI_INCOMPATIBLE_STRUCT_VERSION (-9)` comes back — that is what turns "no error, so the version is probably right" into "the version is right". This test performs no `SetDisplayConfig` of any kind, not even with `VALIDATE_ONLY`.

- [ ] **Step 5: Verify and commit the binding**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
$env:RUN_NVAPI_INTEGRATION='1'; go test ./internal/scaling -run Integration -v; Remove-Item Env:RUN_NVAPI_INTEGRATION
git add internal/scaling
git commit -m "feat: bind the NVAPI display configuration interface"
```

Expected: layout assertions pass with no GPU; the opt-in read reports the real scaling values and the negative control returns `-9`; neither workflow sets the new variable.

---

### Task 15: Give the session the scaling controller and the ordering rules

**Files:**
- Modify: `internal/app/session.go`
- Modify: `internal/app/session_test.go`
- Create: `internal/app/scaling_test.go`
- Modify: `internal/app/provider.go`
- Modify: `cmd/resolution-tray/main_windows.go`

**Interfaces:**
- Produces: `NewSession(displays, processes, scalings, profile)`, `Session.ApplyGPUScaling()`, `Session.RestoreGPUScaling()`, `Session.RefreshScaling()`, `Snapshot.Scaling`.

`Session.opMu` is already the one place that serializes display workflows, which is the whole reason the scaling controller belongs here and not beside the UI: a second lock would make "which one first" a question with no structural answer.

- [ ] **Step 1: Build the shared-recorder fakes — before any code has to satisfy them**

This step is what turns the ordering rules from advice into structure, so it comes first and stands alone. In `internal/app/scaling_test.go`:

- one recorder shared by the fake `scaling.Controller` and the fake `display.Controller`;
- after **every successful** `scaling.Apply`, the fake scaling controller **renames every display in the fake display controller** (`\\.\DISPLAY1` → `\\.\DISPLAY7`, and so on) while leaving `HardwareID`, `MonitorIdentity` and displayId untouched;
- the fake display controller **fails any call naming a device that no longer exists**.

Renaming on *every* set is harsher than the hardware (the measurement was one renumber in three sets), and that is the point: a code path that carries a name across a set fails deterministically in the test suite instead of probabilistically on the user's desktop. Wire the existing session fixture to a no-op scaling controller so every current test keeps passing unchanged — this step compiles and is green on its own.

- [ ] **Step 2: Write the failing ordering and isolation tests**

```go
func TestEnableResolvesTheTargetAfterAnyScalingSetNotBefore(t *testing.T)          // R1
func TestRestoreAppliesTheDisplayLayoutBeforeAnyScalingRestore(t *testing.T)       // R2
func TestShutdownRestoresScalingOnlyAfterTheDisplayRestoreSucceeded(t *testing.T)  // R2
func TestAFailedDisplayRestoreSkipsTheScalingRestoreEntirely(t *testing.T)         // R2
func TestNoScalingSetHappensInsideAnApplyLayout(t *testing.T)                      // R3
func TestScalingFailuresNeverChangeManagedSavedOrToggleAvailability(t *testing.T)
func TestDisplayFailuresNeverChangeScalingOwnershipOrSavedValue(t *testing.T)
func TestScalingRestoreFailureDoesNotBlockShutdown(t *testing.T)
func TestSnapshotDeviceNameIsRefreshedAfterAScalingSet(t *testing.T)
```

Then the cycle, which is the behaviour this task exists to get right:

```go
func TestScalingWhileManagedRestoresThenSetsThenReappliesInThatOrder(t *testing.T)   // R5, happy path
func TestTheCycleReresolvesTheTargetAndLayoutAfterTheScalingSet(t *testing.T)        // R5, and R1 as a live path
func TestTheCycleRebuildsSavedFromTheFreshLayoutNotTheConsumedOne(t *testing.T)      // R5
func TestTheScalingRestoreButtonRunsTheSameCycleAsTheApplyButton(t *testing.T)       // both buttons, one path
func TestACycleWhoseDisplayRestoreFailsNeverReachesNVAPI(t *testing.T)               // failure point 1
func TestACycleWhoseScalingSetFailsLeavesTheDesktopRestoredAndUnowned(t *testing.T)  // failure point 2
func TestACycleWhoseReapplyFailsKeepsTheScalingResultAndOwnsNoDisplayMode(t *testing.T) // failure point 3
func TestTheWatcherCannotRestoreWhileTheCycleIsRunning(t *testing.T)                 // generation + opMu
func TestAGameThatEndsDuringTheCycleStillRestoresOnAFullDelayAfterwards(t *testing.T) // seeded seen, fresh countdown
```

These are the tests the Step 1 shared recorder earns its keep on, and they are the reason that recorder must survive this task rather than be simplified away. The cycle is the **only** production path that issues an NVAPI set before a `ResolveTarget`, so it is the one place where a stale `\\.\DISPLAYn` would actually reach `ChangeDisplaySettingsExW` and change the wrong screen. With the recorder renaming every device after every successful set, a re-apply that reuses *anything* from before the set — the target, the layout, the plan, or the consumed `saved` — fails with "device does not exist" instead of quietly passing.

`TestTheCycleReresolvesTheTargetAndLayoutAfterTheScalingSet` is what makes R1 non-vacuous in v1. Until the cycle exists, nothing in the product calls a scaling set before an `Enable`, so R1 is enforced only by a test; the cycle turns it into a path users actually run. Assert the call order on the shared recorder: `ResolveTarget` and `CurrentLayout` both appear *after* the `scaling.Apply`, and neither the target nor the layout from before the set is reused.

`TestTheWatcherCannotRestoreWhileTheCycleIsRunning` drives the controllable clock so a pending automatic restore comes due between the cycle's restore and its re-apply. Assert that the tracker's restore never fires, that the process checker's observation is discarded on the generation check rather than on the `managed` check (both stop it; the generation is the one that stops it *first* and is therefore the one being tested), and that the desktop is changed exactly twice — restore, re-apply — never three times.

`TestAGameThatEndsDuringTheCycleStillRestoresOnAFullDelayAfterwards` is the other half of that, and it is the one that keeps a shipped promise intact. Let the watched process disappear *during* the cycle, then let the cycle finish and advance the fake clock. Assert three things: the restore does fire; it fires a **full** `RestoreDelay` after the **first post-cycle poll that saw the process absent**, not measured from any instant before the cycle; and it does not fire early. Also assert the negative that makes the mechanism visible — the replacement tracker's `missing`, `missingAt` and `fired` are all zero-valued, only its `seen` was seeded.

The three failure-point tests each assert the whole end state, not just the error: which of `managed`, `saved`, `scalingOwned` and `scalingSaved` changed, where the desktop ended up, how many display applies and NVAPI sets happened, whether the watcher was restored, and whether the toggle is usable afterwards. Failure point 1 additionally asserts the NVAPI fake was **never called at all**, and failure point 3 asserts the session ends in exactly the ordinary unmanaged state plus whatever that successful scaling write left behind — no half-ownership, no special recovery path, and no rollback of a write that really happened.

- [ ] **Step 3: Run them and confirm they fail for the right reason**

Run: `go test ./internal/app -run 'Scaling|Cycle'`

Expected: FAIL. Read the failures — any that fail with "device does not exist" rather than an assertion mismatch is the recorder doing its job and pointing at a real stale-name path.

- [ ] **Step 4: Implement the session side**

Three scaling fields, parallel to and independent of `managed` / `saved`, plus one watcher field that belongs to the display side:

```go
scalingOwned bool   // true only after this run successfully changed scaling
scalingSaved scaling.Value // the effective value read back before the change; never inferred
scalingID    uint32

gameSeen     bool // "the watched process has been seen running during this ownership";
                  // same lifetime as managed, seeds each startWatcher's tracker
```

Ordering, under `opMu`: entering is GPU then display, leaving is display then GPU. Any GDI name used to reach NVAPI is consumed into a displayId inside `internal/scaling` and never leaves it; the display side does its own fresh `ResolveTarget` afterwards. No NVAPI set ever happens inside `applyLayout`, whose rollback state is keyed by device name.

While `managed` is true, a scaling press — apply or restore, they behave identically — runs the full cycle (R5) as **one** operation under **one** acquisition of `opMu`:

1. `stopWatcher()`, which increments the generation before cancelling, so a poll already waiting at the gate returns without evaluating its tracker;
2. the whole of `restoreSaved`. A failure here aborts before any NVAPI call, keeps `managed` and `saved`, calls `ensureWatcher()` and returns — exactly `Disable`'s failure path — with a message naming both the restore failure and the fact that scaling was not changed;
3. the scaling apply or restore. A failure here does **not** re-apply the game mode: the desktop stays on the restored arrangement, unowned, and the effective scaling value is re-read and shown as the existing rule requires;
4. the whole of `Enable`'s apply half, re-resolved from scratch — fresh `ResolveTarget`, fresh `CurrentLayout`, fresh `PlanModeChange`, `CDS_TEST`, `ApplyLayout` — with `saved` rebuilt from that fresh layout and `startWatcher()` at the end, re-armed on a new `gameTracker` whose `seen` is seeded from `s.gameSeen` and whose countdown fields are zero — so a game that ended mid-cycle still triggers a normal, full-length automatic restore. A failure here books the scaling write that succeeded (ownership taken on an apply, released on a restore) and takes no display ownership;
5. release `opMu`, **then** `joinWatcher` the watcher stopped in step 1. Never join while holding the lock.

Add `StateScalingCycle`, held for the whole cycle with the status line naming the phase (恢復原始排列… / 寫入縮放設定… / 重新套用 …). `Snapshot.Managed` stays true for the cycle's whole duration even though the internal `s.managed` legitimately goes false between steps 2 and 4; put that in a comment at the `updateSnapshot` call, because it reads like a bug and someone will "fix" it. Every exit from the cycle corrects `Snapshot.Managed` to what is actually true.

`ensureWatcher`'s existing rule is reconciled, not overturned: the replacement watcher still starts from a **fresh tracker object**, because a preserved one carries `fired == true` and would never arm again. Only the `seen` boolean is seeded. One deliberate consequence beyond the cycle: after a failed automatic restore, the keep-alive watcher now re-arms on a full delay by itself instead of waiting for the game to be seen running again, which is what that function's comment already promises.

`Shutdown` is re-ordered: stop watcher → display restore → **scaling restore, if owned** → `closed = true` → close notifications. A scaling restore failure raises one tray balloon and **does not** take the `ensureWatcher` keep-alive path and **does not** stop the exit; a display restore failure still does both, exactly as today. The two are deliberately different because a display left in the wrong mode is a broken desktop, while an unrestored scaling value is runtime-only and dies at the next reboot or driver reload.

At the tail of every workflow, re-read the target so `Snapshot.Target.DeviceName` is not a name that a set invalidated. The UI never calls anything with that name, but a stale one on screen destroys the user's trust in the rest of the window.

- [ ] **Step 5: Wire the composition root**

`main_windows.go` constructs `scaling.NewWindowsController()` and hands it to the provider, which hands it to each session it builds and `Close()`s it when the process ends. A machine with no NVIDIA driver produces a controller whose `Probe` reports unavailable with a reason; nothing fails and nothing changes.

- [ ] **Step 6: Verify and commit the session integration**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
go test -count=20 ./internal/app
./build.ps1
git add internal/app internal/scaling cmd
git commit -m "feat: order scaling and display changes so no device name goes stale"
```

Expected: all tests pass under 20 repeats; the renaming fake is active in every scaling test, not only the ordering ones.

---

### Task 16: Put GPU scaling on screen with its own availability state

**Files:**
- Modify: `internal/ui/window_windows.go`
- Modify: `internal/ui/window_windows_test.go`
- Modify: `internal/ui/settings_windows.go`

**Interfaces:**
- Consumes: `Snapshot.Scaling`.
- Produces: a second, independent availability group and the measured non-native reminder.

- [ ] **Step 1: Write the failing availability-isolation tests**

```go
func TestScalingUnavailabilityLeavesTheFourByThreeToggleUsable(t *testing.T)
func TestTargetUnavailabilityLeavesTheScalingButtonReasonIntact(t *testing.T)
func TestScalingButtonTextSwitchesToRestoreAndPrintsTheValueItWillRestore(t *testing.T)
func TestScalingLineShowsTheEffectiveValueNotTheRequestedOne(t *testing.T)
func TestAMismatchedReadBackIsShownAsBothValuesAndIsNotAnError(t *testing.T)
func TestNonNativeChoiceShowsTheMeasuredScalingWarning(t *testing.T)
func TestScalingButtonStaysEnabledWhileTheSessionOwnsAnAppliedMode(t *testing.T)
func TestScalingButtonWarnsThatTheScreensChangeModeTwiceMoreWhileManaged(t *testing.T)
func TestEveryMutatingControlIsDisabledDuringTheScalingCycle(t *testing.T)
func TestSettingsStaysDisabledWhileManagedEvenThoughScalingDoesNot(t *testing.T)
```

`updateAvailability` currently latches one `unavailableReason` string and `availableControls` derives one `interactive` flag from it, which would let a single NVAPI error disable the 4:3 toggle. That is the concrete UI change this task exists for.

The four newly-named tests encode the Task 15 decision on the UI side. The scaling button is **enabled** while the session owns a mode — that is the only moment the user can see the black bars it fixes — but its label or the line beside it says what pressing it costs (畫面會先恢復原始排列、變更縮放、再切回遊戲模式). `設定…` is the deliberate contrast and stays disabled, for the reason recorded in Global Constraints. And while `Snapshot.State` is `StateScalingCycle`, every mutating control is disabled, including the scaling button itself: `opMu` already makes a second click merely queue, but a queued command against a desktop that is mid-change is not something to offer.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/ui`

- [ ] **Step 3: Split the availability state and render the scaling row**

`controls` grows `scalingApply` and `scalingRestore`, fed by a separate `scalingUnavailableReason`. Disabled always shows its reason **beside the button**, never only in a tooltip, matching how the unsupported-mode case already behaves. The reason wording comes from the scaling spec's table: no DLL names the detected adapter's `DeviceString` and points at that vendor's control panel; entry points missing names the driver version; the target not being on an NVIDIA path names the monitor.

The row itself:

```text
GPU 縮放：全螢幕（由 GPU 執行）            ← Raw=2
GPU 縮放：長寬比（由顯示器執行）            ← Raw=6
GPU 縮放：無法讀取——找不到 NVIDIA 驅動      ← unavailable
[ 設為全螢幕縮放（GPU） ]  /  [ 還原 GPU 縮放設定（長寬比（由顯示器執行）） ]
```

A mismatched read-back prints both values and is not styled as an error; the button does not render as "on". And the tool never claims the black bars are gone — it reports only what the driver says the scaling setting is.

While the cycle runs, the row and the status line come from `StateScalingCycle` and name the phase rather than the result: 正在變更 GPU 縮放：恢復原始排列… / 寫入縮放設定… / 重新套用 1920 × 1440 @ 180 Hz…. `Snapshot.Managed` stays true throughout, so nothing in the window flickers to 啟用 at the moment pressing it would be meaningless. Each of the three failure exits has its own wording, and each must say what the desktop is doing now as well as what failed: restore failed → the mode is still applied and scaling was not changed; scaling failed → the desktop is back at the original arrangement and nothing is owned; re-apply failed → the scaling value did change, the desktop is at the original arrangement, and the toggle will put the mode back.

- [ ] **Step 4: Upgrade the non-native reminder from static to measured**

The profile spec promised a static reminder when the chosen mode's aspect differs from the native one. With a read path available it becomes measured — 這個模式是 4:3，而目前的 GPU 縮放是「長寬比（由顯示器執行）」，畫面會有黑邊 — in the settings dialog's 備註 column and in the main window. It never blocks the choice: the user may want the bars, or may have handled it elsewhere. Alongside it, keep the honesty line permanently: 若遊戲內仍有黑邊，請到 NVIDIA 控制台勾選「覆寫遊戲和程式所設定的縮放模式」——這一項工具無法代為設定。 That checkbox has no NVAPI interface at all, and the tool must not imply otherwise.

- [ ] **Step 5: Reconcile the window layout one last time**

Two designs have now each added to a fixed-size window: a fourth button and an extra line from the profile work, one more line and one more button from this. Lay them out together rather than incrementally — roughly `460×320`, with 隱藏至系統匣 living in the tray menu if the button row does not fit at the fixed width. Build and look at it; a screenshot is not required but the binary must actually be launched to confirm nothing is clipped.

- [ ] **Step 6: Verify and commit the scaling UI**

```powershell
gofmt -l ./internal ./cmd
go build ./...
go vet ./...
go test ./...
./build.ps1
git add internal/ui
git commit -m "feat: show and set GPU scaling without disabling the 4:3 controls"
```

---

### Task 17: Documentation, guidance and the release verification pass

**Files:**
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify only what a concrete review finding requires.

**Interfaces:**
- Validates every interface produced by Tasks 1–16.

- [ ] **Step 1: Rewrite the user documentation**

`README.md`:

- the 事前準備：NVIDIA 全螢幕縮放 section changes from "do this once" to "if you choose a non-native aspect ratio you need this, and the tool now has a button for it on NVIDIA; on AMD and Intel it is still a manual step in that vendor's control panel";
- add the honest boundary: 覆寫遊戲和程式所設定的縮放模式 has no API and remains manual;
- document the config file path, `RESOLUTION_TRAY_CONFIG`, the atomic write, and what each rejection message means;
- document first run, 稍後再設定, and that abandoning it leaves no file;
- document that scaling changes are runtime-only and are restored on exit or on demand, never persisted;
- replace the hard-coded `2560→1920`/`640 px` walkthrough with the general rule, keeping one worked example;
- add `RUN_NVAPI_INTEGRATION` beside `RUN_DISPLAY_INTEGRATION`, and state that CI sets neither.

`CLAUDE.md`: refresh the package layout for `internal/config` and `internal/scaling`, the commands (including the new opt-in gate), the testing section (`-count=20 ./internal/app`, `-race` in CI only, the shared-recorder rule), and add to the hard-constraints list: never `SAVE_TO_PERSISTENCE`; the NVAPI apply flag word is exactly `0`; no NVAPI set while a display mode is owned; `runtime.KeepAlive` around every NVAPI syscall; never take the first of several identity matches. Task 1 already corrected the two contradicting bullets — do not undo that wording, extend around it.

- [ ] **Step 2: Review the whole branch diff against the three specs**

```powershell
git diff main...HEAD --stat
git diff main...HEAD -- . ':(exclude)docs/superpowers/plans/2026-09-14-configurable-tray.md'
```

Check specifically for: a `\\.\DISPLAYn` carried across a scaling set; `0x02` anywhere but its declaration; `CDS_UPDATEREGISTRY`; a missing `runtime.KeepAlive`; any write on a config rejection path; any "first match wins" loop; a hard-coded `1920`, `2560`, `4:3` or `XMI27B2` outside tests and `LegacySeedProfile`; UI updates outside `Synchronize`; any new `OpenProcess`; generated artifacts staged.

- [ ] **Step 3: Run the full automated verification**

```powershell
gofmt -l ./internal ./cmd
go mod tidy -diff
go build ./...
go vet ./...
go test ./...
go test -count=20 ./internal/app ./internal/scaling
$env:RUN_DISPLAY_INTEGRATION='1'; go test ./internal/display -run 'MiMonitor|Integration' -v; Remove-Item Env:RUN_DISPLAY_INTEGRATION
$env:RUN_NVAPI_INTEGRATION='1'; go test ./internal/scaling -run Integration -v; Remove-Item Env:RUN_NVAPI_INTEGRATION
./build.ps1
git diff --check
git status --short
```

`-race` is not in this list on purpose: it needs a C toolchain the development machine does not have, and CI runs it on every push.

- [ ] **Step 4: Perform the manual verification**

Work through the section at the end of this plan and record the results — in particular the one measurement neither spec has: writing scaling `2` while the desktop is at a non-native resolution.

- [ ] **Step 5: Commit documentation and any review fixes**

```powershell
git add README.md CLAUDE.md
git commit -m "docs: document configuration and GPU scaling"
```

If Step 2 or Step 4 finds a concrete defect, add a focused regression test, make the smallest fix, re-run Step 3, and commit it separately as `fix: <the user-visible behaviour>`. Do not create an empty commit when nothing is found.

---

## What a human has to verify by hand

None of the following can be automated, and each is here because something in the two specs is either unmeasured or unsafe to exercise from a test.

**New to this release:**

1. **A non-native resolution with the scaling button — and the measurement that is still missing.** The scaling spec is explicit that every write experiment was performed at the panel's native `2560×1440`, while the whole reason the feature exists is the non-native case. Do it in this order and write down each result: read and record the current scaling value at native resolution; switch to the configured 4:3 mode; **then** set scaling to `2`; read it back and record whether the read-back matches; confirm the other three displays' resolutions, refresh rates and colour depths are unchanged and the desktop has no gap or overlap; restore scaling and record whether the restore was faithful; **and after every single step, record whether any `\\.\DISPLAYn` changed.** A mismatched read-back here is not a bug — it is the normalisation the spec predicted and it must be recorded, not fixed.

   **Then do it again without leaving the game mode first**, which is the cycle: with the game mode applied and owned, press the scaling button and confirm all three legs — the desktop returns to the original arrangement, the scaling value changes, and the game mode comes back — that every display's resolution, refresh rate and colour depth is identical before and after, that the primary is still at `(0,0)`, and that the watcher is running again afterwards. Record whether any `\\.\DISPLAYn` changed at each leg. This is the one path in the product where a stale device name could reach `ChangeDisplaySettingsExW`, so it is the one path worth watching by hand.
2. **The growing direction on real hardware.** Pick a mode larger than the current one and apply it. `orderForApply`'s grow branch, and the outward-shift half of the planner, have unit tests but have never touched a real driver: the shipped tool only ever narrowed the target. Confirm the neighbours move before the target's mode lands, that the desktop is never momentarily overlapping, that Windows does not repack the arrangement, and that restoring returns every display to its original coordinate.
3. **Two monitors of the same model.** If the user ever has two `XMI27B2`-class panels attached at once: confirm they appear as two rows with distinct interface paths, that selecting one records `ModelWasUnique: false`, that only the selected one is ever changed, and that swapping the cables between ports makes the tool refuse and ask for a reselection rather than applying the mode to the other panel.
4. **The complete first run on a second monitor.** Delete (or point `RESOLUTION_TRAY_CONFIG` away from) the config, start the tool, and walk the wizard end to end choosing a different monitor than the Mi Monitor. Confirm nothing changed on screen at any point during the wizard, that 稍後再設定 leaves no file, and that saving produces a working profile with one click.
5. **The migration path.** With the original hardware attached and no config file, confirm the wizard opens pre-filled with `MONITOR\XMI27B2` and `1920×1440 @ 180 Hz`, that the explanatory line is shown, and that one save click produces a config whose behaviour is identical to the shipped tool's.
6. **The non-NVIDIA message.** If any machine with an AMD or Intel display is available, confirm the scaling button is disabled with the vendor named and the control-panel path given, and that the 4:3 toggle still works normally.

**Still required from the shipped release:**

7. Startup changes no display; enabling changes only the target; disabling returns every display to its recorded mode and coordinate; the automatic restore fires three seconds after the watched process disappears; a manual disable during a pending restore wins immediately; closing and minimising hide to the tray and the tray actions keep working.
