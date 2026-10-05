# Mecatui in-app yolo toggle — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — a client-local relaunch affordance for the embedded server plus one remembered client preference. Operator posture stays fixed for each `app.Build`; `/yolo` rebuilds the embedded server exactly as quitting and relaunching with `--posture` and `--resume` would. The remembered state is an operator-tier input under the user's own XDG state directory, equivalent in authority to the existing operator `settings.yaml` `posture:` key. No composition, permission-fold, or wire semantics change.
**Decision record:** None — the change reuses the existing posture ladder ([ADR 0022](../adr/0022-allow-all-posture.md) semantics as implemented in `internal/app/posture.go`) and the existing mecatui quit-with-intent relaunch pattern (`ConnectRestartIntent`); it introduces no new durable architecture decision.
**Phase:** mecatui operator ergonomics
**Status:** proposed, 2026-10-05. Drafted from issue investigation; human decisions recorded 2026-10-05.
**Delivery:** Split. The change adds a user-facing command with a security-relevant confirmation and refusal surface, so the interface deserves review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#2057](https://github.com/stacklok/mecatl/issues/2057).
**Plan PR:** <added when opened>
**Approved baseline:** <merged plan commit; absent until approved>

In the default (embedded) mecatui, typing `/yolo` switches the embedded server to posture `yolo`
after an explicit confirmation. Typing `/yolo` again switches it back. Posture is resolved once per
`app.Build` (`internal/app/posture.go` (`applyPosture`)), so the switch is a relaunch:
the TUI exits with a posture restart intent, the composition root tears down the embedded server,
rebuilds it at the target posture, and resumes the same session through the existing exact
`--resume` path (`cmd/mecatui/startup_resume.go` (`startupResumeConfig`)). This mirrors the existing
`ConnectRestartIntent` relaunch in `cmd/mecatui/main.go` (`restartFromConnectIntentWith`).
`mecatui connect` never offers `/yolo`, because the remote deployment owns its posture.

Quitting mecatui while in yolo ends the session normally, like any other quit. mecatui remembers
the yolo state for that workspace, so the next launch there starts in yolo, with a fresh session
by ordinary launch rules. Turning yolo off with `/yolo` clears the remembered state.

## Human decisions

- [x] **Quitting and relaunching mecatui while in yolo:** quitting ends the session as any quit does (normal final handoff line). The next launch starts in the same yolo state you quit with. — Decision: remember the `/yolo` state per workspace in client state. It is keyed like the model selection (`stateKey`: realpath, with linked worktrees folded to the main worktree root). The next embedded launch in that workspace starts at yolo with a fresh session by ordinary launch rules. Turning yolo off with `/yolo` clears the remembered state. An explicit `--posture` flag on a launch wins for that launch and does not change the remembered state. The remembered state is ignored by `mecatui connect` and ignored, with a stderr warning, when the launch is privileged outside a sandbox.
- [x] **Turning yolo off (`/yolo` while in yolo):** — Decision: reproduce this process's pre-yolo launch inputs exactly. Those inputs are the original `--posture`, `--trust-project`, and the first launch's trust-prompt outcome, excluding `--yolo` and the remembered yolo state. When the process launched at yolo through `--posture yolo` or operator `posture: yolo`, the posture input becomes `strict`, while `--trust-project` and the trust outcome are kept.
- [x] **Relaunch mechanism:** — Decision: quit the Bubble Tea program, close that frame's transport and status source, and relaunch from a loop in `run()` with the resumed session (screen redraw, MCP reconnects, the embedded scheduler restarts). This reuses a proven path and keeps posture strictly per-Build; an in-place server swap is rejected.
- [x] **Non-durable store:** — Decision: refuse `/yolo` under `--no-store` with a message naming the flag, because the in-memory session would not survive the rebuild.
- [x] **Confirmation scope:** — Decision: entering yolo requires confirmation with a distinct `y` key. Enter, Esc, and any other key cancel, and pasted input never confirms. Turning yolo off needs no confirmation. A launch that restores the remembered yolo state needs no confirmation; it warns on stderr and shows the YOLO badge.
- [x] **Worktree-bound sessions:** — Decision: refuse `/yolo` while the current session is on a sibling worktree, with a message to switch back to the default workspace first. Worktree listing is trust-gated and the exact reference includes the worktree revision, so the rebuilt server might not reattach it.

## Interface contract

- **gRPC / protobuf:** None — no RPC, message, or field changes. Relaunch uses existing `CreateSession`/`GetSession`/transcript RPCs through the existing startup-resume path; the posture badge already reads `Capabilities.posture`.
- **Exported Go APIs / interfaces:** No `engine/` API change (api-compat unaffected). Binary-internal additions in `cmd/mecatui/ui`, which is not part of the engine stability surface:
  - `type PostureRestartIntent struct { EnterYolo bool; ResumeSessionID string }` — consumed by `main` after Bubble Tea exits.
  - `type PostureSwitch interface { Refusal(enterYolo bool) error }` — composition-owned pre-check that returns a user-facing reason (privileged-outside-sandbox via `app.PostureRefusalReason`, `--no-store`) or nil.
  - `ui.Deps.PostureSwitch PostureSwitch` — nil means `/yolo` is not registered.
  - `ui.Deps.PostureNotice string` — a one-time status notice shown after a posture relaunch (for example, the resume-fallback notice in AC3.4).
  - `func (m Model) PostureRestartIntent() (PostureRestartIntent, bool)` — reporter read by `main`, mirroring `ConnectRestartIntent()`.
  - `yolo` is added to `builtinNameRegistry`, so `/yolo` is intercepted as a known builtin even when it is not registered.
  - `ui.PostureSwitch` gains no persistence method. `main` writes the remembered state after the user confirms entry and after a successful turn-off intent, before relaunching.
  - In `cmd/mecatui` (package main, unexported): a `postureStore` over `$XDG_STATE_HOME/mecatui/posture.yaml` with `LoadYolo(workspace string) bool`, `SaveYolo(workspace string, on bool) error`. It uses the same fail-soft read cap, version gate, owner-only atomic write, and `stateKey` as `selectionStore`.
  - In `cmd/mecatui` (package main, unexported): `runOptions` gains the posture-relaunch state. That state is the target posture, the captured pre-yolo launch inputs (posture flag, `--trust-project`, first trust-prompt outcome), the exact resume session ID, and a flag that suppresses the trust prompt and selects resume-fallback behavior. `run()` drives relaunches in a loop instead of recursing into `runWithOptions`.
- **Tool schemas:** None — `/yolo` is a client-local mecatui builtin. It is not a model-facing tool and the model cannot invoke it.
- **CLI / config:** New mecatui builtin `/yolo` (no arguments; toggles). It is registered only when the transport mode is embedded (`config.mayEmbed()`), the UI is not a debug session, and `caps.Posture` is non-empty. No new flags or settings keys, and nothing is written to `settings.yaml`. At launch, embedded mode applies the remembered yolo state as `posture=yolo`, `postureFlagSet=true` only when no explicit `--posture` flag was given. It is applied after the privileged-outside-sandbox check: when that check would refuse, the state is ignored and a warning is printed instead. A launch that restores yolo prints `mecatui: posture yolo restored for this workspace; type /yolo to turn it off` to stderr, alongside the existing yolo warning. The relaunch applies, in memory, `postureFlagSet=true` and `allowAllTools=false` over the re-parsed original argv. On entry it sets `posture=yolo`. On exit it applies the captured pre-yolo inputs from the exit-target decision. Clearing `--yolo` is required because `ResolveAliasPosture` folds MAX-tier, so a launch-time `--yolo` would otherwise make leaving impossible. The explicit flag also out-ranks operator `posture:` YAML. The pre-TUI trust prompt (`applyTrustPrompt`) is not asked again on a posture relaunch; the first launch's outcome is reused. The relaunch also clears the seed prompt (`-p`/`--prompt-file`), `--resume-latest`, and `--browse-sessions`, and sets the exact resume ID. The yolo state persists per workspace across launches, as described above.
- **Events / persistence:** No session events or snapshot fields change; the session survives through the existing durable store. There is one new client-side state file, `$XDG_STATE_HOME/mecatui/posture.yaml` (mode 0600, directory 0700): `version: 1` plus a `workspaces` map from each workspace's `stateKey` to `{yolo: true}`. An entry is removed when yolo is turned off. A missing, oversized, malformed, or unknown-version file reads as "no remembered yolo", which fails safe to the lower posture.
- **Security / authority:** Only a human typing `/yolo` in the embedded TUI can trigger it, and entering requires the distinct confirming key. `/yolo` is a registered builtin name, so it is never sent to the model and never expands a server or project slash command named `yolo`, in any client state. A seed prompt `-p /yolo` still stops at the confirmation. Before exiting the TUI, the privileged-outside-sandbox refusal runs on the same predicate as launch (`embeddedPrivileged` + `app.PostureRefusalReason`); `app.Build` stays the fail-closed backstop. Yolo applies to the whole embedded server: every session, subagent, branch, and schedule hosted by that process. It turns allow-all ON, loosens the child substitution floor, and raises interactive project trust. A deny in any scope and every configured Ask still apply. Leaving reproduces the pre-yolo inputs, so posture-derived project trust is dropped. Explicit `--trust-project`, the first trust-prompt outcome, `trustedWorkspaces`, and undrifted remembered trust keep granting trust as they did before entry. Remembered trust that drifted during yolo (for example, the model edited project steering files) stays ungranted, and no prompt is shown because the relaunch is non-interactive. The remembered yolo state is written only by `main` in response to a confirmed `/yolo`, never by the server, the model, or a project file. It lives in the user's own state directory outside every workspace, so a repository cannot plant it. It is keyed per workspace, so yolo chosen in one repository never applies to another.
- **Compatibility / migration:** Additive. Embedded-only, so client and server are always the same build. The new state file starts absent, and older mecatui builds ignore it. No migration.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — `/yolo` enters yolo and keeps the conversation

The command follows the mecatui builtin registry (`cmd/mecatui/ui/builtins.go`) and the
posture semantics in [AGENTS.md](../../AGENTS.md) (posture ladder, `applyPosture` runs before the
trust fold). The relaunch reuses the composition-root loop in [architecture](../architecture.md)'s
mecatui client section.

**Acceptance:**
- AC1.1: In an embedded session at idle with posture below yolo, `/yolo` opens a confirmation that names the consequences: allow-all, the child injection defense off, project trust, the whole embedded server restarting, and that plan mode still blocks changes. Only the `y` key confirms. Enter, Esc, any other key, and pasted input (including a paste of `/yolo` followed by `y`) cancel, leaving the posture, session, and transport unchanged.
  - verify: `TestMecatuiYoloToggle_Scenario1_ConfirmationGatesEntry`
- AC1.2: Confirming quits the program with `PostureRestartIntent{EnterYolo: true, ResumeSessionID}` set to the current session ID, and with no `ConnectRestartIntent`.
  - verify: `TestMecatuiYoloToggle_Scenario1_ConfirmEmitsIntent`
- AC1.3: `main` writes a `restarting embedded server at posture yolo` line to stderr and relaunches the embedded server at effective posture `yolo` (the badge reads YOLO and `caps.Posture == "yolo"`). It resumes the same session ID with its authoritative transcript and its current server-side permission mode, including `plan`. The trust prompt is not asked again, the final-session handoff line is not written for the intermediate exit, and the seed prompt is not resubmitted.
  - verify: `TestMecatuiYoloToggle_Scenario1_RelaunchResumesSameSessionAtYolo`
- AC1.4: Toggling N times does not nest call frames or accumulate open transports or status sources; each frame closes its resources before the next relaunch.
  - verify: `TestMecatuiYoloToggle_Scenario1_RepeatedTogglesDoNotAccumulate`
- AC1.5: `/yolo` never reaches the model and never expands a server or project command named `yolo`. This holds in every client state, including before capabilities load and when `/yolo` is not registered (it is then refused as an unavailable builtin).
  - verify: `TestMecatuiYoloToggle_Scenario1_NeverForwardedToModel`

### Scenario 2 — `/yolo` again leaves yolo

Leaving relies on the MAX-tier alias fold and the interactive trust floor described in
[AGENTS.md](../../AGENTS.md) (posture ladder; root-aware project trust,
[ADR 0095](../adr/0095-root-aware-project-trust.md)).

**Acceptance:**
- AC2.1: In an embedded session at posture yolo, `/yolo` relaunches without confirmation at the exit target from the resolved human decision, resuming the same session.
  - verify: `TestMecatuiYoloToggle_Scenario2_LeaveRestoresPriorPosture`
- AC2.2: A process launched with `--yolo` (alias) or operator `posture: yolo` can leave yolo. The relaunch drops `--yolo` and out-ranks the operator YAML with an explicit posture, so the MAX-fold cannot re-raise it. A launch with `--yolo --trust-project` leaves to effective `trusted`, keeping the explicit trust grant.
  - verify: `TestMecatuiYoloToggle_Scenario2_LeaveDefeatsLaunchAlias`
- AC2.3: After leaving, posture-derived project trust is no longer granted. The first launch's trust-prompt outcome, `--trust-project`, `trustedWorkspaces`, and undrifted remembered trust grant exactly as before entry. Drifted remembered trust stays ungranted without a prompt.
  - verify: `TestMecatuiYoloToggle_Scenario2_LeaveDropsPostureDerivedTrust`

### Scenario 3 — refusals leave everything untouched

The privileged-outside-sandbox refusal is the one [ADR 0022](../adr/0022-allow-all-posture.md)
defines for allow-all tiers; `connect` never embeds per [ADR 0087](../adr/0087-mecatui-staged-transport-migration.md).

**Acceptance:**
- AC3.1: `/yolo` is not registered under `mecatui connect`, in a debug session, or when the server reports no posture.
  - verify: `TestMecatuiYoloToggle_Scenario3_NotRegisteredOutsideEmbedded`
- AC3.2: While a run is active, an approval is pending, or the session is not yet ready, `/yolo` shows a warning status and emits no intent.
  - verify: `TestMecatuiYoloToggle_Scenario3_RefusedUnlessIdle`
- AC3.3: When `PostureSwitch.Refusal` returns an error (privileged outside a sandbox, or `--no-store` per the resolved decision), or the session is on a sibling worktree per the resolved decision, `/yolo` shows that reason, emits no intent, and the session continues.
  - verify: `TestMecatuiYoloToggle_Scenario3_CompositionRefusalShown`
- AC3.4: If the relaunched resume of the exact session fails, mecatui does not exit. It starts the ordinary fresh session at the new posture and shows `PostureNotice` saying the previous chat can be continued from `/sessions`. This fallback applies only to posture relaunches; an explicit `--resume` keeps its existing fail-on-error behavior.
  - verify: `TestMecatuiYoloToggle_Scenario3_ResumeFailureFallsBackWithNotice`
- AC3.5: If the rebuild itself fails (validation, provider, or embedded server start), mecatui exits non-zero and writes the reason plus the resumable session ID (`mecatui --resume <id>`) to stderr, so the conversation is never orphaned.
  - verify: `TestMecatuiYoloToggle_Scenario3_RebuildFailurePrintsResumeHandle`

### Scenario 4 — quitting in yolo, then relaunching, starts in yolo

The remembered state follows the existing client-state conventions of the model-selection store
(`cmd/mecatui/state.go` (`selectionStore`)). The privileged-outside-sandbox refusal is unchanged from
[ADR 0022](../adr/0022-allow-all-posture.md).

**Acceptance:**
- AC4.1: After confirming `/yolo` in workspace W, quitting mecatui ends the session normally (the final handoff line is written). A later bare embedded launch in W, with no posture flags, starts at effective posture `yolo` with a fresh session. It prints the restore line to stderr and shows the YOLO badge, with no confirmation.
  - verify: `TestMecatuiYoloToggle_Scenario4_RelaunchInSameWorkspaceRestoresYolo`
- AC4.2: Turning yolo off with `/yolo` removes W's entry, so the next launch in W starts at its ordinary resolved posture. Yolo remembered for W never applies to a different workspace; a linked worktree of W shares W's key.
  - verify: `TestMecatuiYoloToggle_Scenario4_StateIsPerWorkspaceAndClearedOnTurnOff`
- AC4.3: An explicit `--posture` flag on the launch wins for that launch and leaves the remembered state unchanged. `mecatui connect` ignores the state. A privileged launch outside a sandbox ignores it with a stderr warning and starts at the ordinary posture instead of refusing to start.
  - verify: `TestMecatuiYoloToggle_Scenario4_ExplicitFlagConnectAndPrivilegedIgnoreState`
- AC4.4: A missing, oversized, malformed, or unknown-version `posture.yaml` reads as "not yolo". A failed write after a confirmed toggle shows a warning that the choice will not be remembered, without blocking the switch.
  - verify: `TestMecatuiYoloToggle_Scenario4_StateFileFailsSafe`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Switching posture on a remote `mecatui connect` target | Not planned | The remote deployment's operator owns posture; a client must not change it. |
| `/posture <tier>` for arbitrary tiers (strict/trusted/auto) | Follow-up issue if wanted | The issue asks for yolo only; the same intent shape could later carry a tier. |
| A `/yolo` slash command in Studio | Separate Routine change | Studio already switches posture through its Safety pill and controller restart. |
| Writing the chosen posture to `settings.yaml` | Not planned | The remembered state lives in per-workspace client state instead, so it never applies globally or edits the operator's config file. |
| Resuming the previous session automatically on the next launch | Not planned | Quitting ends the session; `--resume`/`/sessions` remain the explicit continuation paths. |
| Making posture mutable inside a running `app.Build` | Not planned | Posture is per-Build by design; rebuilding preserves every existing composition invariant. |

## Definition of done

1. `task lint`, `task test`, `task docs`, and `task api:check` pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. `user-docs/mecatui/using-the-tui.md` lists `/yolo`, and `user-docs/features/permissions-and-posture.md` describes the in-app switch, its embedded-only scope, its refusals, and the per-workspace remembered state with how to clear it.
5. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The relaunch interrupts anything the embedded server hosts beyond the current session, such as background schedule fires or other sessions, in both directions. The idle gate covers only the current session. On entry the confirmation says the server restarts; on exit the stderr restart line is the only signal. Embedded server shutdown can take up to the existing cleanup bound (~45 s hard exit) while the alt-screen is gone.
- Exiting and re-entering the alt-screen is visible as a redraw. Acceptable for a deliberate, rare action.
