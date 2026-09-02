# Fire-result delivery — acceptance plan

**Phase:** scheduled tasks — delivery channel
**Status:** landed, 2026-07-27. Synthesised from the design discussion closing #307's deferred "ping me when X" follow-up.
**Issue:** [stacklok/mecatl#189](https://github.com/stacklok/mecatl/issues/189) (scheduled tasks; delivery was ADR 0073's out-of-scope follow-up).
**ADR:** [ADR-0075](../adr/0075-fire-result-delivery.md) — pins the origin-capture + fenced-untrusted delivery + pending-drain + best-effort decisions.
**Accumulator branch:** `acc/fire-result-delivery` (off `main`).

The smallest set of work that lets a scheduled fire **report its result back into
the conversation that created it, rendered live in the operator's mecatui** — so
"remind me of X", "ping me when Y", and "monitor this PR every 30m" answer *in the
same chat, as it happens* — instead of stranding the outcome in a hard-to-find
`sched--` session or an invisible queue.

**The e2e flow this plan delivers:** the operator tells the model in mecatui
"monitor this PR every 30m" → the model calls `Schedule create` (the origin chat
is captured) → the scheduler fires unattended → the fire's result is rendered as
a fenced-untrusted note → **it appears in the operator's live mecatui chat as a
delivery card, with the model's acknowledgment, no operator input required.** If
any link in that chain is missing, the feature is not usable and does not ship.

The doc is organized scenario-first because acceptance is about what the running
harness can demonstrate, not which packages exist on disk.

## Why these scope cuts

- [ADR-0075](../adr/0075-fire-result-delivery.md) — delivery is a fenced-untrusted
  harness note recorded as ordinary history, drained at the origin's next
  run-entry boundary. It is NOT a new event family, NOT a live instruction, NOT a
  synchronous run on a busy origin.
- [ADR-0059](../adr/0059-scheduled-tasks.md) decision #7 — the fire still mints a
  fresh `sched--` session and ends there; delivery is a side-channel after the
  terminal result, never a fire failure mode.
- [ADR-0073](../adr/0073-schedule-tool.md) decision #1 — the in-chat `Schedule`
  tool is the single authoring surface; origin capture rides its `create` verb.

## In scope — 6 scenarios, in 3 waves

Scenarios are grouped into 3 waves. Each is independently demoable; later waves
assume earlier ones but don't change their acceptance criteria.

- **Wave 1 — engine/store surface (Scenarios 1–4):** delivery is durable and
  correct (origin capture → fenced render → durable queue → delivery run + drain).
- **Wave 2 — embedded TUI surface (Scenario 5):** delivery is *visible* in the
  operator's mecatui — the actual product, via the in-process path.
- **Wave 3 — remote wire subscription (Scenario 6):** the SAME live card extended
  to a remote mecated over gRPC/SSE — the bigger change, last because it is
  additive to a working embedded product.

Waves 1 + 2 deliver the full e2e for the operator's actual (embedded) usage and
are the ship-gate. Wave 3 extends it to remote; it lands on the same accumulator
but is sequenced last.

### Scenario 1 — Origin capture at create-time

The model calls `Schedule create` in a conversation; the tool stamps the calling
session's id onto the new `ScheduleSpec.OriginSessionID` so the fire path later
knows where to deliver. The capture rides the SAME validated create-seam
(`validateScheduleSpec` + `applyScheduleDefaults`) the REST/gRPC handlers call —
never a second path ([ADR-0073](../adr/0073-schedule-tool.md) decision #1). The
origin id is metadata (never rendered into a prompt), so it carries no injection
weight. An out-of-band create (empty origin) is unchanged.

**The capture seam is NEW — it does not exist today.** A catalog tool's `Execute`
receives only `(ctx, call, ws)`; neither the ctx nor the `Workspace` carries a
session id, and `parentCaps` (the Subagent seam) has no `SessionID`. The
Schedule tool takes the plain `Execute` path. So origin capture requires a new,
layer-clean plumbing seam: the session id is bound **per-session** into the
tool's `ScheduleManager` closure by composition at catalog-assembly time (the
per-session engine factory / `sessionEngineFactory` already assembles a catalog
per session — the manager closure captures that session's id), NOT a ctx-value
and NOT a `parentCaps` widening. This is the load-bearing seam for all four
scenarios.

This is a `ScheduleSpec` field addition, so it touches the engine's exported
surface — `task api:update` + an `engine/CHANGELOG.md` note ([ADR-0037](../adr/0037-engine-stability-contract.md)).

**Work:**
- engine domain (`engine/port`): add `OriginSessionID string` to `ScheduleSpec`
  with the field-by-field contract doc (empty = no delivery; metadata-only).
- engine app (`engine/agent`): the `Schedule` tool's `create` verb stamps the
  session id its manager closure was bound with onto the spec before the
  create-seam. The id is the CREATING session only — the model cannot supply or
  influence it (no `origin` arg on the tool schema), so a model can't redirect
  delivery to a session it didn't create.
- adapters (`internal/adapter/server`): `validateScheduleSpec` rejects a
  non-empty `OriginSessionID` that names a non-existent session, fail-closed.
- composition (`internal/app`): bind the per-session id into the manager closure
  at catalog assembly; thread the origin id through the create-seam unchanged.

**Acceptance:**
- AC1.1: A schedule created via the in-chat `Schedule create` verb persists the
  calling session's id in `Spec.OriginSessionID`, bound via the composition
  closure (not a ctx-value, not a model-supplied arg).
  - verify: `TestFireDelivery_Scenario1_CreateCapturesOriginSession`
- AC1.2: A schedule created out-of-band (REST/gRPC, no conversation) persists an
  empty `OriginSessionID` and behaves exactly as before.
  - verify: `TestFireDelivery_Scenario1_OutOfBandCreateHasEmptyOrigin`
- AC1.3: A create whose `OriginSessionID` names a non-existent session is
  rejected fail-closed by the create-seam (the same class as the other spec
  rejections).
  - verify: `TestFireDelivery_Scenario1_UnknownOriginRejected`
- AC1.4: The tool schema exposes NO `origin` argument and the model cannot
  influence the captured id — delivery is always to the creating session.
  - verify: `TestFireDelivery_Scenario1_OriginNotModelForgeable`
- AC1.5: The `OriginSessionID` value never appears in any model-visible surface.
  An engine is built, a schedule is created via the tool, and the id is asserted
  absent from the captured `LLMRequest.System` and the `ToolResult` text (an
  executable assertion, per [ADR-0070](../adr/0070-model-visible-affordance-gate.md)
  — not a grep over a subset of render paths).
  - verify: `TestFireDelivery_Scenario1_OriginIDNotModelVisible`

---

### Scenario 2 — Fenced-untrusted delivery rendering

Given a fire's terminal `EvResult` (stop reason + final text), the delivery
renderer produces a **fenced untrusted** harness note — schedule name, fire id,
stop reason, and the fire's final text, rune-clamped and `NeutraliseFraming`'d —
using the EXACT `agent.FenceUntrusted` discipline carried context already uses
([ADR-0059](../adr/0059-scheduled-tasks.md) Phase 2). A forged `<<<UNTRUSTED`
marker or harness section header inside the fire's text cannot break out of the
block. The note is framed as data ("a scheduled task reported"), never as a live
instruction.

**Work:**
- engine app (`engine/agent`): a `renderFireDelivery` renderer sibling to
  `renderCarriedContext` — provenance header + fenced body + `NeutraliseFraming`,
  clamped to a bounded rune budget.
- composition (`internal/app`): the fire path calls the renderer on the terminal
  result; the output is the enqueued pending-delivery note text.

**Acceptance:**
- AC2.1: The rendered note wraps the fire's outcome in `agent.FenceUntrusted`
  with a provenance header naming the schedule and fire id.
  - verify: `TestFireDelivery_Scenario2_RenderFencesOutcome`
- AC2.2: A fire text containing a forged `<<<UNTRUSTED` closing marker or a
  harness section header is neutralised — the rendered note keeps it inside the
  fence.
  - verify: `TestFireDelivery_Scenario2_NeutralisesForgedFraming`
- AC2.3: An over-long fire text is clamped to the delivery rune budget (the note
  never blows the origin conversation's context window).
  - verify: `TestFireDelivery_Scenario2_ClampsToRuneBudget`
- AC2.4: A fire that ended with no meaningful text (an empty terminal) still
  renders a note carrying the stop reason — the delivery is never a silent blank.
  - verify: `TestFireDelivery_Scenario2_EmptyTerminalStatesStopReason`
- AC2.5: The provenance header's model-authored fields are neutralised. The
  schedule NAME is model-authored at create time (only validated non-empty), so
  a name containing a forged `Tool:`/`Policy:`/`<<<UNTRUSTED` line is passed
  through `NeutraliseFraming` and the whole header is placed INSIDE the
  `FenceUntrusted` block — the header's trusted framing and the attacker-chosen
  name are defanged alike (the `renderCarriedContext` precedent,
  [`internal/app/scheduler_fire.go`](../../internal/app/scheduler_fire.go)).
  - verify: `TestFireDelivery_Scenario2_ProvenanceHeaderNeutralised`

---

### Scenario 3 — Delivery into an idle or terminal origin session

A fire of a schedule with a non-empty `OriginSessionID` completes; the origin
session is idle or in a terminal state (completed/cancelled/failed). The fire
path enqueues the note and drives a delivery run on the origin through the
EXISTING `StartRunContent` → `loadAndReopen` funnel, which reopens-if-completed /
interrupts-if-cancelled / recovers-if-failed before recording the note ([ADR-0027](../adr/0027-cloud-native.md),
[`AGENTS.md` — the run-entry seams](../../AGENTS.md)). The note lands as ordinary
harness-framed user history in the origin conversation, and the origin's model
sees it on the next turn. Delivery happens AFTER the fire's terminal result and
is decoupled from the fire's success ([ADR-0075](../adr/0075-fire-result-delivery.md)
decision #4).

**Two origin states need explicit handling the naive design misses:**
- **An AWAITING origin** (parked on a permission ask) is NOT covered by
  `loadAndReopen` (which handles only completed/cancelled/failed). A delivery
  run on an awaiting origin must NOT bypass the pending ask; the note is queued
  and drained at the awaiting-resume boundary (the `resumeFromAwaiting` path),
  exactly like the busy case — the ask resolves first.
- **The session lease is process-held for the session's life**, released only by
  `CloseSession`/shutdown, never per-run. In a single process the fire's
  delivery `StartRunContent` on the same origin short-circuits `heldLeases[id]`
  (already ours) — no collision. In a MULTI-process deployment the origin's
  owner process holds the lease, so a DIFFERENT replica's fire cannot deliver;
  that replica queues + WARNs (single-writer is intentional, [ADR-0027](../adr/0027-cloud-native.md)
  Phase 4) and the owning replica's next run-entry drains the queue. Delivery is
  an in-process convenience layered on the durable queue, never a cross-process
  takeover.

**Work:**
- ports / composition (`internal/app`): a DURABLE per-session pending-delivery
  queue (survives restart — see Scenario 4) + the idle-origin delivery drive via
  `StartRunContent` (reusing `loadAndReopen`).
- engine domain (`engine/session`): the note is recorded via the existing
  `RecordUserPrompt` path (ordinary history) — no new mutation verb.
- composition (`internal/app`): the fire path invokes delivery after
  `RecordFire`, gated on non-empty `OriginSessionID`; a delivery error WARNs and
  never fails the fire.

**Acceptance:**
- AC3.1: A completed origin session receives the delivered note as a recorded
  user message and is reopened to idle (not left in the terminal state).
  - verify: `TestFireDelivery_Scenario3_CompletedOriginReopenedAndNotified`
- AC3.2: A cancelled origin session is recovered via `Interrupt` and receives the
  note; a failed origin is recovered via `Recover` and receives the note.
  - verify: `TestFireDelivery_Scenario3_TerminalOriginRecovered`
- AC3.3: The delivered note appears in the origin conversation's recorded history
  (replay-durable) and is provider-legal (never inside a `tool_use` pair;
  `ValidateToolPairing` holds).
  - verify: `TestFireDelivery_Scenario3_NoteIsProviderLegalHistory`
- AC3.4: A delivery failure (e.g. the origin vanishes between enqueue and drive)
  WARNs and leaves the recorded fire untouched — the fire's stop reason and
  `GetFire` result are unchanged ([ADR-0075](../adr/0075-fire-result-delivery.md)
  decision #4).
  - verify: `TestFireDelivery_Scenario3_DeliveryFailureNeverFailsFire`
- AC3.5: An origin parked AWAITING an approval is NOT driven past its pending
  ask; the note is queued and delivered only after the ask resolves (via the
  `resumeFromAwaiting` boundary).
  - verify: `TestFireDelivery_Scenario3_AwaitingOriginQueuedNotBypassed`
- AC3.6: After a compaction pass with a delivered note in the origin's tail
  window, the note survives verbatim AND still carries an intact `<<<UNTRUSTED`
  fence pair — a model replaying the compacted history sees the fire body as
  fenced data, not as a genuine user instruction (the `isGenuineUserTurn`
  classification preserves it verbatim; the fence is the load-bearing guard).
  - verify: `TestFireDelivery_Scenario3_DeliveredNoteSurvivesCompactionFenced`
- AC3.7: A fire delivered into an origin with a STRICTER posture than the fire's
  pinned posture does not loosen the origin's posture: the origin's subsequent
  tool calls still resolve against the origin's own policy, and a delivered note
  claiming "the human approved X" does not change the origin's permission
  verdict for X (claims of approval inside the untrusted fence are void).
  - verify: `TestFireDelivery_Scenario3_DeliveryDoesNotLoosenOriginPosture`

---

### Scenario 4 — Busy origin: the pending-delivery drain

A fire completes while the origin session has a run in flight. A synchronous
delivery would collide with the single-writer run-entry discipline (`runEntryMu`
+ the session lease) and the state machine, so the note is queued and drained at
the origin's **next turn boundary** by the loop ([ADR-0075](../adr/0075-fire-result-delivery.md)
decision #3). The drain records the note at **Step 2a, BEFORE `BeginTurn`** — the
same seam `injectBackgroundNotice` uses ([`engine/agent/loop.go`](../../engine/agent/loop.go)),
where history always ends on a user prompt / tool result / nudge, never inside a
`tool_use` pair — so the recorded note is provider-legal and never orphans a
pending tool call ([`AGENTS.md` — compaction/tool-pairing](../../AGENTS.md)).

**The drain is per-SESSION, not per-Run — the exactly-once ledger must survive
the current run.** The background-children `noticed`/`delivered` bookkeeping is
owned by the parent `Run` and dies with it; a pending-delivery queue must instead
be keyed on the SESSION so a note queued during one run is drained on the next
run if the current one ends first. That ledger is a new outlives-a-call resource:
it is inventoried in [ADR-0027](../adr/0027-cloud-native.md) List 1, and its
restart-fidelity decision (List 2) is **persist-in-snapshot** — the queue is
durable, so a restarted process drains the still-pending notes rather than losing
them (the cross-process lease case in Scenario 3 depends on this).

**The origin's lifecycle matters.** An origin that is a child session
(`subagent-`/`parallel-`/`team-` prefixed) or a `sched--` fire session is
short-lived and reaped by GC; a schedule created inside a Subagent delegation
captures a child id that the childGC collects. Such an origin degrades to
pull-only with a WARN (never a silent no-op, never a delivery loop into another
fire's chat). A deleted origin likewise drops the delivery with a WARN and the
result stays pull-able.

**Work:**
- engine app (`engine/agent`): a pending-delivery drain hook at the turn
  boundary (Step 2a, alongside `injectBackgroundNotice`, BEFORE `BeginTurn`)
  that records queued notes for the running session, exactly-once.
- ports / composition (`internal/app`): the DURABLE per-session pending-delivery
  queue (persist-in-snapshot; ADR 0027 List 1 + List 2 rows) the fire path
  enqueues to and the loop drains; the session-keyed exactly-once ledger.
- composition (`internal/app`): child/GC'd/deleted origin detection degrades to
  pull-only with a WARN; the drain is registered wherever the origin session's
  engine runs (main + per-session), never in child catalogs.

**Acceptance:**
- AC4.1: A fire completing during an in-flight origin run does NOT interrupt or
  collide with that run; the note is queued.
  - verify: `TestFireDelivery_Scenario4_BusyOriginQueuesNotCollides`
- AC4.2: A queued note is drained and recorded exactly once — recorded at Step 2a
  BEFORE the next `BeginTurn` (never mid-dispatch between a `tool_use` and its
  result), never twice, never lost — and the exactly-once ledger survives the end
  of the run it was queued during (a note queued in run N drains in run N+1 if
  run N ends first).
  - verify: `TestFireDelivery_Scenario4_DrainedExactlyOnceAcrossRuns`
- AC4.3: Multiple fires completing during one origin run accumulate; each
  pending note is drained (no coalescing that loses a fire's outcome). A bounded
  backlog cap drops the oldest with a WARN rather than growing unboundedly on an
  overloaded origin.
  - verify: `TestFireDelivery_Scenario4_MultiplePendingAllDrained`
- AC4.4: A fire whose origin session was deleted, OR whose origin is a collected
  child/`sched--` session, drops the delivery with a WARN and never fails the
  fire nor delivers into another fire's chat; the result remains pull-able via
  `ListFires`.
  - verify: `TestFireDelivery_Scenario4_NonDeliverableChildOriginDropsWithWarn`
- AC4.5: The pending-delivery queue is durable: a process restart with notes
  still pending drains them on the origin's next run-entry (the
  persist-in-snapshot List 2 decision), it does not lose them.
  - verify: `TestFireDelivery_Scenario4_PendingQueueSurvivesRestart`

---

### Scenario 5 — The TUI surfaces the delivery live (the actual product)

Scenarios 1–4 make delivery *durable*; this scenario makes it *visible* — and it
is the whole point ([ADR-0075](../adr/0075-fire-result-delivery.md) decision #5).
The mecatui is a **thin relay with no server→client push
channel**: it renders only the events of a `Converse` stream it opened by sending
a prompt ([`cmd/mecatui/client/stream.go`](../../cmd/mecatui/client/stream.go);
one Converse = one run). A server-initiated delivery run has **no client draining
it**, so its events never reach the screen — the "ping" would sit invisible until
the operator happened to type something. That is the useless shape this scenario
exists to prevent.

Because mecatui hosts mecated **in the same process** ([`cmd/mecatui/embed`](../../cmd/mecatui/embed/embed.go)),
the scheduler, the origin session, and the TUI share a process — so the delivery
run CAN surface to the connected TUI in-process, with no cross-process push. The
mechanism is a **session event subscription**: the Service exposes a per-session
live event stream a connected client holds open (the `relayEventsSSE` merged-stream
precedent, [`internal/adapter/server/http.go`](../../internal/adapter/server/http.go));
the embedded delivery run's events are published onto it, and the TUI renders them
through the SAME `EventToMsg` projection a user-initiated run uses — projection
equivalence ([`cmd/mecatui/client/stream.go`](../../cmd/mecatui/client/stream.go) `readEventLoop`).

The delivered note is rendered as a **distinct delivery card** (a scheduled-task
icon + the schedule name + the fenced outcome), NOT as a user-typed prompt and
NOT silently as the model's own text — the operator must be able to tell "a
scheduled task reported" apart from "I said that." The model's one-line
acknowledgment (the delivery run's assistant reply) renders normally after it.
This is the AC that, if not green, means the feature does not ship.

**Work:**
- ports (`engine/port`): a per-session live event subscription seam the Service
  exposes (a merged stream over the session's runs, distinct from a single run's
  `Run.Events()`), or an equivalent in-process publish hook the embedded server
  uses. The loop stays storage-agnostic — it emits; the relay publishes.
- adapters (`internal/adapter/server`): the delivery run's events are published
  to the session's live subscription (in-process for the embedded server; the
  gRPC/SSE relay for a remote client). The delivered note carries a delivery
  marker so the client can render it distinctly.
- composition (`internal/app` / `cmd/mecatui/embed`): the embedded server drives
  the delivery run and publishes its events onto the origin session's live
  subscription the connected TUI holds open.
- TUI (`cmd/mecatui/client` + `ui`): hold the session subscription open; map the
  delivery event to a delivery card (a new `tea.Msg` + render), reusing
  `readEventLoop`/`EventToMsg`. A reconnecting TUI replays missed deliveries via
  the existing `StreamSessionEvents` replay feed (which relays `EvUserPrompt` —
  [`internal/adapter/server/grpc.go`](../../internal/adapter/server/grpc.go):386).

**Acceptance:**
- AC5.1: With a mecatui connected to a session, a fire of that session's schedule
  renders the delivered note in the live chat AS IT HAPPENS — no operator input,
  no manual refresh — as a distinct delivery card naming the schedule.
  - verify: `TestFireDelivery_Scenario5_ConnectedTUIRendersDeliveryLive`
- AC5.2: The delivery card is visually distinct from a user-typed prompt AND from
  the model's own text (a scheduled-task affordance + the schedule name), so the
  operator can tell provenance at a glance.
  - verify: `TestFireDelivery_Scenario5_DeliveryCardDistinctProvenance`
- AC5.3: The delivered note the TUI renders is the SAME fenced-untrusted content
  the engine recorded (no second render path, no un-fenced echo) — the TUI
  projects the recorded history, it does not re-render the fire's raw output.
  - verify: `TestFireDelivery_Scenario5_TUIRendersRecordedNoteNotRaw`
- AC5.4: A TUI that reconnects AFTER a delivery sees the delivered note via the
  replay feed (the `StreamSessionEvents` path relays `EvUserPrompt`), so a
  delivery is never lost to a disconnect.
  - verify: `TestFireDelivery_Scenario5_ReconnectReplaySeesDelivery`
- AC5.5: `go run ./cmd/mecademo` demonstrates the e2e: a schedule created in-chat
  fires and its result appears in the SAME session's transcript as a delivered
  note, with no separate `sched--` session hunt.
  - verify: demonstration — a mecademo flow showing create → fire → in-chat
    delivery, captured in the plan's done-gate run.

---

### Scenario 6 — Remote wire subscription (the bigger change)

Scenario 5's live card rides the **embedded in-process** path: mecatui hosts
mecated in the same process, so the delivery run's events reach the connected TUI
without any wire change. A **remote** mecated (the TUI connects over gRPC to a
separate daemon) has no such in-process shortcut — the per-session live event
subscription must cross the wire. This is the bigger change, scoped as the LAST
wave because it is additive to a working embedded product: Wave 1 + Wave 2
already deliver the full e2e for the operator's actual (embedded) usage, so this
wave extends the SAME live-card behaviour to a remote origin rather than unblocking
it.

The mechanism is the wire analogue of Scenario 5's in-process subscription: a
per-session **server-streaming event subscription RPC** (a `StreamSessionLive`
sibling to the existing `StreamSessionEvents` replay feed, [`internal/adapter/server/grpc.go`](../../internal/adapter/server/grpc.go):378)
that a connected TUI holds open for its active session, onto which the Service
publishes every run's events for that session (including a server-initiated
delivery run). It must hold the SAME invariants as the in-process path:
projection equivalence (a delivery projects identically to a user-initiated run),
the dead-client drain-to-discard discipline, and the log-only-kind client-wire
skip — EXCEPT `EvUserPrompt`, which the LIVE delivery card needs (the replay feed
already relays it, [`internal/adapter/server/grpc.go`](../../internal/adapter/server/grpc.go):386;
the live subscription must too, or the delivery card has no note to render). This
is a proto + wire change, so it lands `task generate` and the contract regen.

**Work:**
- contracts (`contracts/proto/mecatl/v1/`): a per-session live event
  subscription RPC (server-streaming) + the delivery-event marker; `task generate`
  regenerates `contracts/gen/` (never hand-edit).
- adapters (`internal/adapter/server`): the gRPC + SSE handlers publish each
  session's run events (incl. server-initiated delivery runs) onto the
  subscription, holding drain-to-discard + the delivery-carries-EvUserPrompt rule.
- TUI (`cmd/mecatui/client`): when connected to a REMOTE server, hold the live
  subscription open for the active session and render delivery cards from it
  (the SAME `EventToMsg` path as the embedded case — one projection, two
  transports).

**Acceptance:**
- AC6.1: A mecatui connected to a REMOTE mecated renders a fire's delivered note
  in the live chat as it happens, as a delivery card — parity with the embedded
  AC5.1, over the wire.
  - verify: `TestFireDelivery_Scenario6_RemoteTUIRendersDeliveryLive`
- AC6.2: The live subscription relays the delivery's `EvUserPrompt` (the note
  body) on the wire — the delivery card renders the SAME recorded note, not a
  placeholder (the live-wire log-only skip is amended for the delivery case
  only; the other two log-only kinds stay skipped).
  - verify: `TestFireDelivery_Scenario6_LiveSubscriptionRelaysDeliveryNote`
- AC6.3: A dead/disconnected client's subscription drains-to-discard without
  wedging the delivery run (the durable log still records the tail; the fire and
  the recorded note are unaffected).
  - verify: `TestFireDelivery_Scenario6_DeadClientDrainsWithoutWedging`
- AC6.4: The remote and embedded paths render the SAME delivery card for the
  SAME recorded note (one `EventToMsg` projection; a delivery is
  indistinguishable across transports).
  - verify: `TestFireDelivery_Scenario6_TransportProjectionParity`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| `context_from` chaining (seed from a *specified* prior fire) | issue #258 | [ADR-0059](../adr/0059-scheduled-tasks.md) — orthogonal to delivery |
| Driver `ScheduleStoreService` (remote registry) | issue #257 | [ADR-0059](../adr/0059-scheduled-tasks.md) — store transport, not delivery |
| Wake-on-change / no-LLM gate | issue #261 | [ADR-0059](../adr/0059-scheduled-tasks.md) — pre-fire gate |
| Event/webhook triggers (GitHub, Slack) | issue #259 | [ADR-0059](../adr/0059-scheduled-tasks.md) — a trigger ingestion subsystem |
| Per-fire egress allowlist | issue #260 | [ADR-0059](../adr/0059-scheduled-tasks.md) — net-policy infra |
| Mid-TURN interruption (the note appearing while the origin is mid-tool-call) | a later wave | [ADR-0075](../adr/0075-fire-result-delivery.md) decision #3 — delivery is a turn-boundary / new-run surface, never a mid-stream injection |

## Cross-cutting deliverables

- `task api:update` for the `ScheduleSpec.OriginSessionID` field + an
  `engine/CHANGELOG.md` note (Added = minor) per [ADR-0037](../adr/0037-engine-stability-contract.md).
- The Schedule tool's `Spec().Description` + the `schedulePostureNote` gain the
  model-visible line that a created schedule reports back into this conversation
  ([ADR-0070](../adr/0070-model-visible-affordance-gate.md) — a model-facing
  affordance needs a prompt-layer instruction + a test proving it lands).
- `docs/architecture.md` + `docs/design/IMPLEMENTATION-NOTES.md` (scheduled-tasks
  section) updated for the delivery channel; `task docs` regenerates `llms.txt`.
- The outlives-a-call resource inventory ([ADR-0027](../adr/0027-cloud-native.md)
  **List 1**) gains the durable per-session pending-delivery queue row, and
  **List 2** (rehydrate-fidelity ledger) records its **persist-in-snapshot**
  decision (AC4.5) — the queue survives restart so a cross-process / restarted
  origin drains still-pending notes rather than losing them.

## Sequencing recommendation

Three waves on the same accumulator, in order.

**Wave 1 — engine/store surface (Scenarios 1–4).** Scenario 1 (the spec field +
origin capture) unblocks everything and is the api-gate surface. Scenario 2 (the
renderer) is independent and can land in parallel. Scenario 3 (idle/terminal
delivery) is the core happy path; Scenario 4 (the busy-origin drain + durable
queue) is the genuinely novel mechanism and carries the most review weight.

**Wave 2 — embedded TUI surface (Scenario 5).** Depends on Wave 1's delivery run
existing. NOT optional: it is the only wave that makes the feature *visible* to
the operator in their actual (embedded) usage. The in-process session event
subscription is the one new mechanism; design it against the `relayEventsSSE`
merged-stream precedent and the `readEventLoop` projection-equivalence invariant
so a delivery projects identically to a user-initiated run. **Waves 1 + 2 are the
ship-gate** (DoD #8).

**Wave 3 — remote wire subscription (Scenario 6).** The bigger change, last:
additive to a working embedded product, it extends the SAME live card to a remote
mecated over a new server-streaming subscription RPC (proto + `task generate`).
Sequenced after Wave 2 so the projection it must match (the embedded card) is
already pinned by AC5.1–5.4.

## Named tests landing in this plan

`TestFireDelivery_Scenario1_*`, `TestFireDelivery_Scenario2_*`,
`TestFireDelivery_Scenario3_*`, `TestFireDelivery_Scenario4_*`,
`TestFireDelivery_Scenario5_*`, `TestFireDelivery_Scenario6_*`, plus the
ADR-0070 prompt-land test `TestFireDelivery_ScheduleToolNoteLands`.

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — `llms.txt` regenerated and the matlatl strict link gate green.
3. `task api:check` passes (or `task api:update` was run and the
   `engine/CHANGELOG.md` note is present) — this plan adds `ScheduleSpec.OriginSessionID`.
4. `task ac-trace-strict` — every AC's `verify:` proof resolves (this plan is `landed`).
5. The named tests are green and grep-locatable by their identifiers.
6. `go run ./cmd/mecademo` still prints a full offline session.
7. `TestFireDelivery_ScheduleToolNoteLands` proves the model-visible
   reports-back instruction lands in the built engine's system prompt (ADR 0070).
8. **The e2e is demoable in mecatui (Waves 1 + 2):** a schedule created in-chat
   ("monitor this PR every 30m") fires and its result appears in the SAME
   session's live mecatui chat as a delivery card, with no operator input and no
   `sched--` session hunt (AC5.1/AC5.5). If this is not green, the plan is NOT
   satisfied.
9. **Wave 3 (Scenario 6):** the remote live card (AC6.1) is green — or, if Wave 3
   is split to a follow-up accumulator, the plan is satisfied at DoD #8 and
   Scenario 6 carries forward as its own gated plan. This is the ONE explicit
   fork; it is a named decision, not a silent cut.

## Deferred decisions and known risks

- **Wave 3's fork (DoD #9) is the one open sequencing call.** The ship-gate is
  Waves 1 + 2 (the embedded e2e). Wave 3 (remote) is scoped as a full gated
  scenario on the same accumulator; whether it lands on THIS accumulator or
  carries forward as its own gated plan is decided at the Wave 2 boundary — a
  named fork, not a silent "v2".
- **The busy-origin drain is the load-bearing engine novelty.** It reuses the
  `injectBackgroundNotice` Step-2a seam, but the pending-delivery queue is a new
  DURABLE per-session structure (persist-in-snapshot) keyed on the session, not
  the per-Run `noticed`/`delivered` registry — inventoried in [ADR-0027](../adr/0027-cloud-native.md)
  List 1 + List 2 (AC4.2, AC4.5 pin the behaviour).
- **The session event subscription is the load-bearing TUI novelty.** In-process
  (Wave 2) it is a new Service-owned live stream (the `relayEventsSSE`
  merged-stream precedent), distinct from a single run's `Run.Events()`; over the
  wire (Wave 3) it is a new server-streaming RPC. Both must hold projection
  equivalence (a delivery projects identically to a user-initiated run), the
  dead-client drain-to-discard discipline, and — for the delivery case — relay
  the note's `EvUserPrompt` on the live wire (AC6.2).
- **The capture seam is new plumbing.** The per-session session-id binding into
  the ScheduleManager closure (composition, at catalog assembly) is the
  layer-clean mechanism; a ctx-value or `parentCaps` widening was rejected
  (layering / fragility). AC1.1 pins it.
- **Mid-TURN interruption is a later concern, not this plan.** Delivery surfaces
  at a turn boundary / as a new run — never a mid-stream injection into an
  in-flight turn. The operator sees the note when the current turn ends or as a
  new delivery run; that is the honest surface.
- **Delivery is best-effort.** A deleted / collected-child / `sched--` origin
  degrades to pull-only with a WARN; the result stays pull-able. The fire is
  never hostage to the origin's availability.
- **Multi-process delivery is owner-only.** A different replica's fire cannot
  deliver into an origin another process holds the lease for; it queues + WARNs
  and the owner's next run-entry drains (AC4.5's durable queue is what makes this
  safe). Single-process mecated/mecatui delivery is unaffected.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan
is satisfied.
