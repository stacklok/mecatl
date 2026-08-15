# ADR 0027 — Cloud-native arc: disposable process, externalized state, durable record

- Status: Accepted
- Date: 2026
- Scope: process disposability — snapshot fidelity, awaiting-approval evict/rehydrate, durable event log, multi-replica readiness (session leasing, SHIPPED)

## Context

The harness already had turn-boundary persistence and a stateless full-replay LLM provider, making it unusually close to disposable by construction. The remaining gaps were: the snapshot was not fully faithful (profile, provider/model selector, and cumulative token usage were not persisted); a process death while a run was parked awaiting approval stranded the session; and the event stream (approvals, pre-compaction history) was emitted and discarded rather than durably recorded. Without these, a restarted process could lose the user's permission grants, their budget progress, and the audit record.

## Decision

Deliver the arc in four phases: Phase 1 adds three missing snapshot fields (profile, provider/model selector, cumulative usage) and generalizes the rehydration seam; Phase 2 adds a resume-from-awaiting loop entry so a post-restart `Approve` re-enters the loop at the exact pending ask; Phase 3 adds a durable append-only event log (port, local JSONL adapter, gRPC driver service) with two consumers (compaction archive, permstore verdict replay); Phase 4 adds cross-process single-writer enforcement via session leasing (a new `port.SessionLease` seam with in-memory/flock/gRPC-driver/k8s adapters, acquired at the run-entry funnel and renewed by a Service-owned goroutine). The loop stays storage-agnostic throughout — it only emits events, never imports the log or lease port; the lease renewer and held-lease registry live on the server `Service`/composition, the same discipline as the event log.

## Consequences

Phases 0–4 are shipped; the harness is now genuinely disposable across process restarts AND safe under a multi-replica deployment that wires a session lease. Teams are the largest honest gap: mid-round team coordination state does not survive restart (row 10 of the fidelity ledger). Phase 4 makes single-writer enforcement CODE-ENFORCED when a lease backend is wired (decision (c) update below); without a lease backend the unchanged v1 constraint — session-affinity routing, one writer per session — still applies and is the byte-identical default. Current behaviour is in `docs/architecture.md`; shipped and deferred items are in `docs/design/PRODUCTION-READINESS.md`.

---

Builds on the shipped driver-seams arc (`DRIVERS.md`) and the no-FS
session profile (issue #55, commit `9f8ba8c`); informed by the cloud-native kit
inventory (PR #54), whose subsystem #6 advice ("don't design resource lifetimes,
inventory them") this doc executes, and whose session-state gaps (§1.1 to §1.4) the
later phases close. Every file:line below was verified against main at the time of
writing; line numbers drift, symbols don't.

## What the arc is

Make the harness process genuinely disposable: kill it at any moment, start another
one over the same stores, and lose nothing the user cares about. That decomposes into
three properties:

1. **Externalized state**: nothing load-bearing lives only in process memory.
2. **Evict/rehydrate**: a session parked mid-turn (awaiting a human approval) can be
   resumed by a different process than the one that parked it.
3. **Durable record**: the rich history (events, approvals, pre-compaction turns) is
   persisted, not emitted-and-discarded.

This is the Tier-2 cloud arc that `DRIVERS.md` recorded as deferred (ask
externalization, outbox, leasing), now planned as concrete phases.

## What is already true

The harness is unusually close by construction:

- **Turn-boundary persistence.** The session aggregate round-trips through a stable
  snapshot (`engine/adapter/sessnap/sessnap.go:32-43`) saved at every turn boundary.
  Restore drives the state machine through its public transitions, including
  re-raising the pending ask via `PauseForApproval` for an awaiting session
  (`sessnap.go:113-167`). Kill the process between turns and a reload reconstructs
  the conversation exactly.
- **Stateless full-replay provider.** The LLM adapters keep no server-side
  conversation state (`store:false`, byte-stable prompt prefix); resume is "load the
  snapshot and replay". There is nothing provider-side to externalize.
- **The driver protocol.** Six ports already cross process boundaries over gRPC
  (`contracts/proto/mecatl/driver/v1`): sessions, memory, skills, soul, agent defs,
  slash commands, with conformance suites as the contract (`DRIVERS.md`). The session
  snapshot crosses as an opaque format-tagged blob
  (`grpcdriver.SnapshotFormat = "sessnap-json/1"`,
  `internal/adapter/grpcdriver/sessionstore.go:30`).
- **The no-FS profile and its rehydration seam** (commit `9f8ba8c`, issue #55). A
  session can run with no filesystem at all (`engine/adapter/nofs`), and the first
  run-entry rehydration seam exists: `Service.rehydrateNoFSSession` (since generalized
  to `Service.rehydrateSession` in Phase 1)
  (`internal/adapter/server/service.go`) rebuilds a restarted no-fs session's
  per-session engine through the same factory create used, double-defended by the
  empty-root chokepoint in the osfs workspace factory
  (`internal/app/build.go:3815`). That is the evict/rehydrate mechanism in miniature,
  verified e2e across two Builds over a shared store. Details in
  `IMPLEMENTATION-NOTES.md` ("Session profiles").

What is NOT yet true: compaction destructively rewrites the only durable record;
and two processes over one store have no writer exclusion. (The per-session facts,
namely the provider selector, token usage, and profile, ARE now in the snapshot as
of Phase 1; a process death while a run is awaiting approval no longer strands the
session as of Phase 2, which re-enters the loop AT the ask; and the event stream is
now durably recorded at the relay as of Phase 3a, the durable-log FOUNDATION,
including the chronological approval record via `EvApproval`. See the ledger.) Those
gaps are exactly the phases below.

## Phase plan

Each phase is independently shippable, CI-green, and gated on a falsifiable
demonstration; the live e2e suite (`e2e/`) is the verification lane for the
cross-process gates.

### Phase 0: inventory + decisions (this doc)

Three lists: the resource inventory, the rehydrate-fidelity ledger, the recorded
decisions. Gate: the doc exists and the ledger has an explicit decision per row. No
behavior change.

**Per-phase re-audit (every phase, not just Phase 0).** Each phase below re-confirms
this Phase 0 inventory as part of its gate: re-walk List 1 and List 2 against the git
log since the last audit, add any new resource that outlives a tool call (the List 1
shape) or holds restart-losable session/run state (the List 2 shape), and record the
audit in the phase gate. The inventory drifts the moment a new goroutine, cache,
breaker, or in-memory map lands without a row; three resources (the modelhook
guardrail breakers, the WebSearch provider, the `askReviewBreaker`) already had to be
backfilled after `f1f4e31`, which is exactly the drift this re-audit step prevents.
The CLAUDE.md "outlives-a-call resource" gotcha is the author-time half of the same
discipline.

### Phase 1: snapshot fidelity (SHIPPED)

Make the snapshot faithful enough that a restarted process is indistinguishable
mid-conversation. Per the ledger below, all three facts are now persisted:

- `profile` is an additive, omitempty snapshot field (the same precedent as
  `ProviderPhase`/`Parts`; the empty-workspace inference stays the second defense).
- The provider/model selector is persisted as the opaque `ProviderID`/`ModelID`
  label pair, so rehydration rebuilds the SAME per-session engine instead of
  falling to the default-provider floor (re-derive via the engine factory, never
  clone-and-swap; the existing discipline).
- `session.Usage` is persisted, so the `MaxRunTokens` budget brake survives
  restart. The brake is evaluated against the cumulative aggregate Usage;
  `resetToIdle` preserves Usage (the deliberate divergence from Counters).

The whole arc presupposes a DURABLE store is actually in use. As of issue #79,
`mecatui` enables one by default (a per-workspace JSONL store under
`$XDG_STATE_HOME/mecatui/sessions`, owner-only `0700`), so the restart-fidelity
this phase delivers is now exercised by the default interactive client, not only
by an operator who passed `mecated --store-dir`.

A documented side effect: because `resetToIdle` preserves Usage, a reused
child/member session's per-engine `MaxRunTokens` brake is now CUMULATIVE across
`Reopen` (team rounds, structured-output validation retries), which is the
intended "cap the whole call" reading rather than a per-Reopen fresh allowance.
The one deliberate exception is the team lead's synthesis turn, which calls the
explicit `Session.ResetUsage` seam so a budget-stopped working run still produces
the deliverable. The team-AGGREGATE budget is unchanged (it sums per-round
`EvResult.Usage`, the zero-based per-run delta), so the two budgets stay
independent.

The rehydration-widen call: Phase 1 widened the ENGINE-REBUILD trigger
(`needsRehydration`/`Service.rehydrateSession`, generalized from the no-fs-only
`9f8ba8c` seam) to ANY session with a persisted selector or non-default profile,
because the post-restart engine must be the SAME model, not just no-fs. A default
FS session (empty selector, default profile, non-empty workspace) still rides the
shared engine with no rehydration. The awaiting-approval mid-turn loop entry stays
Phase 2 (no new run-entry seam landed here, only the existing engine-rebuild at
`Service.StartRunContent` widened).

Gate (extends the `9f8ba8c` drill): cross-Build e2e
(`TestSelectorSessionSurvivesRestartE2E`): create a no-fs session on a selector
model with a partially-consumed budget, kill the process, restart over the shared
store, verify same catalog + same model + budget continues. Mutation-verified per
leg.

**Phase 1 re-audit (List 1 / List 2).** Phase 1 added no new outlives-a-call
resource (List 1): the three persisted facts all live ON the existing session
aggregate/snapshot and the existing per-session engine registry (row 11), which
rehydration now rebuilds faithfully rather than degrading. No new goroutine,
cache, breaker, or in-memory map landed. List 2 rows 1/2/3 move to SHIPPED above;
the remaining rows are unchanged.

### Phase 2: awaiting-approval evict/rehydrate (SHIPPED)

The kit inventory's §1.4, built on the rehydration seam `9f8ba8c` created. The
process is now disposable even while a run is parked awaiting a human approval:

- A resume-from-awaiting entry at the run-entry funnel: `Approve`/`Deny` against a
  session whose process died while parked loads the snapshot (state=awaiting,
  `Pending` set), rebuilds the engine (Phase 1 makes this faithful), re-enters the
  loop AT the ask, and delivers the verdict. `ErrNoActiveRun` stops being terminal
  for awaiting sessions ONLY; idle/completed/cancelled/failed stay terminal.
- The loop re-entry is the new piece: resume previously re-entered at turn
  boundaries only (`internal/adapter/server/service.go` (`loadAndReopen`)); this
  adds the mid-turn cursor as a FOURTH, awaiting-only entry. The three existing
  seams (Reopen/Interrupt/Recover, all via `resetToIdle`) did NOT widen: the fourth
  seam drives the existing awaiting-only `engine/session/session.go` (`ResumeWith`),
  which preserves Counters/Usage (NOT `resetToIdle`, which would zero them and clear
  the pending ask). No new aggregate transition verb was added.
- The mechanism is `engine/agent/loop.go` (`ResumeApproval`) →
  `engine/agent/dispatch.go` (`driveFromAwaiting`), launched under the same
  Run-construction preamble as a prompt run (the shared `engine/agent/loop.go`
  (`startRun`)) and continuing through the SHARED `engine/agent/loop.go`
  (`runLoop`) the prompt path also drives (factored out of `drive` so there is ONE
  loop, not two). The service half is `internal/adapter/server/service.go`
  (`resumeFromAwaiting`), reached from `internal/adapter/server/service.go`
  (`ApproveRun`) on a `LookupRun` MISS; the SAME-PROCESS path (a live run) is
  unchanged and tried FIRST. Engine + environment resolution is shared with the
  prompt path via `internal/adapter/server/service.go` (`engineAndEnvironmentFor`) so
  the two cannot drift.
- Exactly-once tool execution is the load-bearing property. The pending tool call is
  resolved through the SAME post-authorize tail the live loop uses (deny →
  synthetic deny result; allow → `preHook` + `execute`, so PostToolUse hooks + the
  audit recorder + `EvToolResult` fire identically; allow-always → also
  `Policy.Learn`), exactly once. Every OTHER unanswered tool call on the trailing
  assistant message is closed out as a synthetic aborted error result (the
  `closeOutInterruptedTurn` analogue) so the replayed history has no dangling
  tool_use (provider-valid: `session.ValidateToolPairing` passes). One ordered
  `RecordToolResults` slice (pending result + synthetic siblings) is recorded, then
  the loop continues.
- Concurrent-Approve exactly-once: two `Approve` calls for the SAME awaiting session
  that both miss the live-run fast path must not both spawn a resumed run
  (`ResumeApproval` spawns the driving goroutine immediately, so a spawn-then-cancel
  loser could execute the tool before its Cancel landed). The resume DECISION is
  serialized per session by a keyed lock (`internal/adapter/server/service.go`
  (`resumeFromAwaiting`), `Service.resumeMu`): under the per-session lock the loser
  re-checks `LookupRun`, sees the winner's now-registered run, and routes its verdict
  to that run's channel (the same-process path), so the pending tool runs exactly
  once. Pinned by `internal/adapter/server/resume_awaiting_test.go`
  (`TestConcurrentApproveAfterRestartExecutesOnce`), mutation-verified (drop the
  serialization and the Write executes twice).
- Wire exposure (asymmetric, by design for this phase): the rehydrate-resume path is
  reachable only through the HTTP `POST /v1/sessions/{id}/approve` endpoint, which
  relays the resumed run as an SSE body (`internal/adapter/server/http.go`
  (`relayRunSSE`)). The gRPC `Converse` stream has NO rehydrate path: its
  `ResumeApproval` control frame resolves the ask against the stream's OWN live
  in-process run only (`internal/adapter/server/grpc.go` (`readControl`)), so a gRPC
  client whose session was evicted has no resume path over `Converse` and a verdict
  frame for a dead run is silently dropped. No proto/driver change either way. The
  gRPC rehydrate path is a tracked follow-up (additive, out of this gate); see the
  Deferred note below.
- Active eviction (a parked run voluntarily releasing its goroutine and heap after
  some idle period) is a follow-on knob, not this phase's gate. Approve-after-crash
  is the essence; eviction is then just choosing to crash on purpose.
- Phase-2 wart (FIXED in Phase 3b): permstore rules are in-memory, so a rehydrated
  session re-asked. Phase 3b replays the durable log's allow-always verdicts into the
  fresh permstore on load (`internal/app/approvalreplay.go` (`replayApprovals`)), so a
  previously-allow-always'd tool is no longer re-asked after a restart. (The
  allow-always verdict on the resume path also re-Learns for later calls in the same
  resumed run, the partial in-run cover that predated 3b.)

Gate (CI-green, offline): the two-Build drill
`TestApproveAfterRestartE2E` (`internal/app/approve_after_restart_test.go`): Build
#1 raises a Write ask + persists the awaiting snapshot, `Close()` is process death,
Build #2 over the SAME store calls `ApproveRun` → resume-from-awaiting, the pending
Write executes EXACTLY ONCE and the run reaches `StopEndTurn` completed.
Mutation-verified per leg (revert the resume seam → `ErrNoActiveRun`; skip the
sibling close-out → dangling tool_use; re-dispatch → double execution). The
server-layer state gate + same-process-unchanged are pinned by
`internal/adapter/server/resume_awaiting_test.go`, and the engine-layer
exactly-once / deny / not-awaiting / wrong-askID / multi-tool sibling close-out by
`engine/agent/resume_approval_test.go`. The LIVE SIGKILL e2e
(`e2e/approve_after_kill_test.go`) is the stated live counterpart; it currently
Skips with an honest harness-gap note (the live harness has no shared-store
second-spawn and no HTTP approve client), with the offline two-Build gate as the
required proof. Two things the offline two-Build deliberately does NOT exercise (only
a real SIGKILL would): (a) a torn final append racing the kill, since
`jsonlstore.appendLine` is not an atomic rename, and (b) OS-crash durability, since
there is no fsync. Both are narrow and out of scope for "disposable process" (the
thesis is process restart, not host crash); see the latent-durability note below.
Child asks (subagent-surfaced asks) are documented NOT rehydratable this round
(run-scoped by design) and, verified below, are structurally unreachable through this
seam, an honest note rather than silence.

Deferred (additive, out of this gate): the gRPC `Converse` rehydrate-resume path (a
gRPC client re-attaching to an evicted awaiting session, symmetric to the HTTP
`/approve` SSE relay) and active eviction (a parked run voluntarily releasing its
goroutine after an idle period). Both are follow-ons; approve-after-crash, the
disposability essence, is the shipped gate.

**Phase 2 re-audit (List 1 / List 2).** Phase 2 added no new outlives-a-call
resource (List 1): `ResumeApproval` mints an ordinary `agent.Run` (already row 9 /
14's shape) through the SAME registry (`Service.runs`) the prompt path uses, and
the resume orchestration lives on the existing session aggregate + the existing
per-session engine registry (row 11). No new goroutine, cache, breaker, or
in-memory map landed; `driveFromAwaiting` is a sibling of `drive` over the shared
`runLoop`, and `resumeFromAwaiting` reuses `engineAndEnvironmentFor` +
`rehydrateSession`. List 2 row 5 moves to resolved and row 6 is verified below; the
remaining rows are unchanged. No `PendingAsk` field was added (the Q4 verification
showed none is needed).

### Phase 3: durable event log / outbox (the new durable artifact)

The kit inventory's §1.1 to §1.3 taken incrementally, NOT full CQRS. The snapshot
stays the replay projection; the log is additive.

- **3a (SHIPPED):** an append-only per-session event log behind a new port
  (`port.EventLog`, `engine/port/eventlog.go`: `Append` + a streamable
  `Read` returning `iter.Seq2[session.Event, error]`), local JSONL adapter first
  (the jsonlstore precedent: the one `internal/adapter/store/jsonlstore/jsonlstore.go`
  (`Store`) now also implements it, with a `.events.jsonl` sidecar and a per-record
  `eventlog-json/1` format tag), wired at the server relay
  (`internal/adapter/server/grpc.go`, `internal/adapter/server/http.go`): the stream
  the harness already emits, persisted instead of discarded. The chronological
  approval record neither mecatl nor Claude Code had is now captured as `EvApproval`
  (`engine/session/event.go` (`ApprovalPayload`), tool name + verdict string + askID,
  no raw args), emitted by the loop at both verdict sites
  (`engine/agent/dispatch.go` (`authorize`) and `engine/agent/dispatch.go`
  (`resolvePendingCall`)) and recorded by the relay. The loop stays storage-agnostic
  (it only emits; it never imports `port.EventLog`). 3a is the FOUNDATION: the log
  records the stream; nothing consumes it yet (that is 3b). The log is now publicly
  readable over the `HarnessService` via the server-streaming `StreamSessionEvents` RPC
  (issue #245 Phase 1) — the client-tier surface over the same `port.EventLog.Read`,
  replaying a session's full timeline (including the log-only `EvApproval` /
  `EvCompactionArchive` / `EvUserPrompt` a live `Converse` relay skips); no new List 1 /
  List 2 row (the RPC adds no outlives-a-call resource — it reads the existing log).
- **3b (SHIPPED):** the durability CONSUMERS of the 3a log. (1) Non-destructive
  compaction archive: a new `EvCompactionArchive`
  (`engine/session/event.go` (`CompactionArchivePayload`)) carries the pre-compaction
  conversation the loop captures BEFORE `ReplaceHistory` mutates it
  (`engine/agent/loop.go` (`maybeCompact`)), emitted AFTER a successful replace (the
  degrade path emits nothing) so the durable record stops being lossy; "what did the
  agent do in turn 12" stays answerable after compaction. The archived span is the
  parent's OWN conversation, so it opens no gauntlet-#7 surface. (2) permstore
  allow-always rules recoverable from verdict events (kills the Phase 2 re-ask wart):
  on a post-restart load the composition closure
  `internal/app/approvalreplay.go` (`replayApprovals`), invoked from
  `internal/adapter/server/service.go` (`maybeReplayApprovals`) once per id, reads the
  logged allow-always `EvApproval`s, correlates each metadata-only askID back to its
  ToolCall in the loaded conversation (the askID encodes the call id, see
  `engine/agent/dispatch.go` (`newAskID`)), and re-drives the existing `Policy.Learn`
  on that call to re-derive the real rule from history the session already carries (no
  port widened; the event stays metadata-only, so no leak). Both consumers keep the
  loop storage-agnostic: the loop only EMITS `EvCompactionArchive`; the replay lives in
  composition. Like `EvApproval`, `EvCompactionArchive` is log-only (skipped on the
  client wire).
- **3c (SHIPPED):** the driver service (`EventLogService`,
  `contracts/proto/mecatl/driver/v1/event_log.proto`), the PROD/remote path for the
  durable log, same conformance-as-contract discipline as the other six seams. The Go
  `port.EventLog` is THE contract; the gRPC service is ONE adapter, validated against
  the SAME `engine/adapter/eventlogconformance/eventlogconformance.go` (`Run`) suite the
  local jsonlstore passes (the client over bufconn and the jsonlstore reference both run
  it). Append is UNARY; Read is SERVER-STREAMING (the FIRST streaming driver RPC; every
  other seam is unary), chosen because a run's log grows unbounded (a unary Read would
  hit the 64 MiB cap) and a stream maps 1:1 onto `port.EventLog`'s lazy `iter.Seq2` Read.
  The event crosses as an opaque format-tagged blob (`eventlog-json/1`, the SAME tag the
  jsonlstore writes), decoded harness-side only, exactly like the SessionStore snapshot
  (`internal/adapter/grpcdriver/eventlog.go` (`EventLog`) is the client,
  `internal/adapter/grpcdriver/eventlog.go` (`NewEventLogServer`) the server wrapper).
  Composition wires it via `--event-log-url` through the existing `driverConns`
  connection cache, INDEPENDENT of the session store (`internal/app/build.go`
  (`buildStore`)); empty = the local default, byte-identical to pre-3c. This is the
  prerequisite issue #28 (session-scoped background detach) has been waiting on; #28
  itself stays its own arc.

With 3c shipped, **Phase 3 is COMPLETE**.

Gate (MET, lands with 3b): a session with one compaction and three verdicts can be
fully reconstructed (user-rich timeline including pre-compaction turns) from store +
log alone; a test client renders it. Pinned by
`internal/app/phase3_gate_test.go` (`TestPhase3ReconstructFromStoreAndLog`): Build #1
takes deny/allow-once/allow-always AND crosses a compaction boundary over a real
jsonlstore; Build #2 reconstructs from `SessionStore.Load` + `EventLog.Read` alone and
asserts the pre-compaction turns (from the archive, absent from the compacted
snapshot), all three verdicts, and the live reasoning/message/turn events. The no-leak
guard is mutation-verified by `TestPhase3LogNoChildLeak` (a Subagent child's
secret-shaped arg never appears in any logged event body; the log respects the same
redaction the event stream already enforces, gauntlet #7).

**Phase 3a re-audit (List 1 / List 2).** Phase 3a added ONE new outlives-a-call
durable artifact (List 1): the `.events.jsonl` event log, which joins row 8's
jsonlstore file set (it is the SAME `Store` instance, opened per-call with
`O_APPEND` and closed immediately (no new held handle, no new goroutine, no new
cache or breaker). The in-process additions (`Service.EventLog`/`Service.appendEvent`
and the loop's two `EvApproval` emits) are run-scoped: the emits ride the existing
`Run.Events()` channel (already a row) and the relay's `Append` rides the existing
relay loop (no new resource). `Service.resumeMu` (named in the Phase 2 re-audit) is a
transient per-session decision LOCK, not an inventory row, and is unchanged here. The
re-audit verdict is CLEAN: no new resource whose lifecycle escapes the relay loop. The
gate's redaction subtest mutation-verifies the no-leak inheritance (the log stores
already-redacted events; it adds no redaction of its own).

**Phase 3b re-audit (List 1 / List 2).** Phase 3b added NO new outlives-a-call
resource (List 1): both consumers reuse existing artifacts. The compaction archive is
an EVENT on the existing `Run.Events()` channel, persisted by the existing relay
`Append` to the existing `.events.jsonl` (row 8): no new file, handle, goroutine, or
cache. The permstore-replay is TRANSIENT composition work: a closure invoked once per
load that calls the existing `Policy.Learn` into the existing per-session permstore
(row 6); the new `Service.replayedApprovals` map is a per-process dedup SET keyed by
session id (process-scoped, bounded by the session count the Service already tracks),
not an inventory row, the same shape as `Service.resumeMu`. List 2: row 4 (permstore
rules) moves to RESOLVED (replayed from the log), and row 9 (pre-compaction history)
moves to RESOLVED (archived to the log). The re-audit verdict is CLEAN.

**Phase 3c re-audit (List 1 / List 2).** Phase 3c added NO new inventory row. The
per-session grpcdriver EventLog client (`internal/adapter/grpcdriver/eventlog.go`
(`EventLog`)) is a process/handle exactly like the other six driver clients: it rides
the EXISTING `driverConns` connection cache (List 1 row 19, `internal/app/driverstore.go:54-90`):
equal URLs share one lazy `ClientConn`, and a deployment can point `--event-log-url`
at the same driver process as `--session-store-url` to multiplex one connection, so it
introduces no new outlives-a-call resource, only one more consumer of an existing one.
Its server-streaming Read opens a stream per replay that the client's iterator closes on
early break (a child-context cancel honouring the `iter.Seq2` early-exit obligation),
held only for the duration of one `Read` call, never across calls. The
`engine/adapter/eventlogconformance/eventlogconformance.go` (`Run`) suite is a test-only
artifact (no production resource). List 2 is unchanged: the durable log itself was
already row 8 (jsonlstore) / row 4+9 (RESOLVED via the log); 3c only swaps WHERE the log
lives (local file vs remote driver), not WHAT it durably holds. The re-audit verdict is
CLEAN: no new resource whose lifecycle escapes a call.

### Phase 4: multi-replica readiness — session leasing (SHIPPED)

Cross-process single-writer enforcement via a per-session lease, so two replicas
over one shared store never both drive the same session id. The seam is a NEW
optional port, discovered by type assertion exactly like `PrunableStore`, wired
ONLY when an operator selects a backend by flag — the default path is
byte-identical with no lease.

- **`port.SessionLease`** (`engine/port/lease.go`): `Acquire`/`Renew`/`Release`
  over an immutable `Lease` value (`SessionID`, `Owner`, a monotonic fencing
  `Token`, an `Expiry`). Two sentinels mirror the `PrunableStore` precedent:
  `ErrLeaseHeld` (held by a live competitor / lost on Renew — TRANSIENT) and
  `ErrLeaseUnsupported` (the backend can never lease — sticky-disable). The
  contract is pinned by `engine/adapter/leaseconformance/leaseconformance.go`
  (`Run`), the SAME suite every adapter passes.
- **Adapters** (the EventLog-seam template): the in-memory reference
  (`engine/adapter/memlease`, `port.Clock`-injected; `engine/adapter/memstore` also
  implements it so the type-assert discovery path is exercised offline), the
  single-host flock lease (`internal/adapter/flocklease`, a per-id flock sentinel +
  an atomic record file, crash-recovery for free), the gRPC driver
  (`SessionLeaseService` in `contracts/proto/mecatl/driver/v1/session_lease.proto`,
  client/server in `internal/adapter/grpcdriver/sessionlease.go` — the multi-host
  path), and the Kubernetes lease (`internal/adapter/k8slease`, a
  `coordination.k8s.io/v1` Lease per session — the in-cluster multi-replica path;
  `leaseTransitions` is the fencing token, a resourceVersion CAS Update maps a 409
  Conflict to `ErrLeaseHeld`).
- **Run-entry gate.** The lease is acquired at the run-entry funnel
  (`internal/adapter/server/service.go` (`acquireLease`)), called by
  `StartRunContent` and `resumeFromAwaiting` AFTER the per-session `runEntryMu` so
  same-process exclusion stays cheap and the `resumeMu→runEntryMu` lock order
  holds. A competing live owner refuses the run with
  `ErrSessionLeasedElsewhere` (gRPC `FAILED_PRECONDITION` / HTTP 409). The lease is
  acquired ONCE per session (held for its life), renewed by a Service-owned
  goroutine (`renewLoop`), and released on `CloseSession` / shutdown via a
  cancel-detached short-timeout ctx (the `appendEvent` precedent). A lost lease
  (Renew → `ErrLeaseHeld`) cancels the live run (fail-safe: `StopCancelled` is
  recoverable). The loop NEVER imports `port.SessionLease`.
- **Composition** (`internal/app/build.go` (`buildSessionLease`)): an INDEPENDENT
  override (`--session-lease-url` driver / `--session-lease-k8s-namespace` /
  `--session-lease-dir` flock) wins, else the configured store is type-asserted for
  the seam, else no lease (the v1 default). The owner identity is built once per
  Build (`<hostname>-<pid>-<nonce>`) so two Builds in one process get distinct
  owners (the cross-process gate's twin-Build test relies on it). The token is
  plumbed but NOT consulted (CAS-Save enforcement deferred — the lease grant itself
  is the enforcement).

Gate (CI-green, offline): the two-Build drill
`internal/app/lease_exclusion_test.go` (`TestCrossProcessLeaseExclusion`,
`TestCrossProcessDoubleExecutionPreventedByLease` — the cross-process twin of
`TestConcurrentApproveAfterRestartExecutesOnce`,
`TestCompositionByteIdenticalWithoutLease`): two Builds over a shared store + flock
lease, distinct owners; Build #2's run-start is refused with
`ErrSessionLeasedElsewhere` while #1 holds the lease, and succeeds after #1
releases. The LIVE counterpart is `e2e/lease_exclusion_test.go` (two real mecated
over a shared `--store-dir` + `--session-lease-dir`: B's run-start → HTTP 409 while
A holds, B succeeds after A is SIGKILLed and the flock auto-releases). The
server-layer renewer/sticky-disable/release behaviour is pinned by
`internal/adapter/server/lease_test.go`.

**Phase 4 re-audit (List 1 / List 2).** Phase 4 added ONE new outlives-a-call
resource (List 1 row 27): the `Service.heldLeases` map plus its per-session
renewer goroutines — owner `server.Service`, scope SESSION, cleanup =
renewer-cancel + `Release` on `CloseSession`/`Close`, re-attach = reacquired on the
next run-entry (or a crashed holder's lease lapses after the TTL and a survivor
takes over). List 2 is UNCHANGED: a lease is DERIVED state (nothing a restart
needs to reload — a restarted process re-acquires on the next run-entry), so it
adds no rehydrate-fidelity row. The re-audit verdict is CLEAN. Decision (c) below
moves to CODE-ENFORCED-when-wired.

The stated v1 constraint still stands as the DEFAULT (no lease backend): session-
affinity routing, one writer per session. A deployment that cannot guarantee
affinity now wires a lease backend instead of relying on the deployer.

**Phase 5 re-audit (List 1 / List 2).** Phase 5 (scheduled tasks, ADR 0059)
added TWO new outlives-a-call resources (List 1 rows 30–31) and ONE new
rehydrate-fidelity concern (List 2 row 21), all behind the `--scheduler` flag
(byte-identical default when unwired):

- the scheduler tick goroutine (List 1 row 30): owner
  `internal/adapter/scheduler` (`Scheduler`), scope PROCESS, cleanup =
  `Stop` cancels the tick loop + joins in-flight fires (with a grace), then
  releases the leader lease; re-attach = a restarted process RE-ACQUIRES the
  leader lease (or ticks standalone with no lease backend) and re-polls
  `ScheduleStore.Due` — the schedule store is the durable ground truth, the
  in-memory lookahead is derived. The tick goroutine is owned by the scheduler
  the `Service` holds (`Service.scheduler`), so `Service.Close` stops it FIRST
  (so in-flight fires drain while the service is still alive to serve them).
- the leader-lease renewer goroutine (List 1 row 31): owner
  `internal/adapter/scheduler` (`Scheduler`), scope PROCESS, cleanup =
  cancelled at `Stop` (the leader lease is released alongside the tick loop);
  re-attach = a restarted leader re-acquires the well-known
  `__scheduler__` lease (the SAME backend as the run-entry session lease, a
  different id so they never contend). It is hygiene, NOT correctness: the
  `ScheduleStore.Claim` mutex is the at-most-once fence; the leader lease only
  prevents two replicas from ticking the same store concurrently (double-fire
  prevention in a multi-replica deployment).

List 2 row 21 records the EvSchedule fire records: the
`EvScheduleFired`/`Skipped`/`Failed` events are emitted by the scheduler via
the composition-injected `EmitScheduleEvent` callback and APPENDED to the fire
session's durable `EventLog` (so schedule lifecycle rides the same durable log as
the fire's own events). They are reconstructable via `EventLog.Read` — a
restarted process reads them back from the durable log (decision = derive). A
skipped fire with no session id is dropped from the durable log (the log is
session-keyed) and is durable-log-only for v1 (pull via GetFire/ListFires; live
broadcast deferred). The re-audit verdict is CLEAN: no restart-losable
session/run state that is not already derived from the durable `ScheduleStore`
+ `EventLog`.

**Permanent-provider-error re-audit (List 1 / List 2 — issue #346, ADR 0097).**
The permanent-provider-error signal (ADR 0097) added NO new outlives-a-call
resource (List 1). The two new pieces are: (a) the `Session.permanent` boolean —
a field ON the existing session aggregate/snapshot, purely additive (persisted,
no new file/handle/goroutine); and (b) `Service.recoverNotices` sync.Map — a
transient per-process map keyed by session id, bounded by the session count the
Service already tracks, the same shape as `Service.resumeMu` /
`Service.replayedApprovals` (not an inventory row). List 2 gains two rows (24–25):
`Session.permanent` (persist-in-snapshot — survives restart, read by the run-entry
funnel to emit the advisory) and `Service.recoverNotices` (reset-by-design — a
restart loses a one-time advisory, safe). The re-audit verdict is CLEAN.

**ToolHive-LLM-direct-mode re-audit (List 1 / List 2 — issue #265, ADR 0102).**
Direct mode added TWO new outlives-a-call resources (List 1 rows 39–40), both
build-scoped and both OPT-IN (they exist only when the resolved routing mode is
`direct` — the proxy path is byte-identical to pre-#265 and allocates neither):
the in-process OIDC token source, and the OS-keyring/D-Bus connection its secrets
provider opens. List 2 gains NO row: the access token is a short-lived derived
credential (re-minted from the refresh token on the next request) and the refresh
token itself lives in the OS keyring with only its REFERENCE in ToolHive's config
— both already survive a restart outside mecatl's snapshot, so the decision for
each is derive, not persist. The `goleak` D-Bus ignores in
`internal/app/leakmain_test.go` are the visible symptom of row 40 and are scoped
to a top-of-stack pin, not a blanket suppression. The re-audit verdict is CLEAN.

**Caller-identity re-audit (List 1 / List 2 — issue #367, ADR 0100).** The
caller-identity track (attribution only — nothing is refused on identity grounds
yet) adds ONE new outlives-a-call resource (List 1 row 41): the token validator's
JWKS cache and its background refresh. Everything else it adds is a FIELD on an
already-inventoried artifact:

- **`internal/syscaller` earns no row.** The registry is a package-level slice of
  string constants (`internal/syscaller/syscaller.go` (`Roots`)) — compile-time
  data, not an allocated resource, and it holds nothing per session or per run.
  `syscaller.Context` stamps a value on the root context of goroutines that are
  ALREADY inventoried (rows 1 childgc, 30 scheduler tick, 31 leader-lease renewer,
  and the two consolidators); a context value on an existing root changes no
  lifetime, no cleanup and no re-attach answer, so annotating those rows would be
  the only honest alternative and it would say nothing new.
- **The session owner and `Event.Actor` are List 2 concerns, not List 1** (rows 26
  and 27): both are fields on the existing snapshot / existing durable event log,
  adding no handle, goroutine, cache or map.
- **The schedule owner earns a List 2 row (28)** because it is state a restart
  would otherwise lose: it is captured at CREATE, and a fire that happens after a
  restart must still run as the caller who created the schedule.

The re-audit verdict is CLEAN. The shipped `toolhive-core/authn` v0.0.39 validator
is wired by the shared OIDC config; its cached keys are bounded by default under
[ADR 0101](./0101-bounded-jwks-staleness.md), and remain reconstructible rather
than persisted.

**Execution-environment persistence re-audit (List 1 / List 2 — issue #462 phase 3, ADR 0106).**
Phase 3 added NO new outlives-a-call resource (List 1): `server.Config.EnvironmentResolver`
is a composition-injected function value (no goroutine, no cache, no handle), and the
`internal/adapter/remoteenv` reference fake is a contract proof only, NOT wired by default
`app.Build` (no production resource). List 2 gains NO new row: `session.EnvironmentRef`
is a field on the already-inventoried snapshot (row 8) — it persists ON the existing session
file and reattaches a live `Environment` at run entry through the resolver, adding no handle of
its own (the reattached `Environment` is per-run, mirroring the default path's fresh-per-run
construction, not a cached override). The in-tree Kinds never reach the resolver; a legacy
zero ref is stamped from the first resolved live Environment on the next save (no migration
sweep). The re-audit verdict is CLEAN: no restart-losable state beyond what the snapshot
already holds, no new outlives-a-call resource.
### Phase 6: crash-orphaned running-session recovery (SHIPPED)

A session's `state` is persisted mid-turn — every `e.save` after `BeginTurn`
writes `StateRunning` well before the turn (let alone the run) reaches a
terminal state — so a process that crashes, is killed, or loses its host while
a session is `StateRunning` leaves the LAST persisted snapshot reading
"running" forever: none of Reopen/Interrupt/Recover is legal from `running`,
so nothing ever recovers it (issue #475). Two real symptoms: the `/sessions`
listing lies forever ("in progress" with nobody driving it), and a follow-up
prompt on the SAME id risks reaching the provider with a dangling
`tool_use`/`function_call` left by the abandoned turn (an unrepaired history →
provider HTTP 400 → `failed`).

The fix adds a 4th terminal-recovery seam and two independent repair paths
that route through it, plus a composition-level sweep for the case where
nobody ever touches the orphan again:

- **`Session.Abandon()`** (`engine/session/session.go` (`Abandon`)): the 4th
  seam, sibling of Reopen/Interrupt/Recover, legal ONLY from `StateRunning`.
  Repairs any trailing unanswered `tool_use` via the SAME
  `closeOutInterruptedTurn` history repair the other three seams use, with its
  own abandonment-accurate close-out wording (never claiming a cancellation or
  a failure, neither of which was observed), then `resetToIdle()`s. `awaiting`
  is untouched by design — see the widened AGENTS.md invariant.
- **The run-entry funnel's own repair** (`internal/adapter/server/service.go`
  (`StartRunContent`)): placed AFTER `runEntryMu.lock(id)` and the REAL
  `acquireLease` have both succeeded — never inside `loadAndReopen`'s pre-lock
  switch, where a trial lease could race a concurrent caller or reject a
  peer's genuine `acquireLease`. The successful acquire IS the proof of
  exclusive ownership, so no trial lease or age-horizon oracle is needed here;
  an `IsLive(id)` guard refuses the repair outright (rather than racing it) if
  THIS process still has a genuinely live run for `id` (`runEntryMu` only
  serializes run-ENTRY, not a run's whole lifetime, so a second
  `StartRunContent` for the same id can land here while an earlier run this
  process started is still mid-flight).
- **`Service.SessionStale` / `Service.SettleIfStale`**
  (`internal/adapter/server/service.go`): the shared staleness DECISION and
  repair WRITE. `SessionStale` mirrors
  `internal/adapter/scheduler/scheduler.go`'s own
  `shouldReconcileStaleFire`/`isPriorFireLive` ordering — an age horizon
  (`staleSessionWindow`, 30m) is a HARD PRECONDITION checked BEFORE any
  liveness/lease signal, because it is the ONLY defense that covers
  `subagent-*`/`parallel-*`/`team-*` child sessions at all (`IsLive`'s own doc
  comment says it never sees engine children even mid-run — a liveness-only
  oracle would be blind to exactly the population issue #475's confirmed bug
  came from); `IsLive` is checked next; and, when a `port.SessionLease` is
  wired, a bounded trial Acquire is a SECONDARY refinement (with a
  self-held-lease correction and a sticky `LeaseSweepDisabled` fallback on
  `ErrLeaseUnsupported` — never a per-candidate downgrade to local-only
  liveness, which would reintroduce the cross-replica unsoundness the lease
  branch exists to prevent). `SettleIfStale` re-checks `State==StateRunning`
  (closing the meta-scan/load TOCTOU) and `!IsLive(id)` before calling
  `Abandon()`+`Store.Save`. It is the SWEEP's repair path ONLY — the funnel's
  `StartRunContent` repair calls `Abandon()`+`Store.Save` directly on the
  in-memory session it already holds, since routing through `SettleIfStale`'s
  fresh `Store.Load` would leave the funnel's own in-memory session (already
  loaded, about to be handed to `engine.Run`) untouched and still carrying its
  unpaired `tool_use` — the HTTP-400 this whole fix exists to prevent would
  survive unnoticed.
- **The composition-level sweep** (`internal/app/session_reconcile.go`
  (`startStaleSessionReconcile`)): a startup-sweep-then-ticker goroutine
  (mirroring the `startLiveModelRefresh` idiom, not `childGC`'s Build-ctx-tied
  one — this sweep always keeps a persistent ticker running, so it needs its
  own cancelable lifetime) that lists every stored session via
  `Service.ListSessions`, narrows to `state=="running"` rows excluding
  `sched--` fire ids (the scheduler owns its own stale-fire reconciler), and
  settles every candidate `SessionStale` judges stale via `SettleIfStale`.
  This is the ONLY repair path that reaches a
  `subagent-*`/`parallel-*`/`team-*` child crash-orphaned in `StateRunning`,
  and the ONLY mechanism that fixes the `/sessions`-display symptom for a
  session nobody ever re-opens (the funnel's repair only fires when a caller
  re-opens the EXACT orphaned id).

This is honestly **last-write-wins narrowed by a wide age window, not
atomic**: `Store.Save` has no CAS/fencing consulted anywhere in this design,
so nothing here actually PREVENTS an already-in-flight `e.save` from a
genuinely live run landing after a sweep's repair — the 30-minute age window
is what makes the residual race acceptable in practice, not a proof that it
cannot happen.

Gate (CI-green, offline): `engine/session/session_test.go`
(`TestAbandonFromRunningClosesOutOrphansAndIdles`,
`TestAbandonRefusedOutsideRunning`, `TestAbandonPreservesUsage`,
`TestAbandonIsIdempotentAgainstAlreadyRepairedHistory`) pin the seam's
precondition, history repair, and usage preservation;
`internal/adapter/server/staleness_test.go` (`TestSessionStaleWithinAgeWindowNeverStale`,
`TestSessionStaleLeaseSelfHeldIsStale`,
`TestSessionStaleLeaseUnsupportedDisablesSweepNotFallback`, and siblings) pin
the age-first/IsLive/lease ordering; `internal/adapter/server/settle_test.go`
(`TestSettleIfStaleAbandonsRunningSessionThenNoOps`,
`TestSettleIfStaleSkipsGenuinelyLiveSession`) pin the repair write;
`internal/adapter/server/staterunning_repair_test.go`
(`TestStartRunContentAbandonsStaleRunningSession`,
`TestStartRunContentLeavesLiveRunningSessionAlone`) pin the funnel repair; and
`internal/app/session_reconcile_test.go`
(`TestStaleSessionReconcileSettlesChildCandidate`,
`TestStaleSessionReconcileExcludesScheduleFireSessions`,
`TestStaleSessionReconcileNeverTouchesAwaiting`,
`TestStaleSessionReconcileLeavesFreshRunningAlone`,
`TestSweepStaleSessionsSkipsWhenLeaseSweepDisabled`,
`TestStartStaleSessionReconcileExitsOnCancel`) pin the sweep's candidate
narrowing and its join-on-close.

**Phase 6 re-audit (List 1 / List 2).** Phase 6 added ONE new outlives-a-call
resource (List 1 row 43): the stale-session sweep goroutine — owner
`internal/app` (`startStaleSessionReconcile`), scope PROCESS, cleanup = the
returned closer now `cancel()`s AND `wg.Wait()`s (mirroring
`startLiveModelRefresh`'s exact idiom) so `Built.Close()` cannot return while a
sweep pass is mid-`SettleIfStale`, folded into `Built.Close`'s `closeAll`;
re-attach = a restarted process starts a FRESH sweep at the next `Build`'s
startup pass — there is nothing to carry over, since the durable snapshot the
sweep repairs already IS the state in question. It is deliberately NOT tied to
`Build`'s own ctx, unlike `childGC` (a no-op-by-default goroutine most callers
never notice outlives one `Build` call, since `ChildGCInterval` defaults to
0/startup-only) — this sweep always runs a persistent ticker, so tying it to a
ctx a test fixture never cancels would leak a goroutine per test. List 2 gains
NO row: the sweep and the funnel repair both act on the ALREADY-persisted
`session.Session` snapshot — there is no new restart-losable state here, only
a repair of state that already existed. The re-audit verdict is CLEAN.

Named residuals (accepted gaps, not silently omitted):

- An orphaned-`awaiting` session that gets a NEW prompt instead of a resume
  stays wedged (`BeginTurn` is illegal from `awaiting`, so `RecordUserPrompt`
  rejects with `ErrIllegalTransition`) — not fixed by this phase; see the
  widened AGENTS.md invariant.
- The scheduler's own stale-fire reconciler
  (`internal/app/scheduler_reconcile.go` (`makeReconcileStaleFire`)) only ever
  inspects the MOST RECENT fire per schedule (`sched.State`), so an older
  orphaned `sched--` session beyond the latest fire is reconciled by NEITHER
  mechanism: `session_reconcile.go`'s sweep deliberately excludes the whole
  `sched--` family (this reconciler owns it), and this reconciler itself never
  looks that far back.
- A genuinely-live `subagent-*`/`parallel-*`/`team-*` child session id could in
  principle be repaired out from under a SINGLE `StartRunContent` call at
  that child id — unlike the main-session case, there is no "first" funnel
  call that registered the live run for `IsLive` to see: the child is driven
  in-process by its parent's own dispatch, so `IsLive` is structurally blind
  to it (see the doc comment on `IsLive` itself) and any `StartRunContent`
  reaching a live child's id would find `IsLive` false and proceed. The
  funnel's repair relies on "a successful lease acquire is its own proof,"
  not an age horizon, for that specific path — distinct from the sweep, which
  DOES gate every child candidate (these families included) on the age
  horizon first. **This is NOT theoretical** — a child's id is deliberately
  surfaced to the same caller that owns its parent session (the
  `agentId:`/`Team id:` result trailer, `InspectSubagent`/`InspectMember`'s
  `MemberSessionID(teamID, member)` scheme), so any caller with ordinary
  prompt-endpoint access could `StartRunContent` a live child's id directly
  and race the parent's own drive. The fix (a panel-review finding on this
  same issue's Step 3) is `internal/adapter/server/service.go`'s
  `isDelegationChildSessionID` guard: `StartRunContent` now rejects ANY
  `subagent-`/`parallel-`/`team-` prefixed id outright with
  `ErrInvalidArgument`, unconditionally, before `loadAndReopen`, before the
  lease/lock, and before the `StateRunning` crash-orphan repair branch can
  even be reached — closing the wire path structurally rather than trying to
  make the repair itself race-safe. `sched--`-prefixed schedule-fire sessions
  are deliberately excluded (`scheduler_fire.go`'s own `StartRunContent` call
  on a fire session IS the legitimate driver for that family). Pinned by
  `TestStartRunContentRejectsDelegationChildSessionID`
  (`internal/adapter/server/staterunning_repair_test.go`). `SessionStale`'s
  age-first ordering remains the sweep's own, independent defense for this
  population — the two defenses are complementary, not redundant.

### Sequencing rationale

0→1→2 is a strict dependency chain (rehydrate needs faithful snapshots). 3 is
independent of 2 and could swap, but 2-before-3 is recommended: it completes the
disposability thesis with the least code while the `9f8ba8c` rehydration seam is
fresh, and its one wart (re-asks) is exactly what 3b fixes, a clean handoff. 4 waits
for a deployment that needs it.

Explicitly deferred, unchanged from the PR #54 evaluation: FS-as-driver (the
`DRIVERS.md` workspace-driver sketch; the filesystem returns as an optional mounted
capability), fork merge-back, environment provisioning, the substrate/mount-table
model, ProcessHost, a memfs-scratch profile (needs a snapshot story, naturally
revisitable after Phase 1).

## List 1: resource inventory

Every resource the harness allocates whose lifecycle outlives a single tool call,
tagged with its de-facto scope in the nesting `call ⊂ run ⊂ session ⊂ team ⊂
process`. "Re-attach" answers: can a restarted process recover it?
**reconstructible** (rebuilt from config/disk on next Build), **persisted** (the
durable artifact survives and is reloaded), or **lost** (gone, possibly leaking).

| # | Resource | Owner | Scope | Cleanup today | Re-attach | Evidence |
|---|---|---|---|---|---|---|
| 1 | Global MCP manager (`globalMgr`) | `app.Build` | process | `mcpClose` in Build's `closeAll`; NEVER folded into per-session close (`build.go:980`) | reconstructible (reconnects from config at next Build) | `internal/app/build.go:1868` (`connectMCP`) |
| 2 | Per-session client MCP managers | `sessionEngineFactory` | session | per-session close func, invoked by `Service.CloseSession` (`service.go:743`) and shutdown | lost (client specs are not persisted; a client re-mounts via `LoadSessionWithMCP`, `service.go:968`) | `internal/app/build.go:962` |
| 3 | Preserved-fork LRU (`LRUForkReaper`) — Parallel ONLY | `app.Build` (shared via `catalogAssets`, built only when Parallel is enabled) | process | LRU eviction runs each entry's cleanup (dir removal) outside the lock | registry lost; the preserved fork DIRS remain on disk un-tracked (a leak on crash) | `engine/agent/forkreaper.go:41,64`; built at `internal/app/build.go:1936`. NOTE: the writable Subagent (`mode:"read-write"`) no longer creates a per-call fork dir — it writes the parent workspace directly (ADR 0077), so it contributes no fork dirs here; the shared `autoMerger` (`forker.SerializingMerger`) is likewise Parallel-only now |
| 4 | Project memory store (flock pair: `memory.json` + `memory.lock`) | `app.Build` | process handle, per-directory data | flock held per-operation only; one `*Store` per dir per process. Current entries, revision history, and tombstones share ONE locked atomic document — no split-file transaction | persisted (data/history on disk; legacy entries lazily materialize on first mutation; handle rebuilt at next Build) | `internal/app/build.go`; `internal/adapter/memory/store.go` (`Store`, `withExclusiveLock`, `materializeLegacy`) |
| 5 | User-model store (same adapter, XDG dir) | `buildUserModelStore` | process handle, per-user data | as above | persisted, including operator-profile source state and lifecycle history | `internal/app/build.go` (`buildUserModelStore`) |
| 6 | permstore learned allow-always rules | `app.Build` | session (data), process (store) | `Forget(sessionID)` via `OnCloseSession` (`service.go:748`); capped at 256/session | **replayed (Phase 3b)**: in-memory still, but a post-restart load re-Learns the allow-always rules from the durable log's verdict events (`internal/app/approvalreplay.go` (`replayApprovals`)), so a previously-allow-always'd tool is not re-asked | `engine/adapter/permstore/permstore.go:42,48` |
| 8 | jsonlstore session files + `.tools.jsonl` audit + `.events.jsonl` event log (Phase 3a) | jsonlstore | per-session files | none needed: files opened per call (`O_APPEND`), closed immediately, never held; `Delete` removes all three (sidecars first, session file last) for the canonical family, and again for a pre-rewrite family whose snapshot proves the same id (ADR 0104) | persisted (`Load` reads the latest snapshot line; `EventLog.Read` scans all event lines cumulatively) | `internal/adapter/store/jsonlstore/jsonlstore.go` (`appendLine`) |
| 9 | Live-run registry (`Service.runs`) | `server.Service` | run | `deregister` after the wire adapter drains `run.Events()` | lost (the run dies with the process; the session snapshot persists) | `internal/adapter/server/service.go:368` |
| 10 | Team registry (`Service.teams`) | `server.Service` | team | removed at team terminal | **lost** (see ledger row 10: the whole coordination state) | `internal/adapter/server/service.go:369` |
| 11 | Per-session engine registry (`Service.sessionEngines`) | `server.Service` | session | evicted + closed at `CloseSession` and shutdown | lost; rehydrated via `Service.rehydrateSession` for the no-fs profile, a persisted selector, OR an empty workspace (`needsRehydration`); decision = derive (nothing new persisted). **Registration sources (ADR 0030 Layer 3 added the last two):** create (selector / client-MCP / no-fs), restart rehydration, a CASE-1 mode→model REBUILD (a registered per-session engine whose `builtForMode` no longer matches `session.Mode`), and a CASE-2 mode-PROMOTION (a default-FS session whose mode resolves a different model via the `ModeNeedsEngine` predicate). The mode-rebuild + promotion go through the SAME `Service.buildAndRegisterSessionEngine` helper as rehydration, under the per-session `runEntryMu` (the use-after-close guard: a rebuild's prior-engine `Close` and the `s.runs[id]` liveness check sit in one critical section, so a displaced engine is never closed while a run holds it). **At-cap failure semantics:** a CASE-2 promotion is a NEW per-session registration, so a plan-mode `StartRun` can now fail with `ErrTooManySessionEngines` where the original (shared-engine) create succeeded — graceful (no panic, no silent shared-engine fallback that would run plan mode on the wrong model). A CASE-1 rebuild reuses the existing slot (no cap pressure). The compaction context-window is NOT a rehydration trigger — every engine (shared + per-session) carries a resolve-at-use `Deps.ContextWindow` closure (`reg.windowResolver`) read live on each `maybeCompact`/`Engine.ContextWindow`, so a live-only default model self-corrects to its live window on the next turn with no rebuild. **Reasoning-effort (ADR 0055):** a session whose resolved effort differs from the operator default gets a RE-MINTED provider adapter (`providerEntry.remintEffort`) carrying its OWN resilience breaker/retry-wrapper instance — benign (session-scoped, the same posture as rows 21/22; decision = derive — the effort is the List-2 row-19 persisted label and the breaker re-arms on the next Build/rehydration). The three utility engines (guardrail checker, ask-reviewer, model-router) deliberately pin the OPERATOR-DEFAULT provider, not the re-minted one, so a session's effort never raises their spend | `internal/adapter/server/service.go` (`sessionEngines`) |
| 12 | Per-session environment overrides (`Service.sessionEnvironments`: nofs, ACP buffers) | `server.Service` | session | evicted at `CloseSession` (`service.go:1694`) | lost; the no-fs override is re-registered by rehydration; ACP override is re-registered on editor reconnect. **EnvironmentRef persistence (ADR 0106, issue #462 phase 3):** a non-in-tree ref now persists ON the snapshot (`sessnap.Snapshot.EnvironmentRef`, `omitzero`) and reattaches a live `Environment` at run entry through `server.Config.EnvironmentResolver` — the in-tree Kinds never reach the resolver (they re-derive through the factories); a nil/mismatch/nil-Workspace result fails loudly (`ErrFailedPrecondition`), never a silent local fallback. The resolver is independent of engine rehydration (a remote session on a default provider/model rides the shared engine). A legacy zero ref is stamped from the first resolved live Environment on the next save (no migration sweep). | `internal/adapter/server/service.go:722` |
| 13 | Background children (`childRunRegistry`) | `agent.Run` | run | `drainChildren` at both terminate paths (cancel + join + seal) | registry lost; the child SESSIONS persist via `WithSubagentStore`/`WithMemberStore` and are individually resumable | `engine/agent/childregistry.go:128-131`; `engine/agent/subagent.go:599,1267` |
| 14 | askRegistry channel park + childAskRouter | `agent.Run` | run | unregistered on verdict/retract; dies with the run | lost, BUT no longer stranding (Phase 2): a post-death `Approve` on an awaiting session re-enters the loop AT the ask via `internal/adapter/server/service.go` (`resumeFromAwaiting`) rather than returning `ErrNoActiveRun`; the channel park itself is rebuilt by the resumed run | `engine/agent/permission.go:30-32,161` |
| 15 | Hook subprocesses | hookexec, per invocation | call | spawn, wait (30s default timeout, process-group kill) | nothing to re-attach | `internal/adapter/hookexec/hookexec.go:116,130` |
| 16 | Child-session retention GC goroutine | `startChildGC` | process | exits on ctx done or sticky `ErrPruneUnsupported` | reconstructible (restarts with the process; the swept artifact is the store) | `internal/app/childgc.go:235` |
| 17 | Memory/user-model consolidation goroutines (dream) | `app.Build` | process | exit on ctx done | reconstructible | `internal/app/build.go:1893,1901,1920` |
| 18 | Live model-catalog refresh goroutine | `app.Build` | process | one-shot; `refreshClose` in `closeAll` | reconstructible (embedded catalog is the floor) | `internal/app/modellister.go:35`; `internal/app/build.go:763` |
| | _(traceability)_ `reg.meta` (the refreshed `liveMetaStore`) ALSO feeds the `resolved_model` echo for BOTH branches via the injected `server.Config.ResolveContextWindow` closure (issue #66, promoted by the resolve-at-use unification). The echo closure is the SIBLING `reg.echoWindowResolver` (NOT the engine's `reg.windowResolver`): both share the `resolveWindowCore` precedence so they agree on every override / catalogued / live window, differing ONLY in the terminal unknown branch — the echo returns a deliberate PROVISIONAL `0` while the refresh is in flight (the client refetches), then floors an uncatalogued model to 128k once SETTLED (no-network boundedness). That settle is tracked by a NEW `completed atomic.Bool` ON this same `liveMetaStore` (`reg.meta`), set by the SAME one-shot refresh goroutine this row owns (every settle path; never on a shutdown-cancel). **No new resource row** — the flag lives on the already-inventoried store and is set by the already-inventoried goroutine; `decision = derive` (a restart re-runs the refresh, which re-settles the flag). | | | | | `internal/app/livemeta.go` (`windowResolver`, `echoWindowResolver`, `markRefreshCompleted`); `internal/app/build.go` (`ResolveContextWindow`); `internal/adapter/server/service.go` (`ResolvedModel`) |
| 19 | Driver connection cache (`driverConns`, one lazy `ClientConn` per URL) | `app.Build` | process | once-guarded closes folded into the per-seam closes | reconstructible (lazy redial next Build) | `internal/app/driverstore.go:54-90` |
| 20 | WebSearch `SearchProvider` (shared `*http.Client` + egress semaphore) | `app.Build` | process | none needed (stateless client; the semaphore is a per-process egress bound) | reconstructible (rebuilt from the backend-tier config at next Build) | `internal/app/build.go` (`buildSearchProvider`); `engine/adapter/search/httpsearch.go:79-80` (issue #26) |
| 21 | modelhook guardrail breaker (`failureStreak`) | `modelhook.Runner` (composition) | session | dies with the session Runner | **lost** (in-memory; a rehydrated session gets a closed breaker, fail-safe) | `internal/adapter/modelhook/breaker.go`; constructed on `modelhook.Runner` (commit `a032412`) |
| 22 | `askReviewBreaker` (headless ask-reviewer circuit breaker) | `agent.Run` | run | dies with the run | lost (run-scoped by design) | `engine/agent/askadjudicator.go:144` (commit `1b774d4`) |
| 23 | model-router classifier engine (ADR 0031; ADR 0034 reuses it for team members + Parallel branches via the SAME `routeTask` closure — NO new engine) | `buildModelRouterTask` closure (composition) | session (re-derived per session; ONE engine built per classification call) | none needed (tool-less, no hooks, no goroutine; GC'd after the one-turn drive) | reconstructible (rebuilt from the operator taxonomy + the session's provider/model at next Build / next classification); decision = derive (nothing persisted) | `internal/app/build.go` (`buildModelRouterTask`); `engine/agent/modelrouter.go` (`RunModelRouter`) |
| 24 | `modelRouterBreaker` (per-run model-router circuit breaker, ADR 0031; ADR 0034 reuses the SAME per-run breaker for team members + Parallel branches — NO new breaker) | `agent.Run` | run | dies with the run | lost (run-scoped by design, mirroring `askReviewBreaker` row 22) | `engine/agent/modelrouter.go` (`modelRouterBreaker`); armed in `engine/agent/loop.go` (`startRun`) |
| 25 | `gitWorktreeLister` (osfs-backed worktree discovery, issue #102) | `app.Build` | process lifetime | none (value type, no goroutine, no Close needed) | reconstructible (rebuilt from `cfg.Shell` at next Build; no state) | `internal/app/build.go` (`buildWorktreeLister`) |
| 26 | routed team-member / Parallel-branch child engine (ADR 0034) | `buildMemberEngine` (member) / `buildParallelEngineFactory` (branch), minted in composition | per-AddMember (member; reused across rounds, torn down on member teardown) / per-call (branch; torn down with the branch fork) | dies with the member/branch (the SAME lifecycle as the non-routed member/branch engine it replaces — routing changes only the model, not the lifetime) | reconstructible (a new team/Parallel call re-classifies + re-mints); decision = derive (nothing persisted; the routed model is List-2 row 18) | `internal/app/build.go` (`buildMemberEngine`, `buildParallelEngineFactory`) |
| 27 | held session leases + per-session renewer goroutines (`Service.heldLeases`, Phase 4) | `server.Service` | session (one lease + renewer per leased session) | renewer cancelled + `SessionLease.Release` (cancel-detached short-timeout ctx) on `CloseSession` and shutdown `Close`; a lost lease cancels the run and drops the hold | **reconstructible** (a restart re-acquires on the next run-entry; a crashed holder's lease lapses after the TTL and a survivor takes over — no persisted state, the lease is derived); only constructed when a lease backend is wired (`SessionLease != nil`), else absent (byte-identical default) | `internal/adapter/server/service.go` (`heldLeases`, `acquireLease`, `renewLoop`, `releaseLease`); wired at `internal/app/build.go` (`buildSessionLease`) |
| 28 | MCP standalone-SSE listener goroutine (`handleSSE`) per connected server | `mcp.Server` | per connected server (rides the SDK session, opened after `initialize` when `DisableStandaloneSSE: false`) | `Server.Close()` → `session.Close()` → `conn.Close()` cancels `connCtx` → `handleSSE` returns (async; the `mcp` package's `goleak` gate has a targeted ignore list for the SDK + stdlib goroutines that unwind asynchronously after close) | none (the SDK reconnects the stream itself on a transient drop; #177/ADR 0056 reconnects the whole session when the SSE reconnect exhausts → `ErrSessionMissing`) | `internal/adapter/mcp/mcp.go` (`dial`); ADR 0057 |
| 29 | guardrail session waiver (`WaiverHolder`, ADR 0062) | `app.Build` constructs; the engine arms it via the `modelhook.Runner`'s `port.HookApprovalLearner` on a human `VerdictAllowAlways`, `modelhook.Runner.check` consults it | process | self-clearing; dies with the process (no `Close` — a nil `*WaiverHolder` is the byte-identical OFF posture) | **lost** (in-memory; a waiver never silently survives restart — fail-safe: the call re-blocks/re-asks until a human re-approves it, ADR 0062) | `internal/adapter/modelhook/waiver.go` (`WaiverHolder`); armed via `internal/adapter/modelhook/modelhook.go` (`LearnHookApproval`); constructed in `internal/app/build.go` |
| 30 | scheduler tick goroutine (Phase 5, ADR 0059) | `internal/adapter/scheduler` (`Scheduler`), held by `server.Service.scheduler` | process | `Scheduler.Stop` cancels the tick loop + joins in-flight fires (with a grace) + releases the leader lease; `Service.Close` stops it FIRST so fires drain while the service is alive | **reconstructible** (a restarted process re-acquires the leader lease or ticks standalone, and re-polls `ScheduleStore.Due` — the store is ground truth, the lookahead is derived); only constructed when `--scheduler` is set (byte-identical default when unwired) | `internal/adapter/scheduler/scheduler.go` (`tickLoop`, `Start`, `Stop`); wired at `internal/app/build.go` (`startScheduler`) |
| 31 | scheduler leader-lease renewer goroutine (Phase 5, ADR 0059) | `internal/adapter/scheduler` (`Scheduler`) | process | cancelled at `Scheduler.Stop` (the leader lease is released alongside the tick loop) | **reconstructible** (a restarted leader re-acquires the well-known `__scheduler__` lease on the SAME backend as the run-entry session lease, different id — no contention; a non-leader stands down). Hygiene, NOT correctness: `ScheduleStore.Claim` is the at-most-once fence; the lease only prevents two replicas ticking the same store | `internal/adapter/scheduler/scheduler.go` (`renewLeader`); `internal/app/build.go` (`buildScheduler`) |
| 32 | scheduler per-schedule-name `fireMu` mutex map (Phase 5, ADR 0059; `lockFireName`) | `internal/adapter/scheduler` (`Scheduler.fireMu sync.Map`) | process | lazily created, never pruned (one `*sync.Mutex` per distinct schedule name); dies with the process at `Scheduler.Stop` | **reconstructible** (a restart re-acquires lazily on the next fire — it is a synchronization gate, not state-of-record; the at-most-once truth lives in `ScheduleStore.Claim`/`ClaimNow`). Growth is bounded by authenticated-caller schedule-name cardinality (the RPCs are auth-gated; names are operator-curated via CreateSchedule). A `FireNow` on a nonexistent name still acquires an entry before the `Load` miss — accepted: the TOCTOU fence requires the lock precede `Load` so a second concurrent caller sees the first's Claim | `internal/adapter/scheduler/scheduler.go` (`fireMu`, `lockFireName`); called from `fireOne` + `FireNow` |
| 33 | `liveOutcomeStore` (per-provider live-listing last-known-good + status, issue #262, ADR 0064) | `providerRegistry` (`app.Build`) | process | none needed (in-memory map, mutex-guarded; dies with the process) | **reset-by-design**: re-probed from scratch at the next Build (`probeToolhive`) — a last-known-good snapshot or an "unreachable"/"unauthorized" status is diagnostic/UX state, never session-of-record; nothing here needs to survive a restart | `internal/app/modellister.go` (`liveOutcomeStore`, `newLiveOutcomeStore`); constructed in `internal/app/registry.go` (`buildProviderRegistry`) |
| | _(traceability)_ the on-demand `/models`-open refresh cooldown (`refreshStaleModelsState.last`, issue #262) is a tiny mutex + timestamp CLOSED OVER by the `Service.SetModelsRefresher` closure — it is process-scoped, reset-by-design (a restart simply re-probes on the next `/models` open), and earns **no separate row**: it lives on the SAME already-inventoried refresh mechanism as row 18 (the live model-catalog refresh), not a new goroutine or persisted artifact. The per-call HTTP client the `openaicompat`/`toolhivellm` listers use is a plain per-call `*http.Client` (no long-lived pool, no row needed — mirrors the OpenRouter/Anthropic listers' existing non-rows) | | | | | `internal/app/modellister.go` (`refreshStaleModelsState`, `refreshStaleModels`) |
| 34 | DURABLE per-session pending-delivery queue (ADR 0075 fire-result-delivery Scenario 4; `port.DeliveryQueue` + the `FileDeliveryQueue` durable adapter) | composition (`internal/app`) — a `port.DeliveryQueue` wired at `app.Build` (the fire path enqueues, the loop's turn-boundary drain + the run-entry funnel dequeue) | session (keyed on the ORIGIN session id — the session that created the schedule — NOT a per-Run registry; the exactly-once ledger is session-scoped, so a note queued in run N drains in run N+1 if run N ends first) | the `FileDeliveryQueue` writes a per-session `.delivery.jsonl` append-only sidecar + a `.delivery.ledger.json` delivered-seq ledger under the SAME store dir as the session snapshot (the `.events.jsonl` precedent); files opened per call, never held. `NopDeliveryQueue` is the byte-identical no-delivery default (a deployment with delivery unwired sees nothing); `InMemoryDeliveryQueue` is the memstore-tier default (in-process, not durable — honest degradation to empty across a restart). A bounded backlog cap drops the OLDEST pending note with a WARN (the injected `port.Diagnostics`) rather than growing unboundedly on an overloaded origin | **persisted** (the `.delivery.jsonl` + `.delivery.ledger.json` sidecars survive the restart and are reloaded on the origin's next run-entry; the monotonic per-session seq is DERIVED from the durable enqueue log so a restart does not re-mint seq 1). The `NopDeliveryQueue` default and the `InMemoryDeliveryQueue` memstore-tier queue are NOT durable (byte-identical no-delivery / empty-after-restart for a restarted process) | `internal/app/delivery_queue.go` (`FileDeliveryQueue`, `InMemoryDeliveryQueue`); `engine/port/delivery_queue.go` (`DeliveryQueue`, `DeliveryNote`, `NopDeliveryQueue`); see List 2 row 23 |
| 35 | Per-session live event subscription registry (ADR 0075 fire-result-delivery Scenario 5; `Service.Subscribe`/`PublishSessionEvent` + the `subscriptions` map) | `internal/adapter/server.Service` (the relay — the loop stays storage-agnostic and never calls it) | process (an in-memory `map[session.SessionID]map[int64]chan session.Event`, guarded by a dedicated `subMu sync.RWMutex` separate from `s.mu` so a delivery-run Publish does not contend with the run/registry hot path) | each `Subscribe` returns a buffered (64) channel + an IDEMPOTENT unsubscribe func (a `sync.Once` — safe to call explicitly AND deferred); the subscriber goroutine owns draining. `PublishSessionEvent` holds `subMu`'s read lock while it sends NON-BLOCKING to each subscriber: a full channel DROPS the event (drain-to-discard — a dead client never wedges the delivery run). Unsubscribe and `Service.Close` close/remove channels under the write lock; no recover is needed. `Service.Close` marks `subscriptionsClosed`, and post-shutdown `Subscribe` registration is rejected with an immediately closed channel | **in-memory only** (a live-subscription registry has no restart fidelity to preserve — a subscriber that disconnects re-Subscribes on reconnect and catches missed deliveries via the `StreamSessionEvents` replay feed / the durable queue, List 1 row 34 / List 2 row 23). Nothing persisted; a restart drops the registry and every subscriber reconnects | `internal/adapter/server/service.go` (`Subscribe`, `PublishSessionEvent`, `subscriptions`); `internal/app/scheduler_delivery_run.go` (the delivery driver's Publish fan-out) |
| 36 | `escapePolicy` per-root classifier cache (`escapePolicy.clfs`: session workspace root → `*escapeClassifier`, built once per root; path-escape-posture plan, docs/acceptance/path-escape-posture.md AC-W2-F3) | `app.Build` (ONE instance wraps the shared main-engine permission policy; per-session root-awareness comes from `ws.Root()` on every `Evaluate`, never a per-session policy) | process (entries live as long as the shared policy; dies with the process at Build teardown) | lazily created under a mutex on first `Evaluate` for a root, never pruned. **Cardinality is deployment-bounded by construction**: one entry per DISTINCT session workspace root the process ever classifies — the same order as the (already capped) live-session count plus the bounded historical-session set the run-entry funnel rehydrates, NOT per-session-per-tool-call growth; the value is a pair of canonicalized string slices (no `*os.Root`, no open handles), so a stale entry costs bytes, not fds. An LRU was rejected: eviction only ever drops a REBUILDABLE classifier (the next `Evaluate` re-canonicalizes the root and rebuilds), so bounding buys nothing the rebuild does not already make cheap | **reconstructible** (a restarted process rebuilds the shared policy at the next Build and re-derives each root's classifier lazily on the next `Evaluate`; the cache is a pure derivation of the session root — nothing persisted, decision = derive) | `internal/app/escapepolicy.go` (`escapePolicy.clfs`, `classifierFor`); `internal/app/escapeclassifier.go` (`escapeClassifier`) |
| 37 | mecatequi OTLP metrics push PeriodicReader + flush-on-exit (ADR 0098) | `cmd/mecatequi` (`realMain`, via `internal/cliconfig.HeadlessTelemetry`) | process (OPT-IN: only when `--otlp-metrics-endpoint` is set) | the `defer flushTelemetry` runs BEFORE `defer built.Close()` (LIFO → flush first), bounded by `--otlp-shutdown-timeout`; `Providers.Shutdown` flushes + stops the periodic reader. `os.Exit` then kills any lingering in-flight export goroutine (a short-lived run does not drain it) | **reset-by-design**: a single-shot run has no scrape state to persist; the periodic reader is process-local and the pushed metrics are the collector's record, not mecatl's. Nothing here needs a List 2 row | `cmd/mecatequi/main.go` (`realMain`); `cmd/mecatequi/observability.go` (`buildObservability`, `flushTelemetry`); `internal/cliconfig/telemetry.go` (`HeadlessTelemetry`); `internal/adapter/telemetry/otlp.go` (`newMetricPushReader`) |
| 38 | mecak8s `/metrics` loopback listener (ADR 0098) | `cmd/mecak8s` (`serve`) | process (OPT-IN: only when `--metrics-addr` is set; loopback-only, fail-closed at parse time) | the `metricsSrv *http.Server` joins the `errCh` set and the `boundedShutdown` sequence (its `Shutdown` runs alongside the API listener's); SIGTERM also flushes OTLP via the `defer flushTelemetry` (LIFO before `built.Close()`) | **reset-by-design**: the prometheus reader is process-local; the scraped metrics are the scraper's record. The admin mux (`/metrics` + pprof/expvar) is loopback-only and carries no durable state. Nothing here needs a List 2 row | `cmd/mecak8s/serve.go` (`serve`, `boundedShutdown`, `isLoopbackAddr`); `cmd/mecak8s/observability.go` (`buildObservability`, `flushTelemetry`); `internal/adapter/telemetry/adminmux.go` (`NewAdminMux`) |
| 39 | ToolHive-LLM direct-mode OIDC token source (issue #265, ADR 0102) | `internal/app` — built ONCE per Build inside `newDirectGatewayEntry` and captured by the `bearerRoundTripper`; the underlying `*llm.TokenSource` is toolhive's `pkg/auth/tokensource.OAuthTokenSource` | process (OPT-IN: constructed ONLY when the resolved routing mode is `direct` — the proxy path allocates none of this and stays byte-identical to pre-#265) | none needed and none exists: it holds no goroutine, no fd and no timer — an in-memory access token + expiry behind its own mutex, refreshed lazily on the per-request `Token(ctx)` call and dying with the process. It is deliberately NOT re-minted per session: every per-session/heal engine re-mint appends the SAME `WithHTTPClient`, so one token source serves every session (`construct()` closes over `extra`). Note the mutex serializes `Token` across concurrent sessions, so a slow IdP refresh blocks other requests for its duration — bounded by each caller's request ctx | **reconstructible** (a restarted process rebuilds it at the next Build and re-derives an access token from the keyring-held refresh token; decision = derive). Nothing enters a snapshot: the access token is short-lived derived material, and the refresh token already survives outside mecatl in the OS keyring with only its REFERENCE (`CachedRefreshTokenRef`) in ToolHive's config — so no List 2 row | `internal/adapter/toolhivellm/tokensource.go` (`DirectTokenSource`, `buildTokenSource`); consumed by `internal/app/registry.go` (`newDirectGatewayEntry`, `bearerRoundTripper`) |
| 40 | OS-keyring / D-Bus connection behind the direct-mode secrets provider (issue #265, ADR 0102) | toolhive's `pkg/auth/secrets` (`GetSystemSecretsProvider`), opened transitively by row 39's construction; mecatl never holds the handle | process (OPT-IN with row 39; on Linux this is a `godbus` connection with its own reader/writer goroutines) | NOT mecatl-owned — no `Close` seam is exposed and none is folded into Build's `closeAll`; the connection is process-scoped and released at exit. This is the accepted residual, and it is why `internal/app/leakmain_test.go` pins `godbus/dbus/v5.newConn.func1` + `(*Conn).inWorker` by top-of-stack (a narrow pin, NOT a blanket suppression — any other leak still fails the gate) | **reconstructible** (re-opened lazily by the next Build's token-source construction; it is a transport to the keyring, never state-of-record — the credential it fetches is the keyring's, decision = derive). No List 2 row | `internal/adapter/toolhivellm/tokensource.go` (`buildTokenSource`); the goleak pins live in `internal/app/leakmain_test.go` (`TestMain`) |
| 41 | OIDC token-validator JWKS cache + its background key-rotation refresh (caller identity, ADR 0100 decision 3/7 and ADR 0103; the `toolhive-core/authn` validator wrapped by the opt-in `authn/oidc` module and adapted to `server.PrincipalValidator`) | the `authn/oidc.Validator` instance, constructed once per process through `internal/cliconfig/oidc.go` (`OIDCValidator`) and handed to `server.SecurityConfig.Validator`; the module owns the reusable lifecycle seam, never the JWT/JWKS mechanics | process (OPT-IN: only when `--oidc-issuer` is set; the zero value is identity OFF and allocates nothing) | **explicit teardown at the edge**, NOT the root context's cancel: the validator's background refresh is stopped by its OWN `Close()`, and cancelling a context does not call it. `internal/adapter/server/authn.go` (`Authenticator.Close`) type-asserts an OPTIONAL `io.Closer` on the configured validator (the `port.HookApprovalLearner` idiom — `PrincipalValidator` stays single-method, so a fake without teardown needs none) and closes it once (`sync.Once`); both mains `defer auth.Close()` (`cmd/mecated/main.go`, `cmd/mecak8s/serve.go`). The SERVER-ROOT context is still what the constructor is handed — deliberately NOT a per-request one, which would tear key rotation down with the first request — but it bounds in-flight fetches, not the refresh loop's lifetime. The refresh goroutine has no caller and therefore runs under the explicit system principal `mecatl:internal / jwks-refresh` (`internal/syscaller/syscaller.go` (`RootJWKSRefresh`)) | **reconstructible** (a restarted process re-resolves the flags and re-fetches the key set on the next Build; nothing is persisted and nothing should be — a cached signing key is a derivation of the IdP's live JWKS). Cached-key trust is bounded by `--oidc-max-jwks-staleness` (1h default; 0 explicitly disables the bound; ADR 0101) | `authn/oidc/oidc.go` (`Validator`, `NewValidator`, `Close`); `internal/cliconfig/oidc.go` (`OIDCConfig`, `OIDCValidator`, `RegisterOIDCFlags`); `internal/adapter/server/authn.go` (`PrincipalValidator`); `internal/syscaller/syscaller.go` (`RootJWKSRefresh`) |
| 42 | osfs same-path mutation lock stripes (ADR 0104) | `internal/adapter/osfs` package | process (fixed array of 256 `sync.Mutex` values, physical-target `hash/maphash` stripe) | none needed: fixed allocation, no goroutine/fd/map entry, released with the process. Existing targets canonicalize fully; missing targets canonicalize parent+basename, so stable symlink aliases converge. A collision only serializes unrelated mutations; concrete osfs bootstrap `Write` plus Workspace `CreateFile`/`ReplaceFile` use the same stripe across Workspace instances. The guarantee is process-scoped and assumes a non-cooperating writer does not race target existence or symlink identity during pre-lock canonicalization | **reset-by-design**: pure synchronization, no state-of-record. Re-created as zero-value mutexes at process start; backend file contents remain authoritative. Multi-process deployments receive only per-backend-handle atomicity until remote backend CAS lands. No List 2 row | `internal/adapter/osfs/osfs.go` (`pathLocks`, `pathLock`) |
| 43 | Stale-session sweep goroutine (Phase 6, issue #475) | `internal/app` (`startStaleSessionReconcile`) | process (unconditional — always runs a startup sweep then a persistent ticker; there is no operator-facing flag to disable it, only the per-pass `LeaseSweepDisabled` sticky-disable when a wired lease backend does not support leasing) | the returned closer `cancel()`s the sweep's own ctx AND `wg.Wait()`s the goroutine (mirroring `startLiveModelRefresh`'s exact idiom), folded into `Built.Close`'s `closeAll` so a caller that never cancels `Build`'s own ctx (the common test-fixture shape) still gets a clean, race-free teardown — `Built.Close()` cannot return while a sweep pass is mid-`SettleIfStale` | **derived** (a restarted process starts a fresh sweep at the next `Build`'s startup pass; nothing about the sweep itself is state-of-record — it repairs the ALREADY-persisted `session.Session` snapshot, so there is nothing to carry over). No List 2 row | `internal/app/session_reconcile.go` (`startStaleSessionReconcile`, `sweepStaleSessions`); wired at `internal/app/build.go` |
| 44 | Automatic-learning interval admission counter (#507) | `internal/app` (`learningAdmission`), shared by the main and per-session engines | process lifetime | no goroutine or handle; dies with composition | **reset-by-design**: the legacy interval is best-effort process-local debounce, not accepted learning state; restart resets the count while durable user-model facts remain authoritative. See List 2 row 26 | `internal/app/usermodelreview.go` (`learningAdmission`) |
| 45 | Operator-profile last-good snapshot (#508 slice) | `agent.Run` | run | dies with the run | **reset-by-design**: a new run reads the durable source; this is fail-soft continuity, never state-of-record. See List 2 row 27 | `engine/agent/loop.go` (`Run.operatorProfile`, `refreshOperatorProfile`) |
| 46 | Durable staged-learning proposal document + stable flock (issue #509, ADR 0109) | `app.Build` constructs `reflectionstore.Store` in `reflections/` beside the configured or conventional user-model path in automatic modes; Off installs a `lazyProposalRepository` and constructs neither directory nor flock until an explicit reflection/proposal operation | process handle over principal/project-partitioned durable data | the flock is acquired and released per operation; files are opened per call. A bounded JSON document is committed with temp-file fsync, rename, and directory fsync; empty startup creates no proposal document | **persisted**: proposals, CAS versions, bounded decisions, and promotion receipts reload from the document. `promoting` plus a memory revision carrying the same `ProposalID` reconciles after restart without a second write. See List 2 row 29 | `internal/adapter/reflectionstore/store.go` (`Store`, `locked`, `save`); `engine/adapter/memorypromotion/memorypromotion.go` (`Process`); `internal/app/build.go` |
| 47 | Evidence-reflection worker pool, fair per-principal queues, singleflight map, and bounded receipt cache (issue #509, ADR 0109) | `app.Build` constructs exactly one `reflectionCoordinator` when automatic learning is Review/Auto; Off constructs none and explicit reflection runs synchronously | process (global worker count; bounded per-principal FIFO queues with fair principal rotation) | every job has a timeout and per-job byte cap; oversized raw trajectory material is rejected before projection/marshal or queue allocation, and retained jobs share an aggregate queued-byte cap. The effective receipt bound is raised to at least queue capacity plus workers, then a live receipt is reserved before admission; receipt pressure rejects rather than accepting an unreportable job. The coordinator owns a lifecycle context. `Built.Close` stops admission, publishes bounded closed receipts for queued waiters, clears queued/pending state, cancels active jobs, and joins every started worker before returning. Completed receipts evict oldest-first, while principal+session+trajectory digest singleflight collapses duplicates | **reset-by-design**: queued/running jobs and bounded receipts are transient coordination state. Durable staged proposals and promoted memory remain authoritative; restart does not retrospectively sweep old completed sessions. See List 2 row 30 | `internal/app/reflection_coordinator.go` (`reflectionCoordinator`, `newReflectionCoordinator`, `Close`); `internal/app/reflection_observer.go` (`reflectionObserver`) |
| 48 | Durable agent-owned skill manifest, immutable version files, bounded receipt index, and stable flock (issue #510, ADR 0111) | `app.Build` constructs one lazy `skillstore.Store` beside the user-model store and shares it with reflection, server lifecycle APIs, and every catalog assembly | process handle over principal/project-partitioned durable data | the stable `skills.lock` flock is acquired and released per operation; files are opened no-follow and regular-file status is checked from the opened descriptor. `manifest.json` (including the bounded, stable-order receipt index) and each content-addressed `versions/<version>/SKILL.md` use temp-file fsync, rename, and directory fsync. Empty startup is lazy. A crash before manifest commit can leave only an unreferenced immutable version, which reopen safely ignores | **persisted**: bounded lifecycle metadata, provenance, validation disposition, evaluations, active selection, immutable bodies, and historical-version receipt pages reload together. Expired receipt cursors fail closed. Exact convergence preserves the stricter similarity disposition. Cross-store proposal linkage is create-then-CAS-link and reconciles by ProposalID/SkillID without a duplicate. See List 2 row 31 | `internal/adapter/skillstore/store.go` (`Store`, `locked`, `save`, `ensureVersion`, `ListSkillReceipts`); `internal/adapter/skillstore/materialize.go` (`MaterializeProposal`) |
| 49 | Partitioned live learned-skill generations and publication gate (issue #510, ADR 0111) | `app.Build` constructs one `skillfs.AtomicCatalog` plus one `learnedSkillPublication`; caller-bound per-session catalogs and the server lister select immutable views from them | process (one mutex plus bounded immutable path-free generations keyed by principal/project; no goroutine, watcher, path, asset materialization, or workspace root) | one gate serializes collision check, durable transition, authoritative reread, generation swap, and partition-local failure quarantine. Refresh retains unrelated partitions and immutable external entries. Each tool request uses one caller-bound view for Spec/inventory/Execute. Caller-scoped list/run hydration reconciles global and admitted project partitions; uncertain reads clear that partition. Build teardown needs no explicit close | **derived** from List 2 row 31's active durable versions plus the build-time immutable external skill snapshot. Views/generations reset on restart and lazily reconstruct for the authenticated caller; a crash after durable activation but before publication heals without another mutation. No separate persisted catalog state | `engine/adapter/skillfs/atomic.go` (`AtomicCatalog`, `RefreshPartitions`, `View`, `LiveTool`); `internal/app/learned_skills.go` (`learnedSkillPublication`, `learnedSkillPublisher`); `internal/app/catalog.go` (`registerSkillFamily`) |
| 50 | Local encrypted credential-store records, stable flock sentinels, and store-owned encryption key (issue #519, ADR 0108) | canonical MCP profiles in `internal/cliconfig` (`MCPProfiles`), transferred to `app.Build`; explicit login borrows the same Store | namespace/store handle for the in-memory key; durable per-record files under the explicitly selected root | operations open and close flock/data/temp files per call; stable `.lock` sentinels and encrypted `.cred` records persist; `EncryptedFileStore.Close` closes the rooted namespace handle and clears the sole long-lived key copy it owns. A crash may leave an owner-only encrypted temp, deliberately not swept | **persisted / explicitly reattached**: encrypted records remain authoritative on disk; login and serving processes select the same explicit root/namespace and inject the same externally acquired 32-byte key. Rotated refresh credentials survive a second process restart. No List 2 row: credentials are adapter-owned durable source of truth, not session/run state | `internal/adapter/credentialstore/encrypted_file.go` (`EncryptedFileStore`, `NewEncryptedFile`, `Close`); `internal/adapter/credentialstore/envelope.go`; `internal/cliconfig/mcpprofile.go` (`LoadMCPProfiles`) |
| 51 | Optional per-server MCP OAuth controller: one official handler, token source/CAS version, authorization flight, and dedicated hardened HTTP client's idle pool; plus its credential record (issue #521/#542, ADR 0110/0111) | `internal/adapter/mcp.Server` | connected server / one credential identity | `Server.Close` closes the MCP session first, then idempotently closes the controller's HTTP idle pool; the injected mutable Store or read-only Reader handle is borrowed and remains injector-owned. No background refresh, listener, browser, or timer goroutine | **persisted / explicitly reattached when mutable; source-defined when read-only**: a new controller derives the same opaque key from injected identity/config, reloads the injected persistence source, and reattaches through the official handler's initial-token-source hook. Opt-in in-memory refresh deliberately resets on restart and the old source record is re-read. In-memory flight/idle state resets. No List 2 row: credentials are adapter-owned state, not session/run state | `internal/adapter/mcp/oauth.go` (`OAuthController`, `NewOAuthController`, `Close`); `internal/adapter/mcp/oauth_tokensource.go`; `internal/adapter/mcp/mcp.go` (`Close`) |
| 52 | Opt-in MCP OAuth loopback interaction: random IPv4 listener, bounded callback HTTP server, runtime serialization gate, and optional browser child (issue #522, ADR 0112) | `mcp/oauthlogin.Runtime`; the one-shot caller owns the `Authorize` operation | one authorization operation; the gate lives with the explicitly constructed runtime | every return path cancels presentation, publishes a terminal callback outcome, performs detached bounded `http.Server.Shutdown`, closes the listener, joins `Serve`, and only then releases the runtime gate. The fixed-argv browser command inherits the operation context and is killed on cancellation; no refresh goroutine exists | **per-operation / reset-by-design**: listener, server, callback state, and browser process must not survive. The resulting credential is row 51's durable adapter record. No List 2 row because no session/run state is held | `mcp/oauthlogin/runtime.go` (`Runtime`, `Authorize`, `stopServer`); `mcp/oauthlogin/callback.go`; `mcp/oauthlogin/browser.go`; invoked by `internal/app/mcplogin.go` (`LoginMCP`) |
| 53 | Explicit environment credential Reader handle (issue #542, ADR 0111) | canonical MCP environment profiles loaded by `internal/cliconfig.MCPProfiles` and transferred to `app.Build`; used by mecak8s/mecatequi/mecated serving without a presenter | namespace/key/environment-name/lookup tuple | no goroutine, fd, cache, mutation, or global lookup; `EnvironmentReader.Close` idempotently seals the borrowed lookup handle. Each `Get` snapshots its immutable lookup configuration under the lifecycle read lock, invokes the host callback without that lock, then rechecks closure before returning | **explicitly reattached / source-defined**: restart reconstructs the Reader with the same tuple and receives the process/pod's current environment snapshot. In-memory refresh is not written back. Kubernetes Secret env rotation requires an external controller and pod restart, or a future Secret `resourceVersion` CAS writer. No List 2 row: this is adapter credential state, not session/run state | `internal/adapter/credentialstore/environment.go` (`EnvironmentReader`, `NewEnvironment`, `Close`); `internal/cliconfig/mcpprofile.go` (`LoadMCPProfiles`) |
| 54 | Canonical MCP profile loader's shared encrypted Stores and per-profile environment Readers (issue #523, ADR 0113) | `internal/cliconfig` (`MCPProfiles`), transferred to `app.Build` through `Config.MCPProfileLifecycle` | process / one loaded operator profile set; local Stores are shared by `(root, key-env reference)` within the load | partial-load failure closes every source already opened; successful Build closes the global MCP manager/controllers first, then the profile lifecycle, and `MCPProfiles.Close` closes every distinct Store/Reader exactly once. The default/no-profile path opens nothing and owns nothing | **persisted / explicitly reattached for local; source-defined for environment**: restart reloads operator metadata and references, derives the identical opaque record key, and reopens row 50/53. Process-local read-only refresh resets by design. No List 2 row because credentials remain adapter state, not session/run state | `internal/cliconfig/mcpprofile.go` (`LoadMCPProfiles`, `MCPProfiles`, `Close`); `internal/app/build.go` (`Config.MCPProfileLifecycle`, `Build`) |

| 55 | Automatic-learning sliding reservations, per-principal cooldown map, and completed trajectory-digest LRU (ADR 0114) | `app.Build` constructs one `automaticAdmissionController` only when automatic learning is enabled and shares it across main/per-session reflection observers | process (one mutex; one-hour process/principal count+reserved-token entries, per-principal weighted cooldowns, and a 24-hour completed-digest LRU capped at 1024; digests contain no raw identity/text labels) | no goroutine or durable handle. Coordinator capacity and in-flight duplicate checks run before the reservation callback; accepted failures/timeouts/abstentions retain reservations. `Built.Close` closes the coordinator, and the controller dies with composition. No shutdown/startup catch-up | **reset-by-design**: restart resets windows, cooldowns, and completed digests. Durable proposal IDs/CAS remain authoritative and converge any retry. Budgets are deliberately process-local; multiple replicas multiply aggregate capacity. See List 2 row 32 | `internal/app/learning_controller.go` (`automaticAdmissionController`, `reserve`, `complete`); `internal/app/reflection_observer.go` (`submit`) |

**Configurable-learning-trigger re-audit (List 1 / List 2 — ADR 0114).** The controller is List 1 row 55 and its restart-reset policy is List 2 row 32. It adds no goroutine and no shutdown catch-up. The existing coordinator remains row 47 and is no longer constructed in Off.

**Evidence-reflection re-audit (List 1 / List 2 — issue #509, ADR 0109).** Standard
wires the durable proposal artifact and its per-operation flock (List 1 row 46), plus the bounded Build-owned coordinator (row 47). Proposal lifecycle state survives a crash (List 2 row 29); transient queue, singleflight, and receipt state deliberately resets without a retrospective session sweep (List 2 row 30).

**Learned-skill re-audit (List 1 / List 2 — issue #510, ADR 0111).** The durable
manifest/flock is row 48 and the no-goroutine live generation pointer is row 49. Durable lifecycle,
proposal linkage, and crash-window recovery are List 2 row 31; the pointer is reconstructed from
that state and the immutable external snapshot. The skill pipeline is synchronous inside row 47's
existing job and therefore adds no queue, worker, breaker, or receipt cache.

**Hermetic MCP OAuth acceptance re-audit (List 1 / List 2 — issue #524).** The
cross-boundary test adds no production resource. It proves rows 50–54 compose across explicit
login, two serving processes, durable refresh rotation, reconnect, and manager-before-source
teardown. Credentials remain adapter-owned source-of-truth state, so List 2 still gains NO
row.

**Operator MCP profile-loader re-audit (List 1 / List 2 — issue #523, ADR 0113).** The loader adds
ONE optional owner for the already-inventoried credential source handles (List 1 row 54).
It shares local handles within one load, closes partial construction, and transfers successful
loads to Build so OAuth controllers stop before their borrowed sources. No config means no
lookup or handle. Restart fidelity remains row 50/53's explicit reattachment, so List 2 gains
NO row.

**MCP OAuth loopback re-audit (List 1 / List 2 — issue #522, ADR 0112).** The
explicit host runtime adds ONE per-operation resource family (List 1 row 52). Cleanup joins
the callback server before `Authorize` returns; a shared runtime's channel gate is only
serialization state. The durable result remains row 51's credential record. No session/run
state is added, so List 2 gains NO row.

**Read-only credential-source re-audit (List 1 / List 2 — issue #542, ADR 0111).**
The explicit environment Reader adds ONE optional outlives-a-call handle (List 1 row 53),
owned by the canonical profile loader and closed by `MCPProfiles.Close`. It allocates no
goroutine, file descriptor, cache, mutable record, or default environment lookup. Reader-only
OAuth updates row 51's restart decision: an opt-in memory-only refresh resets and the unchanged
source record is re-read. List 2 gains NO row because credentials remain adapter-owned rather
than session/run state.

**MCP OAuth controller re-audit (List 1 / List 2 — issue #521/#542, ADR 0110/0111).** The
optional controller adds ONE outlives-a-call resource (List 1 row 51), owned by one MCP
`Server` and absent unless an embedding supplies `ServerConfig.OAuth`. Its credential record
is adapter credential state and therefore adds NO List 2 session/run row. Reattachment
requires the same explicit subject, resource, issuer, client registration, Reader, and
external source configuration; mutable durability additionally requires the same writer
and key. No composition or implicit key acquisition is added.

**Credential-store re-audit (List 1 / List 2 — issue #519, ADR 0108).** The
substrate adds ONE optional outlives-a-call resource (List 1 row 50): durable encrypted
records and stable lock sentinels plus the handle-owned key. Nothing constructs it by
default; ownership begins only when an explicit consumer (such as ADR 0110's optional MCP
OAuth controller embedding) injects a store. List 2 gains
NO row because this issue persists no session/run state and the credential records are
the adapter's own source of truth. Reattachment requires the future external key source
to inject the same key; the store provides no environment, XDG, password, or fallback
acquisition path.

**Issue #388 re-audit (bounded embedded-mecatui shutdown).** #388 added NO new
outlives-a-call resource row. The work BOUNDS existing rows' cleanup, it does not add
a resource: the package-level timeout `var`s (`gracefulStopTimeout`,
`compositionCloseTimeout`, `stopLeadershipJoinTimeout`, `engineCloseTimeout`,
`managerCloseTimeout`, the 45s `runCleanup` cap) are compile-time configuration of
already-inventoried goroutines/servers (rows 1, 2, 11, 27, 28, 30), and the
goroutines spawned on the bounded-timeout paths are abandoned-by-design at process
exit (a shutdown-only posture, documented in `docs/design/IMPLEMENTATION-NOTES.md`
"Bounded shutdown"). The one behavioural change to an existing row: `Service.Close`
now CANCELS in-flight runs (row 9) via `run.Cancel()` on shutdown, EXCLUDING runs
parked on a permission ask whose durable awaiting snapshot is the Phase-2 resume
point (row 14) — the race-free `runState.awaiting` atomic, set by `Persist` only
AFTER the durable `Save` lands (the H1 ordering). That exclusion PRESERVES row 14's
re-attach contract rather than weakening it (a cancelled-over write would have
destroyed the resumable awaiting snapshot). A shutdown-cancelled scheduled fire is
persisted terminal (`cancelled`, Interrupt-recoverable) via
`settleFireTerminalSnapshot` calling the existing `Service.Persist` (row 8's
jsonlstore) — no new artifact. The `cmd/mecatui` signal-handler goroutine
(`setupSignalHandler`) is process-scoped, retired on the normal quit path via
`forceExit`, and never outlives the process; decision = derive.

**Issue #386 re-audit (in-flight scheduled-fire state).** #386 added NO new List-1
row. The in-flight fire state is a PERSISTED lifecycle stage in the durable
`ScheduleStore` (row 8's jsonlstore / the redisstore sibling) — `RecordFireStart` /
`RecordFireProgress` / the `LastFireStartedAt`/`LastFireProgressAt`/`FireDeadline`
fields live in the store, not a lost-on-restart in-memory resource, which is exactly
why the crash-distinction criterion is satisfiable. The stale-fire reconciler is a
new STEP in the already-inventoried scheduler tick goroutine (row 30), not a new
goroutine/LRU; it detects store-only and reconciles via a composition callback. The
`defaultFireTimeout` `time.AfterFunc` watchdog is per-fire run-scoped (dies with the
fire's run, row 9). The started-notice rides the existing `DeliveryQueue` (row 34).
`decision = derive` throughout.

### Does resource-lifetime management earn a seam now?

The kit inventory's question, answered: **no, not yet; the hand-managed lifecycles
suffice until Phase 4.** The table sorts cleanly into three clusters, and none of
them wants a generic resource manager today:

- **Process-scoped infrastructure (rows 1, 3-5, 7, 16-20)** is reconstructible from
  config at the next Build. The only genuine restart liability in the cluster is row
  3's preserved-fork directories, which leak on crash because the LRU registry (the
  only thing that knows to delete them) is in-memory. That is small, bounded by the
  LRU cap per process lifetime, FS-profile-only (no forks exist under no-fs), and a
  startup sweep of the fork-dir naming convention would fix it without any
  abstraction. Row 20 (the WebSearch provider) is a process-scoped resource too, but
  with no crash-leak shape: a stateless `http.Client` plus an in-process semaphore,
  nothing on disk to orphan.
- **Session-scoped server state (rows 2, 6, 11, 12, 21)** is exactly the rehydration
  surface Phases 1-3 address one row at a time: row 11/12 via profile+selector in
  the snapshot (Phase 1), row 6 via verdict events replayed into the permstore
  (Phase 3b, SHIPPED), row 2 stays client-owned by design. Row 21 (the modelhook guardrail breakers) resets fail-safe
  and is observability/spend-bounding only, not correctness-critical, so it stays
  reset-by-design.
- **Run-scoped ephemera (rows 9, 13, 14, 22)** dies with the run by design; Phase 2
  changes what "the run died" means for the one row that matters (14), without making
  the others durable.

The first thing that would force a real seam is cross-process exclusion (leasing),
which is a driver-protocol concern (Phase 4), not an in-process resource manager. The
trip-wire to revisit: if a future arc adds a fourth process-scoped resource with a
crash-leak shape like row 3, or if leasing lands and wants a uniform "what does this
process hold" enumeration, build the seam then, against this inventory. The three
post-`f1f4e31` additions (rows 20-22) do NOT trip it: row 20 is process-scoped but
leak-free, rows 21-22 are session/run-scoped, so the conclusion stands.

## List 2: rehydrate-fidelity ledger

Everything a live run or session holds that is NOT reconstructed by loading the
sessnap snapshot. The snapshot truth is `engine/adapter/sessnap/sessnap.go:32-43`:
`id, state, mode, limits, counters, workspace, created_at, messages[]` (each with
`role, text, tool_calls, tool_result, reasoning, phase, parts`), `pending`,
`stop_reason`. Nothing else is persisted.

Decisions: **persist-in-snapshot** (additive field), **derive** (recomputable from
what is persisted), **reset-by-design** (documented, acceptable),
**fix-via-event-log** (Phase 3 makes it durable).

| # | Item | Where it lives | On restart today | Decision | Phase |
|---|---|---|---|---|---|
| 1 | Session profile (no-fs vs default) | **SHIPPED (Phase 1)**: persisted as an additive opaque `Profile` label on the aggregate (`session.Session`) and the snapshot (`sessnap.Snapshot`); the empty-workspace inference stays the second defense (`needsRehydration`, `Service.rehydrateSession`) | correctly rehydrated from the persisted label, with the inference still covering a pre-label snapshot | persist-in-snapshot (inference stays as second defense) | 1 (SHIPPED) |
| 2 | Provider/model selector | **SHIPPED (Phase 1)**: persisted as the opaque `ProviderID`/`ModelID` label pair on the aggregate (`session.Session`) + snapshot (`sessnap.Snapshot`); `Service.rehydrateSession` re-derives the SAME engine via the factory from the persisted pair (no longer the default-provider floor) | rebuilt on the SAME provider+model via the factory; a default session (empty pair) rides the shared engine. The compaction context-window is NO LONGER a rehydration trigger (issue #66, superseded by the resolve-at-use unification): every engine carries a live-first `Deps.ContextWindow` closure (`reg.windowResolver`) read at use, so a live-only default model self-corrects to its live window on the next turn with no rebuild | persist-in-snapshot (selector); rehydration re-derives the engine via the factory; the engine COMPACTION window is derived at USE via the resolve-at-use closure (decision = derive) | 1 (SHIPPED); window unified to resolve-at-use #66 |
| 3 | `session.Usage` (cumulative run tokens) | **SHIPPED (Phase 1)**: a `Usage` field on the aggregate (`session.Session`) accumulated by `RecordUsage`, persisted as the additive `usage` snapshot field (`sessnap.Snapshot`); the budget brake (`budgetExhausted`) is evaluated against the cumulative `sess.Usage`, and `resetToIdle` DELIBERATELY preserves it. The additive `ReasoningTokens` field (issue #213) rides this SAME snapshot path — it is a subset of `OutputTokens` (not added to `TotalTokens()`), so no new row is strictly required; it is noted here per the inventory discipline | the `MaxRunTokens` brake continues across restart instead of re-granting a fresh budget; reasoning spend is preserved as observability, not budget | persist-in-snapshot (additive `usage` field; the budget reads the cumulative aggregate; `ReasoningTokens` rides the same additive field) | 1 (SHIPPED) |
| 4 | permstore allow-always rules | `permstore.Memory.bySession` (`engine/adapter/permstore/permstore.go:48`) | **SHIPPED (Phase 3b)**: on a post-restart load, `internal/app/approvalreplay.go` (`replayApprovals`) (invoked once per id by `internal/adapter/server/service.go` (`maybeReplayApprovals`)) reads the logged allow-always `EvApproval`s, correlates each metadata-only askID back to its ToolCall in the loaded conversation (the askID encodes the call id, `engine/agent/dispatch.go` (`newAskID`)), and re-drives the existing `Policy.Learn` to re-derive the real rule from history; the previously-allow-always'd tool is NOT re-asked | fix-via-event-log (verdict events replayed into permstore; metadata-only event + real rule from history = no leak, no port widened) | 3b (SHIPPED) |
| 5 | The pending PARENT ask | **SHIPPED (Phase 2)**: the data IS in the snapshot (`Pending`, `engine/adapter/sessnap/sessnap.go` (`Snapshot`), restored via `PauseForApproval`); the LIVENESS is now recovered too: `Approve`/`Deny` on a runless awaiting session loads the snapshot, re-enters the loop AT the ask via `engine/agent/loop.go` (`ResumeApproval`), and drives to completion | resolved: `internal/adapter/server/service.go` (`resumeFromAwaiting`) rebuilds the engine and re-enters; `ErrNoActiveRun` is no longer terminal for awaiting (still terminal for idle/completed/cancelled/failed) | the resume-from-awaiting loop entry (durability was already correct; Phase 2 adds the liveness) | 2 (SHIPPED) |
| 6 | Pending CHILD asks (childAskRouter) | run-scoped in-memory routing of child-namespaced askIDs | lost with the run | reset-by-design AND verified structurally unreachable through the resume seam (`engine/agent/dispatch.go` (`driveFromAwaiting`), the Q4 note): a surfaced child ask sets the CHILD session's `pending` (its own loop calls `PauseForApproval`), never the PARENT's (the parent stays `StateRunning` inside the delegation tool call), and the server persists/resumes only top-level runs, so a restored `StateAwaiting` session ALWAYS holds a parent-OWN ask. No `PendingAsk` marker field was needed; were a surfaced-child ask ever persisted onto a parent, it would close out as an ordinary unanswered sibling (the honest aborted-result wording), not a silent stall | reset-by-design; Phase 2 re-enters at the PARENT ask only, child asks non-rehydratable (verified, honest, not silent) | 2 (verified) |
| 7 | Background children | `childRunRegistry` (`engine/agent/childregistry.go:131`), run-scoped; child sessions persist via `WithSubagentStore` (`subagent.go:599`), and parallel branches likewise via `WithParallelStore` (`parallel-<callID>-<i>`, commit `fe9ffe5`, forensically loadable through the `{subagent-, parallel-}` prefix gate) | the running children die un-drained; their persisted sessions remain individually loadable/resumable (`resume:` / `InspectSubagent`), but nothing reconnects them to the parent | reset-by-design for v1; session-scoped detach is issue #28, gated on the event log | 3c → #28 |
| 8 | Workspace read-version ledger (ADR 0104) | in-memory map on each live Workspace (`osfs`, `memfs`, ACP), keyed with I/O-free lexical Clean/Rel normalization and storing opaque authoritative `FileVersion` tokens (physical symlink aliases may conservatively miss) | the Workspace/ledger is lost. The first Edit or existing-file Write on a rebuilt Workspace is REFUSED ("not read this session") until Read records a fresh version. Fail-safe, never silently wrong; no-fs has an inert ledger | reset-by-design. The ledger is scoped to the live Workspace/environment instance; the default Service factory creates a fresh Workspace per run, while explicit no-fs/ACP overrides retain their existing owner-defined lifetime. Persisting versions is deferred until a remote environment/backend can define durable identity and true CAS (ADR 0104) | deferred |
| 9 | Pre-compaction history | `maybeCompact` rewrites the conversation via `ReplaceHistory` and that is what the next Save persists | **SHIPPED (Phase 3b)**: `engine/agent/loop.go` (`maybeCompact`) captures the pre-compaction conversation BEFORE `ReplaceHistory` mutates it and emits it as `EvCompactionArchive` (`engine/session/event.go` (`CompactionArchivePayload`)) AFTER a successful replace (the degrade path emits nothing); the relay Appends it to the durable log, so the replaced span is recoverable via `EventLog.Read` and the durable record stops being lossy. The archived span is the parent's OWN conversation, so no gauntlet-#7 surface | fix-via-event-log (archive the replaced span before `ReplaceHistory`; the loop only emits, the relay persists) | 3b (SHIPPED) |
| 10 | Mid-round team state | the `team.Team` aggregate (roster, goal, tasks, mailbox, findings; `engine/team/team.go:179`) and `Supervisor.members` runtime (`engine/agent/teamsupervisor.go:298`) are in-memory only; `Service.teams` (`service.go:369`) likewise. ONLY member sessions persist (`persistMember`, `teamsupervisor.go:1187`, under `MemberSessionID`, `teamsupervisor.go:1509`) | a mid-round team is unrecoverable: member transcripts survive as orphan sessions, the coordination state (who was assigned what, the findings ledger, the round number, the goal) is gone; there is no resume-team seam | reset-by-design for v1 (teams are run-scoped work units); the event log is the prerequisite for anything better, and re-creating the team from scratch is the documented recovery | 3 (prereq), honest note now |
| 11 | The event stream | `Run.events`, a buffered channel (cap 64, `engine/agent/loop.go:575`), relayed by gRPC `Converse` / HTTP SSE | **SHIPPED (Phase 3a)**: the relay now `Append`s EVERY observed event to `port.EventLog` (`internal/adapter/server/service.go` (`appendEvent`)), DECOUPLED from the client send (it runs even on the drain-to-discard path after a dead client, so the post-disconnect tail incl. the terminal `EvResult` is recorded (the log survives the client; this is UNLIKE the healthy-path-only awaiting-ask Persist), before `toProto`; the verdict half is captured as `EvApproval` (`engine/session/event.go` (`ApprovalPayload`)) emitted by the loop at both verdict sites. The live channel is still run-scoped (dies with the run), but the rich timeline (reasoning, ask/verdict pairs, delegation lifecycle) is now durably recorded and replayable via `EventLog.Read` | fix-via-event-log: persisted at the server relay (the loop stays storage-agnostic) | 3a (SHIPPED); a CONSUMER that replays it is 3b. **The RECONSTRUCTION direction** — folding the log into a `*session.Session` for an event-log-system-of-record host — ships as the reference `engine/adapter/eventsource.Fold` (issue #115, [ADR 0038](./0038-event-sourced-rehydration.md)); that work also adds the log-only `EvUserPrompt` event so the durable log records the user-prompt text (and the harness continuations), closing this row's "show what the user asked" gap |
| 12 | Per-session client MCP mounts | session-supplied specs, never persisted; the manager is row 2 of the inventory | lost; the owning client re-mounts via `LoadSessionWithMCP` (`service.go:968`) | reset-by-design, client-owned (the client holds the specs; the server cannot reconstruct credentials it never stored) | n/a |
| 13 | The in-flight turn (LLM stream) | nowhere; no mid-stream checkpoint exists | a turn cut by process death is lost and replayed from the last turn boundary; this is the stateless-replay thesis working as designed | reset-by-design (turn-boundary granularity is the contract; Phase 2 adds the one finer-grained cursor that matters, the ask) | n/a |
| 14 | Run plumbing (diagnostics binding, askID serial, ctx) | minted fresh per `Run` | rebuilt trivially | derive | n/a |
| 15 | modelhook guardrail breaker (`failureStreak`) | per-session in-memory on `modelhook.Runner` (`internal/adapter/modelhook/breaker.go`, commit `a032412`) | reset to zero: a rehydrated session gets a closed breaker | reset-by-design (fail-safe; the consecutive-failure escalation, not correctness; mirrors the permstore shape of row 4) | n/a |
| 16 | `askReviewBreaker` (headless ask-reviewer breaker) | run-scoped on `agent.Run` (`engine/agent/askadjudicator.go:144`, commit `1b774d4`) | dies with the run | reset-by-design (run-scoped; same cluster as rows 13/14) | n/a |
| 17 | Per-session engine's resolved-for mode (`sessionEngine.builtForMode`) | **SHIPPED (ADR 0030 Layer 3)**: the per-session engine carries the `PermissionMode` its model was resolved for; the SOURCE OF TRUTH is the already-persisted `session.Mode` snapshot field. A restart rebuilds the engine on the persisted mode (`Service.rehydrateSession` passes `sess.Mode` to the factory), so a session parked in plan mode rehydrates on the plan model; a between-turns mode switch re-resolves via the same shared `buildAndRegisterSessionEngine` path (`engineAndEnvironmentFor` CASE 1 rebuild / CASE 2 promote) | derive (nothing new persisted — the mode is already in the snapshot; `builtForMode` is recomputed from it at rebuild). Byte-identical when no plan slot is active (`ModeNeedsEngine` nil) | ADR 0030 L3 (SHIPPED) |
| 18 | A delegation's ROUTED model — Subagent (ADR 0031), team member + Parallel branch (ADR 0034) | nowhere — the model the router chose for a child is NOT persisted; the child SESSION persists (row 7), but which model it ran on is a per-delegation, decide-once classification | a NEW delegation classifies fresh (the classifier is cheap): a new Subagent call, a new team via `CreateTeam`/the Team tool (members re-route at `AddMember`), a new Parallel call (branches re-route in `runBranch`). A Subagent `resume` is NOT re-classified (the router gates on `!resuming`); a member is routed once at AddMember and reused across rounds (never re-routed on `Reopen`). No fidelity gap: the route is advisory model selection, never correctness | derive (a fresh delegation re-classifies on demand; a resumed Subagent / re-Reopened member is never re-routed) — no new snapshot field | ADR 0031 + ADR 0034 (decide-once, derived) |
| 19 | Reasoning-effort selector (ADR 0055) | **SHIPPED**: persisted as the additive opaque `ReasoningEffort` label on the aggregate (`session.Session`) + snapshot (`sessnap.Snapshot`) + `eventsource.SessionMeta`; `Service.rehydrateSession` re-derives the SAME effort via the factory from the persisted label (`needsRehydration` fires on a non-empty effort), re-minting the same-effort adapter | rebuilt on the SAME normalised+clamped effort via the factory; an unset effort rides the operator default. The re-minted adapter's resilience breaker resets to closed (fail-safe, derived — inventory row 11) | persist-in-snapshot (the neutral effort label); rehydration re-mints the engine via the factory (decision = derive — the breaker is re-armed) | 0055 (SHIPPED) |
| 20 | guardrail session waiver (`WaiverHolder`, ADR 0062) | process-lifetime in-memory map (`internal/adapter/modelhook/waiver.go`; constructed in `internal/app/build.go`, armed by the engine via `modelhook.Runner.LearnHookApproval`) | a session waiver is lost on restart | reset-by-design (fail-safe; a restarted session re-blocks/re-asks until a human re-approves — the waiver arms ONLY from a genuine human `VerdictAllowAlways` verdict, never a prompt scan, ADR 0062) | 0062 |
| 21 | EvSchedule fire records (`EvScheduleFired`/`Skipped`/`Failed`, Phase 5 ADR 0059) | the scheduler emits a `SchedulePayload` lifecycle event per fired/skipped/failed fire via the composition-injected `EmitScheduleEvent` callback (`Service.EmitScheduleEvent`), which APPENDS it to the fire session's durable `EventLog` (so schedule lifecycle rides the same durable log as the fire's own events) | **derive**: the events are in the durable log, reconstructable via `EventLog.Read`; the fire's terminal stop reason + session id are ALSO in the `ScheduleFire` record (`ScheduleStore.LoadFire`/`ListFires`). A skipped fire with no session id is dropped from the durable log (the log is session-keyed) and is durable-log-only for v1 (pull via GetFire/ListFires; live broadcast deferred) | derive-from-EventLog (the events are durable; no new snapshot field — the `ScheduleFire` record is the schedule-indexed pointer, the `EventLog` is the session-indexed timeline) | 5 (Phase 2a, #232) |
| 22 | ToolHive LLM gateway last-known-good model snapshot + provider_status (issue #262, ADR 0064) | `providerRegistry.outcomes` (`liveOutcomeStore`, List 1 row 33), process-lifetime in-memory only | a restart loses the last-known-good snapshot and the recorded status; the Build-time probe (`probeToolhive`) re-establishes both from scratch before the first `ListModels`/session create | **reset-by-design**: this is diagnostic/UX state (what to show while the gateway is down, and what to default to), never session-of-record — nothing here needs a durable row, and re-probing at every Build is the cheap, correct behaviour (the alternative, persisting a stale model list across a restart, would risk advertising a model the gateway no longer serves) | 262 (ADR 0064) |
| 23 | Pending scheduled-fire delivery notes (the per-session pending-delivery queue, ADR 0075 fire-result-delivery Scenario 4; List 1 row 34) | **persist-in-snapshot (sidecar)**: the `port.DeliveryQueue` (`FileDeliveryQueue` durable adapter) writes a per-session `.delivery.jsonl` append-only enqueue log + a `.delivery.ledger.json` delivered-seq ledger under the SAME store dir as the session snapshot (the `.events.jsonl` precedent); Pending = enqueue log − delivered ledger. The monotonic per-session seq (the exactly-once ledger key) is DERIVED from the durable enqueue log so a restart does not re-mint seq 1 — the ledger continues. Keyed on the ORIGIN session id (the session that created the schedule), NOT a per-Run registry, so a note queued in run N drains in run N+1 if run N ends first (the ledger is session-scoped). The bounded backlog cap drops the OLDEST pending note with a WARN (the injected `port.Diagnostics`) rather than growing unboundedly | a restarted process drains the still-pending notes on the origin's next run-entry (AC4.5) — the sidecars survive the restart and are reloaded; nothing is lost. `NopDeliveryQueue` (no-delivery default) and `InMemoryDeliveryQueue` (memstore-tier default) are NOT durable: they degrade honestly to empty across a restart (byte-identical no-delivery / empty-after-restart for the restarted process), the same posture as a store with no persistence | persist-in-snapshot (per-session sidecar files under the store dir; the seq is derived from the durable enqueue log, the delivered set is the durable ledger; the queue is keyed on the session id, not the run) | 0075 (fire-result-delivery plan, task 04; AC4.2 ledger half + AC4.5 pin the behaviour) |
| 24 | Failure-permanence flag (`session.Session.permanent`, issue #346 / ADR 0097) | `engine/session/session.go` (`RecordFailurePermanence` / `FailurePermanence`): a boolean on the `StateFailed` aggregate, set right after `Fail()`, cleared by `resetToIdle` (every transition out of `StateFailed`). Persisted as the additive `permanent` snapshot field (`engine/adapter/sessnap/sessnap.go` (`Snapshot`), `omitempty` — no format-tag bump); restored via `RestoreState` → `RecordFailurePermanence(true)` | survives restart so the run-entry funnel can read it before `Recover` clears it | **persist-in-snapshot** (additive boolean on the existing snapshot; recovered on restart to drive the pre-retry advisory) | 0097 (issue #346) |
| 25 | Pre-retry advisory notice store (`Service.recoverNotices`, issue #346 / ADR 0097) | `internal/adapter/server/service.go` (`recoverNotices` sync.Map): keyed by session id, holding the advisory text for a session that just recovered from a PERMANENT failure. `loadAndReopen` stores the notice BEFORE `Recover()` clears the permanence flag; `RecoverNotice(id)` returns+deletes it ONCE. The relay adapters emit an `EvRecoverNotice` synthetic event before the main event loop | a restart drops the in-memory map — the recovered session is now idle and the notice is gone (the next prompt is a fresh request, not a retry of the failed one) | **reset-by-design**: a one-time advisory that a restart correctly loses (the post-restart run is a genuine fresh attempt, not a replay of the original failure). The same shape as `Service.replayedApprovals` / `Service.resumeMu` — no persistent state needed | 0097 (issue #346) |
| 26 | Automatic-learning interval admission counter (#507; List 1 row 44) | process-local `learningAdmission.count`, shared across main and per-session engines | restart resets the debounce position; already-written user-model facts remain durable | **reset-by-design**: cadence is best-effort compatibility behavior, not state-of-record | 0105 |
| 27 | Operator-profile last-good snapshot (#508 slice; List 1 row 45) | run-local `agent.Run.operatorProfile` | restart loses it; the next run reads the durable source and omits on first-read failure | **reset-by-design**: never persisted into conversation or snapshot state | #508 slice |
| 28 | Memory lifecycle history and tombstones (ADR 0107; List 1 rows 4–5) | the same per-scope `memory.json` document as current entries | restart reloads current state, opaque versions, attribution, history, and tombstones together; a legacy flat entry is projected without rewrite and materialized only by its next locked mutation | **persist-in-store**: one flocked load→mutate→atomic-rename transaction; deliberately no history sidecar that could drift from current state | 0107 |
| 29 | Staged learning proposal lifecycle and promotion receipt (issue #509; List 1 row 46) | the standard `reflectionstore` proposal document, partitioned by hashed principal/project identity; resulting memory revision provenance is in `MemorySource.ProposalID` | restart reloads the exact proposal status/version/decision history. A crash after the memory CAS but before proposal finalization leaves `promoting`; reconciliation compares the current memory revision's proposal id and finalizes without writing again. A missing or different current link conflicts fail-safe | **persist-in-store**: proposal lifecycle and memory revision are independently durable; reconciliation closes the intentional cross-store crash window without claiming a distributed transaction | 0109 |
| 30 | Reflection queue, running jobs, singleflight keys, and bounded completion receipts (issue #509; List 1 row 47) | process-local `reflectionCoordinator` maps/queues and workers | restart cancels/loses uncommitted queued or running extraction and ephemeral receipts; already staged proposals and memory revisions remain durable | **reset-by-design**: there is no retrospective sweep of old sessions. A caller may explicitly reflect a completed session again; deterministic proposal IDs and CAS make that retry converge | 0109 |
| 31 | Agent-owned skill lifecycle and proposal linkage (issue #510; List 1 row 48) | `skillstore` bounded manifest plus immutable content-addressed `SKILL.md` files; the proposal's terminal linkage remains in `reflectionstore` | restart reloads exact versions, revisions, lifecycle states, histories, and active selection. A crash after draft persistence but before proposal linkage leaves an inactive Draft and possibly an unreferenced immutable file; retry converges the exact body/provenance and CAS-links the same SkillID | **persist-in-store**: both stores are independently atomic. ProposalID provenance and deterministic SkillID/version provide idempotent reconciliation without claiming a distributed transaction; no startup sweep and no automatic activation | 0110 |
| 32 | Automatic-learning process/principal windows, weighted cooldowns, and completed-digest cache (ADR 0114; List 1 row 55) | process-local `automaticAdmissionController` only; nothing is persisted | restart grants a fresh automatic window and forgets cooldown/completed-cache membership. Existing staged/promoted proposal state remains durable and deterministic proposal IDs make a retried reflection converge. There is no historical/startup/shutdown sweep | **reset-by-design**: spend-control and duplicate-suppression hints, not accepted learning state. N replicas have N independent budgets | 0114 |

Two ledger observations worth stating in prose:

- **The awaiting state was the one place where durability and liveness diverged**
  (row 5): the snapshot faithfully holds the parked ask and restores it through the
  real state machine, and Phase 2 closed the liveness half: a loaded awaiting
  session is now re-entered AT the ask (`engine/agent/loop.go` (`ResumeApproval`)).
  Phase 2 was small precisely because the hard half (durability) shipped with
  sessnap; only the loop-entry liveness was missing.
- **Teams are the largest honest gap** (row 10). The member-session persistence
  gives forensics, not resumption. Saying "mid-round teams do not survive restart"
  in the operator docs is part of this arc's v1 posture; pretending otherwise is
  not.

### Re-audit: MCP typed tool results (#223)

Per the inventory discipline ("Added an outlives-a-call resource? Inventory it
in `docs/adr/0027-cloud-native.md`"), the typed-tool-results widening
([ADR 0078](./0078-mcp-typed-tool-results.md)) was re-audited. Expected outcome,
confirmed:

- **No new List 1 row.** The change is a value-object widening on the existing
  session aggregate / event log — an additive `Parts []session.Content` field on
  `session.ToolResult` (`engine/session/toolcall.go`) (`ToolResult`), carried on
  `EvToolResult` and reconstructed by `engine/adapter/eventsource`
  (`eventsource.go`) (`Fold`). It introduces no new goroutine, LRU, map, breaker,
  `*http.Client`, or semaphore whose lifetime outlives a tool call.
- **No new List 2 row.** A zero-value `Parts` is the legacy string-only shape, so
  `engine/adapter/sessnap` and `Fold` load old snapshots/events unchanged; there
  is no restart-losable state beyond what snapshots already carry (the typed
  blocks are part of the persisted `ToolResult`, same as `Content` today).

- **The one resource this feature CAN introduce is Phase 2.** The
  `FetchMcpResource` model-facing affordance (fetching an `https://`
  `resource_link`, ADR 0078 decision 5) introduces an `http.Client`. It MUST be
  **per-call** (bounded, no cross-origin credential attachment) — and if Phase 2
  instead caches/reuses it, that client earns a List 1 row at that time. Not
  inventoried now because Phase 2 has not landed.

## List 3: decisions

Three decisions this arc must record now. Each carries a recommendation; all three
are **OPEN** until the maintainer confirms.

### (a) Memory scope key for FS-less sessions (OPEN)

Today memory is opened from `cfg.MemoryDir` once at build time
(`internal/app/build.go:1895`; the `--memory-dir` flag,
`cmd/mecated/main.go:820`) and shared by every session in the process. The
semantics are per-project-directory: facts written over one project dir are visible
to later sessions over the same dir. A no-FS cloud session has no directory, so
"which memory does this session see" becomes a real question.

What the driver protocol can already express: nothing scope-shaped. Every
`MemoryStoreService` RPC carries only the operation's own arguments; `RecallRequest`
is `{key}` (`contracts/proto/mecatl/driver/v1/memory_store.proto:99-102`),
`ListRequest` is `{prefix}`, `IndexRequest` is empty. There is no tenant, principal,
session, or namespace field anywhere in the service. The scope boundary is therefore
the **endpoint**: whatever backend `--memory-store-url` points at IS the scope, and
the process holds one shared connection per URL (`driverConns`,
`internal/app/driverstore.go:54-90`).

Options:

1. **Per-deployment (status quo).** The deployment's memory driver/dir is the
   scope; a hosting platform that wants per-tenant memory runs one harness (or one
   driver endpoint) per tenant, or implements scoping driver-side keyed on
   connection auth.
2. **Per-principal/tenant key threaded through the driver protocol.** Add a scope
   field to every memory RPC (or a per-stream header), plumbed from session
   creation. A protocol change plus a port change (`tool.MemoryStore` would need the
   key on every call or a scoped-store factory).
3. **Per-agent-identity.** Key memory on the soul/agent identity rather than the
   tenant; same plumbing cost as 2 with a different key choice. Note this is now
   *partially shipped for the read path*: per-agent persistent memory (issue #33,
   commit `31d716e`) keys a read-only `MEMORY.md` on the agent def name under an FS
   root (`<root>/agents-memory/<defName>/`, user or project tier,
   `internal/app/agentdefs.go`), injected as fenced untrusted data into the agent's
   prompt. It is FS-rooted and read-only, so it does not touch `tool.MemoryStore` or
   the driver protocol, and it fail-softs to empty under no-fs (no workspace/XDG base),
   so it is an instance of this option's *keying idea* without a no-fs story or a
   write path yet.

**Recommendation: 1 for this arc.** It is honest about what is built, requires no
protocol or port change, and composes with the existing posture (the driver endpoint
is already the trust and capability boundary, `DRIVERS.md` "Trust & security
posture"). Option 2 is the eventual multi-tenant answer, but threading a scope key
through six methods, the port, the conformance suite, and the driver protocol is
speculative until a real multi-tenant consumer exists; the no-speculative-widening
rule applies. Record the gap, defer the widening.

### (b) Profile in the snapshot, recommend YES in Phase 1 (DECIDED: YES, SHIPPED)

The `9f8ba8c` rehydration derives the profile from "a persisted empty workspace can
only be no-fs" (`service.go:1031-1041`). The inference is sound today because every
other path requires a non-empty workspace, but it is a pun: it breaks the day a
second workspace-less profile exists (a memfs-scratch profile, a remote-FS profile),
and both are named candidates in the deferred list.

**DECIDED YES, SHIPPED in Phase 1: `Profile` is an additive, omitempty snapshot
field, and the empty-workspace inference stays as the second defense.** The
precedent is exact: `ProviderPhase` (json `"phase,omitempty"`) and `Parts`
(`contentToDTO`) were both added additively with no format-tag bump, and the
format-tag contract says the tag changes only if the encoding itself is replaced
(`SnapshotFormat`); additive fields ride `sessnap-json/1` unchanged, so remote
drivers store and return the new field opaquely with zero driver changes. The known
downgrade edge is already recorded as accepted (`DRIVERS.md` Deferred §5): an OLDER
harness loading a newer snapshot silently sheds the field; for the profile
specifically, the retained empty-workspace inference (`needsRehydration`,
`Service.rehydrateSession`) means even that downgrade path stays correct for no-fs,
which is exactly why the inference was not deleted when the field landed. As built,
the same additive treatment carries `ProviderID`/`ModelID` (the selector pair) and
the pointer-omitempty `usage` field.

### (c) v1 multi-replica stance: session affinity, single writer (RESOLVED — CODE-ENFORCED when a lease is wired, Phase 4)

Originally stated as a deployment requirement: **route every session to exactly
one harness process; never run two processes against the same session id
concurrently.** As of Phase 4 this is now CODE-ENFORCED whenever an operator wires
a session-lease backend (`--session-lease-dir` / `--session-lease-k8s-namespace` /
`--session-lease-url`): the run-entry funnel acquires a per-session lease and a
second replica is refused with `ErrSessionLeasedElsewhere` (HTTP 409). Without a
lease backend the original constraint is the byte-identical DEFAULT and remains a
deployer responsibility; the code below still assumes it everywhere a writer exists
in the no-lease default:

- **jsonlstore is append-only with an in-process mutex only.** `Save` serializes
  through `st.mu` and appends a snapshot line via `O_APPEND` open-write-close
  (`internal/adapter/store/jsonlstore/jsonlstore.go:264-265`); there is no atomic
  rename, no file lock, no cross-process guard, and `Load` takes the last line.
  Two processes appending to one session file interleave at the mercy of OS append
  atomicity, last-write-wins at best.
- **Latent durability gap (Phase 2-adjacent, revisit if "disposable" widens to host
  crashes).** Because jsonlstore neither fsyncs nor uses an atomic rename, the
  awaiting-snapshot durability the Phase 2 resume relies on rests on OS page-cache
  survival, not on-disk durability: it is correct across a PROCESS restart (the
  Phase 2 thesis, and what the kit and gate exercise) but NOT host-crash-safe (a torn
  trailing append, or a snapshot still only in the page cache when the kernel dies, is
  lost). This is acceptable for v1 disposability (process restart). If "disposable"
  ever has to mean host crashes, jsonlstore needs fsync + atomic-rename (or a durable
  driver backend); this rides the same store-hardening track as the leasing concern
  in decision (c) / Phase 4.
- **The driver protocol is last-write-wins by contract.** `SaveRequest` carries
  `{session_id, snapshot}` and nothing else, no version, no CAS token, no lease
  (`contracts/proto/mecatl/driver/v1/session_store.proto:96-107`; "Save overwrites:
  Load returns the most recent snapshot saved under the id").
- **The server's run-exclusion is in-process only.** `Service.runs`
  (`service.go:368`) prevents two concurrent runs of one session within a process;
  `loadAndReopen` (`service.go:868`) consults nothing cross-process before
  reopening. Two replicas can each load, reopen, and run the same session, and each
  will happily persist over the other.
- **The GC liveness predicate is process-local**, already recorded: `Service.IsLive`
  sees only this process's runs (`internal/app/childgc.go:77-91`; `DRIVERS.md`
  Deferred §7), mitigated by age ordering and idempotent best-effort deletes, not by
  exclusion.

**Resolution (Phase 4, SHIPPED): solved as a standalone `port.SessionLease` seam,
acquired at the run-entry funnel, not as in-process locking** — a process-local
guard cannot enforce a cross-replica property. Recommendation as built: a
SIBLING port discovered by type assertion (not folded into `SessionStoreService`),
so the lease backend is chosen independently of the store (the same independence
`--event-log-url` has). The flock adapter is honestly single-host (the memory-store
flock precedent); the k8s and gRPC-driver adapters are the multi-host paths. The
Phase 0 inventory confirmed sessions are the first and, for now, only resource that
needs a lease; the GC liveness gap (`Service.IsLive` is process-local) is NOT yet
on the lease — a follow-up could consult the lease for cross-process liveness, but
that rides its own change. Operator guidance (the flag matrix, k8s RBAC, the
flock single-host caveat, the multi-replica posture) is in `docs/usage.md`.

## Relationship to other docs

- [Cloud-Native Harness Kit — definition](../cloud-native-harness-kit.md): the
  conceptual/positioning definition this internal arc feeds into — this ADR is the
  mecatl-internal engineering, that doc is the kit positioning.
- `DRIVERS.md`: the shipped distribution layer this arc builds on; its "Deliberately
  deferred" list items (cross-process GC liveness, server-wrapper promotion) intersect
  Phases 3-4.
- `IMPLEMENTATION-NOTES.md` "Session profiles": the as-built no-FS profile and
  rehydration detail this doc's framing summarizes.
- The cloud-native kit inventory (PR #54): the upstream speculative subsystem
  inventory; this arc executes its session-state priorities (§1.1-§1.4) and its
  resource-lifetimes advice (#6), and consciously defers its filesystem, forking, and
  execution-environment subsystems behind the no-FS cut.
- `BACKGROUND-SUBAGENTS.md`: issue #28 (session-scoped detach) is gated on Phase 3's
  event log and stays its own arc.


---

*Part of the [design docs](../design/README.md). Related: [BACKGROUND-SUBAGENTS.md — Background Subagents + Per-Child Cancel over a Shared Child-Run Registry](0015-background-subagents.md), [Conversation compaction](0012-compaction.md), [mecatl — Architecture](0004-v1-architecture.md).*
