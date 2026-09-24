# ADR 0027 — Cloud-native arc: disposable process, externalized state, durable record

- Status: Accepted
- Date: 2026
- Scope: process disposability — snapshot fidelity, awaiting-approval evict/rehydrate, durable event log, multi-replica readiness (session leasing, SHIPPED)

## Context

The harness already had turn-boundary persistence and a stateless full-replay LLM provider, making it unusually close to disposable by construction. The remaining gaps were: the snapshot was not fully faithful (profile, provider/model selector, and cumulative token usage were not persisted); a process death while a run was parked awaiting approval stranded the session; and the event stream (approvals, pre-compaction history) was emitted and discarded rather than durably recorded. Without these, a restarted process could lose the user's permission grants, their budget progress, and the audit record.

## Decision

Deliver the arc in four phases: Phase 1 adds three missing snapshot fields (profile, provider/model selector, cumulative usage) and generalizes the rehydration seam; Phase 2 adds a resume-from-awaiting loop entry so a post-restart `Approve` re-enters the loop at the exact pending ask; Phase 3 adds a durable append-only event log (port, local JSONL adapter, gRPC driver service) with two consumers (compaction archive, permstore verdict replay); Phase 4 adds cross-process single-writer enforcement via session leasing (a new `port.SessionLease` seam with in-memory/flock/gRPC-driver/k8s adapters, acquired at the run-entry funnel and renewed by a Service-owned goroutine). The loop stays storage-agnostic throughout — it only emits events, never imports the log or lease port; the lease renewer and held-lease registry live on the server `Service`/composition, the same discipline as the event log.

## Consequences

Phases 0–4 are shipped; the harness is now genuinely disposable across process restarts AND safe under a multi-replica deployment that wires a session lease. Teams are the largest honest gap: mid-round team coordination state does not survive restart (row 10 of the fidelity ledger). Phase 4 makes single-writer enforcement CODE-ENFORCED when a lease backend is wired (decision (c) update below). Current composition automatically wires the flock backend beneath every local JSONL StoreDir; other stores without a lease retain the v1 session-affinity constraint, and destructive maintenance fails closed. Current behaviour is in `docs/architecture.md`; shipped and deferred items are in [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

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
The one deliberate exception is the team lead's synthesis turn, which captures
its current cumulative main usage as an internal immutable run baseline so a
budget-stopped working run still produces the deliverable without resetting
lifetime accounting. The team-AGGREGATE budget is unchanged (it sums per-round
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
ADR 0243 supersedes that local-jsonlstore limitation; this historical reasoning remains
unchanged for the Phase 2 gate.
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
  single-host flock lease (`internal/adapter/flocklease`, an operation-scoped stable
  transition lock + a retained generation-specific liveness flock + atomic record;
  expiry permits generic TTL takeover even while the old process lives, while a free
  recorded-generation lock detects SIGKILL for immediate pre-expiry crash takeover;
  issue #1333 — a `Renew` past TTL whose durable record still names the caller at its
  own fencing token RECLAIMS with a fresh expiry instead of declaring loss, since on a
  single host that can only be true if nobody else raced an Acquire in the gap — a
  process suspended past the TTL, e.g. laptop sleep, no longer loses its lease to a
  competitor that never ran; a record whose owner/token DID change is still
  unconditional `ErrLeaseHeld`),
  the gRPC driver
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

**Phase 4 re-audit (List 1 / List 2).** Phase 4's outlives-a-call resource is
inventoried in List 1 row 27: `Service.heldLeases` plus its per-session renewer
and, for the local adapter, the retained generation-specific flock fd. Ownership is
split between the Service lifecycle and `flocklease.Lease`; cleanup = renewer-cancel +
exact-token `Release`, which tombstones and closes the generation fd. The stable
per-session sentinel is held only while serializing a record transition. Re-attach =
reacquired on the next run-entry; expiry permits generic TTL takeover, while a crashed
process has its generation fd released by the OS so a survivor detects the free lock
and takes over immediately before expiry. List 2 is
UNCHANGED: a lease is DERIVED state (nothing a restart needs to reload — the
durable record only preserves fencing-token monotonicity), so it adds no
rehydrate-fidelity row. The re-audit verdict is CLEAN. Decision (c) below moves
to CODE-ENFORCED-when-wired.

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

**Semantic-retry re-audit (List 1 / List 2, issue #409, ADR 0239).**
ADR 0239 supersedes ADR 0203's binary decision. The retry work adds NO new
outlives-a-call resource to List 1. Typed failure disposition/progress and failed-step
retry intent are fields on the existing Session and snapshot; attempt diagnostics are
emitted synchronously, and semantic buffering lives only for one provider attempt.
List 2 row 24 covers typed failure metadata. Row 25 remains the reset-by-design advisory
derived from typed permanent classification. New row 38 records persisted failed-step
retry intent and crash recovery. The re-audit verdict is CLEAN.

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

**Caller-identity re-audit (List 1 / List 2 — issue #367, ADR 0204).** The
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
[ADR 0205](./0205-bounded-jwks-staleness.md), and remain reconstructible rather
than persisted.

**Execution-environment persistence re-audit (List 1 / List 2 — issue #462 phase 3, ADR 0214).**
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
- **`Service.SessionStale` / `Service.StaleRunningCandidates` /
  `Service.SettleIfStale`** (`internal/adapter/server/service.go`,
  `internal/adapter/server/stale_maintenance.go`): the shared staleness DECISION,
  root-authorized metadata-only enumeration, and repair WRITE. The only accepted
  caller for enumeration and settlement is the explicit
  `mecatl:internal / stale-session-reconcile` system root; ordinary callers and
  other system roots receive `ErrManagementUnauthorized`. Enumeration reads the
  optional metadata pager only (never a transcript), admits only valid,
  non-scheduled `running` rows, and skips ownerless rows when ownership is
  enforced. Settlement reloads and rechecks that same narrow predicate before
  writing, so the root cannot turn an arbitrary session ID into a maintenance
  capability. `SessionStale` mirrors
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
  (mirroring the `startLiveModelRefresh` idiom; the child-GC worker now uses the
  same Build-owned cancel-and-join lifecycle) that runs under the dedicated
  `stale-session-reconcile` system root, asks
  `Service.StaleRunningCandidates` for content-free eligible metadata, and
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
sweep repairs already IS the state in question. The child-GC worker now follows
that same owned-lifetime rule: its idempotent closer cancels and joins startup,
ticker, and context-aware blocked storage work before Service/store teardown,
independent of whether the caller cancels the Build context. List 2 gains
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

**Legacy-adoption re-audit (issue #593, ADR 0226).** No new List 1 resource is
introduced: preflight is request-scoped, apply borrows the existing keyed run-entry
lock, mutation lease, and per-session engine registry (rows 11 and 27), and the
idempotency proof is persisted on the target snapshot rather than held in a new map.
List 2 row 34 records the durable source relationship and request digest. Cancellation
or failure before the target `SessionStore.Save` publishes no snapshot; successful
save is the existing store's atomic visibility boundary.

**Session-migration re-audit (issue #589, ADR 0226).** List 1 gains row 56:
the durable migration-job registry. Plans are request-scoped and read-only; no
process map or worker goroutine is introduced. Apply/resume are caller-driven bounded
batches that borrow the existing run-entry locks and lease registry (rows 9 and 27)
and jsonlstore family locks (row 8). Each mutating job drive additionally holds a stable
job-ID flock from load through its final checkpoint; a stale overlapping resume conflicts,
and cancellation cannot be overwritten by an older in-memory record. List 2 gains row 35: durable progress is
persisted after each committed family using only one-way caller/item handles and
sanitized reason codes, so a replacement process resumes without transcript/path/error
material in job state. A crash before v2 verification retains v1; one after atomic
promotion may leave both, with verified v2 authoritative and the retry removing v1.

**Bounded reflection materialization re-audit (ADR 0300).** List 1 row 47 is
widened rather than adding a second reflection owner: the existing Build-owned
coordinator now also owns one closeable materialization lifecycle gate/context and
bounded active-operation accounting. Scans run synchronously on callers and start no
goroutine per job. Close rejects new scans, cancels and joins active scans, then executes
the existing queue/worker shutdown. List 2 row 29 adds the immutable aggregate manifest
to durable proposal state; row 30 records that materialization accounting and every
pre-admission operation reset by design. Cancellation or close before admission leaves
no queue, singleflight, receipt, reservation, provider, repository, or proposal state.

**Direct MCP reconciliation re-audit (ADR 0355).** List 1 row 64 inventories the
Build-owned worker, jitter/cooldown timers, coalescing state, source LKG/status, immutable current
and retiring runtimes, revision-tagged caches, and run/operation pins. Explicit
refresh adds no outlives-a-call resource: it borrows the existing run-entry lock,
mutation lease, and guarded SessionStore save. The exact-name authority union is
persisted in the existing session snapshot; runtime revisions and cached source
status reset and reconstruct from configured sources on restart.

## List 1: resource inventory

Every resource the harness allocates whose lifecycle outlives a single tool call,
tagged with its de-facto scope in the nesting `call ⊂ run ⊂ session ⊂ team ⊂
process`. "Re-attach" answers: can a restarted process recover it?
**reconstructible** (rebuilt from config/disk on next Build), **persisted** (the
durable artifact survives and is reloaded), or **lost** (gone, possibly leaking).

| # | Resource | Owner | Scope | Cleanup today | Re-attach | Evidence |
|---|---|---|---|---|---|---|
| 58 | Title coordinator (two workers, 64-item queue, dedupe set, and retry timers) | `server.Service` / `app.Build` | process | `Service.Close` cancels and joins workers before session dependencies close; queue admission is non-blocking and bounded | reset-by-design; durable session attempt records make a crash-interrupted claim terminal rather than rebilling | `internal/adapter/server/title_coordinator.go` (`titleCoordinator`) |
| 59 | Provider OIDC bearer sources and runtime registry | `internal/cliconfig.NativeEndpointLoader` / `NativeEndpointRuntime` | process, one per configured OIDC provider | `NativeEndpointLoader.Close` closes every loader-owned runtime; each runtime closes its serving Store handles | reconstructible from explicit operator provider configuration and an exact protected record; access tokens reset and are refreshed from that record | `internal/cliconfig/native_endpoint.go` (`NativeEndpointLoader`, `NativeEndpointRuntime`, `Source`, `Close`) |
| 60 | Caller OIDC JWKS validator cache | `authn/oidc.Validator` | process | validator `Close` stops its refresh work during command-root shutdown | reset/refetch: a restart fetches current signing keys; this authenticates callers only and is separate from provider issuer/gateway trust | `authn/oidc/oidc.go` (`Validator`, `Close`) |
| 61 | Provider OIDC issuer and gateway HTTP clients | `internal/cliconfig.NativeEndpointRuntime` | process, one pair per provider runtime | clients have no independent goroutine; their transports die with the runtime/loader | reconstructible from explicit issuer/gateway trust policies and CA bundles; neither trust root is reused for the other | `internal/cliconfig/native_endpoint.go` (`OpenNativeEndpointRuntime`, `nativeTrustClient`) |
| 62 | Provider OIDC keyring and encrypted Store handles | `internal/cliconfig.NativeEndpointRuntime` | operation handles; serving Store handles live for the runtime | Login/status/logout defer Store close; `Source` handles close at runtime/loader close | reopened only from explicit `credential_store.oidc.home` and provider identity; no discovery, migration, plaintext, or environment fallback | `internal/cliconfig/native_endpoint.go` (`open`, `Login`, `Status`, `Logout`, `Close`); `internal/adapter/llmendpoint/lifecycle.go` (`NewProtectedStore`) |
| 63 | Provider OIDC transaction-lock handles | `llmendpoint.TransactionLocker` | one provider lifecycle operation | `With` releases its owner-only flock after the callback; no lock is retained across gateway requests | reset/reacquired after restart from the hashed provider identity; the durable record remains the source of truth | `internal/adapter/llmendpoint/lifecycle.go` (`TransactionLocker`, `With`) |
| 64 | Direct MCP source reconciler worker, bounded jitter and one-second cooldown timers, coalescing channel/waiters, per-source LKG/status, immutable current and bounded retiring runtimes, one deferred-successor bit, revision-tagged engine caches, and run/operation pins | `app.Build` through `connectMCP`; `server.Service` owns root-run and direct-Team pin lifetime | process publication state plus one construction-scoped pin for the shared catalog and one pin per active root run, direct `RunTeam`, or resource/prompt operation; a created-but-unrun direct Team stores declarations only and retains no runtime pin | the construction pin is released only by the Build close fold, after the shared catalog and engine stop borrowing its exact manager/revision pair. The Build close fold first cancels Service operations without detaching their pins, then `mcpRuntimeSet.close` waits for actual `removeRunState`/`RunTeam`/operation settlement before closing managers. Publication never mutates a live manager, a displaced runtime closes only after its pins drain, and a full retirement set discards the candidate without force-close and schedules one retry after drain. Direct `RunTeam` acquires current once and builds its supervisor, member engines, referenced specialists, and root authority from that operation context. Cached-engine close never closes a direct manager. Caller cancellation only stops that caller's reconciliation wait; if a complete runtime publishes before the construction pin, both the shared manager and revision derive from that pin even when the initiating waiter reported an error | reset-by-design and reconstructible: restart re-resolves static configuration and ToolHive, reconnects one complete runtime, and begins with no source LKG, retired runtime, pin, cache revision, or pending invalidation; direct Teams are process-local and are not restored; no runtime metadata is durable | `internal/app/mcp_reconciler.go` (`mcpSourceReconciler`, `mcpReconcileCandidate`, `Close`); `internal/app/mcp_runtime.go` (`mcpRuntimeSet`, `pin`, `publish`, `release`); `internal/adapter/server/service.go` (`beginRunAdmission`, `removeRunState`, `engineAndEnvironmentFor`, `SessionEngineResult.RuntimeRevision`); `internal/adapter/server/team.go` (`createTeamInEnvironment`, `buildTeamForOperation`, `RunTeam`, `CleanupTeam`); `internal/app/build.go` (`connectMCP`) |
| 1 | Direct global MCP managers (one per immutable current/retiring runtime) | `app.Build` / `mcpRuntimeSet` | process, bounded current plus retired set | a candidate owns its manager from complete construction; unchanged/failed/unpublishable candidates close immediately, retired managers close after operation pins drain, and joined Build close closes the remainder. They are NEVER folded into per-session engine close | reconstructible (reconnects a complete desired set from ordered sources at next Build) | `internal/app/mcp_reconciler.go` (`mcpReconcileCandidate`); `internal/app/mcp_runtime.go` (`mcpRuntimeSet`); `internal/app/build.go` (`connectMCP`) |
| 2 | Per-session client MCP managers and their original `Service.clientMCPSpecs` mount declarations | `sessionEngineFactory` / `Service` | session / process-local map | per-session close func, invoked by `Service.CloseSession` (`service.go:743`) and shutdown. **Two callers now reach this** (issue #821, Scenario 9): the ACP adapter, which calls `CloseSession` on editor disconnect, and the gRPC/HTTP `CreateSession` wire path, whose teardown is the precondition-checked `EndSession` (same teardown) or `Service.Close`. `CloseSession`, failed-create rollback, and `Service.Close` remove the corresponding `clientMCPSpecs` entry; an engine-rebuild failure deliberately retains it because the surviving session can retry/remount the same client tools. The wire caller is a client that may simply go away without ending its session, so a mounted manager can outlive its last use until process shutdown — bounded by `MaxSessionEngines` (which fails a create at the cap rather than growing without limit), not by a reaper. That residual is the same one every per-session engine has, and it is why the field is deployment-gated to a UNIX-socket-only daemon rather than exposed to an anonymous network caller. A create REFUSED for an unreachable server does not add to it: `verifyClientMCPMounted` runs before any id is minted and invokes the same `closeFn` every other rejection in `createPerSessionEngine` does, so the partially-connected manager is torn down on that path rather than left registered against a session that does not exist | **reset/remount by design:** client specs, including secret-shaped headers, are never persisted. A restart drops `clientMCPSpecs` and every client manager; the client explicitly re-mounts via `LoadSessionWithMCP`, `service.go:968`, or creates a new session with `mcp_servers`. Within one Service lifetime, a rebuild reuses the retained original declaration. | `internal/app/build.go:962`; `internal/adapter/server/service.go` (`clientMCPSpecs`, `ClientMCPFromWire`, `WithClientMCP`) |
| 3 | Preserved-fork LRU (`LRUForkReaper`) — Parallel ONLY | `app.Build` (shared via `catalogAssets`, built only when Parallel is enabled) | process | LRU eviction runs each entry's cleanup (dir removal) outside the lock; graceful app shutdown calls `Built.Close`, which drains retained entries and eviction cleanups already detached before closure outside the lock | registry lost; the preserved fork DIRS remain on disk un-tracked (a leak on crash) | `engine/agent/forkreaper.go:41,64`; built at `internal/app/build.go:4895`. NOTE: the writable Subagent (`mode:"read-write"`) no longer creates a per-call fork dir — it writes the parent workspace directly (ADR 0077), so it contributes no fork dirs here; the shared `autoMerger` (`forker.SerializingMerger`) is likewise Parallel-only now |
| 4 | Project memory store (flock pair: `memory.json` + `memory.lock`) | `app.Build` | process handle, per-directory data | flock held per-operation only; one `*Store` per dir per process. Current entries, revision history, and tombstones share ONE locked atomic document — no split-file transaction | persisted (data/history on disk; incompatible documents fail validation without rewrite; handle rebuilt at next Build) | `internal/app/build.go`; `internal/adapter/memory/store.go` (`Store`, `withExclusiveLock`) |
| 5 | User-model store (same adapter, XDG dir) | `buildUserModelStore` | process handle, per-user data | as above | persisted, including operator-profile source state and lifecycle history | `internal/app/build.go` (`buildUserModelStore`) |
| 6 | permstore learned allow-always rules | `app.Build` | session (data), process (store) | `Forget(sessionID)` via `OnCloseSession` (`service.go:748`); capped at 256/session | **replayed (Phase 3b)**: in-memory still, but a post-restart load re-Learns the allow-always rules from the durable log's verdict events (`internal/app/approvalreplay.go` (`replayApprovals`)), so a previously-allow-always'd tool is not re-asked | `engine/adapter/permstore/permstore.go:42,48` |
| 7 | Redis snapshots + event/tool sidecars + derivative metadata indexes | redisstore | managed Redis keyspace; one global and owner-specific sorted metadata index plus generation fields | Save atomically replaces the snapshot and its bounded metadata row; Delete atomically removes snapshot, metadata membership, and event/tool sidecars. Invalid current snapshots or unmatched current index memberships fail closed. Redis owns persistence/eviction policy and connection cleanup remains `Store.Close` | persisted. The derivative index uses the canonical `mecatl:session-metadata:*` keys alongside the original session, event, generation, tool, lineage, and ledger key families. Malformed or inconsistent addressed records fail closed; there is no inventory scan, adoption, or migration path | `internal/adapter/redisstore/redisstore.go` (`Save`, `Delete`); `internal/adapter/redisstore/metadata_index.go` (`pageSessionMetadata`) |
| 8 | jsonlstore current snapshot + `.tools.jsonl` audit + `.events.jsonl` event log (Phase 3a) + stable per-family flock/temp generations (issue #586) + derivative inventory catalog (issue #587) | jsonlstore | per-session files plus one store-wide adapter-private catalog with its own process mutex/flock | mutation files open per call and close immediately; EventLog.Read is the documented exception, retaining only its bounded descriptor/section view until iteration ends. Save, Delete, EventLog.Append, and ToolCall share the stable owner-only `.family.lock` identity for the complete same-family mutation, while unrelated families remain independent. Temps carry a random process-owner token plus monotonic generation; startup non-blockingly reaps only protocol-valid inactive generations after taking that family lock, and Save reaps them after waiting for the lock. Ordinary failures remove their own temp; lock sentinels persist. `Delete` removes sidecars before snapshots. Catalog rebuild/publication takes only its dedicated catalog mutex/flock—never a store-wide session-operation lock—and reconciles obsolete generations there; blocked inventory work cannot delay family reads or mutations. Save/Delete advance a durable generation marker while holding the affected family lock. The owner-only catalog contains only bounded discovery metadata, the marker fingerprint, and v1 file size/mtime/mode reconciliation metadata, never transcript/tool/event content | persisted (`Load` reads one authoritative current snapshot; malformed or incompatible addressed content fails closed without rewrite); locks are reconstructible synchronization, and a crashed holder's flock is OS-released so the next startup/Save removes its temp; `SnapshotDurability` reports weaker filesystems explicitly. EventLog.Read releases the family flock after capturing a bounded complete prefix but retains its descriptor/section view until iteration ends. **ADR 0243 supersedes the former local-jsonlstore durability limitation.** The catalog is derivative and reconstructible from current top-level metadata headers; every ready read revalidates the O(1) marker/directory stamp. Retention paging and stale-session reconciliation consume this metadata projection while retaining their state/liveness/lease rechecks | `internal/adapter/store/jsonlstore/jsonlstore.go` (`withSnapshotFamilyLock`, `reapSnapshotTempsAtStartup`, `replaceCurrentSnapshot`, `SnapshotDurability`); `internal/adapter/store/jsonlstore/inventory_catalog.go` (`inventoryCatalog`, `inventoryFingerprint`, `withInventoryCatalogLock`); `internal/adapter/store/jsonlstore/metalist.go` (`discoveryMetaList`) |
| 9 | Live-run registry (`Service.runs`) | `server.Service` | run | `deregister` after the wire adapter drains `run.Events()` | lost (the run dies with the process; the session snapshot persists) | `internal/adapter/server/service.go:368` |
| 10 | Team registry (`Service.teams`) and each supervisor's enrolled member resources | `server.Service` / `agent.Supervisor` | team | removed at team terminal. `Supervisor.Close` is idempotent and concurrent-safe: every caller waits for exact-once member teardown, while `Run` separately preserves ordered event delivery and waits for all callbacks before returning; member factories and cleanup callbacks must not synchronously re-enter `Close`. Pre-run startup rollback closes constructed members, attempts snapshot deletion while each mutation lease is still held, then releases the lease; deletion is best-effort and a failure leaves the snapshot for retention with the diagnostic `abandoned team member snapshot could not be deleted; left for retention` | **lost** (see ledger row 10: the whole coordination state); a leftover abandoned member snapshot is durable and handled through supported retention rather than unsafe deletion of a possibly live session | `engine/agent/teamsupervisor.go` (`Close`, `Run`); `internal/adapter/server/team.go` (`RunTeam`, `deleteAbandonedMembers`) |
| 11 | Per-session engine registry (`Service.sessionEngines`) | `server.Service` | session | evicted + closed at `CloseSession` and shutdown | lost; rehydrated via `Service.rehydrateSession` for the no-fs profile, a persisted selector, OR an empty workspace (`needsRehydration`); decision = derive (nothing new persisted). **Registration sources (ADR 0030 Layer 3 added the last two):** create (selector / client-MCP / no-fs), restart rehydration, a CASE-1 mode→model REBUILD (a registered per-session engine whose `builtForMode` no longer matches `session.Mode`), and a CASE-2 mode-PROMOTION (a default-FS session whose mode resolves a different model via the `ModeNeedsEngine` predicate). The mode-rebuild + promotion go through the SAME `Service.buildAndRegisterSessionEngine` helper as rehydration, under the per-session `runEntryMu` (the use-after-close guard: a rebuild's prior-engine `Close` and the `s.runs[id]` liveness check sit in one critical section, so a displaced engine is never closed while a run holds it). **At-cap failure semantics:** a CASE-2 promotion is a NEW per-session registration, so a plan-mode `StartRun` can now fail with `ErrTooManySessionEngines` where the original (shared-engine) create succeeded — graceful (no panic, no silent shared-engine fallback that would run plan mode on the wrong model). A CASE-1 rebuild reuses the existing slot (no cap pressure). The compaction context-window is NOT a rehydration trigger — every engine (shared + per-session) carries a resolve-at-use `Deps.ContextWindow` closure (`reg.windowResolver`) read live on each `maybeCompact`/`Engine.ContextWindow`, so a live-only default model self-corrects to its live window on the next turn with no rebuild. **Reasoning-effort (ADR 0055):** a session whose resolved effort differs from the operator default gets a RE-MINTED provider adapter (`providerEntry.remintEffort`) carrying its OWN resilience breaker/retry-wrapper instance — benign (session-scoped, the same posture as rows 21/22; decision = derive — the effort is the List-2 row-19 persisted label and the breaker re-arms on the next Build/rehydration). The three utility engines (guardrail checker, ask-reviewer, model-router) deliberately pin the OPERATOR-DEFAULT provider, not the re-minted one, so a session's effort never raises their spend | `internal/adapter/server/service.go` (`sessionEngines`) |
| 12 | Per-session environment overrides (`Service.sessionEnvironments`: nofs, ACP buffers) | `server.Service` | session | evicted at `CloseSession` (`service.go:1694`) | lost; the no-fs override is re-registered by rehydration; ACP override is re-registered on editor reconnect. **EnvironmentRef persistence (ADR 0214, issue #462 phase 3):** a non-in-tree ref now persists ON the snapshot (`sessnap.Snapshot.EnvironmentRef`, `omitzero`) and reattaches a live `Environment` at run entry through `server.Config.EnvironmentResolver` — the in-tree Kinds never reach the resolver (they re-derive through the factories); a nil/mismatch/nil-Workspace result fails loudly (`ErrFailedPrecondition`), never a silent local fallback. The resolver is independent of engine rehydration (a remote session on a default provider/model rides the shared engine). A legacy zero ref is stamped from the first resolved live Environment on the next save (no migration sweep). | `internal/adapter/server/service.go:722` |
| 13 | Background children (`childRunRegistry`) | `agent.Run` | run | `drainChildren` at both terminate paths (cancel + join + seal) | registry lost; the child SESSIONS persist via `WithSubagentStore`/`WithMemberStore` and are individually resumable | `engine/agent/childregistry.go:128-131`; `engine/agent/subagent.go:599,1267` |
| 14 | askRegistry channel park + childAskRouter | `agent.Run` | run | unregistered on verdict/retract; dies with the run | lost, BUT no longer stranding (Phase 2): a post-death `Approve` on an awaiting session re-enters the loop AT the ask via `internal/adapter/server/service.go` (`resumeFromAwaiting`) rather than returning `ErrNoActiveRun`; the channel park itself is rebuilt by the resumed run | `engine/agent/permission.go:30-32,161` |
| 15 | Hook subprocesses | hookexec, per invocation | call | spawn, wait (30s default timeout, process-group kill) | nothing to re-attach | `internal/adapter/hookexec/hookexec.go:116,130` |
| 16 | Child-session retention GC goroutine + bounded storage-maintenance status (`storageMaintenanceState`) | `startChildGC` / `app.Build` | process | `Built.Close` invokes the returned idempotent cancel-and-join cleanup before Service/store teardown; cancellation covers startup, ticker, and context-aware list/delete/lease work, clears active health without claiming success, and sticky `ErrPruneUnsupported` also exits the worker. Disabled/unsupported paths return a no-op cleanup. Before logging enabled or scheduling its first sweep, automatic retention verifies `Service.MaintenanceMutationAvailable`; runtime loss/unsupported exclusion stops scheduling and settles health unavailable. Every automatic candidate deletion uses the same mandatory maintenance exclusion as manual cleanup; shareable stores without a working lease never start the worker | mixed: retention last/next sweep and last failure reset by design; active cleanup resets with its in-memory job registry (row 57) | `internal/app/childgc.go` (`startChildGC`); `internal/app/storage_health.go` (`storageMaintenanceState`); `internal/adapter/server/storage_management.go` (`MaintenanceMutationAvailable`) |
| 17 | Memory/user-model consolidation goroutines (dream) plus each consolidator's process-local rotating cursor and serialization gate | `app.Build` creates the goroutines; `dream.Consolidator` owns its cursor/gate | process | goroutines exit on ctx done; the cursor/gate die with their consolidator | **reset-by-design**: restart recreates the ticker/consolidator but resets candidate rotation and any in-flight serialization state. The gate serializes only one consolidator instance; it is not a cross-replica claim mechanism. | `internal/app/build.go` (`startMemoryConsolidation`, `startUserModelConsolidation`); `internal/adapter/dream/dream.go` (`Consolidator`, `GeneratePlan`, `ApplyPlan`) |
| 18 | Provider discovery owner: frozen listers, provider-local attempts/deadlines/completion channels/cooldowns, immutable observations/outcomes/model-status projection, and one-shot startup worker (ADR 0353) | `app.Build` | process; at most one active fetch per available lister provider | `providerDiscovery.Close` prohibits new attempts/publications, cancels and joins workers and deadline callbacks before borrowed credentials close, on both Build failure and normal Close. A timed-out fetch keeps its slot until actual return; caller cancellation affects only its wait. No-lister/native-only startup starts no workers | **reset-by-design**: restart starts without observations or cooldowns. Catalog/config remain pure resolution floors; native authenticated providers remain demand-only | `internal/app/provider_discovery.go` (`providerDiscovery`, `request`, `Close`); `internal/app/build.go` (`Build`) |
| | _(traceability)_ `liveMetaStore` is only a read view over row 18's owner. The engine, resolved-model echo, picker, DiscoverModels, and schedule validator consume the same accepted observation generation. Failed/empty latest outcomes retain positive last-good metadata but cannot supply healthy omission evidence. The echo is 0 for a blocked target; the engine's defensive 128000 floor is not admission evidence. No global settlement channel, prior-rejection history, independent metadata/outcome store, or writable inventory survives the migration. | | | | | `internal/app/livemeta.go`; `internal/app/provider_discovery.go` (`resolveModelWindow`); `internal/adapter/server/service.go` (`ListModelSnapshot`) |
| 19 | Driver connection cache (`driverConns`, one lazy `ClientConn` per URL) | `app.Build` | process | once-guarded closes folded into the per-seam closes | reconstructible (lazy redial next Build) | `internal/app/driverstore.go:54-90` |
| 20 | WebSearch `SearchProvider` (shared `*http.Client` + egress semaphore) | `app.Build` | process | none needed (stateless client; the semaphore is a per-process egress bound) | reconstructible (rebuilt from the backend-tier config at next Build) | `internal/app/build.go` (`buildSearchProvider`); `engine/adapter/search/httpsearch.go:79-80` (issue #26) |
| 75 | Jev delegated-router client and eight-slot semaphore (ADR 0352) | `app.Build`, retained on the private `Config.jevRouter` shared by shared and per-session router closures | process, one client and concurrency bound per Build | no explicit close; the stateless HTTP client owns no background goroutine and the semaphore dies with the Build | reconstructible from operator router configuration and the host-provided credential at the next Build; carries no session/run state, so List 2 gains no row | `internal/adapter/jevrouter/jevrouter.go` (`Router`, `New`); `internal/app/slots.go` (`prepareJevRouter`) |
| 21 | modelhook guardrail breaker (`failureStreak`) | `modelhook.Runner` (composition) | session | dies with the session Runner | **lost** (in-memory; a rehydrated session gets a closed breaker, fail-safe) | `internal/adapter/modelhook/breaker.go`; constructed on `modelhook.Runner` (commit `a032412`) |
| 22 | `askReviewBreaker` (headless ask-reviewer circuit breaker) | `agent.Run` | run | dies with the run | lost (run-scoped by design) | `engine/agent/askadjudicator.go:144` (commit `1b774d4`) |
| 23 | model-router classifier engine (ADR 0031; ADR 0034 reuses it for team members + Parallel branches via the SAME `routeTask` closure — NO new engine) | `buildModelRouterTask` closure (composition) | session (re-derived per session; ONE engine built per classification call) | none needed (tool-less, no hooks, no goroutine; GC'd after the one-turn drive) | reconstructible (rebuilt from the operator taxonomy + the session's provider/model at next Build / next classification); decision = derive (nothing persisted) | `internal/app/build.go` (`buildModelRouterTask`); `engine/agent/modelrouter.go` (`RunModelRouter`) |
| 24 | `modelRouterBreaker` (per-run model-router circuit breaker, ADR 0031; ADR 0034 reuses the SAME per-run breaker for team members + Parallel branches — NO new breaker) | `agent.Run` | run | dies with the run | lost (run-scoped by design, mirroring `askReviewBreaker` row 22) | `engine/agent/modelrouter.go` (`modelRouterBreaker`); armed in `engine/agent/loop.go` (`startRun`) |
| 25 | `gitWorktreeLister` (osfs-backed worktree discovery, issue #102) | `app.Build` | process lifetime | none (value type, no goroutine, no Close needed) | reconstructible (rebuilt from `cfg.Shell` at next Build; no state) | `internal/app/build.go` (`buildWorktreeLister`) |
| 26 | routed team-member / Parallel-branch child engine (ADR 0034) | `buildMemberEngine` (member) / `buildParallelEngineFactory` (branch), minted in composition | per-AddMember (member; reused across rounds, torn down on member teardown) / per-call (branch; torn down with the branch fork) | dies with the member/branch (the SAME lifecycle as the non-routed member/branch engine it replaces — routing changes only the model, not the lifetime) | reconstructible (a new team/Parallel call re-classifies + re-mints); decision = derive (nothing persisted; the routed model is List-2 row 18) | `internal/app/build.go` (`buildMemberEngine`, `buildParallelEngineFactory`) |
| 27 | held session leases + per-session renewer goroutines (`Service.heldLeases`, Phase 4) + local retained generation-liveness flock fds + local mutation-capability/lost-owner maps (`SessionMutationCapability`, `Service.lostOwnership`, ADR 0294) | `server.Service` owns the lifecycle and shares the capability with its guarded SessionStore/EventLog/ToolCallRecorder projections; `flocklease.Lease` owns each local generation fd | session (one lease + renewer, retained generation fd, and capability state per leased session; one lightweight lost-owner entry until local teardown after definitive renewal loss) | acquire grants the capability. Close and joined graceful drain cancel renewal, invalidate locally, then release with a cancel-detached bounded context. Lease loss or drain timeout invalidates before cancelling the run; a definitive non-awaiting loss releases the exact generation, while awaiting loss retains the invalid hold without Release so takeover preserves the durable handoff point. Capability and held-lease tombstones are removed when stale lifecycle references settle, while `lostOwnership` prevents that stale Service from reacquiring until explicit `CloseSession` teardown — with TWO independent, narrowly-scoped exceptions, both root-authorized (`stale-session-reconcile`) and both re-verifying under `s.mu` before writing: (1) the stale-session reconcile sweep's `SettleIfStale`, whose caller is independently pre-verified (age-horizon + local liveness) as recovering a genuine crash orphan rather than a live handoff, and which clears `lostOwnership` itself on a successful re-Acquire (`acquireLeaseCore`'s `bypassTombstone` path, used only via `acquireMutationLeaseForStaleSettle`) — this covers ONLY a `StateRunning` candidate; and (2) `ReconcileLeaseLossTombstone` (issue #1334), the awaiting/cancelled counterpart, for the sessions `onLeaseLost` itself drives OUT of `StateRunning` while handling the very loss that set the tombstone (to `awaiting` via the `preserveAwaiting` branch, or eventually `cancelled`) — a population `SettleIfStale`'s `StateRunning`-only candidacy can never rediscover. It performs a bounded TRIAL Acquire+immediate-Release (the shared `leaseTrial` helper, also used by `SessionStale`'s own refinement) against exactly the ids `Service.lostOwnership` already names (an in-memory read, never a store-wide scan — that population is the steady state for huge numbers of ordinary finished sessions), clears the tombstone plus any stale invalid `heldLeases` entry `onLeaseLost`'s `preserveAwaiting` branch left behind, and never holds a lease or repairs session state itself: the next genuine run-entry's existing `loadAndReopen`/`resumeFromAwaiting` still does that. Wired into the SAME composition-level sweep pass as `SettleIfStale` (`internal/app/session_reconcile.go`'s `reconcileLeaseLossTombstones`), no new goroutine. Awaiting loss also retracts the local ask while preserving the durable snapshot. No mutation is admitted after invalidation starts, while an already-admitted backend call may still finish because this is local invalidation rather than token-bearing storage fencing. Local exact-token Release durably tombstones then closes/unlocks its generation fd; stale release cannot touch a successor, the stable per-session sentinel is operation-scoped, and failure paths close newly opened handles or retain failed-close references for retry. SIGKILL makes the kernel close every retained fd | **reconstructible/reset-by-design** (a restart starts with empty capability/lost-owner maps and re-acquires on the next admitted operation; record expiry preserves the generic TTL takeover contract while a crashed local holder's generation flock releases immediately so a survivor can take over before expiry and increment the token preserved in the durable record. No local validity/tombstone is persisted, and it must not survive process identity. The durable awaiting `PendingAsk` and session state remain in the snapshot; see List 2 row 42). Constructed when a lease backend is explicitly wired or store-provided, and automatically as the existing flock lease beneath every local JSONL StoreDir. Other no-lease shareable stores do not gain destructive-maintenance authority; automatic retention fails closed | `internal/adapter/server/service.go` (`heldLeases`, `lostOwnership`, `acquireLease`, `acquireLeaseCore`, `acquireMutationLeaseForStaleSettle`, `leaseTrial`, `SessionStale`, `ReconcileLeaseLossTombstone`, `LostOwnershipCandidates`, `renewLoop`, `onLeaseLost`, `GracefulDrain`, `releaseLease`); `internal/app/session_reconcile.go` (`reconcileLeaseLossTombstones`); `internal/adapter/server/mutation_capability.go` (`SessionMutationCapability`); `internal/adapter/flocklease/flocklease.go` (`Lease`, `heldLease`, `Acquire`, `Release`); wired at `internal/app/build.go` (`buildSessionLease`) |
| 28 | MCP standalone-SSE listener goroutine (`handleSSE`) per connected server | `mcp.Server` | per connected server (rides the SDK session, opened after `initialize` when `DisableStandaloneSSE: false`) | `Server.Close()` → `session.Close()` → `conn.Close()` cancels `connCtx` → `handleSSE` returns (async; the `mcp` package's `goleak` gate has a targeted ignore list for the SDK + stdlib goroutines that unwind asynchronously after close) | none (the SDK reconnects the stream itself on a transient drop; #177/ADR 0056 reconnects the whole session when the SSE reconnect exhausts → `ErrSessionMissing`) | `internal/adapter/mcp/mcp.go` (`dial`); ADR 0057 |
| 29 | guardrail session waiver (`WaiverHolder`, ADR 0062) | `app.Build` constructs; the engine arms it via the `modelhook.Runner`'s `port.HookApprovalLearner` on a human `VerdictAllowAlways`, `modelhook.Runner.check` consults it | process | self-clearing; dies with the process (no `Close` — a nil `*WaiverHolder` is the byte-identical OFF posture) | **lost** (in-memory; a waiver never silently survives restart — fail-safe: the call re-blocks/re-asks until a human re-approves it, ADR 0062) | `internal/adapter/modelhook/waiver.go` (`WaiverHolder`); armed via `internal/adapter/modelhook/modelhook.go` (`LearnHookApproval`); constructed in `internal/app/build.go` |
| 30 | scheduler tick goroutine (Phase 5, ADR 0059) | `internal/adapter/scheduler` (`Scheduler`), held by `server.Service.scheduler` | process | `Scheduler.Stop` cancels the tick loop + joins in-flight fires (with a grace) + releases the leader lease; `Service.Close` stops it FIRST so fires drain while the service is alive | **reconstructible** (a restarted process re-acquires the leader lease or ticks standalone, and re-polls `ScheduleStore.Due` — the store is ground truth, the lookahead is derived); only constructed when `--scheduler` is set (byte-identical default when unwired) | `internal/adapter/scheduler/scheduler.go` (`tickLoop`, `Start`, `Stop`); wired at `internal/app/build.go` (`startScheduler`) |
| 31 | scheduler leader-lease renewer goroutine (Phase 5, ADR 0059) | `internal/adapter/scheduler` (`Scheduler`) | process | cancelled at `Scheduler.Stop` (the leader lease is released alongside the tick loop) | **reconstructible** (a restarted leader re-acquires the well-known `__scheduler__` lease on the SAME backend as the run-entry session lease, different id — no contention; a non-leader stands down). Hygiene, NOT correctness: `ScheduleStore.Claim` is the at-most-once fence; the lease only prevents two replicas ticking the same store | `internal/adapter/scheduler/scheduler.go` (`renewLeader`); `internal/app/build.go` (`buildScheduler`) |
| 32 | scheduler per-schedule-name `fireMu` mutex map (Phase 5, ADR 0059; `lockFireName`) | `internal/adapter/scheduler` (`Scheduler.fireMu sync.Map`) | process | lazily created, never pruned (one `*sync.Mutex` per distinct schedule name); dies with the process at `Scheduler.Stop` | **reconstructible** (a restart re-acquires lazily on the next fire — it is a synchronization gate, not state-of-record; the at-most-once truth lives in `ScheduleStore.Claim`/`ClaimNow`). Growth is bounded by authenticated-caller schedule-name cardinality (the RPCs are auth-gated; names are operator-curated via CreateSchedule). A `FireNow` on a nonexistent name still acquires an entry before the `Load` miss — accepted: the TOCTOU fence requires the lock precede `Load` so a second concurrent caller sees the first's Claim | `internal/adapter/scheduler/scheduler.go` (`fireMu`, `lockFireName`); called from `fireOne` + `FireNow` |
| 33 | Former independent live-listing outcome store and global refresh cooldown | consolidated into row 18's discovery owner by ADR 0353 | process | no independent resource remains | **reset-by-design** with row 18 | `internal/app/provider_discovery.go` |
| 34 | DURABLE per-session pending-delivery queue (ADR 0075 fire-result-delivery Scenario 4; `port.DeliveryQueue` + the `FileDeliveryQueue` durable adapter) | composition (`internal/app`) — a `port.DeliveryQueue` wired at `app.Build` (the fire path enqueues, the loop's turn-boundary drain + the run-entry funnel dequeue) | session (keyed on the ORIGIN session id — the session that created the schedule — NOT a per-Run registry; the exactly-once ledger is session-scoped, so a note queued in run N drains in run N+1 if run N ends first) | the `FileDeliveryQueue` writes a per-session `.delivery.jsonl` append-only sidecar + a `.delivery.ledger.json` delivered-seq ledger under the SAME store dir as the session snapshot (the `.events.jsonl` precedent); files opened per call, never held. `NopDeliveryQueue` is the byte-identical no-delivery default (a deployment with delivery unwired sees nothing); `InMemoryDeliveryQueue` is the memstore-tier default (in-process, not durable — honest degradation to empty across a restart). A bounded backlog cap drops the OLDEST pending note with a WARN (the injected `port.Diagnostics`) rather than growing unboundedly on an overloaded origin | **persisted** (the `.delivery.jsonl` + `.delivery.ledger.json` sidecars survive the restart and are reloaded on the origin's next run-entry; the monotonic per-session seq is DERIVED from the durable enqueue log so a restart does not re-mint seq 1). The `NopDeliveryQueue` default and the `InMemoryDeliveryQueue` memstore-tier queue are NOT durable (byte-identical no-delivery / empty-after-restart for a restarted process) | `internal/app/delivery_queue.go` (`FileDeliveryQueue`, `InMemoryDeliveryQueue`); `engine/port/delivery_queue.go` (`DeliveryQueue`, `DeliveryNote`, `NopDeliveryQueue`); see List 2 row 23 |
| 35 | Per-session live event subscription registry (ADR 0075 fire-result-delivery Scenario 5; `Service.Subscribe`/`PublishSessionEvent` + the `subscriptions` map) | `internal/adapter/server.Service` (the relay — the loop stays storage-agnostic and never calls it) | process (an in-memory `map[session.SessionID]map[int64]chan session.Event`, guarded by a dedicated `subMu sync.RWMutex` separate from `s.mu` so a delivery-run Publish does not contend with the run/registry hot path) | each `Subscribe` returns a buffered (64) channel + an IDEMPOTENT unsubscribe func (a `sync.Once` — safe to call explicitly AND deferred); the subscriber goroutine owns draining. `PublishSessionEvent` holds `subMu`'s read lock while it sends NON-BLOCKING to each subscriber: a full channel DROPS the event (drain-to-discard — a dead client never wedges the delivery run). Unsubscribe and `Service.Close` close/remove channels under the write lock; no recover is needed. `Service.Close` marks `subscriptionsClosed`, and post-shutdown `Subscribe` registration is rejected with an immediately closed channel | **in-memory only** (a live-subscription registry has no restart fidelity to preserve — a subscriber that disconnects re-Subscribes on reconnect and catches missed deliveries via the `StreamSessionEvents` replay feed / the durable queue, List 1 row 34 / List 2 row 23). Nothing persisted; a restart drops the registry and every subscriber reconnects | `internal/adapter/server/service.go` (`Subscribe`, `PublishSessionEvent`, `subscriptions`); `internal/app/scheduler_delivery_run.go` (the delivery driver's Publish fan-out) |
| 36 | `escapePolicy` per-root classifier cache (`escapePolicy.clfs`: session workspace root → `*escapeClassifier`, built once per root; path-escape-posture plan, docs/acceptance/path-escape-posture.md AC-W2-F3) | `app.Build` (ONE instance wraps the shared main-engine permission policy; per-session root-awareness comes from `ws.Root()` on every `Evaluate`, never a per-session policy) | process (entries live as long as the shared policy; dies with the process at Build teardown) | lazily created under a mutex on first `Evaluate` for a root, never pruned. **Cardinality is deployment-bounded by construction**: one entry per DISTINCT session workspace root the process ever classifies — the same order as the (already capped) live-session count plus the bounded historical-session set the run-entry funnel rehydrates, NOT per-session-per-tool-call growth; the value is a pair of canonicalized string slices (no `*os.Root`, no open handles), so a stale entry costs bytes, not fds. An LRU was rejected: eviction only ever drops a REBUILDABLE classifier (the next `Evaluate` re-canonicalizes the root and rebuilds), so bounding buys nothing the rebuild does not already make cheap | **reconstructible** (a restarted process rebuilds the shared policy at the next Build and re-derives each root's classifier lazily on the next `Evaluate`; the cache is a pure derivation of the session root — nothing persisted, decision = derive) | `internal/app/escapepolicy.go` (`escapePolicy.clfs`, `classifierFor`); `internal/app/escapeclassifier.go` (`escapeClassifier`) |
| 37 | mecatequi OTLP metrics push PeriodicReader + flush-on-exit (ADR 0098) | `cmd/mecatequi` (`realMain`, via `internal/cliconfig.HeadlessTelemetry`) | process (OPT-IN: only when `--otlp-metrics-endpoint` is set) | the `defer flushTelemetry` runs BEFORE `defer built.Close()` (LIFO → flush first), bounded by `--otlp-shutdown-timeout`; `Providers.Shutdown` flushes + stops the periodic reader. `os.Exit` then kills any lingering in-flight export goroutine (a short-lived run does not drain it) | **reset-by-design**: a single-shot run has no scrape state to persist; the periodic reader is process-local and the pushed metrics are the collector's record, not mecatl's. Nothing here needs a List 2 row | `cmd/mecatequi/main.go` (`realMain`); `cmd/mecatequi/observability.go` (`buildObservability`, `flushTelemetry`); `internal/cliconfig/telemetry.go` (`HeadlessTelemetry`); `internal/adapter/telemetry/otlp.go` (`newMetricPushReader`) |
| 38 | mecak8s `/metrics` loopback listener (ADR 0098) | `cmd/mecak8s` (`serve`) | process (OPT-IN: only when `--metrics-addr` is set; loopback-only, fail-closed at parse time) | the `metricsSrv *http.Server` joins the `errCh` set and the `boundedShutdown` sequence (its `Shutdown` runs alongside the API listener's); SIGTERM also flushes OTLP via the `defer flushTelemetry` (LIFO before `built.Close()`) | **reset-by-design**: the prometheus reader is process-local; the scraped metrics are the scraper's record. The admin mux (`/metrics` + pprof/expvar) is loopback-only and carries no durable state. Nothing here needs a List 2 row | `cmd/mecak8s/serve.go` (`serve`, `boundedShutdown`, `isLoopbackAddr`); `cmd/mecak8s/observability.go` (`buildObservability`, `flushTelemetry`); `internal/adapter/telemetry/adminmux.go` (`NewAdminMux`) |
| 39 | ToolHive-LLM direct-mode OIDC token source (issue #265, ADR 0102) | `internal/app` — built ONCE per Build inside `newDirectGatewayEntry` and captured by the `bearerRoundTripper`; the underlying `*llm.TokenSource` is toolhive's `pkg/auth/tokensource.OAuthTokenSource` | process (OPT-IN: constructed ONLY when the resolved routing mode is `direct` — the proxy path allocates none of this and stays byte-identical to pre-#265) | none needed and none exists: it holds no goroutine, no fd and no timer — an in-memory access token + expiry behind its own mutex, refreshed lazily on the per-request `Token(ctx)` call and dying with the process. It is deliberately NOT re-minted per session: every per-session/heal engine re-mint appends the SAME `WithHTTPClient`, so one token source serves every session (`construct()` closes over `extra`). Note the mutex serializes `Token` across concurrent sessions, so a slow IdP refresh blocks other requests for its duration — bounded by each caller's request ctx | **reconstructible** (a restarted process rebuilds it at the next Build and re-derives an access token from the keyring-held refresh token; decision = derive). Nothing enters a snapshot: the access token is short-lived derived material, and the refresh token already survives outside mecatl in the OS keyring with only its REFERENCE (`CachedRefreshTokenRef`) in ToolHive's config — so no List 2 row | `internal/adapter/toolhivellm/tokensource.go` (`DirectTokenSource`, `buildTokenSource`); consumed by `internal/app/registry.go` (`newDirectGatewayEntry`, `bearerRoundTripper`) |
| 40 | OS-keyring / D-Bus connection behind the direct-mode secrets provider (issue #265, ADR 0102) | toolhive's `pkg/auth/secrets` (`GetSystemSecretsProvider`), opened transitively by row 39's construction; mecatl never holds the handle | process (OPT-IN with row 39; on Linux this is a `godbus` connection with its own reader/writer goroutines) | NOT mecatl-owned — no `Close` seam is exposed and none is folded into Build's `closeAll`; the connection is process-scoped and released at exit. This is the accepted residual, and it is why `internal/app/leakmain_test.go` pins `godbus/dbus/v5.newConn.func1` + `(*Conn).inWorker` by top-of-stack (a narrow pin, NOT a blanket suppression — any other leak still fails the gate) | **reconstructible** (re-opened lazily by the next Build's token-source construction; it is a transport to the keyring, never state-of-record — the credential it fetches is the keyring's, decision = derive). No List 2 row | `internal/adapter/toolhivellm/tokensource.go` (`buildTokenSource`); the goleak pins live in `internal/app/leakmain_test.go` (`TestMain`) |
| 41 | OIDC token-validator JWKS cache + its background key-rotation refresh (caller identity, ADR 0204 decision 3/7 and ADR 0206; the `toolhive-core/authn` validator wrapped by the opt-in `authn/oidc` module and adapted to `server.PrincipalValidator`) | the `authn/oidc.Validator` instance, constructed once per process through `internal/cliconfig/oidc.go` (`OIDCValidator`) and handed to `server.SecurityConfig.Validator`; the module owns the reusable lifecycle seam, never the JWT/JWKS mechanics | process (OPT-IN: only when `--oidc-issuer` is set; the zero value is identity OFF and allocates nothing) | **explicit teardown at the edge**, NOT the root context's cancel: the validator's background refresh is stopped by its OWN `Close()`, and cancelling a context does not call it. `internal/adapter/server/authn.go` (`Authenticator.Close`) type-asserts an OPTIONAL `io.Closer` on the configured validator (the `port.HookApprovalLearner` idiom — `PrincipalValidator` stays single-method, so a fake without teardown needs none) and closes it once (`sync.Once`); both mains `defer auth.Close()` (`cmd/mecated/main.go`, `cmd/mecak8s/serve.go`). The SERVER-ROOT context is still what the constructor is handed — deliberately NOT a per-request one, which would tear key rotation down with the first request — but it bounds in-flight fetches, not the refresh loop's lifetime. The refresh goroutine has no caller and therefore runs under the explicit system principal `mecatl:internal / jwks-refresh` (`internal/syscaller/syscaller.go` (`RootJWKSRefresh`)) | **reconstructible** (a restarted process re-resolves the flags and re-fetches the key set on the next Build; nothing is persisted and nothing should be — a cached signing key is a derivation of the IdP's live JWKS). Cached-key trust is bounded by `--oidc-max-jwks-staleness` (1h default; 0 explicitly disables the bound; ADR 0205) | `authn/oidc/oidc.go` (`Validator`, `NewValidator`, `Close`); `internal/cliconfig/oidc.go` (`OIDCConfig`, `OIDCValidator`, `RegisterOIDCFlags`); `internal/adapter/server/authn.go` (`PrincipalValidator`); `internal/syscaller/syscaller.go` (`RootJWKSRefresh`) |
| 42 | osfs same-path mutation lock stripes (ADR 0208) | `internal/adapter/osfs` package | process (fixed array of 256 `sync.Mutex` values, physical-target `hash/maphash` stripe) | none needed: fixed allocation, no goroutine/fd/map entry, released with the process. Existing targets canonicalize fully; missing targets canonicalize parent+basename, so stable symlink aliases converge. A collision only serializes unrelated mutations; concrete osfs bootstrap `Write` plus Workspace `CreateFile`/`ReplaceFile` use the same stripe across Workspace instances. The guarantee is process-scoped and assumes a non-cooperating writer does not race target existence or symlink identity during pre-lock canonicalization | **reset-by-design**: pure synchronization, no state-of-record. Re-created as zero-value mutexes at process start; backend file contents remain authoritative. Multi-process deployments receive only per-backend-handle atomicity until remote backend CAS lands. No List 2 row | `internal/adapter/osfs/osfs.go` (`pathLocks`, `pathLock`) |
| 43 | Stale-session sweep goroutine (Phase 6, issue #475) | `internal/app` (`startStaleSessionReconcile`) | process (unconditional — always runs a startup sweep then a persistent ticker; there is no operator-facing flag to disable it, only the per-pass `LeaseSweepDisabled` sticky-disable when a wired lease backend does not support leasing) | the returned closer `cancel()`s the sweep's own ctx AND `wg.Wait()`s the goroutine (mirroring `startLiveModelRefresh`'s exact idiom), folded into `Built.Close`'s `closeAll` so a caller that never cancels `Build`'s own ctx (the common test-fixture shape) still gets a clean, race-free teardown — `Built.Close()` cannot return while a sweep pass is mid-`SettleIfStale` | **derived** (a restarted process starts a fresh sweep at the next `Build`'s startup pass; nothing about the sweep itself is state-of-record — it repairs the ALREADY-persisted `session.Session` snapshot, so there is nothing to carry over). No List 2 row | `internal/app/session_reconcile.go` (`startStaleSessionReconcile`, `sweepStaleSessions`); wired at `internal/app/build.go` |
| 45 | Operator-profile last-good snapshot (#508 slice) | `agent.Run` | run | dies with the run | **reset-by-design**: a new run reads the durable source; this is fail-soft continuity, never state-of-record. See List 2 row 27 | `engine/agent/loop.go` (`Run.operatorProfile`, `refreshOperatorProfile`) |
| 46 | Durable staged-learning proposal document + stable flock (issue #509, ADR 0109) | `app.Build` constructs `reflectionstore.Store` in `reflections/` beside the configured or conventional user-model path in automatic modes; Off installs a `lazyProposalRepository` and constructs neither directory nor flock until an explicit reflection/proposal operation | process handle over principal/project-partitioned durable data | the flock is acquired and released per operation; files are opened per call. A bounded JSON document is committed with temp-file fsync, rename, and directory fsync; empty startup creates no proposal document | **persisted**: proposals, CAS versions, bounded decisions, and promotion receipts reload from the document. `promoting` plus a memory revision carrying the same `ProposalID` reconciles after restart without a second write. See List 2 row 29 | `internal/adapter/reflectionstore/store.go` (`Store`, `locked`, `save`); `engine/adapter/memorypromotion/memorypromotion.go` (`Process`); `internal/app/build.go` |
| 47 | Build-owned reflection materialization lifecycle gate/active-operation accounting plus evidence-reflection worker pool, fair per-principal queues, singleflight map, and bounded receipt cache (issue #509, ADR 0109; refined by ADR 0300) | `app.Build` constructs exactly one lifecycle gate with the reflection coordinator when reflection capability is available; Review/Auto start coordinator workers lazily, while Off has no worker and explicit reflection runs its pre-admission scan synchronously | process (one closeable gate/context and bounded active-operation count; global worker count; bounded per-principal FIFO queues with fair principal rotation) | Materialization streams under the caller plus Build contexts and starts no goroutine per job. It creates no queue/singleflight/receipt/reservation/provider/repository state before successful bounded selection/admission. `Built.Close` first closes the gate, rejects new scans, cancels and joins active scans, then stops coordinator admission, publishes bounded closed receipts for queued waiters, clears pending state, cancels active jobs, and joins every started worker. Selected jobs retain the existing per-job and aggregate queued-byte caps; completed receipts evict oldest-first, while principal+session+protocol+identity-boundary+selected-digest singleflight collapses duplicates | **reset-by-design**: lifecycle accounting, queued/running jobs, singleflight keys, and receipts are transient coordination state. No pre-admission operation survives cancellation/close. The immutable aggregate manifest is persisted with staged proposals (List 2 row 29); restart does not retrospectively sweep completed sessions. See List 2 row 30 | `internal/app/reflection_coordinator.go` (`reflectionCoordinator`, `newReflectionCoordinator`, `Close`); lifecycle wiring at `internal/app/build.go`; `internal/app/reflection_observer.go` (`reflectionObserver`) |
| 48 | Durable agent-owned skill manifest, immutable version files, bounded receipt index, and stable flock (issue #510, ADR 0111) | `app.Build` constructs one lazy `skillstore.Store` beside the user-model store and shares it with reflection, server lifecycle APIs, and every catalog assembly | process handle over principal/project-partitioned durable data | the stable `skills.lock` flock is acquired and released per operation; files are opened no-follow and regular-file status is checked from the opened descriptor. `manifest.json` (including the bounded, stable-order receipt index) and each content-addressed `versions/<version>/SKILL.md` use temp-file fsync, rename, and directory fsync. Empty startup is lazy. A crash before manifest commit can leave only an unreferenced immutable version, which reopen safely ignores | **persisted**: bounded lifecycle metadata, provenance, validation disposition, evaluations, active selection, immutable bodies, and historical-version receipt pages reload together. Expired receipt cursors fail closed. Exact convergence preserves the stricter similarity disposition. Cross-store proposal linkage is create-then-CAS-link and reconciles by ProposalID/SkillID without a duplicate. See List 2 row 31 | `internal/adapter/skillstore/store.go` (`Store`, `locked`, `save`, `ensureVersion`, `ListSkillReceipts`); `internal/adapter/skillstore/materialize.go` (`MaterializeProposal`) |
| 49 | Partitioned live learned-skill generations and publication gate (issue #510, ADR 0111) | `app.Build` constructs one `skillfs.AtomicCatalog` plus one `learnedSkillPublication`; caller-bound per-session catalogs and the server lister select immutable views from them | process (one mutex plus bounded immutable path-free generations keyed by principal/project; no goroutine, watcher, path, asset materialization, or workspace root) | one gate serializes collision check, durable transition, authoritative reread, generation swap, and partition-local failure quarantine. Refresh retains unrelated partitions and immutable external entries. Each tool request uses one caller-bound view for Spec/inventory/Execute. Caller-scoped list/run hydration reconciles global and admitted project partitions; uncertain reads clear that partition. Build teardown needs no explicit close | **derived** from List 2 row 31's active durable versions plus the build-time immutable external skill snapshot. Views/generations reset on restart and lazily reconstruct for the authenticated caller; a crash after durable activation but before publication heals without another mutation. No separate persisted catalog state | `engine/adapter/skillfs/atomic.go` (`AtomicCatalog`, `RefreshPartitions`, `View`, `LiveTool`); `internal/app/learned_skills.go` (`learnedSkillPublication`, `learnedSkillPublisher`); `internal/app/catalog.go` (`registerSkillFamily`) |
| 50 | Local encrypted/plaintext credential records, stable flock sentinels, store-owned encryption key, and clientauth backend pin (issue #519, ADR 0218; clientauth selection ADR 0318) | canonical MCP profiles in `internal/cliconfig` (`MCPProfiles`), transferred to `app.Build`, with explicit login borrowing the Store; remote mecatui login/connect/reauth/logout own their selected clientauth Store handles | namespace/store handle for the in-memory key and rooted namespace; durable records and clientauth `clientauth-credential-backend.json` under the canonical owner-controlled root | operations open/close flock/data/temp files per call; stable per-record `.lock` sentinels and the root selection lock persist. Encrypted records and `clientauth-plaintext/` records remain on disk. Store `Close` closes its rooted handle; encrypted Close additionally clears its owned key. Selection releases its root lock before OAuth; cancellation and logout retain the backend pin, while logout conditionally deletes the target credential. Crashes may leave owner-only temporary files, deliberately not swept | **persisted / explicitly reattached**: MCP reopens its explicit root/namespace with the same externally acquired 32-byte key. Clientauth login pins one backend before OAuth; subsequent commands reopen that exact pinned backend, never detect/fall back/migrate. Plaintext needs no key, retains local CAS/lock protections, and is not encrypted at rest. Rotated credentials survive process restart. No List 2 row: credentials and backend selection are adapter-owned durable source of truth, not session/run state | `internal/adapter/credentialstore/encrypted_file.go` (`EncryptedFileStore`, `NewEncryptedFile`, `Close`, `PlainFileStore`, `NewPlainFile`, `OpenExistingPlainFile`); `internal/adapter/clientauth/credential_backend.go` (`ResolveCredentialStore`, `OpenExistingCredentialStore`); `internal/adapter/credentialstore/envelope.go`; `internal/cliconfig/mcpprofile.go` (`LoadMCPProfiles`) |
| 51 | Optional per-server MCP OAuth controller: one official handler, token source/CAS version, authorization flight, dedicated hardened HTTP client idle pool, and direct-DCR pending/ready registration plus generation-bound no-refresh grant records (issue #521/#542, ADR 0220/0221/0325) | `internal/adapter/mcp.Server` | connected server / one credential identity | `Server.Close` closes the MCP session first, then idempotently closes the controller's HTTP idle pool; the injected mutable Store or read-only Reader handle is borrowed and remains injector-owned. No background refresh, listener, browser, or timer goroutine | **persisted / explicitly reattached when mutable; source-defined when read-only**: a new controller derives the same opaque key from injected identity/config, reloads the injected persistence source, and reattaches through the official handler's initial-token-source hook. DCR reload validates its identity, ready registration generation, and matching grant before token use; pending/corrupt/mismatched records fail closed, while in-memory flight/idle state resets. Opt-in in-memory refresh deliberately resets on restart. No List 2 row: credentials are adapter-owned state, not session/run state | `internal/adapter/mcp/oauth.go` (`OAuthController`, `NewOAuthController`, `Close`); `internal/adapter/mcp/oauth_dcr.go` (`PrepareOAuthDCRLogin`, `resolvePreparedDCR`); `internal/adapter/mcp/oauth_dcr_grant.go`; `internal/adapter/mcp/oauth_tokensource.go`; `internal/adapter/mcp/mcp.go` (`Close`) |
| 52 | Opt-in MCP OAuth loopback interaction: random IPv4 listener, bounded callback HTTP server, runtime serialization gate, and optional browser child (issue #522, ADR 0112) | `mcp/oauthlogin.Runtime`; the one-shot caller owns the `Authorize` operation | one authorization operation; the gate lives with the explicitly constructed runtime | every return path cancels presentation, publishes a terminal callback outcome, performs detached bounded `http.Server.Shutdown`, closes the listener, joins `Serve`, and only then releases the runtime gate. The fixed-argv browser command inherits the operation context and is killed on cancellation; no refresh goroutine exists | **per-operation / reset-by-design**: listener, server, callback state, and browser process must not survive. The resulting credential is row 51's durable adapter record. No List 2 row because no session/run state is held | `mcp/oauthlogin/runtime.go` (`Runtime`, `Authorize`, `stopServer`); `mcp/oauthlogin/callback.go`; `mcp/oauthlogin/browser.go`; invoked by `internal/app/mcplogin.go` (`LoginMCP`) |
| 53 | Explicit environment credential Reader handle (issue #542, ADR 0221) | canonical MCP environment profiles loaded by `internal/cliconfig.MCPProfiles` and transferred to `app.Build`; used by mecak8s/mecatequi/mecated serving without a presenter | namespace/key/environment-name/lookup tuple | no goroutine, fd, cache, mutation, or global lookup; `EnvironmentReader.Close` idempotently seals the borrowed lookup handle. Each `Get` snapshots its immutable lookup configuration under the lifecycle read lock, invokes the host callback without that lock, then rechecks closure before returning | **explicitly reattached / source-defined**: restart reconstructs the Reader with the same tuple and receives the process/pod's current environment snapshot. In-memory refresh is not written back. Kubernetes Secret env rotation requires an external controller and pod restart, or a future Secret `resourceVersion` CAS writer. No List 2 row: this is adapter credential state, not session/run state | `internal/adapter/credentialstore/environment.go` (`EnvironmentReader`, `NewEnvironment`, `Close`); `internal/cliconfig/mcpprofile.go` (`LoadMCPProfiles`) |
| 54 | Canonical MCP profile loader's shared encrypted Stores and per-profile environment Readers (issue #523, ADR 0113) | `internal/cliconfig` (`MCPProfiles`), transferred to `app.Build` through `Config.MCPProfileLifecycle` | process / one loaded operator profile set; local Stores are shared by `(root, key-env reference)` within the load | partial-load failure closes every source already opened; successful Build closes the global MCP manager/controllers first, then the profile lifecycle, and `MCPProfiles.Close` closes every distinct Store/Reader exactly once. The default/no-profile path opens nothing and owns nothing | **persisted / explicitly reattached for local; source-defined for environment**: restart reloads operator metadata and references, derives the identical opaque record key, and reopens row 50/53. Process-local read-only refresh resets by design. No List 2 row because credentials remain adapter state, not session/run state | `internal/cliconfig/mcpprofile.go` (`LoadMCPProfiles`, `MCPProfiles`, `Close`); `internal/app/build.go` (`Config.MCPProfileLifecycle`, `Build`) |
| 55 | Durable automatic-learning reservation ledger (ADR 0259, superseding ADR 0114's process-local accounting when wired) | `app.Build` constructs one `automaticstore.Store` beside the attempt store for local Review/Auto learning, or borrows the negotiated learning-driver `AutomaticAdmissionLedger` | process handle over content-free global/principal reservation windows; no goroutine or retained fd | each admission atomically reserves deterministic attempt-linked count/tokens before attempt creation. The stable flocked store or remote driver enforces global/per-principal limits, weighted cooldown, dedupe, backend-authoritative expiry/reassignment/discovery, and retained/reclaimed charges. The local document prunes resolved records after dedupe retention and caps records at 512 globally and 128 per opaque principal partition; unresolved saturation fails closed before its 16 MiB document bound. `Built.Close` closes only borrowed driver lifecycle through the existing connection owner | **persisted**: windows, charges, fences, cooldown/dedupe membership, and reservation/attempt linkage reload across restart and are shared by independent clients. The durable automatic-admission ledger is the restart authority; expired held records remain discoverable through local and remote implementations | `engine/learning/automatic_ledger.go` (`AutomaticAdmissionLedger`, `MaxAutomaticReservationDiscoveryBatch`, `MaxAutomaticReservationRecords`, `MaxAutomaticReservationRecordsPerPrincipal`); `internal/adapter/automaticstore/store.go` (`Store`, `Reserve`, `DiscoverExpired`, `Retain`, `Reclaim`, `Reassign`); `internal/adapter/grpcdriver/automaticledger.go` (`AutomaticAdmissionLedger`) |
| 57 | Cleanup confirmation signer + bounded opaque-plan/sanitized-job registries (`Service.cleanupTokenKey`, `cleanupPlans`, `cleanupJobs`) | `server.Service` | process (one random HMAC key, at most 128 caller-bound plan payloads, and at most 128 job projections) | no goroutine or external handle; `Service.Close` drops them with the Service. Entries evict oldest when their cap is reached. Tokens expose only an unguessable MAC; stored payloads contain principal/catalog/scope/policy digests and counts, while jobs contain stable codes and sanitized messages | **reset-by-design**: restart invalidates outstanding confirmation tokens and in-memory job inspection; no deletion is resumed implicitly. A caller performs a fresh read-only plan and retries remaining candidates. Committed store deletions are already durable and idempotent | `internal/adapter/server/cleanup.go` (`PlanSessionCleanup`, `ApplySessionCleanup`, `rememberCleanupJob`); `internal/adapter/server/service.go` (`cleanupTokenKey`, `cleanupPlans`, `cleanupJobs`) |
| 58 | Manual dream review registry/coordinator (ADR 0228) | `app.Build` constructs one `dreamReviewCoordinator` and injects it into `server.Service`; absent when no target is capable or ownership enforcement is enabled | process (opaque random IDs; at most 64 total records and 8 non-terminal records per target; each generation/application shares its target consolidator's existing row-17 gate) | no goroutine, timer, fd, or client cleanup operation. Generation/decision lazily remove expired non-active records. Terminal records retain only decision/receipt/failure state for idempotence and immediately zero target, authoritative plan, and review content; all records die with Build/process | **lost by design**: a pending plan is intentionally neither persisted nor replicated. Restart, expiry, or a decision routed to another replica requires explicit regeneration and another provider call. This is not HA/sticky-route portable in v1. See List 2 row 36 | `internal/app/dream_review.go` (`dreamReviewCoordinator`, `Generate`, `Decide`, `cleanupLocked`, `finishLocked`); `internal/adapter/server/dreamreview.go` (`DreamReviewer`) |
| 59 | Engine-child liveness registry + distributed child leases (`sessionLiveness`) | `app.Build`; shared by main/per-session engine `Deps.SessionLiveness`, direct `RunTeam` supervisors via `agent.WithMemberLiveness`, and `server.Service` | process registry plus child lifecycle (one reference-counted distributed hold per queued/running/between-round/synthesis Subagent, Parallel-branch, or Team-member session ID) | registration acquires the configured `SessionLease` before a child becomes runnable and fails the child start if acquisition fails. Each hold has a bounded renewer; definitive/expiry-near loss cancels every local registrant. Existing terminal and pre-start-abort chokepoints release idempotently; release first cancels and joins renewal, then uses a cancel-detached bounded context. Background Subagents hold through detached completion; direct supervisors hold every member across planning gaps and synthesis until `cleanupAll`; Team-tool members receive equivalent coverage through the parent child registry. Background Bash is excluded because it has no session. `Build.Close` closes this owner after `Service.Close` has cancelled/joined runs and before the lease/store backend closes | **reconstructible/reset-by-design**: local reference counts reset on restart; a crashed process loses child execution and its distributed hold lapses by TTL. A survivor may only clean or resume after that expiry. No child lifecycle state is newly persisted | `internal/app/session_liveness.go` (`sessionLiveness`, `renewLoop`, `Close`); `engine/agent/childregistry.go` (`registerProtected`, `markDoneResult`, `remove`); `engine/agent/teamsupervisor.go` (`WithMemberLiveness`, `Run`, `cleanupAll`); `internal/adapter/server/service.go` (`IsLive`) |

| 60 | Steer-while-running wire plumbing (issue #512, ADR 0232): the `Run`-scoped `steerInbox` with its bundled `message_id` watermark, per-stream acknowledgment lane and single-writer gate, and promote-path handoff mailbox | inbox and watermark on `agent.Run`; stream plumbing on one `Converse` RPC | run / RPC | the pending bundle is consumed on drain or dropped on retract; stream state dies with the RPC | **reset-by-design**: in-memory wire/run plumbing, never durable | `engine/agent/steer.go` (`steerInbox`, `steerContent`); `internal/adapter/server/grpc.go` (`steerHandoff`) |
| 61 | mecak8s server-certificate reload lifecycle and expiry observer (issue #789, ADR 0240) | `internal/adapter/tlsreload.Reloader` owns the atomic last-valid certificate, one `filewatch.Watcher`, and one expiry ticker/goroutine; `cmd/mecak8s` owns and closes the adapter | process (only when server TLS files are configured) | `serve` defers lifecycle close until gRPC and HTTP shutdown completes. Close idempotently cancels and joins the projected-file watcher, then stops and joins the fixed-interval expiry observer. Valid reloads atomically publish a fully parsed generation; invalid/mismatched/expired/reordered material retains the prior object. Expiry observation warns once per current generation and is diagnostic only | **reconstructible/reset-by-design**: restart synchronously validates the currently projected cert/key before listening, starts a fresh observer, and forgets prior warning state. Certificate state is transport configuration, not session/run state, so List 2 gains no row. Client CA remains restart-required | `internal/adapter/tlsreload/tlsreload.go` (`Reloader`, `New`, `GetCertificate`, `Close`); `internal/adapter/filewatch/filewatch.go` (`Watcher`, `New`, `Close`); `cmd/mecak8s/serve.go` (`buildTLSConfig`) |
| 62 | Redis client generations plus optional projected-file watcher and bounded reload worker (issue #789, ADR 0240, refined by ADR 0330) | `redisstore.Store` owns the generation manager; mecak8s enables its reload lifecycle when a Redis CA/username/password file is configured | process; one current generation plus only retired generations still leased or completing their claimed asynchronous close | every operation receives an acquired generation explicitly and holds its short manager lease without retaining it in context or holding the manager mutex during Redis I/O. A verified candidate swaps atomically; a rejected candidate closes immediately. Retirement claims close exactly once and runs asynchronously, so swaps never block on old-client close. `Store.Close` rejects acquisitions and swaps, stops the watcher and reload worker, then bounds generation retirement. Rows 72-73 inventory the separate durability/follow clients, follower admission, and the isolated follow-client force-close exception. A followed credential target must be regular; an uncancellable filesystem read can leave one inert goroutine after bounded shutdown, but its late candidate is rejected and closed. Retry uses capped jittered exponential delay, and a newer event restarts at attempt one. No configured files means no watcher or retry goroutine | **reconstructible**: restart reads one typed, stat-read-stat coherent credential snapshot and creates one fresh client pair. Generations and retry counters are transport state, so List 2 gains no row | `internal/adapter/redisstore/generation.go` (`clientGenerations`, `acquire`); `internal/adapter/redisstore/reload.go` (`startReloadLifecycle`, `runReload`, `reloadCandidate`); rows 72-73; ADR 0330 |
| 63 | Run-scoped durable event recorder/coalescer (issue #469, ADR 0243) | each gRPC, HTTP/SSE, schedule, or merged run relay owns one `server.RunEventRecorder` | run relay | every observation is client-streamed unchanged; message/reasoning accumulators are each capped at 1 MiB and flush UTF-8-safe chunks, while non-delta events and turn changes flush pending text and relay completion calls `Close`. Every chunk/boundary is attempted once and cleared regardless of error (the append may already be durable); failures warn once while later events continue | **reset-by-design with bounded loss**: normal turns append one record per present delta kind; oversized turns append the minimum bounded chunk count. A crash or failed append can lose a chunk; snapshots remain the turn-boundary authority and the client stream is unaffected | `internal/adapter/server/event_recorder.go` (`RunEventRecorder`, `Observe`, `Close`) |
| 64 | Remote connection registry, registry flock, and per-target transaction flocks (`clientauth.Registry`) | `cmd/mecatui` | process metadata handle plus durable per-user registry and stable lock sentinels | registry reads/writes take a process mutex plus the owner-only registry flock. Refresh, enrollment, and logout share one canonical-root-plus-target flock; refresh takes it before the source mutex, enrollment takes it after browser interaction, and logout releases it before provider cleanup. Enrollment reconciles ambiguous credential and registry commits under the lock, then compensates only its written credential CAS version when the registry did not commit. Lock handles/fds do not outlive an operation; sentinels persist | **persisted / reconstructed with accepted crash residual**: public target metadata survives restart under `$XDG_CONFIG_HOME/mecatl`; locks are ephemeral synchronization. There is deliberately no transaction journal, so a crash between credential and registry stores can leave partial local state; missing credentials require login and credential-only orphans cannot be enumerated | `internal/adapter/clientauth/store.go` (`Registry`, `OpenRegistry`, `lockTarget`, `replaceTarget`); `internal/adapter/clientauth/enroll.go` (`Enroll`, `compensateEnrollment`); `internal/adapter/clientauth/logout.go` (`Logout`); `internal/adapter/clientauth/oidc.go` (`RefreshSource`, `Token`) |
| 65 | Remote OIDC encrypted credential store, root-scoped OS-keyring encryption key, and root-key flock | `cmd/mecatui` / `clientauth` | process store/key-provider handles plus durable per-user credential records, keyring entry, and root lock sentinel | the stable owner-only root flock is acquired only while loading, migrating, or first-creating the wrapping key and released before return. The old unsuffixed keyring entry is copied to the canonical-root-scoped account under that lock only when an actual encrypted credential record exists; an empty opened namespace is not migration evidence. Credential operations open/close per-call files; `EncryptedFileStore.Close` clears its long-lived key copy | **persisted / explicitly reattached**: encrypted target-bound credentials and refresh rotation survive restart with the same OS keyring. Root identity is the physical canonical store path. A legacy credential identity containing a zero-padded target port needs one login because decimal-port canonicalization changes its record key. No session/run state is held | `internal/adapter/clientauth/store.go` (`KeyringProvider`, `NewKeyringProvider`, `withLock`, `legacyCredentialExists`, `OpenStore`, `OpenExistingStore`, `Credentials`); `internal/adapter/credentialstore/encrypted_file.go` (`EncryptedFileStore`, `Close`) |
| 66 | Remote OIDC refresh source, validator JWKS cache, and scoped HTTPS client | `cmd/mecatui` connection lifecycle | connected target / process | public and proactive token paths serialize through row 64's target flock and then the source mutex. Application `Token` demand, not RPC success or the poller itself, arms one proactive refresh. `run` cleanup closes `RefreshSource` first: `Close` cancels and joins the poller, then closes the validator; only afterward is the credential store closed. The scoped HTTP client has no separate refresh goroutine | **reconstructible**: a later connect reopens the store, rebuilds scoped private-HTTPS transport and validator, then validates or refreshes from the durable credential; cached keys and access tokens are derived state | `internal/adapter/clientauth/oidc.go` (`RefreshSource`, `Token`, `Close`); `cmd/mecatui/main.go` (`resolveTransport`); `authn/oidc/oidc.go` (`Validator`); `authn/oidc/scopedhttps/client.go` (`NewClient`) |
| 67 | Embedded mecatui perf admin listener (ADR 0254) | `cmd/mecatui/embed.Server` | process instance, only with `--perf` | ordinary perf binds owner-private `admin.sock` beside the private gRPC socket; explicit `--perf-addr` and addressless `--perf-mcp` bind loopback TCP (the latter ephemeral). `Server.Close` shuts down the HTTP server and removes the shared private runtime directory. Each instance has its own directory, so defaults do not collide | **reconstructible/reset-by-design**: a restart creates a fresh private directory/socket or ephemeral MCP URL; metrics/profile state is live process data, not session state. No List 2 row | `cmd/mecatui/embed/embed.go` (`setupPerf`, `listenPrivateUnix`, `Close`) |
| 68 | mecak8s plaintext drain-only listener | `cmd/mecak8s` (`serve`) | process | bound before serving; `boundedShutdown` shuts down its `http.Server`, and deferred listener close releases it on every later startup failure | **reconstructible/reset-by-design**: listener state is process-local and recreated from `--drain-addr` (default `0.0.0.0:8082`); no session state exists | `cmd/mecak8s/serve.go` (`drainHTTPMux`, `serve`, `boundedShutdown`); ADR 0290 |
| 69 | Worktree selector HMAC key (ADR 0291) | `app.Build` | process lifetime | no goroutine, registry, map, cache, or external handle; the random key dies with Build/process and needs no explicit cleanup | **reset-by-design**: the key and selectors are not persisted. Restart invalidates every selector, clients relist worktrees, and the server re-enumerates current choices for constant-time matching. There is no selector registry/map to reattach | `internal/app/build.go` (`Build`); `internal/adapter/server/worktree_selector.go` (`WorktreeSelectorIssuer`) |
| 70 | Managed temporary command/job reaper worker, root GC lock/completion record, and per-lease locks (ADR 0281) | `app.Build` / `managedtemp.Namespace` | process worker; durable private managed root | `Built.Close` cancels the worker and waits only `shutdown_reap_timeout`; each startup/periodic sweep is bounded by `reap_timeout`. The root lock is non-blocking, lease locks are acquired non-blockingly per candidate, and only validated unlocked leases past TTL are removed. System or unsupported modes construct no worker and make no mutation | **mixed**: the ticker/worker reset on restart; owner-only completion record and lease manifests persist. A crashed holder releases advisory locks, so a later interval-gated Build sweep can reclaim eligible residue; incomplete/cancelled scans publish no completion record. No List 2 row: this is adapter-owned cleanup coordination, not session/run state | `internal/app/managed_temp_worker.go` (`startManagedTempWorker`); `internal/adapter/managedtemp/reaper_unix.go` (`Sweep`) |

**Remote-login resource re-audit (List 1 / List 2 — ADR 0277).** Remote mecatui adds rows 64–66: the durable public registry plus per-registry/per-target flock domains, the encrypted credential source plus root-scoped keyring key/root flock, and the connected-target refresh/validator transport. These are adapter-owned credential or connection resources, not session/run state, so List 2 gains no row. All flocks are held per operation and process death releases them; their sentinel files persist. There is no cross-store journal, so the documented partial-state crash residual is not rehydratable session state. Registry and credential records are reattached from disk/keyring; validator and access-token state are derived again on the next connect. `mecatui` closes the refresh source, validator, and store before a target-switch restart completes.

| 70 | Durable learning-attempt repository (ADR 0259) | `app.Build` constructs one lazy `attemptstore.Store` beside the user-model store only when Review/Auto learning can apply at some admitted root, and shares it with main, per-session, explicit reflection admission, and recovery. Unwired default/off constructs none; an explicit `LearningStoreURL` instead composes the negotiated remote repository in every mode so explicit reflection, learned-skill inspection, and recovery remain available | process handle over caller-partitioned durable attempt metadata | no goroutine or retained file descriptor; the stable flock is acquired per operation and bounded JSON commits use temp-file fsync, rename, and directory fsync. Empty local startup creates no directory. Admission verifies the exact source session's persisted non-empty ADR-0249 RunID; explicit admission creates directly, while automatic admission first acquires the durable automatic-admission ledger reservation and then idempotently creates before returning `queued`. Claim callers supply only a bounded duration: the backend clock mints absolute acquisition/renewal expiry and decides claim validity, retention age, and discovery eligibility. Each opaque caller partition retains at most 256 records under that same lock: create evicts only its oldest terminal record or returns a content-free quota error when nonterminal work saturates it; no peer partition or claimed work is consulted or deleted | **persisted**: queued/running/terminal state, immutable source/prompt/digest provenance, opaque CAS versions, claims, checkpoints, and safe links reload across processes. The fixed per-partition bound is re-enforced after restart and through the remote driver; terminal cleanup remains partition-local. Bounded `DiscoverWork` returns queued and claim-expired work across opaque partitions through local and remote adapters; the durable learning-attempt repository is authoritative | `internal/adapter/attemptstore/store.go` (`Store`, `Create`, `Get`, `DiscoverWork`); `internal/app/reflection_observer.go` (`createDurableAttempt`, `submit`); `internal/app/build.go` |
| 71 | Learning-attempt discovery/recovery worker (ADR 0259) | `app.Build` starts one `attemptRecovery` whenever a durable repository plus evidence/proposal capabilities are composed, including an explicitly configured remote repository in Off mode and when no work exists yet. Unwired default/off allocates none | process / one bounded sequential worker over recurring authoritative discovery | the worker runs under its own child of the Build lifecycle context. It repeatedly discovers queued and expired-claim attempts, reconstructs exact source evidence through `learningEvidenceLoader`, renews the fenced claim during evidence/model/publication work, cancels that work on renewal loss, and advances claim-fenced proposal/skill checkpoints. Incomplete terminal evidence and transient setup keep the claim as a backend-timed persisted exponential-backoff marker; claim generation makes the third failed claim terminal as `retry_exhausted`, bounding recovery across restarts. `Built.Close` cancels and joins it before closing borrowed composition resources. Off still disables automatic observation and new automatic admission | **derived from persisted authority**: every process continuously re-reads the repository, so post-start admission, coordinator rejection, and expired claims are recoverable without a second process-local queue/cursor/receipt authority. Discovery iteration and polling position are disposable; CAS and claim generations resolve competing replicas | `internal/app/attempt_recovery.go` (`attemptRecovery`, `newAttemptRecoveryLoop`, `startAttemptRecovery`, `recoverAttempt`); `internal/app/attempt_worker.go` (`attemptWorker`, `startAttemptClaimRenewer`); `internal/app/source_evidence.go` (`learningEvidenceLoader`); `internal/app/build.go` |
| 72 | Automatic-reservation discovery/reconciliation worker (ADR 0259) | `app.Build` starts one `automaticReservationReconciliationLoop` whenever both the durable automatic ledger and attempt repository are selected, for local and remote-driver composition | process / one bounded sequential worker polling backend-authoritative expired held reservations | each pass atomically claims at most `MaxAutomaticReservationDiscoveryBatch` records under fresh backend-minted fences, reads the linked attempt partition, then retains an existing attempt's charge or reclaims an absent one. The loop owns a cancellation context and wait group; `Built.Close` cancels and joins it after attempt recovery and before repositories and shared driver connections close | **derived from persisted authority**: a replacement Build discards polling position and discovers the durable held record after its fence expires. The ledger fence/CAS elects one reconciler; deterministic attempt identity prevents duplicates. A crash after Reserve converges to reclaimed, while a crash after attempt Create converges to retained | `internal/app/automatic_reservation_reconciliation.go` (`automaticReservationReconciliationLoop`, `newAutomaticReservationReconciliationLoop`, `automaticReservationReconciler`); wired at `internal/app/build.go` |

**Durable-lineage foundation re-audit (List 1 / List 2).** The optional
`port.SessionLineageReader` adds no process-lifetime goroutine or handle. It extends the
existing store-owned durable indexes in List 1 rows 7–8 with content-free retained records
and deletion tombstones keyed by `(session ID, incarnation)` (session ID, trusted
kind/relationship, a non-reversible owner-scope token, an incarnation token, state, and
deletion time only; never Principal PII).
Redis owns the hash in its managed keyspace; jsonlstore owns one
atomically replaced owner-private index file and a per-operation flock; memstore deliberately
resets its map on restart. The gRPC driver protocol transports the same bounded projection
only when negotiated and creates no client-visible harness API. List 2 gains no independent
rehydration row: lineage is durable store metadata derived at atomic session mutations, not
live run state. Tombstones have no expiry API and are retained indefinitely across restart
and ID reuse; a new retained incarnation never replaces an old tombstone. Bounded reads
order the current retained root before historical root tombstones, then direct rows by ID,
state, and incarnation. Jsonlstore orders the tombstone before snapshot deletion and reconciles each
crash boundary by incarnation; Redis keeps its save/delete scripts atomic.

**Session-debug/admin-transport and network-evidence re-audit (List 1 / List 2 — ADRs 0254 and 0255).**
List 1 row 67 inventories embedded mecatui's opt-in admin HTTP server/listener. The
private UNIX socket shares the existing per-instance runtime directory and cleanup;
streaming-HTTP MCP or explicit addresses use loopback TCP. The debug-engine factory and
target-bound tool add no separate outlives-a-call resource: each debug engine occupies the
existing per-session engine registry (row 11), and its no-fs environment occupies row 12.
The run-local attempt observer owns no goroutine, buffer, or state beyond one provider call;
its sanitized `network.attempt` payloads use the existing loop → relay → EventLog lifecycle
(rows 2/18/39), so no new List 1 resource exists. List 2 row 40 records the durable debug
kind/target relationship and fail-closed factory rehydration; the target's network evidence
survives restart through the existing EventLog, with backend retention and read faults
reported honestly by `InspectSession`. No target run, lease, subscription, or live-follow
resource is created.


**mecak8s credential-reload re-audit (List 1 / List 2 — issue #789, ADR 0240).**
List 1 rows 61–62 inventory both optional projected-file watcher lifecycles, the
last-valid server certificate, its fixed expiry ticker/goroutine, the Redis reload retry
worker, and the replaceable Redis client generations. Serving joins the certificate watcher
and expiry observer after listener shutdown; restart reconstructs the published certificate
from the currently mounted files and resets the once-per-generation warning state. The Redis
Store rejects leases/swaps before closing its watcher and cancelling retry, then bounds both
the reload-worker join and the wait for generation leases/asynchronous client closes. A timed-out
durability client or other non-follow leased client is never force-closed. ADR 0330 permits the
Store to force-close an isolated follow client after the cooperative follower join bound; a claimed
close continues after return, and either timeout produces one count/reason-only warning. A
pathological uncancellable file read may leave one inert
worker after return, but the closed generation manager rejects and closes any late candidate.
Restart reconstructs both last-valid values from the currently mounted regular files. These are transport credentials, not session or run
state, so List 2 gains no row.
| 64 | Mecatui status-line source worker, optional interval ticker, debounce timer, and one command process tree | `cmd/mecatui/customization.statusLineSource`, constructed by the local mecatui client | client process | `StatusLineSource.Close` stops the ticker, cancels the active invocation/tree, joins its reader/worker with a one-second detached bound, then closes `Changed`; commands also have a one-second deadline and use a contained process group | **reset-by-design**: generated surfaces, last-good command surfaces, pending input, and timers are local presentation state. A restart reloads user-global settings and renders afresh; no session/run state is retained | `cmd/mecatui/customization/source.go` (`run`, `Close`); `cmd/mecatui/customization/command.go` (`NewCommandSource`, `runCommand`) |
| 65 | Spawned-daemon gRPC UNIX-socket listener + its owner-only socket file and (when mecated created it) socket directory | `serve` via `listenGRPC`/`listenUnixSocket` (`cmd/mecated/daemonhosting.go`), owned by the `*grpc.Server` once `boundListeners.serveGRPC` hands it over | process | `shutdown`'s `GracefulStop` closes the listener, and `net.UnixListener` unlinks the socket path on close; a listener bound but never served is released by `boundListeners.closeUnserved`, so a half-bound startup failure leaves no socket file or held port behind. A SIGKILL leaves the socket inode as a corpse — `reclaimStaleSocket` removes it on the next start, and REFUSES the start when a dial proves a live peer is still accepting | reconstructible (rebound from `--grpc-unix-socket` at next start, after the stale-vs-live reclaim above). Holds no session or run state, so List 2 gains no row | `cmd/mecated/daemonhosting.go` (`listenUnixSocket`, `reclaimStaleSocket`, `ensureSocketDir`); `cmd/mecated/socketumask_unix.go` (`listenUnixOwnerOnly`); `cmd/mecated/main.go` (`bindListeners`, `boundListeners`) |
| 66 | Lifetime-pipe watcher goroutine + the inherited read-end descriptor | `lifetimePipe`, adopted by `serve` from `--lifetime-pipe-fd` or piped `--lifetime-stdin` (`cmd/mecated/daemonhosting.go`) | process | `lifetimePipe.Close` closes the descriptor on serve's return. It does NOT join the watcher: the goroutine is parked in a blocking `Read` and the process is exiting immediately afterwards, so joining would buy nothing and could block shutdown on a descriptor the parent still holds. The goroutine ends on its own the moment the pipe reaches EOF — which is the ONLY case in which it matters, since that EOF is what triggers the shutdown | reconstructible only by the PARENT: the descriptor is inherited at exec, so a restarted daemon gets a new pipe from whoever spawned it, or none. A daemon restarted by hand simply has no parent to watch. Holds no session or run state, so List 2 gains no row | `cmd/mecated/daemonhosting.go` (`openConfiguredLifetimePipe`, `lifetimePipe`); `cmd/mecated/lifetimefd_unix.go` (`checkLifetimePipeFD`); consumed in `cmd/mecated/main.go` (`serve`) |

**Daemon-hosting re-audit (List 1 / List 2 — issue #821 Scenario 8).** Two new
outlives-a-call resources, rows 65 and 66, both `cmd/mecated`-owned and both
process-scoped. Neither carries session, run, or authority state: the socket is an
endpoint and the pipe is a liveness signal whose bytes are read and discarded, so
List 2 gains no row. The `--ready-file` artefact is deliberately NOT a row — it is
written once and never read or mutated by the daemon again, so nothing about it
outlives the write; a restart simply republishes it. The one genuinely
unreconstructible piece is the inherited descriptor in row 66, which belongs to the
spawning parent by construction.

| 65 | Per-session durable watch registry (`Service.watches`, ADR 0250) | `server.Service` | process (one bucket per session with an attached watcher; each bucket holds one small registration per watcher, and a bucket is deleted when it empties, so a long-lived server does not accumulate one per session ever watched) | each watch's own `defer` unregisters it; `Service.Close` calls `closeWatches`, which cancels every registration and permanently refuses later attachments, so no pump goroutine outlives the Service. A shutdown cancel is a CLEAN end, not a gap: no append failed, so claiming one would be a lie the client would act on. Guarded by a THIRD mutex (`watchMu`, beside `mu` and `subMu`) because `appendEvent` walks this map on the relay thread and must not contend with the run/registry hot path | **reset-by-design / re-attached BY THE CLIENT**: a watch is a live delivery attachment holding no session or run state — the position is the CLIENT's, carried in the cursor it received. A restart ends the stream; the client reconnects and hands its last cursor back, losing nothing. That is the point of a durable cursor, and it is why List 2 gains no row. **KNOWN GAP — no admission control:** there is no cap on attachments per caller, per session or globally, and no idle timeout. Each attachment costs a goroutine, a bounded envelope buffer, and on Redis a pooled connection; the 5-second delivery grace does NOT bound this, since it fires only when the buffer FILLS, which on an idle session never happens — so an idle watch is the cheapest to open and unbounded in lifetime. The isolated follow pool tracked for the appender-contention problem does not cover it: that limit is a socket count inside the storage adapter, which has no principal, and only this layer sees `session.PrincipalFromContext`. A per-principal cap belongs here | `internal/adapter/server/watch.go` (`registerWatch`, `unregisterWatch`, `faultWatchers`, `closeWatches`); `internal/adapter/server/service.go` (`watches`, `Close`) |
| 66 | Per-watcher follow goroutine and bounded delivery buffer (ADR 0250 decision 7) | each `Service.WatchSessionEvents` iterator owns one `pumpWatch` goroutine, one bounded envelope channel (512), and one single-slot terminal-error channel | one watch (a gRPC `WatchSessionEvents` stream or an SSE `GET /v1/sessions/{id}/watch` request) | the iterator's `defer` cancels the pump's context and unregisters the watch, so breaking out early — a client disconnect, a `Send` failure, an assertion in a test — releases the goroutine, the buffer, and the backend read. The pump exits on context cancellation from THREE sources: the caller's context, `faultWatchers`, and `closeWatches`. The buffer is deliberately finite: a watcher that cannot keep up within a 5-second grace is TERMINATED with a resumable `watch_lagging` rather than dropped (dropping is what `Service.Subscribe` does, and stopping it is the entire point of a durable cursor) — so a stalled client bounds its own memory instead of growing an unbounded queue. Decoupling the read from the wire write is also what keeps a slow client from holding a Redis pooled connection open across a blocking `XREAD` | **reset-by-design / re-attached BY THE CLIENT**: same as row 65 — no session or run state is held. The delivery buffer's undelivered tail is deliberately NOT recovered on termination, and the terminal errors carry NO server-side cursor for exactly that reason: the client's own last-received envelope is the only correct resume point, and a server-supplied one would skip the buffered envelopes the client never saw | `internal/adapter/server/watch.go` (`WatchSessionEvents`, `pumpWatch`, `watchDeliveryBuffer`, `watchDeliveryGrace`) |
| 67 | Clear-admission generation fence (`Service.runEntryGenerations`) | `server.Service` | process (one integer per session id that has crossed a Clear cancellation boundary) | no per-id deletion: queued pre-Clear requests may still hold the old value after source settlement, so deleting an entry would make generation zero reusable and defeat the fence. Repeated clears of one id update one entry; growth is bounded by distinct successfully admitted source ids cleared during the Service lifetime | **reset-by-design**: generations coordinate only requests and Clear operations in this process. A restart has no surviving queued request to fence, so the empty map is correct and List 2 gains no row | `internal/adapter/server/service.go` (`runEntryGenerations`, `captureRunEntryGeneration`, `validateRunEntryGeneration`); `internal/adapter/server/placement_successor.go` (`cancelAndAwaitClearSource`) |
| 68 | `@stacklok-oss/mecatl-sdk` client lifecycle registries, connection listeners, and heartbeat timer | one `ClientImpl` | client process | accepted runs and durable attachments unregister on terminal/close; `Client.close()` aborts and drains the remaining owned runs, closes attachments, clears status listeners, cancels the heartbeat, and then releases only client-owned transport/daemon resources | **reset-by-design**: this is live client bookkeeping. Session and event durability remain server-owned; a replacement client reconnects and reloads them explicitly | `sdk/typescript/src/client.ts` (`ClientImpl`) |
| 69 | TypeScript SDK durable-attachment connection, reconnect delay timer, status slot, and application cursor | one `Session.attach()` or `Session.activity()` result | one attachment | iterator return, `close()`, abort, terminal run result, or owning-client close aborts the current transport stream and reconnect delay and unregisters the status slot. Retry resumes from the last delivered cursor rather than a server-side queued position | **reattached by the application**: the opaque cursor is the application's checkpoint; the SDK persists nothing. A replacement process supplies that cursor to a new attachment, with at-least-once redelivery | `sdk/typescript/src/watch.ts` (`WatchConnection`, `AttachedRunImpl`) |
| 70 | SDK-owned local `mecated` child, ConnectRPC gRPC transport (Node UDS or Deno loopback TCP), lifetime endpoint or Deno stdin writer, bounded stderr capture, ready file, and private runtime directory | one `spawn()`-returned client | client process | startup failure and `Client.close()` close the lifetime channel, stop the captured child with bounded TERM/KILL fallback, dispose the transport, and remove the private directory. Parent death closes the lifetime endpoint so the daemon follows its ordinary shutdown path | **reset-by-design**: the default spawned daemon uses in-memory storage. Durable state exists only when the caller explicitly configures a durable backend, whose server-side inventories already own it | `sdk/typescript/src/spawn.ts` (`spawnAttempt`, `stopProcess`); `sdk/typescript/src/deno-spawn.ts` (`spawn`, `stopProcess`); `sdk/typescript/src/client.ts` (`ClientImpl`) |
| 71 | SDK callback-tool loopback listener, random bearer, bounded invocation queue, concurrency counters, and per-call timers | `LoopbackToolHost` owned by one spawned client | client process | session-create failure leaves registration reusable; client close first aborts queued/running handlers, then closes connections and joins the HTTP server before transport and daemon teardown | **reset-by-design**: registrations, bearer, queued calls, and handler state are client-local and intentionally not reconstructible. A restarted application registers its tools again before creating a session | `sdk/typescript/src/tool-host.ts` (`LoopbackToolHost`); `sdk/typescript/src/tool.ts` (`ToolRegistry`) |

**TypeScript SDK client-lifecycle re-audit (List 1 / List 2 — issue #821 Scenario 10).**
List 1 rows 68–71 inventory the complete client-side resource families exposed by
`@stacklok-oss/mecatl-sdk`: shared client bookkeeping, durable watches, local daemon ownership, and
callback-tool hosting. `Query`, `TeamRun`, and the two-run `PlanResolution` add no independent
process, listener, goroutine, or durable store: they own bounded async iterators registered with the
row-68 client and release on terminal, iterator return, abort, or close. **List 2 gains no row.** The
only cross-process continuation data is already server-durable session/event state plus the
application-owned row-69 cursor; the SDK never fabricates a client-side durability layer.

**Durable watch re-audit (List 1 / List 2 — issue #821, ADR 0250).** List 1 rows 65–66
inventory the whole family the watch transport adds: the Service-owned per-session
registry, and per watcher one follow goroutine plus one bounded delivery buffer. Every one
is bounded by an explicit owner — a watch's own iterator releases its goroutine and buffer
on any exit path including an early break, and `Service.Close` cancels the registry so
none outlives the Service. **List 2 gains no row, deliberately and not by omission.** A
watcher holds no session or run state: its position is the client's, carried in the opaque
cursor each envelope delivers, so a restart costs a reconnect and nothing else. The two
terminal errors (`watch_lagging`, `activity_gap`) carry no server-side cursor on purpose —
resuming from the server's furthest-queued position would skip the envelopes still in the
buffer when the watch was terminated, which is precisely the loss the termination existed
to prevent. The honest residual is ADR 0250 decision 6's own: a total backend outage plus
loss of the process holding the watchers leaves an undetectable gap, and that is stated
rather than papered over.

| # | Resource | Owner | Scope | Cleanup today | Re-attach | Evidence |
|---|---|---|---|---|---|---|
| 68 | Session-scoped MCP broker logical sessions, authorization transactions, grants/tokens, callback states, replay ledgers, and hardened OAuth HTTP client | `app.Built` owns one process-wide `internal/adapter/mcpbroker.Runtime`; `mecated` and `mecak8s` mount its fixed callback bundle on their existing primary HTTP mux | process; logical state is partitioned by canonical session ID and private incarnation | `Built.Close` first bounds local attachment closure, then `Runtime.Close` fences the runtime, signals cancellation to every logical session, synchronously clears idle sessions, and returns without waiting on a stuck operation. No cleanup goroutine is launched: the registered operation's release path performs the one final secret/callback cleanup when the deleted/closed session reaches zero active operations. Permanent owner deletion/retention calls idempotent `DeleteSession`; ordinary `CloseSession` does not | **in-memory only / reset-by-design in P10**: restart creates a fresh runtime and loses grants, transactions, callback/replay state, and logical incarnations. The persisted opaque binding then mismatches and reload fails closed; durable or remote broker state is deferred. See List 2 row 45 | `internal/adapter/mcpbroker/runtime.go` (`Runtime`, `DeleteSession`); `internal/adapter/mcpbroker/auth.go` (`Close`); `internal/app/build.go` (`Built`, `MountMCPBrokerHandlers`) |
| 69 | Service-local MCP broker attachment registry and per-session lifecycle serialization | `internal/adapter/server.Service` (`brokerAttachments`, `brokerMu`) | process / canonical session ID; wrappers are local handles, not logical broker state | attach/build/commit/abort, detach, and permanent deletion serialize per session without a process-wide bottleneck. A created attachment is provisional: host persistence calls attachment-scoped `Commit`; rollback calls `Abort`, which removes only a still-private creation and preserves a logical session once a peer has reattached. `CloseSession` removes the engine and attachment before context-bounded close; `Service.Close` closes all attachments concurrently under the same bounded shutdown policy. Attachment close rejects new operations and waits context-sensitively for registered calls; it never deletes logical state | **derived while the process-owned broker incarnation exists; otherwise fail closed**: a local close/reload reattaches by exact persisted binding. Process restart drops the registry and the in-process logical state described by row 68, so reattachment cannot reconstruct authority. See List 2 row 45 | `internal/adapter/server/service.go` (`Service`, `CloseSession`, `Close`, `keyedMutex`); `internal/adapter/server/mcp_broker.go`; `internal/mcpbroker/broker.go` (`Attachment`) |
| 70 | Pending external-authorization expiry observations and continuation registration state | `engine/session.Session` owns the durable pending value; `internal/adapter/server.Service` owns `authorizationExpiry` timers and the registered inert `agent.PreparedRun` | session / process | recheck, cancellation, expiry, `CloseSession`, lease loss, and `Service.Close` stop and remove the exact timer; only a successfully registered prepared run is started, exactly once. Session/service close pair parked calls before releasing the attachment and lease. When no continuation engine can be reconstructed, the Service appends the exact ordered tool results followed by the terminal authorization event | **durable pending / derived timer**: restart reconstructs no timer or continuation. A lease-gated status query either reattaches to authoritative pending/granted/terminal broker state or resolves unavailable in-process state as interrupted; it never recreates or replays a protected mutation ambiguously. Snapshot settlement and fallback EventLog appends are not transactional: as with ordinary relay persistence, each append is attempted once on a cancellation-detached context because retrying an ambiguous post-write error could duplicate an event; failure is warned and returned rather than reported as settled success. See List 2 row 45 | `internal/adapter/server/mcp_authorization.go` (`scheduleAuthorizationExpiry`, `registerPrepared`, `prepareAuthorizationClose`, `appendAuthorizationResolution`); `engine/agent/loop.go` (`PreparedRun`) |
| 71 | ToolHive embedded authorization-server storage | `internal/adapter/mcpbroker.Process`; composition supplies the managed Redis client and ToolHive owns its namespaced records under `mecatl:authserver:` (tests and unconfigured callers may use ToolHive memory storage) | process-owned handle over deployment-wide inner authorization/token records; distinct from mecatl's outer session/enrollment correlation in rows 68–69 | `Process.Close` closes the embedded authorization server/storage before composition closes the shared Redis client. Memory storage dies with the process; Redis records follow ToolHive's own lifecycle and retention | **inner storage may be durable, outer correlation is not**: Redis can preserve ToolHive upstream authorization/token records across process death, but mecatl's callback state, broker incarnation, and mapping from `PendingWorkspaceEnrollment` to that inner operation remain process-local. Restart therefore discards the old exact pending outer correlation and starts a fresh enrollment; it does not discover or resume the inner operation. Replicas likewise cannot route the outer callback without affinity or a future durable-broker design. See List 2 row 46 | `internal/adapter/mcpbroker/toolhive_construction.go` (`ToolHiveConfig`); `internal/adapter/mcpbroker/toolhive_process.go` (`NewToolHiveProcess`); `internal/app/mcp_broker_toolhive.go` (`buildToolHiveAuthRedisClient`) |
| 72 | Per-generation isolated Redis follow client and bounded connection pool | each redisstore `clientGeneration`, owned by `clientGenerations` | one follow client per live or retiring credential generation; `PoolSize` and `MaxActiveConns` use the configured process-local follow-pool size | a rejected candidate closes both clients; generation retirement closes the pair after its leases release. `Store.Close` may asynchronously force-close only the follow client when an admitted read misses the cooperative join bound, while durability clients retain their lease-safe close rule | reconstructible from Redis connection settings and mounted credential files. A restart builds and probes a new pair; it retains no watch position or session authority | `internal/adapter/redisstore/generation.go` (`clientPair`, `buildClientPair`, `forceCloseFollow`); `internal/adapter/redisstore/reload.go` (`reloadCandidate`); ADR 0330 |
| 73 | Redis follower admission, cancellation, and join registry | redisstore `Store` owns one `followerRegistry` | process; one entry per admitted `ReadAfter` iterator with `Follow:true`, capped by `MaxFollowers` | iterator release removes its entry on every exit. `Store.Close` linearizes against admission, rejects new followers, cancels the active snapshot, waits cooperatively, and retains bounded late-cleanup ownership after a follow-client force-close | reset-by-design and reattached by the client. The registry contains no cursor or durable session state; after restart the client resumes with its last processed cursor and unchanged filter | `internal/adapter/redisstore/followers.go` (`followerRegistry`); `internal/adapter/redisstore/cursoreventlog.go` (`ReadAfter`); `internal/adapter/redisstore/redisstore.go` (`Close`); ADR 0330 |
| 74 | Prompt-free persisted-ask continuation contexts and detached relay join registry | `internal/adapter/server.Service` owns the post-acceptance context, exact resumed `agent.Run`, recorder/relay goroutine, and `detachedControlWG`; the unary caller owns only work before atomic acceptance | one process-local entry per accepted persisted ordinary-ask resolution | relay admission adds to the Service wait group before the run is published and is fenced before shutdown wait. `Service.Close` first closes admission, marks every already-durable awaiting handoff `preserveDurable` and every cancellation `cancelSignaled` under the persistence barrier, then cancels contexts/runs and waits with the engine-close bound. Graceful drain and lease loss use the same mark-before-signal order. A preserved terminal relay cannot overwrite the awaiting snapshot | **derived from durable state / reset local ownership**: the context, run, recorder, marker bits, and join count disappear on process loss. A replacement acquires the distributed session lease, reloads the authoritative snapshot after acquisition, and repeats exact run/state/ask/origin/placement validation before rehydrating. See List 2 row 47 | `internal/adapter/server/service.go` (`ResolveRunAsk`, `validatePersistedRunAsk`, `reserveDetachedControlRelay`, `relayDetachedControlRun`, `Close`, `GracefulDrain`, `cancelRegisteredRunState`); ADR 0347 |
| 75 | Mecatui agent-lifecycle hook delivery worker and bounded queue (host editor notifications) | `cmd/mecatui/main.go` owns the `*agenthook.Notifier` for one run generation; the notifier owns its one lazily-started worker goroutine and 64-slot dispatch queue | process, one per mecatui run generation (a `/connect` restart re-enters `runWithOptions` and builds a successor) | `closeAgentLifecycleHook` runs inside `runCleanup` BEFORE any restart: it closes the queue and joins the worker within a 3s bound. Closing the queue alone only stops new enqueues — the worker's `range` would still deliver the whole buffered backlog, each event with a fresh full timeout — so when that bound expires `Close` REVOKES the generation: the delivery in flight is cancelled and every queued event is discarded, then it joins within a short further grace. Exact guarantee: once `Close` returns, no delivery from that generation can START, so a retired generation can never deliver into its successor's run; on the revoked path the only residual is the single already-cancelled invocation. Producers never block: a full queue drops the oldest pending event, and each invocation is separately timeout-bounded with its process group killed | reset-by-design; the queue holds display-only notification signals with no session, run, or transcript authority, and the successor re-detects the host and starts fresh | `cmd/mecatui/agenthook/hook.go` (`Notifier.Close`, `worker`, `enqueue`); `cmd/mecatui/main.go` (`closeAgentLifecycleHook`) |

**Mecatui agent-lifecycle-hook re-audit (List 1 / List 2).** The host-editor
lifecycle emitter adds ONE outlives-a-call resource, List 1 row 75: the ordered
delivery worker and its bounded queue. It gains NO List 2 row, and that is a
decision rather than an omission: the queue carries only display-bound
notification signals (an event name, the session id, and a clamped preview), so a
restart loses nothing a client could not re-derive, and the durable session state
it mirrors is already inventoried elsewhere. What the row DOES record is the
generation boundary, because ordering across it is a correctness property rather
than a best-effort one: in-process restart means two notifier generations can
briefly coexist, and a retired worker delivering a queued terminal after its
successor's busy signal would mark the host idle during a live run.

**Mecatui status-line re-audit (ADR 0247).** List 1 row 64 owns the local source lifecycle. It contains only display-safe input and generated presentation state, never session authority or transcript data, so List 2 gains no row. Shutdown is bounded rather than silently abandoning a live command tree; a process restart deliberately starts from fresh local status state.

**Redis follow-capacity re-audit (ADR 0330).** List 1 rows 72–73 inventory the
per-generation follow clients separately from the Store-owned follower registry.
List 2 gains no row. Neither resource contains durable session or run state, and
clients retain the durable cursor needed to resume after process replacement.

**Steer-while-running re-audit (List 1 / List 2 — issue #512, ADR 0232).** List 1 row 60 inventories the in-memory inbox, stream handoff, and watermark correlation. List 2 row 37 records the pending bundle's deliberate restart-loss; only a drained steer becomes durable ordinary user history.

**Session-storage temp-locking re-audit (List 1 / List 2 — issue #586, ADR 0226).**
The jsonlstore resource in List 1 row 8 now includes one stable per-family flock sentinel
and generation/owner-tagged replacement temps. The flock is acquired per operation and
released on return; process death releases it in the kernel. A later startup or Save
reconstructs the same physical mutation identity and reaps only protocol-valid inactive
temps while holding it, so repeated crashes converge without deleting a live process's
temp or the committed snapshot. This is adapter-owned persisted storage and reconstructible
synchronization, not new session/run state; List 2 gains no row.

**Session-storage indexed-inventory re-audit (List 1 / List 2 — issue #587, ADR 0226).**
List 1 row 8 now includes jsonlstore's durable, adapter-private metadata catalog. It
outlives calls but holds only a rebuildable `SessionDiscoveryMeta` projection and a
snapshot-directory fingerprint: no conversation, tool argument, event body, or authority
moves into it. A restarted process reads or reconstructs it from v2 top-level metadata
headers and bounded v1 tails; missing/corrupt/stale files and ordinary shared-directory
changes converge on the snapshots. No process-local cache or goroutine was added. The
catalog contains no session/run state that is not already authoritative in snapshots, so
List 2 gains no row.

**Session-storage health re-audit (List 1 / List 2 — issue #592, ADR 0226).**
The bounded health provider reuses row 8's derivative catalog and cheap file metadata;
it adds no transcript scan, cache, or goroutine. Row 16 now includes the Build-owned,
mutex-protected last/next-sweep and active-job status attached to the existing retention
sweeper. That status is observability only and resets honestly on restart until the startup
sweep records a fresh value, so List 2 gains no row.

**Session-storage cleanup re-audit (List 1 / List 2 — issue #590, ADR 0226).**
The pure planner adds no resource. Manual confirmation adds List 1 row 57: a process-random
signing key and bounded sanitized job registry owned by `Service`. Both reset by design on
restart; outstanding tokens become invalid and job inspection disappears, while committed
sidecar-first/snapshot-last deletions remain durable. No cleanup resumes implicitly, so List 2
needs no restart-fidelity row: retry begins with a fresh generation-bound dry-run.

**Manual-dream re-audit (List 1 / List 2 — ADR 0228).** The bounded Build-owned
registry/coordinator is List 1 row 58. It starts no goroutine and needs no explicit close; lazy TTL
cleanup bounds stale records, while terminal content disposal limits retention. Pending authoritative
plans and idempotent terminal receipts are restart-losable coordination state recorded in List 2 row
36. Loss is explicit: regenerate and spend again; no snapshot/event-log reconstruction or
cross-replica handoff is claimed.

**Cloud-native learning re-audit (List 1 / List 2 — ADR 0259).** The **durable automatic-admission ledger** (List 1 row 55; List 2 row 32) preserves reservation state across restart and replica replacement; its backend clock alone decides expiry and bounded discovery atomically re-fences orphaned held reservations before reconciliation retains or reclaims their charge. The **durable learning-attempt repository** (List 1 row 70; List 2 row 42) holds the queued/running/terminal lifecycle, immutable content-free source binding, claims, checkpoints, and safe downstream links; it accepts claim durations rather than caller time and uses backend-authoritative time for claim transitions, retention, and work discovery. The **learning-attempt discovery/recovery worker** (List 1 row 71) continuously enumerates that authority, renews claims through exact evidence/model/publication work, cancels on claim loss, and resumes legal boundaries after replacement. Setup or not-yet-delivered terminal evidence uses claim-generation-bounded persisted backoff and reaches `retry_exhausted` after three failed claims rather than hot-looping. The existing coordinator remains disposable scheduling state (List 1 row 47; List 2 row 30), not a second workflow authority. Proposal/skill durability and derived generation publication remain in List 1 rows 46 and 48–49 and List 2 rows 29/31. Remote clients borrow the already-inventoried **driver connection cache** in row 19. Exact evidence loading is bounded per attempt and retains no new outlives-call resource. Attempt watch is absent, so there is no feed, cursor, envelope, cache, or rehydration entry. Unwired default/off constructs no **durable learning-attempt repository**, **durable automatic-admission ledger**, coordinator worker, or **learning-attempt discovery/recovery worker**; authenticated explicit reflection keeps the synchronous lazy local proposal path. An explicitly configured remote store is a repository connection/inspection/recovery opt-in even in Off mode; it does not enable automatic observation or make ordinary completions create attempts. Raw remote repository RPCs remain trusted-single-tenant-only and fail closed when application ownership enforcement is enabled; no multi-tenant or attempt-watch resource is implied.

**Evidence-reflection re-audit (List 1 / List 2 — issue #509, ADR 0109).** Standard
wires the durable proposal artifact and its per-operation flock (List 1 row 46), plus the bounded Build-owned coordinator (row 47). Proposal lifecycle state survives a crash (List 2 row 29); transient queue, singleflight, and receipt state deliberately resets without a retrospective session sweep (List 2 row 30).

**Learned-skill re-audit (List 1 / List 2 — issue #510, ADR 0111).** The durable
manifest/flock is row 48 and the no-goroutine live generation pointer is row 49. Durable lifecycle,
proposal linkage, and crash-window recovery are List 2 row 31; the pointer is reconstructed from
that state and the immutable external snapshot. The skill pipeline is synchronous inside row 47's
existing job and therefore adds no queue, worker, breaker, or receipt cache.

**Validated learned-skill activation re-audit (List 1 / List 2 — ADR 0224).** The policy is a
closed value threaded through the existing synchronous pipeline and repository transaction. It adds
no goroutine, cache, handle, queue, or restart-losable state; `activate_validated` is durable in the
existing row-48 manifest/receipt history and reconstructs through the existing row-31 lifecycle path.
No inventory row changes.

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

**Read-only credential-source re-audit (List 1 / List 2 — issue #542, ADR 0221).**
The explicit environment Reader adds ONE optional outlives-a-call handle (List 1 row 53),
owned by the canonical profile loader and closed by `MCPProfiles.Close`. It allocates no
goroutine, file descriptor, cache, mutable record, or default environment lookup. Reader-only
OAuth updates row 51's restart decision: an opt-in memory-only refresh resets and the unchanged
source record is re-read. List 2 gains NO row because credentials remain adapter-owned rather
than session/run state.

**MCP OAuth controller re-audit (List 1 / List 2 — issue #521/#542, ADR 0220/0221).** The
optional controller adds ONE outlives-a-call resource (List 1 row 51), owned by one MCP
`Server` and absent unless an embedding supplies `ServerConfig.OAuth`. Its credential record
is adapter credential state and therefore adds NO List 2 session/run row. Reattachment
requires the same explicit subject, resource, issuer, client registration, Reader, and
external source configuration; mutable durability additionally requires the same writer
and key. No composition or implicit key acquisition is added.

**Credential-store re-audit (List 1 / List 2 — issue #519, ADR 0218).** The
substrate adds ONE optional outlives-a-call resource (List 1 row 50): durable encrypted
records and stable lock sentinels plus the handle-owned key. Nothing constructs it by
default; ownership begins only when an explicit consumer (such as ADR 0220's optional MCP
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
exit (a shutdown-only posture, documented in [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
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
`forceExit`, and never outlives the process; decision = derive. Mecak8s's separate
shutdown bounds (`--drain-timeout`, `--grpc-stop-timeout`, `--http-shutdown-timeout`,
`--close-timeout`, plus the existing telemetry bound) likewise configure cleanup of
already-inventoried resources and add no resource or rehydration state; their default
sequential sum including the 3s preStop delay is 43s, below the chart's configurable 60s
termination grace.

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
| discovery | Provider observations and their timestamps, latest outcome, attempt identity/deadline, waiter channels, occupied fetch slots, provider-local cooldown, and combined public projection | Build-local `providerDiscovery` (List 1 row 18) | lost; all providers begin unattempted, with no retained observations or cooldown | **reset-by-design**: metadata is rediscovered on eligible startup/demand; positive catalog/config floors remain derivable. No discovery state, effective model identity, or credential is added to session persistence | ADR 0353 |
| 36 | Title coordinator queue/dedupe and in-flight title call | `server.titleCoordinator` | process | `Service.Close` cancels and joins; pending queued work is discarded | reset-by-design: durable incomplete attempts are reloaded as `interrupted` and exhausted, so no uncertain provider call is retried | title coordinator (SHIPPED) |
| 37 | Native endpoint access and refresh tokens | protected `mecatl/provider-oidc/v1` record; transient access material in `nativeBearerSource` | access tokens reset; the encrypted exact-identity record reloads refresh state only | persist-in-record / derive: no OAuth material enters a session snapshot, event, diagnostic, or RPC; an unavailable, corrupt, or identity-drifted record is not-enrolled | ADR 0329 |
| 38 | Native endpoint issuer/gateway clients, keyring/store handles, and transaction locks | `NativeEndpointRuntime` and `TransactionLocker` | handles and locks are closed/released; a restart has no live runtime state | explicitly reattach/reset: Build or local lifecycle reopens them only from explicit configuration; issuer/gateway trust stays separate and the next lifecycle operation reacquires its lock | ADR 0329 |
| 1 | Session profile (no-fs vs default) | **SHIPPED (Phase 1)**: persisted as an additive opaque `Profile` label on the aggregate (`session.Session`) and the snapshot (`sessnap.Snapshot`); the empty-workspace inference stays the second defense (`needsRehydration`, `Service.rehydrateSession`) | correctly rehydrated from the persisted label, with the inference still covering a pre-label snapshot | persist-in-snapshot (inference stays as second defense) | 1 (SHIPPED) |
| 2 | Provider/model selector | **SHIPPED (Phase 1)**: persisted as the opaque `ProviderID`/`ModelID` label pair on the aggregate (`session.Session`) + snapshot (`sessnap.Snapshot`); `Service.rehydrateSession` re-derives the SAME engine via the factory from the persisted pair (no longer the default-provider floor) | rebuilt on the SAME provider+model via the factory; a default session (empty pair) rides the shared engine. The compaction context-window is NO LONGER a rehydration trigger (issue #66, superseded by the resolve-at-use unification): every engine carries a live-first `Deps.ContextWindow` closure (`reg.windowResolver`) read at use, so a live-only default model self-corrects to its live window on the next turn with no rebuild | persist-in-snapshot (selector); rehydration re-derives the engine via the factory; the engine COMPACTION window is derived at USE via the resolve-at-use closure (decision = derive) | 1 (SHIPPED); window unified to resolve-at-use #66 |
| 3 | `session.Usage` (cumulative run tokens) | **SHIPPED (Phase 1)**: a `Usage` field on the aggregate (`session.Session`) accumulated by `RecordUsage`, persisted as the additive `usage` snapshot field (`sessnap.Snapshot`); the budget brake (`budgetExhausted`) is evaluated against the cumulative `sess.Usage`, and `resetToIdle` DELIBERATELY preserves it. The additive `ReasoningTokens` field (issue #213) rides this SAME snapshot path — it is a subset of `OutputTokens` (not added to `TotalTokens()`), so no new row is strictly required; it is noted here per the inventory discipline | the `MaxRunTokens` brake continues across restart instead of re-granting a fresh budget; reasoning spend is preserved as observability, not budget | persist-in-snapshot (additive `usage` field; the budget reads the cumulative aggregate; `ReasoningTokens` rides the same additive field) | 1 (SHIPPED) |
| 4 | permstore allow-always rules | `permstore.Memory.bySession` (`engine/adapter/permstore/permstore.go:48`) | **SHIPPED (Phase 3b)**: on a post-restart load, `internal/app/approvalreplay.go` (`replayApprovals`) (invoked once per id by `internal/adapter/server/service.go` (`maybeReplayApprovals`)) reads the logged allow-always `EvApproval`s, correlates each metadata-only askID back to its ToolCall in the loaded conversation (the askID encodes the call id, `engine/agent/dispatch.go` (`newAskID`)), and re-drives the existing `Policy.Learn` to re-derive the real rule from history; the previously-allow-always'd tool is NOT re-asked | fix-via-event-log (verdict events replayed into permstore; metadata-only event + real rule from history = no leak, no port widened) | 3b (SHIPPED) |
| 5 | The pending PARENT ask | **SHIPPED (Phase 2)**: the data IS in the snapshot (`Pending`, `engine/adapter/sessnap/sessnap.go` (`Snapshot`), restored via `PauseForApproval`); the LIVENESS is now recovered too: `Approve`/`Deny` on a runless awaiting session loads the snapshot, re-enters the loop AT the ask via `engine/agent/loop.go` (`ResumeApproval`), and drives to completion | resolved: `internal/adapter/server/service.go` (`resumeFromAwaiting`) rebuilds the engine and re-enters; `ErrNoActiveRun` is no longer terminal for awaiting (still terminal for idle/completed/cancelled/failed) | the resume-from-awaiting loop entry (durability was already correct; Phase 2 adds the liveness) | 2 (SHIPPED) |
| 6 | Pending CHILD asks (childAskRouter) | run-scoped in-memory routing of child-namespaced askIDs | lost with the run | reset-by-design AND verified structurally unreachable through the resume seam (`engine/agent/dispatch.go` (`driveFromAwaiting`), the Q4 note): a surfaced child ask sets the CHILD session's `pending` (its own loop calls `PauseForApproval`), never the PARENT's (the parent stays `StateRunning` inside the delegation tool call), and the server persists/resumes only top-level runs, so a restored `StateAwaiting` session ALWAYS holds a parent-OWN ask. No `PendingAsk` marker field was needed; were a surfaced-child ask ever persisted onto a parent, it would close out as an ordinary unanswered sibling (the honest aborted-result wording), not a silent stall | reset-by-design; Phase 2 re-enters at the PARENT ask only, child asks non-rehydratable (verified, honest, not silent) | 2 (verified) |
| 7 | Background children | `childRunRegistry` (`engine/agent/childregistry.go:131`), run-scoped; child sessions persist via `WithSubagentStore` (`subagent.go:599`), and parallel branches likewise via `WithParallelStore` (`parallel-<callID>-<i>`, commit `fe9ffe5`, forensically loadable through the `{subagent-, parallel-}` prefix gate) | the running children die un-drained; their persisted sessions remain individually loadable/resumable (`resume:` / `InspectSubagent`), but nothing reconnects them to the parent | reset-by-design for v1; session-scoped detach is issue #28, gated on the event log | 3c → #28 |
| 8 | Workspace read-version ledger (ADR 0208) | in-memory map on each live Workspace (`osfs`, `memfs`, ACP), keyed with I/O-free lexical Clean/Rel normalization and storing opaque authoritative `FileVersion` tokens (physical symlink aliases may conservatively miss) | the Workspace/ledger is lost. The first Edit or existing-file Write on a rebuilt Workspace is REFUSED ("not read this session") until Read records a fresh version. Fail-safe, never silently wrong; no-fs has an inert ledger | reset-by-design. The ledger is scoped to the live Workspace/environment instance; the default Service factory creates a fresh Workspace per run, while explicit no-fs/ACP overrides retain their existing owner-defined lifetime. Persisting versions is deferred until a remote environment/backend can define durable identity and true CAS (ADR 0208) | deferred |
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
| 24 | Typed provider-failure metadata (issue #409 / ADR 0239, superseding ADR 0203's retry decision) | `engine/session/session.go` (`RecordFailureMetadata` / `FailureMetadata`) stores independent `RetryDisposition` and `StreamProgress` on `StateFailed`. `engine/adapter/sessnap/sessnap.go` (`Snapshot`) persists those typed fields; missing typed fields decode to `Unknown`, and unrelated JSON fields are ignored | restart preserves the cause classification and semantic commit boundary needed for an informed recovery or failed-step retry. Snapshots remain conservative when typed fields are absent | **persist-in-snapshot**: typed disposition and progress fields on the current snapshot | 0239 (issue #409), supersedes 0203's decision |
| 25 | Pre-retry advisory notice store (`Service.recoverNotices`, issue #346 / ADR 0203 compatibility) | `internal/adapter/server/service.go` (`recoverNotices` sync.Map): keyed by session id, holding the advisory text for a session that just recovered from a failure classified by typed disposition as `Permanent`. `loadAndReopen` stores the notice BEFORE `Recover()` clears the failure metadata; `RecoverNotice(id)` returns+deletes it ONCE. The relay adapters emit an `EvRecoverNotice` synthetic event before the main event loop | a restart drops the in-memory map. Typed failure metadata remains durable in row 24, but this one-time compatibility advisory is gone | **reset-by-design**: a one-time advisory only. Exact retry does not rely on it; durable retry intent is row 38 | 0203 compatibility; narrowed by 0239 |
| 27 | Operator-profile last-good snapshot (#508 slice; List 1 row 45) | run-local `agent.Run.operatorProfile` | restart loses it; the next run reads the durable source and omits on first-read failure | **reset-by-design**: never persisted into conversation or snapshot state | #508 slice |
| 28 | Memory lifecycle history and tombstones (ADR 0107; List 1 rows 4–5) | the same per-scope `memory.json` document as current entries | restart reloads current state, opaque versions, attribution, history, and tombstones together; incompatible current content fails validation without mutation | **persist-in-store**: one flocked load→mutate→atomic-rename transaction; deliberately no history sidecar that could drift from current state | 0107 |
| 29 | Staged learning proposal lifecycle, immutable aggregate materialization manifest, and promotion receipt (issue #509; List 1 row 46; ADR 0300) | the standard `reflectionstore` proposal document, partitioned by hashed principal/project identity, persists protocol, exact source identity boundary, the complete source-ordered original-coordinate/event-sequence manifest with entry digests/component bindings, aggregate selected-evidence digest, candidate citations, and lifecycle; resulting memory revision provenance is in `MemorySource.ProposalID` | restart reloads the exact manifest and proposal status/version/decision history. Detail/approval re-read and verify the manifest once without re-ranking; unavailable or mismatched source fails precondition. Pre-version ADR-0109 records decode as
`reflection-evidence/legacy-v0`, retaining input-local `EvidenceRef.Ordinal`; new records write v1. A crash after memory CAS but before proposal finalization leaves `promoting`; reconciliation compares the current memory revision proposal id and finalizes without writing again | **persist-in-store**: aggregate provenance is state-of-record rather than reconstructed from candidate citations; proposal lifecycle and memory revision remain independently durable, and reconciliation closes the intentional cross-store crash window without claiming a distributed transaction | 0109, 0300 |
| 30 | Reflection materialization gate/active scans, queue, running jobs, singleflight keys, and bounded completion receipts (issue #509; List 1 row 47; ADR 0300) | process-local Build lifecycle gate/context and active-operation accounting plus `reflectionCoordinator` maps/queues/workers | caller cancellation or `Built.Close` cancels and joins pre-admission scans; no per-job scan goroutine or pre-admission queue/singleflight/receipt/provider/proposal state survives. Restart loses transient accounting and uncommitted queued/running extraction/receipts; already staged proposals retain the complete manifest and memory revisions remain durable | **reset-by-design**: there is no retrospective sweep. Explicit retry re-materializes from durable source; immutable manifest verification, deterministic proposal IDs, and CAS converge an already staged proposal | 0109, 0300 |
| 31 | Agent-owned skill lifecycle and proposal linkage (issue #510; List 1 row 48) | `skillstore` bounded manifest plus immutable content-addressed `SKILL.md` files; the proposal's terminal linkage remains in `reflectionstore` | restart reloads exact versions, revisions, lifecycle states, histories, and active selection. A crash after draft persistence but before proposal linkage leaves an inactive Draft and possibly an unreferenced immutable file; retry converges the exact body/provenance and CAS-links the same SkillID | **persist-in-store**: both stores are independently atomic. ProposalID provenance and deterministic SkillID/version provide idempotent reconciliation without claiming a distributed transaction; no startup sweep and no automatic activation | 0110 |
| 32 | Distributed automatic-learning reservations, weighted cooldowns, deduplication, and global/principal count-token windows (ADR 0259; durable automatic-admission ledger) | deterministic attempt-linked records in `automaticstore.Store` or the negotiated `AutomaticAdmissionLedger` driver | restart and replica replacement reload held/retained/reclaimed charges, fence generations/expiry, cooldown and dedupe membership. A Build-owned worker uses bounded backend-authoritative discovery to re-fence every expired held record, checks the linked `AttemptRepository`, and converges a crash after Reserve to reclaimed or a crash after attempt Create to retained without replaying admission | **persist-in-store and reconcile**: global limits no longer multiply by process when the durable ledger is wired. Deterministic identities and fresh fences prevent a duplicate charge or attempt. Local durable retention is capped at 512 records globally and 128 per opaque principal partition, with resolved entries pruned after dedupe expiry and unresolved saturation rejected. Explicit hard admission keeps its distinct policy while weighted work uses the shared durable attempt lifecycle | 0259 (supersedes ADR 0114's process-local accounting where wired) |
| 33 | Redis derivative session inventory rows, owner indexes, and cursor generations (List 1 row 7) | canonical `mecatl:session-metadata:*` and `mecatl:events-gen:*` keys updated atomically with each current snapshot | restart reloads the persisted current index and generations. Incompatible addressed records fail validation | **persist current records / fail closed**: current Save and Delete maintain the projection; malformed or inconsistent current records fail construction or access. There is no inventory scan, adoption job, or migration path | current storage contract |
| 34 | Legacy-adoption source relationship and idempotency proof (issue #593) | optional `session.Session.Adoption` metadata persisted by `sessnap.Snapshot` as the flat `adoption_source_id` / `adoption_request_digest` fields; only the authenticated server adoption path stamps it | restart reloads the target's immutable source link and caller/source/request digest, so a lost-response retry deterministically resolves to the same complete main session without an in-memory key map | **persist-in-snapshot**: the raw idempotency key is not persisted; its caller/source/request-bound SHA-256 digest and the opaque deterministic target ID are sufficient to converge retries. The legacy source remains unchanged | ADR 0226 |
| 36 | Manual dream pending plans and terminal decision receipts (ADR 0228; List 1 row 58) | process-local `dreamReviewCoordinator.records`; each pending record holds the instance-bound versioned `dream.Plan` plus its detached review, and each terminal record retains only decision/receipt/failure state until lazy TTL eviction | restart or a wrong replica loses every plan and receipt. A pending plan cannot be decided; the operator must explicitly regenerate, spending another provider call. A same-process repeated identical decision is idempotent only while its terminal record remains | **reset-by-design**: authoritative mutation material is deliberately not persisted, reconstructed from a review projection, or accepted from the client. v1 is not HA/sticky-route portable. Durable memory revisions/tombstones from already completed independent operations remain the store's truth; there is no grouped rollback | 0228 |
| 37 | Pending (un-drained) operator steer (issue #512, ADR 0232; List 1 row 60) | the `Run`-scoped `steerInbox` pending bundle, including text, media parts, and the latest `message_id` watermark; in-memory only, never written to the SessionStore / snapshot | **restart-lost BY DESIGN**: a parked steer dies with its run (a process crash, a `Run.Cancel`, or a session `Abandon`) and is never persisted, so nothing rehydrates it; the operator re-sends on the next run. Only a steer that drained at a turn boundary (or the terminal close-drain, `closeSteerDrained`) survives, as an ordinary recorded user continuation in durable history, replayed like any user turn and reconstructed by `eventsource.Fold`. The clean-exit continue-run rule (`finishTurnNoTools`) keeps a live run from terminating while a steer is parked, so the loss window is crash/cancel-only, never a normal end. The bundled `message_id` is lost alongside its pending content | **reset-by-design**: the inbox is best-effort mid-run input, not state-of-record; the same posture as the run-scoped ephemera cluster (rows 13/14/16). The never-drop guarantee is engine-internal for a LIVE run, not a durability claim | 0232 |
| 38 | Aggregate-owned failed-step retry intent (issue #409, ADR 0239) | `engine/session/session.go` (`PrepareFailedStepRetry` / `FailedStepRetryPending`) consumes an eligible Retryable Precommit/Visible failure into `retryPending` plus typed facts. `sessnap.Snapshot` persists it before `RetryFailedStep` launches; `ModelRetryPayload` lets `eventsource.Fold` derive pending/running progress without Text parsing | crash before turn.start restores idle+pending; crash after turn.start restores running+pending and partial deltas are excluded. A clean pre-turn brake leaves idle+pending; cancellation clears intent. Live instruction/system sources are re-resolved | **persist-in-snapshot and derive-from-EventLog**: marker fields plus structured model.retry close prepare/launch/crash windows without persisting whole prompts | 0239 (issue #409) |
| 39 | Relay-buffered message/reasoning deltas (issue #469, ADR 0243; List 1 row 63) | the run-scoped `RunEventRecorder` holds at most one 1 MiB message and one 1 MiB reasoning accumulator for the current turn; full chunks are appended once and cleared even when append reports an error | restart loses pending chunks, and any failed append is treated as an unfillable gap because retry could duplicate a post-write success; successful chunks are in EventLog, while the SessionStore snapshot remains the authoritative completed-turn state | **reset-by-design**: bounded memory and record size intentionally accept process-crash/append-failure loss. Clients still receive original chunks live; normal turns coalesce to one record per present kind and oversized turns to the minimum bounded count | 0243 |
| 40 | Debug-session kind, incarnation-bound target lineage, scoped evidence handles, and debugger EventLog projections (ADRs 0254–0257) | `session.Session.Kind` plus `Session.Relationship.DebugTargetID` and the non-projectable `DebugTargetFingerprint`, round-tripped by sessnap and every SessionStore/driver; durable lineage indexes key content-free rows by `(session ID, incarnation)` and existing typed EventLog records are rescanned for each scoped call; scope/history handles are deterministic domain-separated hashes including the applicable incarnations and retain no server state | restart reloads the debugger's own conversation and exact target-incarnation binding. `Service.rehydrateSession` reloads the target and rebuilds only through `DebugSessionEngine`; invalid metadata, missing factory, target deletion/reuse, or (when ownership is enforced) issuer+subject mismatch fails closed. Display/grant metadata changes do not alter identity, and ownership-disabled deployments omit owner comparisons consistently. Handles remain stable but are accepted only after current root/edge/owner-posture/retention revalidation. Historical tombstones survive same-ID recreation. Retained network attempts, content-digest-free request manifests, delegation payloads, and compaction archives remain in configured EventLog retention; in-memory EventLog intentionally does not | **persist existing snapshot/index/events; derive engine and handles**: no handle map, cache, goroutine, or new durable store was added. Backend absence, pruning, scan bounds, incomplete reconstruction, and unavailable collection/causality facts surface explicitly rather than being inferred | 0254, 0255, 0256, 0257 |
| 41 | Worktree selector HMAC key and issued selectors (ADR 0291; List 1 row 69) | the random selector HMAC key is `app.Build`-owned process state; selectors are returned to clients only and no selector registry/map exists | restart drops the key, invalidating all outstanding selectors; selectors are not persisted in sessions, snapshots, schedules, or driver storage. Clients relist worktrees to obtain selectors scoped to the current process, caller, and source session | **reset-by-design**: key and selectors are not persisted; restart requires relist. Current eligible choices are re-enumerated and constant-time matched, so there is no registry/map to recover | 0291 |
| 42 | Session-lease local validity, lost-owner denial, invalid tombstones, provisional run admission, and drain preservation (ADR 0292; List 1 row 27) | `Service.heldLeases`, `Service.lostOwnership`, `SessionMutationCapability.states`, and live `runState` admission/persistence/preservation fields; durable awaiting truth remains `sessnap.Snapshot.Pending` | process restart drops the local hold, lost-owner denial, mutation grant/tombstone, provisional admission, renewer, run registry, and preservation bit. A successor still cannot act until it acquires the backend lease after release/TTL. It then reloads the authoritative snapshot: `running` is repaired through `Abandon`; `awaiting` retains the exact `PendingAsk` and resumes through the existing awaiting seam | **reset local authority by design; preserve snapshot truth**: local lease validity is never rehydrated or inferred from affinity routing. It is freshly derived only from successful lease acquisition. The snapshot already persists the state and pending ask, so no new durable field is required. Drain/lease-loss preservation prevents a stale relay from overwriting it | 0292 |
| 43 | Durable learning-attempt lifecycle and admission provenance (ADR 0259; List 1 rows 70–71) | caller-partitioned `AttemptRepository` records keyed by deterministic caller/session/RunID/canonical-digest IDs | restart or replica replacement reloads the same queued, running, or terminal attempt plus immutable current-prompt/source binding, opaque version, claim generation/expiry, checkpoints, safe failure code, and authorized proposal/skill links. The Build-owned worker continuously discovers queued and expired-claim records through the repository (including remote drivers), reconstructs exact bounded evidence, requests durations while backend time governs every claim transition, renews claims during long work, and resumes claim-fenced boundaries. Incomplete terminal evidence or transient setup persists exponential backoff in the running claim and reaches `retry_exhausted` on the third failed claim instead of retrying forever; duplicate admission converges to that record | **persist-in-store and re-drive**: admission first verifies the exact non-empty RunID from the authoritative SessionStore, then idempotently creates the attempt before reporting `queued`. Process-local coordinator rejection cannot strand it; skipped/non-admitted completions and unwired default/off composition write no record. A configured remote store may re-drive existing attempts in Off mode but ordinary Off completions do not admit new ones; coordinator and receipt state are not authority | 0259 |
| 45 | Session-scoped MCP broker incarnation, local attachment, and parked authorization (P10–P11; List 1 rows 68–70) | the snapshot persists `session.Session.ExternalBinding` plus the exact private `PendingAuthorization`; the process-owned broker holds logical state, grants/tokens, callback/replay state, timers, and prepared continuations in memory | same-process controls reattach only when the opaque binding matches exactly. **The binding value itself, not merely its persistence, is what defeats a restart**: it is `bindingPrefix + "." + generation`, where `bindingPrefix` is random per process and `generation` is an in-memory counter (`internal/adapter/mcpbroker/runtime.go`), so a byte-perfect restored value is structurally guaranteed stale the moment the broker process restarts — no persistence fix closes this alone. A process restart loses in-process broker authority and timers; a lease-gated status-unavailable observation pairs the parked call as interrupted and never replays the protected mutation. The one narrow exception is the pre-prompt workspace-enrollment seam (`rebindBrokerAttachment`, `internal/adapter/server/mcp_broker.go`), which adopts the live incarnation ONLY where nothing durable was built on the lost one — it must not widen to authorization control paths, which stay fail-closed. A future remote broker can return pending, granted, or a concrete terminal status through the same control flow | **durable pending / reset runtime / derive control**: the aggregate preserves exact effective and deferred calls, while timers and prepared runs are recreated only from authoritative live state. Public wire and client surfaces remain deferred to P12–P14 | P10, P11 |
| 46 | Workspace-enrollment outer correlation versus ToolHive inner authorization storage (ADR 0311; List 1 rows 68–69 and 71) | the snapshot persists the exact `PendingWorkspaceEnrollment`; mecatl's runtime owns its process-local callback state/incarnation mapping, while ToolHive memory or Redis storage owns separate inner upstream authorization/token records | Redis-backed ToolHive records may survive restart, but the outer operation-to-session correlation does not. Rebinding the pre-prompt session to a fresh runtime only discards the exact old pending correlation and permits a fresh enrollment; it is not authority to search ToolHive storage or resume an old operation. A wrong replica has the same correlation loss | **persist aggregate / reset outer correlation / do not infer inner recovery**: terminal observations are repeatable in-process until the aggregate save succeeds; after process loss, discard only the exact old pre-prompt correlation and begin a fresh enrollment. Durable outer correlation, replica routing, and restart recovery remain deferred | ADR 0311 |
| 47 | Prompt-free ordinary-ask handoff and acknowledgement-only resumed-run lifecycle (ADR 0347; List 1 row 74) | the existing session snapshot persists `StateAwaiting`, exact `RunID`, `PendingAsk`, mode, and `EnvironmentRef`; the accepting Service owns only the reconstructed run/context/relay described in List 1 | a restart or replica handoff loses local execution and join bookkeeping but retains the last authoritative awaiting or terminal snapshot. An accepting replica acquires the distributed lease, reloads after acquisition, and revalidates exact run, awaiting state, ask ID, non-plan origin, and placement before `ResumeApproval`; a stale pre-lease snapshot cannot replay allow-once work | **persist existing snapshot, reconstruct under fresh lease**: no new durable field is introduced. Before shutdown cancellation reaches a detached continuation, an already-persisted awaiting point is marked for preservation and the relay is joined boundedly; otherwise terminal cancellation is persisted so accepted work is not ambiguously replayed. Plan asks remain on their dedicated resolution path | 0347 |

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

- **jsonlstore v2 snapshots are atomically replaced and explicitly report crash
  durability.** Later storage-continuity work (ADR 0226) replaced the append-only
  snapshot writer while leaving v1 readable and tool/event sidecars append-only.
  `Save` writes one complete same-directory temporary, syncs it, renames it over
  the authoritative v2 snapshot, then syncs the directory where supported. The
  adapter reports atomic-replace, file-sync, and directory-sync capability
  separately; it claims host-crash safety only when all three hold. Failures before
  rename leave the prior snapshot authoritative; failures after rename are loud and
  leave the new snapshot authoritative. A present v2 snapshot remains authoritative
  over coexisting v1, so failure recovery never resurrects older history. This does
  not add cross-process generation or orphan-temporary reaping; those remain on the
  storage-maintenance track.
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
