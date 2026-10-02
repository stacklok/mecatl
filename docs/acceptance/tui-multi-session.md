# Multi-session mecatui window — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds a server-created worktree placement and stop-and-delete to the public session API, changes local worktree placement identity, and adds a multi-session client surface
**Decision record:** [ADR 0374](../adr/0374-server-created-session-worktrees.md)
**Phase:** mecatui multi-session window
**Status:** proposed, 2026-10-02. Drafted from issue #2058; every human decision resolved by the directing human.
**Delivery:** Split. The plan changes protobuf, HTTP, placement identity, and deletion authority, so the interface needs its own review before implementation.
**Expected tasks:** 6
**Issue:** [stacklok/mecatl#2058](https://github.com/stacklok/mecatl/issues/2058).
**Plan PR:** <added when opened>
**Approved baseline:** <merged plan commit; absent until approved>

One mecatui window holds several sessions. Pressing **←** on an empty prompt opens a list of
the window's sessions, each with a unique label and live status. Switching never stops a
session: background sessions keep running and can pause for approval. A new session can
start in the default checkout or in a fresh server-created worktree. Deleting a session
stops it and, for a session in a server-created worktree, can also remove that worktree.

## Design boundaries

- **Each session keeps its own client model.** The window is a new Bubble Tea root that
  holds one existing `ui.Model` per session and routes messages to it. The existing
  per-session `Model`, reducers, and invariants are reused unchanged rather than split into
  a shared shell plus per-session state. A background model keeps draining its own
  Converse stream, because the server cancels a run whose stream closes
  ([AGENTS.md](../../AGENTS.md), drain-to-discard relays) and stalls one whose stream is
  not read.
- **No detached server runs.** Runs stay tied to their Converse stream. Quitting the window
  ends every session's stream, as it does for the single session today.
- **Placement stays server-owned** ([ADR 0291](../adr/0291-server-owned-session-placement.md)).
  The client asks for a fresh worktree with a boolean intent. It never sends or receives a
  path, and worktree ownership is derived on the server ([ADR 0374](../adr/0374-server-created-session-worktrees.md)).

## Human decisions

- [x] Archive versus delete — Decision: delete only. No archive state, RPC, or list filter.
- [x] Worktree cleanup — Decision: when deleting a session that runs in a worktree mecatl created, offer "delete the chat" and "delete the chat and remove its worktree".
- [x] Lifecycle of a background session — Decision: no separate stop or close action. A session lives in the window until it is deleted, and deleting it stops it if it is running.
- [x] Unique names — Decision: show the session title. When two window sessions share a title, append the ADR 0285 short handle to each of them.
- [x] Managed worktree location — Decision: outside the repository, each on its own branch: `$XDG_STATE_HOME/mecatl/worktrees/REPO-HASH/NAME` on the server host (default `~/.local/state/...`), where REPO-HASH is the repository directory name plus the first 8 hex characters of the SHA-256 of the configured root, on branch `mecatl/NAME`, created from the main checkout's current HEAD.
- [x] Quitting with other sessions running or awaiting — Decision: ask once for confirmation that names how many will be cancelled. The sessions stay saved and can be reopened with `/sessions`.
- [x] Window session list scope — Decision: the list holds only sessions opened in this window (the startup session plus ones created with **n**/**w** or opened from `/sessions`) and is not saved across restarts. The list makes `/sessions` discoverable: it shows a hint and an **o** action that opens `/sessions` to bring in any saved session.

## Interface contract

- **gRPC / protobuf:** additive fields in `contracts/proto/mecatl/v1/harness.proto`, no new RPCs:
  - `CreateSessionRequest.new_worktree` (`bool`, field 12): bind a fresh server-created worktree ([ADR 0374](../adr/0374-server-created-session-worktrees.md) Decision 1). Rejected with `FAILED_PRECONDITION` when worktree creation is unavailable or `profile` is `no-fs`.
  - `ServerCapabilities.create_worktrees` (`bool`, field 31).
  - `DeleteSessionRequest.stop_active` (`bool`, field 2) and `DeleteSessionRequest.remove_worktree` (`bool`, field 3).
  - `DeleteSessionResponse.worktree_removed` (`bool`, field 1): true when the worktree directory was removed; `DeleteSessionResponse.worktree_retained_reason` (`string`, field 2): `dirty` or `shared` (found at the post-stop re-check) or `remove_failed`, empty otherwise.
  - `SessionInventoryCapabilities.remove_worktree` (`bool`, field 11) and `SessionInventoryActionReasons.remove_worktree` (`string`, field 8), with reason codes `not_server_created`, `shared`, `unavailable`. Dirtiness is checked at delete time, not projected in the list.
  - Existing `CreateSessionResponse.placement` returns the new branch as display metadata.
- **Exported Go APIs / interfaces:** no `engine/` API change (`task api:check` unchanged). In `internal/adapter/server`: `PlacementBindRequest.NewWorktree bool`; new optional provider interface `PlacementWorktreeRemover { WorktreeOwnership(ctx, PlacementReattachRequest) (WorktreeOwnership, error); RemoveWorktree(ctx, PlacementReattachRequest) error }`, with `WorktreeOwnership{ServerCreated, Clean bool}`. In `cmd/mecatui/client`: `CreateSessionRequest` gains `NewWorktree bool`; `DeleteSession(ctx, id string, opts DeleteSessionOptions) (DeleteSessionResult, error)` with `DeleteSessionOptions{StopActive, RemoveWorktree bool}`. In `cmd/mecatui/ui`: `NewWindow(Deps) tea.Model` replaces `New` as the program root in `cmd/mecatui/main.go`; `Model` stays the per-session model. The window builds additional session models with an unexported `newSessionModel(deps Deps, open sessionOpen) Model`, where `sessionOpen` is either `{attachID string}` (adopt an existing session without creating one) or `{create: client.CreateSessionRequest}` (run the normal ListModels→CreateSession startup, carrying `NewWorktree`). Each session model gets its own cancellable child of `Deps.Ctx` and per-session wrappers for the side-effecting `Deps` hooks (terminal title, status source) that forward only while that model is active.
- **Tool schemas:** None — no model-facing tool changes.
- **CLI / config:** new rebindable keymap action `sessions` (default `left`), active only when the prompt is empty and no overlay or modal is open, mirroring `edit_back` (`cmd/mecatui/ui/keys.go`, `cmd/mecatui/keymap/validator.go`). No new server flag; the managed worktree root follows the server's XDG state directory.
- **Events / persistence:** no new event or snapshot field. New local worktree refs persist `Revision: "worktree-v1"`; refs persisted with a well-formed hex HEAD revision reattach by path, and any other revision fails closed ([ADR 0374](../adr/0374-server-created-session-worktrees.md) Decision 2). The same PR updates the AGENTS.md ADR 0291 placement bullet, IMPLEMENTATION-NOTES "Server-owned session placement", and the ADR 0027 resource inventory (managed worktree root, per-repository create lock). HTTP `POST /v1/sessions` accepts `new_worktree`; `POST /v1/sessions/{id}/delete` accepts an optional JSON body `{stop_active, remove_worktree}` and returns `204` unless `remove_worktree` was requested, when it returns `200 {"worktree_removed": bool, "worktree_retained_reason": string}`.
- **Security / authority:** every server git call (create, cleanliness check, removal) is admitted under the same gate as worktree listing (configured workspace, shell, trusted project; [ADR 0095](../adr/0095-root-aware-project-trust.md)) and only for local default placement. Git runs with the forker's scrubbed, hook-free environment; repository-configured filter drivers remain an accepted residual under that trust decision. Ownership compares symlink-resolved paths and rejects a symlink leaf. `remove_worktree` acts only on a server-created worktree that no other persisted session (any kind or owner) or schedule binds, under a per-path lock, never uses `--force`, and keeps the branch. `stop_active` acts only after the run-entry lock and the real session lease are held; a lease held elsewhere fails, and delegation-child ids are refused. A fresh worktree session records present, empty enrollment provenance like any fresh create ([ADR 0358](../adr/0358-durable-workspace-enrollment-broker-authority.md)). No path crosses the public API.
- **Compatibility / migration:** additive proto and HTTP fields; old clients and servers ignore them. A new mecatui on an old server hides the **w** action (no `create_worktrees`) and falls back to cancel-then-delete without worktree removal. Existing worktree sessions with HEAD revisions keep working. The TypeScript SDK and `apps/` need no change; the SDK may adopt the fields later.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — A worktree session keeps working after a commit

Today a commit in a worktree breaks the session bound to it, because `Reattach` matches the
stored HEAD (`internal/app/placement.go`, [ADR 0291](../adr/0291-server-owned-session-placement.md) Decision 2).

**Acceptance:**
- AC1.1: A session moved to a worktree with `ClearSession`, followed by a commit in that worktree, starts its next run, lists worktrees, and lists commands without error.
  - verify: `TestTUIMultiSession_Scenario1_WorktreeReattachSurvivesCommit`
- AC1.2: A session whose stored worktree path is no longer a worktree of the configured repository still fails with failed precondition before any filesystem access.
  - verify: `TestTUIMultiSession_Scenario1_RemovedWorktreeFailsClosed`
- AC1.3: A ref persisted with a well-formed hex HEAD revision reattaches by path, and the configured-root ref still requires its exact revision.
  - verify: `TestTUIMultiSession_Scenario1_LegacyRevisionReattaches`
- AC1.4: A local worktree ref whose revision is neither `worktree-v1` nor a well-formed hex object id fails closed.
  - verify: `TestTUIMultiSession_Scenario1_MalformedRevisionFailsClosed`

### Scenario 2 — Create a session in a fresh server-created worktree

The server creates and binds the worktree itself; the client sends only an intent
([ADR 0374](../adr/0374-server-created-session-worktrees.md) Decision 1). Git runs with the
same scrubbed environment as every agent-facing runner ([AGENTS.md](../../AGENTS.md), Finding B).

**Acceptance:**
- AC2.1: `CreateSession{new_worktree: true}` on a trusted local deployment creates a worktree under the managed root on a new `mecatl/<name>` branch from HEAD, binds the session to it, and returns placement metadata with that branch and no path.
  - verify: `TestTUIMultiSession_Scenario2_CreateSessionInNewWorktree`
- AC2.2: The request fails with failed precondition and creates no worktree, branch, or session when the project is untrusted, there is no shell or workspace, the profile is `no-fs`, or the default placement is not local; `ServerCapabilities.create_worktrees` is false in exactly those deployments.
  - verify: `TestTUIMultiSession_Scenario2_NewWorktreeGate`
- AC2.3: Worktree creation runs git with the scrubbed, hook-free environment, so a repository `post-checkout` hook does not run.
  - verify: `TestTUIMultiSession_Scenario2_CreateRunsNoHooks`
- AC2.4: A failure after `git worktree add` removes the partial worktree and branch and persists no session.
  - verify: `TestTUIMultiSession_Scenario2_FailedCreateCleansUp`
- AC2.5: Concurrent creates on one repository each get a distinct worktree and branch; a generated name that collides with an existing path or kept branch is retried; an unborn HEAD fails with failed precondition.
  - verify: `TestTUIMultiSession_Scenario2_ConcurrentAndCollidingCreates`
- AC2.6: A fresh worktree session records present, empty workspace-enrollment provenance.
  - verify: `TestTUIMultiSession_Scenario2_FreshEnrollmentProvenance`

### Scenario 3 — Delete stops the session and can remove its worktree

[ADR 0297](../adr/0297-active-clear-cancellation-boundary.md) is the precedent for acting on an active session.

**Acceptance:**
- AC3.1: `DeleteSession{stop_active: true}` on a running session takes the run-entry lock and lease, cancels the run, waits up to 10 seconds for it to end, and deletes the session. A run that does not end in time returns deadline exceeded and deletes nothing. Without `stop_active` the existing refusal is unchanged.
  - verify: `TestTUIMultiSession_Scenario3_StopActiveDeletesRunningSession`
- AC3.2: `DeleteSession{stop_active: true}` deletes a session parked awaiting approval, including one with no live owner after a restart, and a crash-orphaned `running` snapshot, once this process holds the lease.
  - verify: `TestTUIMultiSession_Scenario3_StopActiveDeletesAwaitingSession`
- AC3.3: `stop_active` on a session whose lease another process holds fails with failed precondition, and a delegation-child id is refused, with nothing cancelled or deleted.
  - verify: `TestTUIMultiSession_Scenario3_StopActiveRequiresLease`
- AC3.4: `remove_worktree` on a clean, unshared server-created worktree deletes the session, removes the worktree directory, keeps the `mecatl/<name>` branch, and returns `worktree_removed: true`.
  - verify: `TestTUIMultiSession_Scenario3_RemoveCleanWorktree`
- AC3.5: `remove_worktree` fails with failed precondition before anything is stopped or deleted when the worktree is dirty, not server-created (including a symlink under the managed root that points elsewhere), or shared with a successor from `ClearSession`/`ForkSession`, a schedule, or a persisted child session.
  - verify: `TestTUIMultiSession_Scenario3_RemoveWorktreeRefusesUnsafe`
- AC3.6: When the stopped run wrote to the worktree before it ended, the session is deleted, the worktree is kept, and the response is `worktree_removed: false` with reason `dirty`.
  - verify: `TestTUIMultiSession_Scenario3_PostStopDirtyKeepsWorktree`
- AC3.7: A Create, Clear, or Fork that targets the same worktree while a removal is in progress waits for the per-path lock and then fails because the worktree is gone.
  - verify: `TestTUIMultiSession_Scenario3_RemovalHoldsPathLock`
- AC3.8: Session inventory rows report `capabilities.remove_worktree` and its reason code.
  - verify: `TestTUIMultiSession_Scenario3_InventoryReportsRemoveWorktree`
- AC3.9: The HTTP delete route accepts the new body and returns the documented status codes.
  - verify: `TestTUIMultiSession_Scenario3_HTTPDeleteBody`

### Scenario 4 — Several sessions in one window, switched with ←

The window root routes messages to one per-session model. The `ui` package keeps importing
only `client` and `theme` ([architecture](../architecture.md); `docs/tui.md`), and session
handles follow [ADR 0285](../adr/0285-predictable-mecatui-session-handles.md).

**Acceptance:**
- AC4.1: **←** on an empty prompt opens the window session list. With text in the prompt, inside an overlay, or in the approval modal, **←** keeps its current meaning. The `sessions` keymap action rebinds it.
  - verify: `TestTUIMultiSession_Scenario4_LeftOpensListOnlyOnEmptyPrompt`
- AC4.2: Each row shows a status (running, needs approval, failed, or idle — idle also covers completed and cancelled turns), the label, and the worktree branch when not the default checkout. Labels are titles; two rows with the same title each append their short handle.
  - verify: `TestTUIMultiSession_Scenario4_ListRowsAndUniqueLabels`
- AC4.3: Switching to another session shows its transcript while the previous session's run keeps streaming in the background; switching back shows every event it produced meanwhile, in order.
  - verify: `TestTUIMultiSession_Scenario4_BackgroundRunKeepsStreaming`
- AC4.4: A background permission ask marks its row "needs approval" and shows a footer badge. Switching to that session opens the ask, and the verdict resolves that session's run only.
  - verify: `TestTUIMultiSession_Scenario4_BackgroundAskSurfacesOnSwitch`
- AC4.5: Only the active session receives keys, mouse, paste, and focus, and sets the terminal title; window size and theme messages reach every session.
  - verify: `TestTUIMultiSession_Scenario4_InputRoutesToActiveOnly`
- AC4.6: Quitting while any other window session is running or awaiting asks once for confirmation that names how many will be cancelled; declining keeps the window open.
  - verify: `TestTUIMultiSession_Scenario4_QuitConfirmsBackgroundSessions`
- AC4.7: Opening a session from `/sessions` adds it to the window list instead of replacing the current session; opening one that is already in the window switches to its row without adding a duplicate.
  - verify: `TestTUIMultiSession_Scenario4_SessionsPickerAddsToWindow`
- AC4.8: Only the active session writes to the shared status-line source, and switching re-submits the newly active session's state. Deleting one session model closes neither the shared status source nor another session's stream.
  - verify: `TestTUIMultiSession_Scenario4_SharedDepsScopedToActive`
- AC4.9: The window session list always shows a hint naming `/sessions` for opening saved sessions, and **o** opens the `/sessions` picker from the list.
  - verify: `TestTUIMultiSession_Scenario4_ListLeadsToSavedSessions`

### Scenario 5 — Start a new session from the list

New sessions use the existing create flow; **w** adds the
[ADR 0374](../adr/0374-server-created-session-worktrees.md) worktree intent.

**Acceptance:**
- AC5.1: **n** in the list creates a session on the default placement and switches to it.
  - verify: `TestTUIMultiSession_Scenario5_NewSession`
- AC5.2: **w** creates a session with `new_worktree` and switches to it, showing the new branch. The action is hidden when the server lacks `create_worktrees`, and a creation error is shown without adding a row.
  - verify: `TestTUIMultiSession_Scenario5_NewWorktreeSession`

### Scenario 6 — Delete a session from the list

**Acceptance:**
- AC6.1: **d** asks for confirmation; confirming deletes with `stop_active`, which stops a running session, and removes the row. Deleting the active session switches to the next row, or opens a new session when it was the last one.
  - verify: `TestTUIMultiSession_Scenario6_DeleteStopsAndRemovesRow`
- AC6.2: For a row whose `remove_worktree` capability is true, the confirmation also offers "delete and remove worktree", and says that ignored files in the worktree are deleted and the branch is kept. A refusal (for example, uncommitted changes) is shown and the session is kept; a post-stop `worktree_removed: false` is shown as "chat deleted, worktree kept" with the reason.
  - verify: `TestTUIMultiSession_Scenario6_DeleteOffersWorktreeRemoval`
- AC6.3: The user docs describe the window list, the `sessions` key action, new-worktree sessions, and deletion with worktree removal.
  - verify: inspection — prose completeness is reviewed by a human; `task docs` and `task site:build` check links

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Archive / hide sessions | Not planned | Human decision: delete only |
| Runs that survive the client stream closing | Later ADR | Runs stay tied to Converse; quitting cancels window sessions |
| Server-side unique titles | Not planned | Human decision: client labels add the handle on collision |
| Deleting the `mecatl/<name>` branch | Not planned | Branches are kept so committed work is never lost |
| Worktree creation for Kubernetes or microVM placements | Later | ADR 0374 admits local placement only |
| TypeScript SDK and `apps/` adoption of the new fields | Later | Fields are additive |
| A list-level push stream for session inventory | Later | The window list is fed by each session's own stream |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and approved commit and reports
   interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Message routing in the window root must re-tag commands returned by each session model,
  including `tea.Batch` and `tea.Sequence` results, so a message always returns to the
  model that requested it. Bubble Tea program commands (quit, clipboard, exec) pass through
  untagged.
- Side-effecting `Deps` hooks (terminal title, status line source, notifications) must be
  scoped per session so a background model cannot change what the active one displays.
- Each background session holds its own transcript and render state in memory.
- The managed worktree root and the per-repository create lock are new outlives-a-call
  resources and get rows in the [ADR 0027](../adr/0027-cloud-native.md) inventory.
- A process that dies between deleting the session and removing its worktree leaves the
  worktree on disk with no session. It is not swept; `git worktree list` still shows it.
- The sharing check for `remove_worktree` scans persisted sessions and schedules.
