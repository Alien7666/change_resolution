# Configurable Tray Codex Continuation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resume the existing configurable-tray plan at the unfinished Task 5, delegate every application-code change, and finish Tasks 4 and 6–17 without losing the three committed tasks or the inherited Task 5 work.

**Architecture:** Keep `internal/display` and `internal/scaling` as sibling packages and let `internal/app.Session` own their ordering. The display series is deliberately serial because Tasks 4, 6 and 7 share the same Win32 controller and test desktop; independent process and scaling work may proceed beside it. Stable `MonitorIdentity` crosses package boundaries, while a GDI `DeviceName` is resolved inside a scaling operation, converted to `displayId`, and never survives an NVAPI set.

**Tech Stack:** Go 1.27.x, Windows/amd64, `github.com/lxn/walk`, `golang.org/x/sys/windows`, Win32 display APIs, Toolhelp, NVAPI through `nvapi64.dll`, PowerShell.

**Spec:** `docs/superpowers/plans/2026-09-14-configurable-tray.md`, governed by:

- `docs/superpowers/specs/2026-09-13-go-display-tray-design.md`
- `docs/superpowers/specs/2026-09-13-configurable-profile-design.md`
- `docs/superpowers/specs/2026-09-14-gpu-scaling-design.md`

## Global Constraints

- Work on `codex/configurable-tray` at baseline `0dbe290`; preserve the seven inherited Task 5 files and do not reuse or clean an obsolete worktree.
- Tasks 1, 2 and 3 are complete at `f9e143a`, `ce27a4b` and `a6e638a`. Task 5 and Task 9 are in progress. Do not dispatch either one a second time.
- Delegate all implementation and fixes. Workers do not run Git commands, spawn helpers, or edit files outside their assigned ownership. The coordinator alone reviews, stages exact paths and commits.
- Use Luna for bounded mechanical work, Terra for isolated feature work, and Sol for native bindings, integration, UI composition and concurrency. Do not use Astra.
- Startup and the first-run wizard remain read-only. Never send `CDS_UPDATEREGISTRY`, `NV_DISPLAYCONFIG_SAVE_TO_PERSISTENCE`, or an NVAPI apply flag other than exactly `0x00`; validate with `CDS_TEST` and `0x01` first.
- A GDI `\\.\DISPLAYn` may be used to obtain an NVAPI `displayId` only inside `internal/scaling`; it must not be returned by scaling, cached across a set, or reused by display code after a set.
- Default tests use fakes. `RUN_DISPLAY_INTEGRATION=1` permits only reads and `CDS_TEST`; `RUN_NVAPI_INTEGRATION=1` permits only reads and the wrong-version negative control. Neither gate authorizes an NVAPI write.
- Manual hardware checks at the end of the original plan are a human release gate. Automated work must record them as pending, never as passed. A built binary may later be launched only to inspect its read-only startup and layout.

## Interface Rulings Before More Dispatches

1. **Task 4 owns monitor discovery.** Add `Targets() ([]domain.Target, error)` to `display.Controller`, backed by the existing fresh `native.listTargets()` read. `ResolveTarget` calls that method. Task 4 additionally owns `internal/display/controller.go`, `internal/display/controller_test.go`, `internal/app/session_test.go`, and `internal/ui/window_windows_test.go` for the interface and fake compile repairs. This is the public monitor list Task 12 consumes; Task 12 must not reach into `windowsNative`.
2. **Task 6 owns every `EnumModes` compile repair.** Its file list also includes `internal/display/fake_win32_windows_test.go`, `internal/app/session_test.go`, and `internal/ui/window_windows_test.go`, because the task extends the fake Win32 mode table and adds `EnumModes` to `display.Controller`.
3. **Task 8 owns optional fallback end to end.** In addition to the original files, it owns `internal/config/config.go`, `internal/config/config_test.go`, `internal/config/store_test.go`, and `internal/domain/profile_test.go`. `File.Profile` and `FromProfile` map `nil` to `nil`; tests compare profiles structurally rather than by pointer identity. `Profile.Name` remains absent from schema v1 by design; UI copy uses `Profile.Monitor.Label`, so a loaded profile may have an empty `Name`.
4. **Tasks 13–14 use an injected resolver, without a package import.** Task 13 defines the constructor seam as a function with the exact shape `func(domain.MonitorIdentity) (domain.Target, error)`. Task 14 exposes `scaling.NewWindowsController(resolve func(domain.MonitorIdentity) (domain.Target, error))`. `internal/scaling` imports only `internal/domain`; `main_windows.go` later passes `displays.ResolveTarget`. Scaling immediately converts the returned `Target.DeviceName` with `NvAPI_DISP_GetDisplayIdByDisplayName`, retains only `displayId`, and never exposes the target. This reuses Task 5's one identity ladder without duplicating it or creating `display`↔`scaling` imports.
5. **Task 12 serializes settings UI actions.** Opening the modal marks the window busy and disables tray mutations before save; `config.Save` then `Provider.Replace` runs as one UI operation. `Provider.Replace` still refuses a managed session, so a late race cannot strand the saved layout.

The full preflight evidence and every shared-file/interface pair are recorded in `.superpowers/sdd/2026-09-14-configurable-tray/preflight.md`.

## Execution Waves

The arrows below are hard dependencies. Items on one line may overlap only while their owned paths are disjoint. During overlap, workers run package-scoped tests; the coordinator waits for the barrier before the repository-wide gate.

| Wave | Work | Model | Hard dependency and ownership |
|---|---|---|---|
| A, active | Task 5 identity resolution | Sol, high | Resume the existing worker and inherited seven-file diff. It exclusively owns `internal/display/controller*`, `internal/display/integration_windows_test.go`, `internal/app/session*`, and `internal/ui/window_windows*` until reviewed and committed. |
| A, active | Task 9 process names | Luna, high | Independent `internal/process/**`; report to `task-9-report.md`. |
| A, after this ruling | Task 13 scaling decision layer | Terra, high | New `internal/scaling/controller.go` and test only; defines the injected resolver seam. It may run beside Tasks 5 and 9. |
| B | Task 4 CCD names plus public `Targets` | Sol, high | **Task 5 commit → Task 4.** Own display CCD/Win32/controller/integration files plus `internal/app/session_test.go` and `internal/ui/window_windows_test.go` for interface repair. |
| B, parallel lane | Task 14 NVAPI binding | Sol, high | **Task 13 commit → Task 14.** New scaling native/test files only; may overlap Task 4 or 6. |
| C | Task 6 mode catalogue | Sol, high | **Task 4 commit → Task 6.** Own all display/domain mode files and the interface fake repairs. |
| C | Task 7 grow-direction proof | Terra, high | **Task 6 commit → Task 7.** No parallel display worker; change `layout.go` only when a new test proves a defect. |
| D | Task 8 configured session | Sol, high | **Tasks 5, 6 and 7 commits → Task 8.** Own app/domain/config optional-fallback changes and the UI field rename. |
| E | Task 10 profile-driven window | Terra, high | **Task 8 commit → Task 10.** Exclusive UI window ownership. |
| E | Task 11 provider/startup | Sol, high | **Task 10 commit → Task 11.** Own provider, main composition and UI provider conversion. |
| F | Task 12 settings/first run | Sol, high | **Tasks 4, 6, 9, 10 and 11 commits → Task 12.** Exclusive ownership of UI settings/window and `main_windows.go`. |
| G | Task 15 scaling/session ordering | Sol, xhigh | **Tasks 8, 11, 12, 13 and 14 commits → Task 15.** Exclusive ownership of app session/provider, shared recorder tests and main composition. |
| H | Task 16 scaling UI | Sol, high | **Tasks 12 and 15 commits → Task 16.** Exclusive UI ownership; keep display and scaling availability independent. |
| I | Task 17 docs and release verification | Terra, high; final reviewer Sol, xhigh | **Tasks 4–16 reviewed and committed → Task 17.** Automated verification and read-only UI inspection may complete; human hardware checklist remains pending evidence. |

## Per-Task Gates

- [ ] Before each dispatch, use the existing exact brief at `.superpowers/sdd/2026-09-14-configurable-tray/task-<N>-brief.md`, append the rulings above to the prompt, name every owned path, and tell the worker that other agents share the worktree.
- [ ] The implementer writes the full report to `.superpowers/sdd/2026-09-14-configurable-tray/task-<N>-report.md`; Task 5 also has `inherited-task5.patch`. The short return contains only status, tests, concerns and changed paths.
- [ ] Freeze writers at every wave barrier. Run `gofmt -l ./internal ./cmd`, `go build ./...`, `go vet ./...`, and `go test ./...`. Add `go test -count=20 ./internal/app` for Tasks 5, 7, 8, 11 and 15, and `go test -count=20 ./internal/scaling` for Tasks 13–15.
- [ ] Run `./build.ps1` for UI/composition tasks 10–12 and 15–16. Run the opt-in read-only integration gate only for Tasks 4, 5, 6 and 14; never run a scaling write as an automated test.
- [ ] Build a review package from the task's owned paths, then require both spec-compliance and code-quality verdicts in `.superpowers/sdd/2026-09-14-configurable-tray/task-<N>-review.md`. All fixes are re-delegated and receive a scoped re-review.
- [ ] After both verdicts pass and the global gate is green, the coordinator stages only that task's exact paths and makes its Conventional Commit from the original plan. Do not include another active worker's files or the two coordination documents in an application commit.
- [ ] After Task 16, run one Sol xhigh whole-branch review against all three specs. Task 17 fixes only concrete findings, each with a focused regression test and a separate `fix:` commit.
- [ ] Task 17 records automated checks separately from the seven manual hardware scenarios. Completion may be reported as “implementation and automated verification complete; manual release gate pending” until a human supplies the measurements.

## Immediate Resume Order

1. Let the existing Task 5 and Task 9 workers finish; do not overwrite their files.
2. Dispatch Task 13 with ruling 4 while those paths remain disjoint.
3. Review and commit Task 5 before anyone opens Task 4, 6 or 7 files.
4. Run the display lane `4 → 6 → 7`; run `13 → 14` in the scaling lane and Task 9 in the process lane beside it.
5. Join the lanes at Task 8, then execute `10 → 11 → 12 → 15 → 16 → 17` serially wherever their UI/app/main ownership overlaps.


## Resume checkpoint — 2026-09-15

Claude continued the same branch after the Codex quota interruption. Its transcript and commits were reconciled at `ccb8ab9`; the working tree was clean. The original plan now has 60/87 steps and 12/17 tasks checked. The completed set is Tasks 1–10, 13 and 14. Do not re-dispatch the earlier Task 4 or Task 13 workers against their obsolete scope.

| Remaining order | Deliverable | Execution |
|---|---|---|
| Task 11, active | Config-driven startup and replaceable session provider | Sol high; app/provider, UI and composition root |
| Task 12 | Settings dialog and first-run wizard | Sol high; consumes the reviewed provider |
| Task 15 | Scaling/session ordering, ownership and watcher lifecycle | Sol xhigh; integrate only after provider/settings interfaces settle |
| Task 16 | Scaling availability, controls and final UI layout | Sol high; consumes reviewed scaling snapshot |
| Task 17 | Documentation, final review and verification | Delegated documentation/fixes; coordinator verifies and records hardware gates |

Only one application implementer runs through this remaining shared app/UI/main sequence. Independent read-only interface and native-safety audits may run alongside implementation. No Astra subagents. All future code fixes remain delegated; the coordinator owns validation, Git and progress records.

Inherited rulings to preserve: ResolutionTray is the settled product name; fallback is derived for the snapshot and restore applies the displayed value; managed restore remains enabled even when fallback is unknown because it uses the saved layout; the seed retains its original fallback; schema v1 does not serialize Profile.Name. Growth tests confirm the final arrangement, but the fixed mixed-axis apply order can transiently overlap, so documentation must not promise every intermediate arrangement is non-overlapping. Neighbour-only layout errors still expose DISPLAYn names; Task 17 assesses presentation without weakening diagnostics.

The coordinator's fresh baseline verification at this checkpoint passed: `go test -count=1 ./...`, `go test -count=20 ./internal/app ./internal/scaling`, `go build ./...`, `go vet ./...`, `gofmt -l ./internal ./cmd` (empty), and `git diff --check`, with both hardware integration gates disabled. Actual display/scaling writes remain manual release verification, not automatic tests.
