# ADR 0015 — Background subagents and per-child cancel

- Status: Accepted
- Date: 2026
- Scope: the child-run registry, background Subagent execution mode, per-child cancel, run-end drain/seal, and completion delivery via notice injection and SubagentStatus

## Context

The two remaining delegation gaps after teams shipped were: (1) per-child cancel — there was no way to cancel a single in-flight child without cancelling the whole run; and (2) background/async delegation — a Subagent call blocked the parent loop for its entire duration, which is the opposite of the parallelism the field converged on (Claude Code `run_in_background`, opencode background tasks). These two features share approximately 60% of their machinery, making them natural co-design.

## Decision

A shared child-run registry on the parent `Run`, keyed by child session id, is the single anchor for per-child cancel functions, surfaced ask ids, background state, and drain/seal coordination. Background mode is a flag on the existing Subagent tool — not a new tool — that returns an immediate started-result and drives the child in a detached goroutine. Completion delivery uses a turn-boundary harness-note (ids only, no child-authored content) plus `SubagentStatus` as the sole body channel, satisfying the OWASP LLM01 role-elevation constraint. Background children are run-scoped in v1; true session-scoped detach is deferred.

## Consequences

Per-child cancel and background execution work across all three delegation families (Subagent, Parallel branch, team member) through a single code path. The "loop emits exactly N diagnostics lines" invariant is amended to three (adding the drain-abandon warn). Session-scoped background detach requires a durable per-session outbox and is explicitly named as v2 work. Current behaviour is described in `docs/architecture.md`; shipped/deferred state is tracked in [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

---

The whole arc is on `main`: **I1** (registry + Subagent cancel, `2d0bb4f`), **I2**
(parallel-branch + team-member cancel, `d6457ef`), **I3a** (background mechanics +
SubagentStatus + seal/drain, `3617285`), **I3b** (notice injection + background-pending
nudge + mecademo, `e249dae`), **I4** (TUI background surfaces + description pass + this
doc's promotion — the final iteration). This document is the design **as built**: the
post-review amendments are folded into the body where they changed it. Per-subsystem
implementation detail lives in [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md); companion docs:
`docs/adr/0014-agent-teams.md`.

The arc closed the two remaining Tier-4/Tier-5 delegation gaps as ONE co-designed feature
pair: per-child cancel (deferred precisely because ~60% of its machinery is the background
registry) and background/async delegation (the field's clearest direction: Claude Code
`run_in_background`/Ctrl+B, opencode background tasks + `task_status`, Cursor cloud
agents).

## Decisions table (as built)

| # | Decision | Chosen | Rejected alternatives | Rationale |
|---|---|---|---|---|
| D1 | Registry location | `childRunRegistry` on the parent `agent.Run`, created unconditionally in `RunContentWith`, mirroring `childAsks *childAskRouter`; exposed downward to tools via `parentCaps`, upward to the server via `Run.CancelChild` | (a) a Service-level registry in `internal/adapter/server` — wrong layer: child Runs/cancel funcs are agent-layer objects and the headless/in-process paths (mecademo, Team tool) need it too; (b) a package-global registry — breaks the one-Run-owns-its-children model and concurrent sessions would share state | The parent Run is already the routing anchor for the only existing parent↔child control channel (`Run.Approve` → `childAskRouter`). Cancel is the same shape: a client frame addressed at a child, routed by the parent Run. Layering stays clean (everything is `engine/agent`). |
| D2 | Child handle / id convention | The child **session id**, verbatim, for all three families: `subagent-<callID>`, `parallel-<callID>-<i>`, `team-<teamID>-<member>` (`MemberSessionID`) | (a) a new synthetic handle ("child-1") — a 4th id scheme to keep in sync; (b) family-specific addressing (`(team_id, member)`, branch index) on the wire — three wire shapes for one operation | The session id is ALREADY: the askID namespace (`newAskID` prefix — a documented consumed contract), the store key (`InspectSubagent`/`InspectMember`/`resume`), the `agentId:` trailer the model reads, and the overlay's `ChildID` focus key. Prefixes are already disjoint by convention. One handle, zero derivation. |
| D3 | Cancel wire shape | `ConverseRequest` oneof member `CancelChild cancel_child = 12;` (`message CancelChild { string child_id = 1; }`) + HTTP `POST /v1/sessions/{id}/cancel-child` | (a) overloading `Cancel` with an optional child_id — mutates the semantics of an existing frame old clients send; (b) a unary RPC for the Converse path — the run is stream-scoped; a unary would need the Service lookup the stream already has, and asymmetric with ResumeApproval | The proto reserves 1–9 for the start family, **10+ for the gate/cancellation family** (10=resume_approval, 11=cancel; 12 was free). CancelChild is exactly a gate/cancellation-family frame. |
| D4 | RunTeam-path teammate cancel | **Shipped (issue #29).** Deferred out of v1, then landed as predicted: the additive unary `CancelTeammate(team_id, member)` (+ HTTP `POST /v1/teams/{id}/members/cancel`) over the existing `Supervisor.CancelMember(name)` seam, gated on the team's `teamRunning` phase (`ErrTeamNotRunning` → FailedPrecondition; unknown member → the family-neutral `ErrChildNotFound`) | Shipping the unary in v1; a new unknown-member sentinel | The gRPC `RunTeam` server-stream has no client→server frames; a separate unary is required. mecatui (the only interactive client) drives teams through the in-loop Team tool on the Converse stream, where CancelChild already reaches members via parentCaps. The headless RunTeam consumer had only the whole-stream cancel (ctx) until the unary landed. |
| D5 | Team-member cancel semantics | **De-schedule**: cancel the member's in-flight drive (per-member ctx) AND mark `stopped=true, stopReason=StopReasonCancelled` via the existing stop machinery; `ReleaseTasks` fires; member appears in the synthesis digest as `[STOPPED: cancelled]` | Skip-turn (cancel only the current drive, reschedule next round) | Skip-turn is a half-state with no precedent in `memberRT` (a member is schedulable or stopped) and confusing UX (the user killed it; it comes back). The existing cancelled-member classification (`runTurn`'s `stop == StopCancelled` branch) already does everything de-schedule needs — reused, adding only the per-member ctx. A de-scheduled member's session persists and is inspectable; deliberate. |
| D6 | Subagent cancel terminal | Success-with-note: `[subagent cancelled by user]` + partial text + the `agentId:` trailer (resumable), distinguished from parent-cancel/timeout by the registry `clientCancelled` flag | Tool error | Mirrors the budget/limit notes in `renderSubagentResult`: partial work is usable and the trailer keeps the child resumable — cancel-then-resume-with-narrower-prompt is the workflow the field converged on. An error result would teach the model the delegation mechanism failed. |
| D7 | Background mechanism | `background: true` arg on Subagent; `run()` registers the child, `startBackground` spawns the drive goroutine and returns an immediate started-result (`backgroundStartedBody`, agentId trailer FIRST line) | (a) a separate `BackgroundSubagent` tool — splits the description/limits/resume/schema surface in two; (b) dispatcher-level async (return a future from dispatch) — rewrites the hot loop for one tool | The Subagent tool already owns the whole child lifecycle (gate, fork, drive, persist, render); background is a flag on WHERE the drain happens, not a new delegation kind. |
| D8 | Background lifetime scope | **Run-scoped with cancel-at-end (v1)**: outstanding background children are ctx-cancelled, persisted, and joined (bounded two-phase drain) before the run's terminal result is emitted. Session-scoped detach is named v2 with its real prerequisite (§5) | True detach in v1 | Two hard constraints make run-scope the only honest v1: (1) **the emit channel**: child observability events flow through the parent Run's `r.events`, which `RunContentWith`'s goroutine closes at run end — an event emitted after close is a send-on-closed-channel panic; (2) **delivery has nowhere to go**: event delivery is run-stream-coupled (Converse/SSE drain `run.Events()`); a child completing with no live run has no client, no outbox, no notification channel. Run-scope still delivers the value: the parent model regains control immediately and overlaps its own turns with child execution — the actual gap (the TURN used to block until every child returned). |
| D9 *(amended)* | Completion delivery to the model | **Notice + collect**: (a) a turn-boundary harness-note user message carrying ONLY ids + stop labels ("N background subagent(s) finished: … Call SubagentStatus with each agent_id…"); (b) `SubagentStatus` as the **SOLE body channel** — bodies return as tool results (the sanctioned role), rendered via `renderSubagentResult` exactly as foreground | Injecting the result BODY as a user message (the original design); injection-only; polling-only; tool_choice-forced collection | The security review killed body injection: child-authored text in a user-role message is role elevation of untrusted content (OWASP LLM01; a body could also forge separators). The notice rides the seam the no-progress nudge proved out (`RecordUserPrompt` at a pairing-safe boundary) and is idempotent metadata; the body has one channel and one role. Cost accepted: one extra model turn + tool call to collect. The registry's `noticed` flag is SEPARATE from `delivered` — a result is never re-noticed but remains collectible; an already-collected result is noticed silently and not listed. |
| D10 *(as built)* | Run-end with outstanding background children | One bounded **background-pending nudge** (once per run, `bgPendingNudged`): when the model produces a meaningful-text turn on a benign stop (the would-be clean `StopEndTurn` terminal in `finishTurnNoTools`) while background children are LIVE, inject "N background subagents still running ([ids only]); collect or cancel them, or finish and they will be cancelled" and grant one more turn. On the second clean end (or any non-clean terminal): cancel + drain + persist | (a) silently cancel (silent loss of delegated work — the same disease the no-progress fix cured); (b) block the terminal until children finish (unbounded delay of the user's result, invisible) | Bounded, visible, structurally identical to existing nudge machinery, EVENT-silent (no `EvNoProgress` — that taxonomy means "the model stalled"). **Placement is load-bearing**: it must hook `finishTurnNoTools` BEFORE the clean-terminal calls — by the time `drainChildren` runs (the top of terminate/terminateComplete) the children it would ask about are already cancelled and the registry sealed. Cancelled-at-end children persist with resumable ids, so the worst case loses no transcript. |
| D11 | Background child permission asks | **The 4-step model unchanged** — surface immediately when the parent is interactive; the parked child blocks only its own goroutine | (a) Claude-Code-style blanket auto-deny for background — CLAUDE.md explicitly forbids collapsing the 4-step model; (b) queue until the next turn boundary — a parked child + a stale queued ask is strictly worse UX than the modal mecatui already renders mid-turn | Mechanically free: the surfaced-ask path never assumed the parent loop was parked — team members already park individually while peers (and the parent) keep running. One nuance (found in review): background introduces ask-emit concurrent with parent assistant DELTAS (the existing asks happened mid-dispatch, parent loop blocked) — channel- and TUI-phase-safe, pinned by a dedicated mid-stream-ask test. The unwind (D13) covers cancellation while parked. |
| D12 | childGate interplay | One shared gate (the single fan-out brake stays single). Background acquisition is **fail-fast**: gate full → model-addressable error listing the running background ids (ids ONLY) + the recoverable actions (wait via `SubagentStatus wait_ms`, or cancel). Foreground acquisition keeps its blocking semantics; per-child cancel is the user's unblock affordance and `SubagentStatus` the model's | (a) a second background-only gate — two brakes whose sum nobody bounds; (b) background doesn't count — unbounded concurrent children, exactly what the gate exists to prevent; (c) fail-fast for foreground too — a behavior change to the existing fan-out contract for no gain | Background children hold slots across turns, so blocking a background START could deadlock the model against itself; failing fast with the ids teaches it to cancel or wait. The failing call's own pre-gate registry entry is removed BEFORE the ids are read, so the error never lists the call's own id. |
| D13 | Parked-ask unwind + retraction | Cancel ⇒ child ctx cancelled ⇒ `askRegistry.await` returns `ok=false` (existing path: discard + deny-cancelled + `StopCancelled`). Parent side: the registry tracks surfaced askIDs per child (recorded at `surfaceAsk` time via the `childPosture.childID` field — set explicitly at all three construction sites, because the team/parallel postures natively held only a member name / branch label); on cancel each is removed from `childAskRouter` (`unregister`) and a **`permission.retract`** event (string-passthrough type, payload = the existing `ask` field carrying only the AskID) is emitted so the client dismisses the modal | (a) no retraction (dangling modal; an answer hits the idempotent-no-op router — "works" but the user approves into the void); (b) a new proto payload message — unnecessary, `PermissionAsk{ask_id}` already carries exactly what retraction needs | Unregister-BEFORE-retract is fail-safe against racing approvals (a late approval is an unknown-ask no-op). mecatui keeps a FIFO ask queue behind the visible `m.ask` head — retraction dismisses the visible match (advancing the queue), removes a queued match in place, and ignores unknown ids. |
| D14 | Events/wire for background | No new family. `subagent.start` gains `bool background = 10` on the proto `Subagent` payload; `subagent.end` fires from the (still-inside-the-run) drain as today. `permission.retract` and the new stop notes are string passthroughs | A `background.*` family | The ChildActivity trip-wire: a 4th delegation FAMILY is the extraction trigger. Background is not a family — it is a delivery mode of `subagent.*` (same flat-fleet aggregation, same redaction chokepoint). One additive proto field + one regen. |
| D15 *(amended)* | Status tool | Read-only `SubagentStatus` (receives the registry via `parentCaps`): no args → roster of THIS run's children (id, family, background?, running/done, stop — ids + enum labels only); `agent_id` → that child's state + (if a finished background child) its rendered result body, delivered exactly once; **`wait_ms`** (capped 120s) parks on the target's `doneCh` — or, with no id, on the registry's terminal generation — before reporting | Folding into `InspectSubagent`; no wait affordance | `InspectSubagent` reads PERSISTED transcripts of finished children through the store (cross-run, bounded rendering); `SubagentStatus` reads LIVE registry state of this run (cheap, no store). Different data source, freshness, and cost. `wait_ms` (added in review) untangles D8/D10/D12: the model can delegate-then-wait without burning turns, D10's "collect or cancel" is satisfiable for a running child, and D12's fail-fast error gains a recoverable action. The tool blocks in its own read-parallel dispatch goroutine — exactly like a foreground Subagent — and holds a dispatch slot while parked (documented in its description). |
| D16 *(amended)* | Parallel/Team handle discoverability on the wire | Additive proto fields so the client never derives ids: `Parallel.child_id = 18` (set on `branch_start`/`branch_end`), `Team.member_session_id = **18**` (set on member-tagged events; 17 was already taken by `Team.dispositions` — a review catch). **Issue #30**: `child_id` is the WIRE half (for the client/overlay); the `branch id:` line in the Parallel **result text** is the MODEL half — the parent LLM reads it to pull a branch transcript via `InspectSubagent`, the same id, no derivation. | Client-side derivation (`parallel-<callID>-<i>`, `team-<id>-<member>`) | The repo's explicit discipline: "the agent_id IS the session id — no derivation contract." mecatui imports no `internal/` package; teaching it the id grammar creates the drift `MemberSessionID` exists to prevent. |

## 1. The child-run registry (shared foundation)

### 1.1 Where it lives and what it is

`engine/agent/childregistry.go`, owned by the parent `Run` exactly as `childAsks` is —
but created **unconditionally** (cancel arrives over the wire only on interactive
surfaces, but background bookkeeping — D8's run-end drain — must work on headless runs
too; the registry is a mutex + map, negligible).

One flat map of `childEntry` keyed by the **child session id** (D2): family
(`childSubagent | childParallelBranch | childTeamMember`), a clamped goal label, the
per-CHILD `context.CancelFunc`, `background`, `clientCancelled`, the surfaced `askIDs`
set, state (`queued | running | done`), the terminal stop, the stored rendered result +
the `delivered` and `noticed` flags (background only), and a `doneCh` closed at the
terminal. The three families' prefixes are disjoint by the existing convention, which the
registry inherits rather than re-engineers. Nothing changed in id GENERATION —
`childSessionID`, `ParallelTool.childSessionID`, `MemberSessionID` remain the single
sources; the registry only consumes their outputs.

**Edge-case vocabulary (hardened in review):**
- Re-registration of a resumed id within one run **overwrites** the done entry (fresh
  `doneCh` — never a double-close), and a **failed resume attempt reinstates** the
  displaced prior done entry + its undelivered result (`childEntry.displaced`).
- A pre-start abort (fork/resume-load/session-build failure, the background gate-full
  path) **removes** the registry entry — no phantom queued entries.
- A cancel-while-queued keeps a meaningful `StopCancelled` entry; a never-driven team
  member is removed at `cleanupAll`.
- `EvSubagentStart` for a background child is emitted **synchronously** before the
  goroutine spawns (deterministic start-before-started-result ordering on the stream).

### 1.2 Registration lifecycle

Registration happens where the per-child cancelable context is minted — **before**
gate/slot acquisition, so a child queued on the `childGate` is already cancellable
(cancel-while-queued unblocks the slot wait via its ctx select):

- **Subagent** (`SubagentTool.run`): after the in-flight guard acquires the child id,
  wrap `ctx` with a per-CALL `context.WithCancel` (not per-drive — a structured-output
  re-drive sequence is cancelled as a whole) and register. `markDone` on every terminal
  (+ `releaseChildID` as before).
- **Parallel branch** (`launchBranch`): per-branch `context.WithCancel` around
  fork+drive — ALL join modes (previously only join=first had a shared cancelable ctx) —
  registered before the worker-semaphore wait.
- **Team member**: `AddMember` mints a **DETACHED** per-member ctx
  (`context.WithCancel(context.Background())`, NOT the enrolment ctx — on the gRPC path
  `AddMember` runs under the CreateTeam REQUEST ctx, which dies before `RunTeam`;
  deriving from it would insta-cancel every member) and registers it under
  `MemberSessionID` via `s.caps` (nil-safe: the RunTeam path registers nothing — D4's
  whole-stream cancel covers it). `driveOneTurn` MERGES the member ctx into each drive's
  ctx via `context.AfterFunc`; `planRound` checks `m.ctx.Err()` up front so an
  idle-between-rounds member is cancellable too.

Entries are never removed during the run (beyond §1.1's pre-start aborts) — `done`
entries are what `SubagentStatus` and the notice read. The whole map dies with the Run.
Threading to tools: `parentCaps` gains `registerChild`/`markDone`/a read handle — the
same layering-clean closure shape as `surfaceAsk`.

### 1.3 Cancel routing (the Run.Approve mirror)

`Run.CancelChild(childID string) bool` — idempotent; unknown or already-done ids return
false. Internally: set `clientCancelled`, snapshot+clear `askIDs`, call `cancel()`
(outside the lock), then for each owned askID `childAsks.unregister(askID)` and emit
`permission.retract` on the parent stream. Server side mirrors `Approve` exactly:
`HarnessServer.readControl` handles the `ConverseRequest_CancelChild` frame directly on
the held `run`; `Service.CancelChild` backs `POST /v1/sessions/{id}/cancel-child`
(LookupRun → run.CancelChild; false → not-found; no run → noActiveRun). No ack frame —
the observable outcome is the child's terminal event. Races are tolerated by
construction: a verdict routed after cancel hits idempotent no-ops; a CancelChild racing
the child's natural terminal is a no-op on `done`.

### 1.4 Why this serves BOTH features without redesign

Cancel consumes: `cancel`, `clientCancelled`, `askIDs`. Background consumes:
`state/result/delivered/noticed/doneCh`, the seal, and the same registration seam.
Per-child cancel of a background child is the SAME code path as cancel of a foreground
child — the difference is solely who drains the run (the dispatcher goroutine vs. a
detached one).

## 2. Background Subagent semantics

### 2.1 The model (D7/D8)

`subagentArgs.Background bool`. Foreground (default) is byte-identical to before.
Background: `startBackground` does a fail-fast gate acquisition (D12), a fail-fast
resume load, the synchronous `EvSubagentStart{Background:true}`, then spawns
`driveBackground` — the goroutine owning fork → drive → persist → `markDoneResult`
(rendered result + family stop note) — and returns the immediate started-result. Every
pre-spawn failure unwinds completely (registry entry removed, gate slot + in-flight id
released) and returns inline.

Composability: `background` composes with `resume`, `output_schema` (the retry loop runs
inside the goroutine), all tighten-only limits, `timeout_ms` (deadline-vs-cancel
disambiguation rides the timeout ctx), `agent`/`model`. The in-flight guard rejects
resuming a still-running background child.

### 2.2 Completion delivery (D9 as amended — notice + collect)

**(a) Turn-boundary NOTICE** (`injectBackgroundNotice`, Step 2a of `Engine.drive`,
BEFORE `preTurnTerminal` — provider-legal because history at that boundary always ends
on the user prompt / tool results / a nudge message): the registry's
`noticeFinishedBackground` returns done ∧ background ∧ ¬noticed children (flipped to
`noticed` under the lock, id-sorted); the loop records ONE harness-framed user message
(`backgroundNoticeText`) carrying ids + stop labels ONLY — nothing child-authored, no
goal labels — telling the model to collect via `SubagentStatus`. `e.save` runs right
after the record (durable across resume/replay). The injection emits NO event, consumes
no no-progress nudge, and does not itself consume a turn. An already-collected result is
noticed silently and not listed.

**(b) `SubagentStatus`** (D15) is the sole BODY channel: collect marks `delivered`
(exactly once); the body is re-keyed to the collecting call's id with the error bit
preserved, so a failed background child surfaces as a real error tool result. Wording is
family-aware (a done team-member/parallel-branch id points at its family's own delivery
channel, never at "its own Subagent call").

Why not a late ToolResult? The tool_use/tool_result pairing for the original call was
closed by the immediate started-result; providers reject a second result for the same
call id. The user-message frame is the only provider-valid late channel — and after the
security amendment it carries harness metadata only.

### 2.3 Lifecycle bounds (D8/D10) — the honest hard part

What `Engine.Run`'s exit would otherwise do: `RunContentWith`'s goroutine runs
`defer close(r.events); defer cancel()`, so every child ctx is cancelled at run end —
children don't leak indefinitely, they die messily — and any child event emitted after
the close **panics**. Orphaned children are an immediate correctness bug, not a slow
leak; the scope is a constraint, not a preference.

The v1 contract, enforced by `drainChildren` at the TOP of both terminate paths (so
child run-end events precede the terminal `EvResult` on the stream):

1. **Clean-end nudge** (D10, as built): lives in `finishTurnNoTools`' real-clean-end
   branch (NOT in terminate — see D10's placement note), once per run.
2. **Two-phase drain**: cancel every live background entry, join each `doneCh` up to
   `childDrainCap` (10s); on overrun, `abortEmits()` (release any child parked in a
   blocked emit — consumer backpressure) and re-join for `childDrainGrace` (1s). Only a
   child that STILL hasn't joined is abandoned, with ONE operator WARN (ids only) — the
   consciously-amended THIRD diagnostics line. An abandoned child's residual emits are
   safe no-ops (seal) and its persist still runs (stores ignore ctx on Save — documented
   residual).
3. **Seal**: `registry.sealed` is set before the goroutine closes `r.events`. ALL child
   emits route through `registry.safeEmit` — the sealed-check and the channel send are
   ONE locked section (a TOCTOU here was a reviewed-out panic), backed by
   `Run.emitOrAbort` (a blocking send that gives up when the seal's abort channel
   closes; the abort channel closes BEFORE the emit lock is taken, or seal would
   deadlock — pinned by `TestSealUnblocksEmitParkedSend`).
4. **Hard abort (the cancel unwedge)**: the seal escape above is reachable only from
   the terminate paths — which are themselves BLOCKED when the consumer stops
   draining mid-run (the loop parked in its own emit, the team forwarder parked in
   `safeEmit`, members parked at the supervisor's evCh send: nothing can reach
   `drainChildren`). `Run.Cancel` therefore arms `Run.hardAbort` (sticky, via a
   `sync.Once` + a `hardAbortGrace`=1s timer, armed BEFORE cancelling the ctx),
   and EVERY guarded send on the run — `emit`, `emitOrAbort`, the supervisor's
   member→evCh forward (threaded down as `parentCaps.hardAbort`; nil without
   parent caps = blocks forever = correct no-abort) — selects on it. The member
   forward ALSO selects on `driveCtx.Done()`, and that arm is LOAD-BEARING for
   member liveness, never redundant defense: the member run's OWN hardAbort is
   never armed (per-member cancel is `m.ctx`, nobody calls `Run.Cancel` on a
   member run), so on the no-parent-caps path (supervisor-direct / gRPC RunTeam)
   driveCtx is the ONLY thing that can unpark a member wedged at the evCh send —
   do not remove it (pinned by `TestCancelMemberUnparksEvChSend`). The
   "emitOrAbort selects only on emitAbort, NOT ctx" rationale is PRESERVED, not
   amended: hardAbort is an explicit signal, not ctx, and the
   cancelled-but-drained delivery contract holds TWO ways — the try-send-first
   shape (non-blocking attempt, then the guarded select) delivers
   deterministically while the buffer has room (a bare two-arm select picks
   RANDOMLY against a closed channel and flakily drops post-cancel events), and
   the GRACE lets a merely-backlogged consumer (full buffer, still draining)
   absorb the post-cancel tail — terminal EvResult included — before parked
   sends give up; once fired the closed channel makes every later would-park
   send drop instantly (the unwedge cost is per-run, never per-event). Pinned by
   `TestCancelUnwedgesStalledTeamRun` (wedge → Cancel → session lands cancelled,
   Interrupt-recoverable), `TestCancelAbortNoChildLeakAfterSeal` (undelivered +
   sticky, not one-shot), and `TestEmitDeliversWithBufferRoomAfterHardAbort`
   (the try-send-first delivery half — the executable form of the preserved
   rationale). The relay
   half: both relays (gRPC `Converse`, HTTP SSE) drain-to-discard after the FIRST
   Send/Write error — record the error, `run.Cancel()`, keep ranging
   `run.Events()` discarding until close, then return the error — so a busy run
   never wedges in its own emits behind a dead client
   (`TestRelaySendErrorDrainsBusyRun` / `TestSSEWriteErrorDrainsBusyRun`).

A goroutine is owned by the Run (spawned under its ctx tree, joined at its end).
Session close / server shutdown need nothing new in v1: a run-scoped child cannot
outlive its run; runs cannot outlive Converse streams; stream teardown already cancels
the run. Hardening found in review: ctx-cancel kills the Bash process immediately, but
`cmd.Wait` can block on pipes inherited by grandchildren — the osfs runner now sets
`cmd.WaitDelay`, making the drain cap a backstop rather than the real bound.

### 2.4 Permission asks from a background child (D11)

Unchanged 4-step resolution; a background child's surfaced ask behaves like a team
member's (parks its own goroutine only). The run-end drain cancelling a parked child
unwinds it through the existing `await` ctx path, and the retraction rides the
child-terminal CHOKEPOINT (a post-arc fix — the drain path originally emitted no
retract; only `Run.CancelChild` did): every child that ran lands exactly one registry
terminal (`markDoneResult`, deferred by all spawning tools), and by then an unanswered
surfaced ask is dead by definition — so the terminal takes (snapshot-and-clears) the
child's still-pending askIDs and emits a `permission.retract` for each, gated on
`childAskRouter.unregister`'s answered-vs-pending bool (route() already deleted an
answered ask's entry → no spurious retract). The retract precedes the doneCh close ⇒
precedes the drain's join ⇒ precedes the seal AND the terminal `EvResult` — so the
client's modal clears on EVERY ctx-driven unwind, not just an explicit CancelChild:
run-end drain, per-call `timeout_ms`, a parallel join=first loser, whole-run cancel,
team teardown. A genuinely ABANDONED child (never reaches its terminal) gets the same
sweep from `drainChildren` itself, pre-seal. The notice may land while an ask is
pending — fine, asks are out-of-band of history.

### 2.5 Interplay matrix

- **childGate**: D12 — shared gate, background fail-fast (ids only), foreground blocking
  unchanged (now user-unblockable via cancel).
- **MaxRunTokens**: inherited as for every child; per-call `max_tokens` tighten-only
  unchanged. Honest note: a background child's spend does not count toward the PARENT's
  `MaxRunTokens` — identical to foreground today; documented, not changed.
- **persistChild**: runs in the goroutine on every terminal — a run-end-cancelled
  background child is persisted, resumable next run (the loss-mitigation that makes
  cancel-at-end acceptable). Persisted child snapshots no longer accumulate forever:
  the composition-layer child-session retention GC (issue #38,
  `internal/app/childgc.go` — age + per-family-cap sweep over the optional
  `port.PrunableStore` seam, `--child-retention`/`--child-retention-max-per-family`/
  `--child-gc-interval`, defaults 168h/500/1h) deletes old `subagent-`/`parallel-`/
  `team-` snapshots; main sessions and in-flight runs are never touched. A child
  swept past retention is simply no longer resumable/inspectable — the same
  affordance loss as an operator deleting the store dir.
- **In-flight guard**: a background child holds its id until `markDone`, so `resume` of
  a running background child is rejected with the existing "already running" error.
- **Resume**: a completed/cancelled background child resumes like any persisted child;
  `resume`+`background` together is legal.
- **Structured output**: the retry loop runs inside the goroutine; `driveChild`
  unchanged.
- **Compaction**: no interaction — compaction rewrites the PARENT conversation; the
  child owns its session. The notice is ordinary recorded history and flows through
  `snapCutToTurnBoundary`/`ValidateToolPairing` like any user message.

### 2.6 Events / UX / wire (D14 + I4)

- `subagent.start` carries `background: true` (proto `Subagent.background = 10`);
  `subagent.tool`/`subagent.end` unchanged — still through `drainChildObserved`.
- `permission.retract`: string EventType, payload = the existing `Event.ask` field
  carrying `{ask_id}` only (server-authored; no spoofing surface).
- mecatui (I4): the client decodes `Background` (proto field 10 → `SubagentMsg`); the
  fleet lane carries a **`⇢ bg`** marker (roster row + focus header); a background
  child's `subagent.end` raises a transient footer notice ("background subagent #hash
  done — result ready for the agent" — the team-done/no-progress advisory channel,
  never scrollback; foreground ends stay silent); the focus pane adds an honest
  delivery line (*running detached* vs *done — result ready for the agent
  (SubagentStatus)*) and never claims a collected/uncollected state — the registry's
  `delivered` flag is deliberately NOT on the wire. The fleet footer counts include
  cross-turn background children until their end arrives.

## 3. Per-child cancel

### 3.1 Wire and routing

Proto (D3): `CancelChild cancel_child = 12`; HTTP `POST /v1/sessions/{id}/cancel-child`.
`readControl` case → `run.CancelChild`; `Service.CancelChild` for HTTP. `false`
(unknown/done) → HTTP not-found; on the stream, ignored-by-design (the TUI shows the key
only for non-terminal lanes; the finished-as-you-pressed race is benign). RunTeam path:
landed (D4, issue #29) — the `CancelTeammate(team_id, member)` unary + HTTP
`POST /v1/teams/{id}/members/cancel` call `Supervisor.CancelMember` directly (no parent
registry on that path), behind a `teamRunning` phase gate.

### 3.2 Per-family terminal semantics

- **Subagent** (D6): per-call ctx cancels mid-drive (loop → `StopCancelled`) or
  mid-gate-wait. `renderSubagentResult` has an explicit `StopCancelled` +
  `clientCancelled` arm: success-with-note `[subagent cancelled by user]` + partial text
  + trailer. The `timeoutCtx` deadline check stays first. Parent-run cancellation (esc)
  is untouched: every child dies as before, `clientCancelled` false everywhere.
- **Team member** (D5): de-schedule. Mid-drive: the existing `runTurn` `StopCancelled`
  branch classifies, marks stopped, releases tasks — zero new stop-path code (and stays
  ordered BEFORE the reopenErr fold; don't disturb `warnUnexpectedReopen`'s
  suppression). Idle-between-rounds: `planRound`'s up-front ctx check → stopped +
  `StopReasonCancelled` + `SetMemberState(MemberStopped)` + `ReleaseTasks` + registry
  markDone. A supervisor-stopped member's entry is marked done WITHOUT cancelling its
  ctx (a budget-stopped lead must stay drivable for the one synthesis turn).
- **Parallel branch**: join-mode semantics fall out of `failed=true` with zero new
  join-path code: `all` — the existing `StopCancelled` arm, failReason "cancelled by
  user" when client-cancelled (vs the pre-existing "cancelled" for a parent cancel);
  `first` — a cancelled branch can never win (the winner test is `!failed`); `judge` —
  excluded from candidates; cancelling the only success degrades to the existing
  all-failed report. A cancelled judge-winner candidate is impossible by construction
  (judging runs only after all branches are terminal; cancel on `done` is a no-op). The
  judge's own run is NOT a registered child and is UNINSPECTABLE — INTENTIONAL by design
  (issue #30 resolved), not a deferred limitation: the judge is a verdict function over
  branch summaries with no persisted session, so there is no transcript to register,
  cancel, or inspect (whole-run cancel covers it). Every mode: normal loser-cleanup;
  bracketing `branch_start`/`branch_end` fire even for cancelled-before-start branches.
  (Each branch — winner and loser — IS persisted via `WithParallelStore` and inspectable
  by its `branch id:` line through `InspectSubagent`'s family-aware gate, issue #30.)

### 3.3 The parked-ask unwind + retraction (D13)

On `CancelChild` of a child parked in `askRegistry.await`: mark `clientCancelled`,
snapshot+clear `entry.askIDs`; `cancel()` → `await` returns `ok=false` → discard →
deny-cancelled → child loop `StopCancelled` → family terminal per §3.2 (no parked
goroutine; gate slot + worktree release via existing defers); then per snapshotted
askID: `childAsks.unregister` (a late ResumeApproval dies as an unknown-ask no-op —
documented stale-verdict behavior), then emit `permission.retract{ask_id}` — both via
the shared `childRunRegistry.retractAsks`, the SAME unregister-then-emit loop the
child-terminal chokepoint uses (§2.4). mecatui
keeps a FIFO ask queue behind the visible modal: a retract matching the VISIBLE
ask dismisses it and advances the queue, a queued match is removed in place, an
unknown/stale id is ignored. Ownership is recorded at the single surfacing seam (`surfaceAsk`)
via the explicit `childPosture.childID` field; verdict-routing leaves the askID set
slightly stale, but the set is now CONSUMED at the child's registry terminal
(`markDoneResult` → `takeAsks`), with each id gated on `unregister`'s
answered-vs-pending bool — a stale already-answered id fails the gate and emits
nothing, so a never-answered ask is retracted on every unwind path (drain / timeout /
parallel loser / whole-run cancel / team teardown) and its router entry no longer
leaks until Run GC. Scoped out, deliberately: (a) a budget-stopped team LEAD's
synthesis-turn ask — `recordAsk` ignores done entries (the lead is marked done before
its one synthesis drive), so such an ask is never registry-owned; pre-existing, rare,
bounded by the synthesis turn itself; (b) the parent run's OWN pending ask at run
end — clients already treat `EvResult` as end-of-asks.

### 3.4 TUI affordance

The ctrl+a overlay's `x` (cancel) key on roster rows / focus panes sends the lane's
`ChildID` — present for subagent lanes; supplied for parallel/team lanes by D16's wire
fields. Key shown only for non-terminal lanes. Confirm-less single keypress
(recoverable: persisted + resumable), matching esc's confirm-less whole-run cancel.

## 4. Phasing (all shipped)

Each iteration ended `task lint && task test` green, mecademo printing its full offline
session, all 10 gauntlet tests passing. ALL proto fields landed in I1's single regen
(`cancel_child=12`, `Subagent.background=10`, `Parallel.child_id=18`,
`Team.member_session_id=18`) — dormant until their iteration wired them.

- **I1** (`2d0bb4f`) — registry + per-child cancel for Subagent: childregistry.go,
  `Run.children`/`Run.CancelChild`, parentCaps extensions, per-call ctx + registration +
  `clientCancelled` terminal note, `childAskRouter.unregister`, `permission.retract`,
  `surfaceAsk` handle threading; proto regen; readControl case; `Service.CancelChild`;
  HTTP `/cancel-child`; TUI subagent-lane `x` + retract-dismisses-modal.
- **I2** (`d6457ef`) — cancel for parallel branches + team members (Converse path):
  per-branch ctx, detached per-member ctx + `CancelMember` + `planRound` check, the D16
  wire fields mapped, TUI `x` on parallel/team lanes. The one deferral —
  the RunTeam-path `CancelTeammate` unary — has since landed (issue #29).
- **I3a** (`3617285`) — background mechanics: `background` arg, `startBackground`/
  `driveBackground`, immediate started-result, `SubagentStatus` (roster/collect +
  `wait_ms`), seal generalised to ALL child emits (`safeEmit` over `Run.emitOrAbort`),
  run-end cancel + two-phase drain + abandon WARN, gate fail-fast, the §1.1 edge-case
  vocabulary, osfs `cmd.WaitDelay`.
- **I3b** (`e249dae`) — completion delivery: the notice injection (Step 2a), the
  background-pending nudge (D10 as built), `noticed` vs `delivered`, mecademo's
  `RunBackgroundScenario` (offline proof: start → started-result → wait → injected
  notice → collection).
- **I4** (final) — TUI background surfaces (client `Background` decode, `⇢ bg` lane
  marker, completion transient notice, focus-pane honesty note, footer-count
  verification), the final Subagent/SubagentStatus description pass, and the docs
  finale (this promotion + architecture/tui/usage/IMPLEMENTATION-NOTES updates).

## 5. Invariant audit

- **read-parallel / mutate-serial**: untouched. Background changes when a ToolResult
  RETURNS, not dispatch ordering; a detached child introduces no new shared-base writer
  (its workspace is its fork for its whole life). The notice is loop-side, not
  dispatch-side.
- **"loop emits exactly TWO lines"**: consciously AMENDED to THREE — the drain-abandon
  WARN (rare, ids only). Cancel/retraction/background events are EVENTS, never
  diagnostics (the child-terminal retract and the drain's abandoned-ask sweep emit no
  diagnostics line). CLAUDE.md carries the carve-out.
- **No-progress nudge vs the background machinery**: the notice precedes `BeginTurn`;
  `finishTurnNoTools`' masking-guard ordering is untouched; the background-pending nudge
  is a SEPARATE bounded counter that never relabels a stop reason and fires only on the
  real-clean-end branch (the no-progress machinery owns the empty turn first; even the
  `StopNoProgress` give-up is not background-nudged).
- **Gauntlet #7**: the notice carries ids + stop labels only; the body returns as a tool
  RESULT through `renderSubagentResult` — the sanctioned channel; `subagent.*` stays
  metadata-only via `drainChildObserved`; `permission.retract` carries an ask_id only
  (on the child-terminal/drain paths exactly as on CancelChild's).
- **ChildActivity trip-wire**: background is NOT a 4th family — one boolean on
  `subagent.*`. The trip-wire stands.
- **"Every run-entry path must reopen-if-completed"**: cancel-then-resume rides
  `resolveResumeSession`'s cancelled→Interrupt recovery — per-child cancel just makes it
  common.
- **4-step child ask model**: preserved verbatim; the unwind/retraction is a new exit,
  not a changed resolution order — and the child-terminal retraction chokepoint only
  widens WHICH unwinds retract, never how an ask resolves.
- **childGate single fan-out brake**: ONE gate with a documented acquisition-mode split.
- **port.LLMRequest neutrality / layering**: untouched; all knobs are tool args,
  Run-scoped state, or proto wire fields; `parentCaps` remains closures+values; mecatui
  imports no `internal/`.
- **Cancel always unwedges (the hardAbort amendment, §2.3 step 4)**: `Run.Cancel` is
  no longer a bare ctx cancel — it first arms the sticky `Run.hardAbort` (a
  `hardAbortGrace` timer), the explicit "stop blocking anywhere" signal every
  guarded send selects on (`emit`, `emitOrAbort`, the supervisor's member forward
  via `parentCaps.hardAbort`), so a run whose consumer stopped draining still
  terminates within the grace (session lands CANCELLED, Interrupt-recoverable —
  identical to a healthy esc-cancel; the normal terminate path still runs after
  the unwind, drainChildren → seal both idempotent). The "emitOrAbort selects on
  emitAbort, NOT ctx" rationale is explicitly PRESERVED: hardAbort is a distinct
  signal, and the try-send-first shape + the grace keep cancelled-but-drained
  delivery (terminal EvResult included) intact even for a backlogged consumer. Layering: hardAbort is an agent-internal
  channel + a `parentCaps` field — nothing from adapters/proto crosses into agent;
  the relay drain-to-discard edits are adapter-side only. No new diagnostics line
  (the loop still emits exactly THREE).

## 6. v2 — session-scoped background (future work, named not designed)

The run-scope line IS the v1 design (see §2.3's constraints — the temptation to let
children outlive the run "since the registry is right there" produces silent result loss
and emit panics without an outbox layer). True detach requires: `context.WithoutCancel`
detachment, a Service-owned child supervisor, a **durable per-session completion
outbox**, re-attach semantics, and shutdown draining — a real architectural addition,
not a flag. The registry, the single-handle convention, `SubagentStatus`, and the notice
seam all carry forward unchanged — only cancel-scope and event routing change. Also
still open: gate starvation under field use (a reserved foreground slot is the named
mitigation if models wedge) and cumulative child-token accounting toward the parent
budget. The RunTeam-path `CancelTeammate` unary (D4) is done (issue #29).


---

*Part of the [design docs](../design/README.md). Related: [Agent definitions (Tier 1)](0013-agent-definitions.md), [Spike: Headless Agent Teams for mecatl](0014-agent-teams.md), [Cloud-native arc: disposable process, externalized state, durable record](0027-cloud-native.md).*
