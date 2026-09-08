---
id: 05-delivery-run-and-drain
title: Delivery run + busy-origin drain + state-aware handling
blocked_by: [03-delivery-renderer, 04-durable-queue]
status: done
branch: "plan-fire-result-delivery/05-delivery-run-and-drain"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

The core delivery mechanism. After a fire of a schedule with a non-empty
`OriginSessionID` reaches its terminal `EvResult` (`internal/app/scheduler_fire.go`
`makeFireFunc`, AFTER `RecordFire`), the fire path renders the note (task 03),
enqueues it (task 04), and drives delivery into the origin session — decoupled
from the fire's success (a delivery error WARNs, never fails the fire).

State-aware handling:
- **Idle/completed origin:** drive a delivery run via the EXISTING
  `StartRunContent` → `loadAndReopen` funnel (`internal/adapter/server/service.go`),
  which reopens-if-completed / interrupts-if-cancelled / recovers-if-failed
  before recording the note as ordinary harness-framed user history
  (`RecordUserPrompt`). The note lands provider-legal (`ValidateToolPairing`
  holds — never inside a `tool_use` pair).
- **Cancelled/failed origin:** recovered by the same funnel.
- **Awaiting origin:** NOT driven past its pending ask — the note is queued and
  delivered after the ask resolves (via `resumeFromAwaiting`).
- **Busy origin (run in flight):** the note is queued; the LOOP drains it at the
  origin's next turn boundary, at Step 2a BEFORE `BeginTurn` (the
  `injectBackgroundNotice` seam in `engine/agent/loop.go`), recorded as ordinary
  history exactly-once via the session-scoped ledger (task 04). Never mid-dispatch
  between a `tool_use` and its result.
- **Deleted / collected-child / `sched--` origin:** degrade to pull-only with a
  WARN; never fails the fire, never a delivery loop into another fire's chat; the
  result stays pull-able via `ListFires`.
- **Multi-process:** a different replica's fire cannot deliver into an origin
  another process holds the lease for — it queues + WARNs and the owner's next
  run-entry drains (single-writer is intentional; the durable queue makes it safe).

Trust: the note survives compaction still fenced (the `isGenuineUserTurn`
classification preserves it verbatim; the fence is the guard), and a delivery
into an origin with a STRICTER posture does not loosen the origin's posture
(claims of approval inside the untrusted fence are void).

The drain is registered wherever the origin session's engine runs (main +
per-session), never in child catalogs. Tests offline: mockllm + memfs / memstore
+ jsonlstore temp dir.

## Acceptance criteria

- AC3.1: A completed origin session receives the delivered note as a recorded
  user message and is reopened to idle (not left in the terminal state).
  - verify: `TestFireDelivery_Scenario3_CompletedOriginReopenedAndNotified`
- AC3.2: A cancelled origin session is recovered via `Interrupt` and receives the
  note; a failed origin is recovered via `Recover` and receives the note.
  - verify: `TestFireDelivery_Scenario3_TerminalOriginRecovered`
- AC3.3: The delivered note appears in the origin conversation's recorded history
  (replay-durable) and is provider-legal (`ValidateToolPairing` holds).
  - verify: `TestFireDelivery_Scenario3_NoteIsProviderLegalHistory`
- AC3.4: A delivery failure WARNs and leaves the recorded fire untouched (ADR
  0075 decision #4).
  - verify: `TestFireDelivery_Scenario3_DeliveryFailureNeverFailsFire`
- AC3.5: An origin parked AWAITING an approval is NOT driven past its pending
  ask; the note is queued and delivered only after the ask resolves.
  - verify: `TestFireDelivery_Scenario3_AwaitingOriginQueuedNotBypassed`
- AC3.6: After a compaction pass with a delivered note in the origin's tail
  window, the note survives verbatim AND still carries an intact `<<<UNTRUSTED`
  fence pair.
  - verify: `TestFireDelivery_Scenario3_DeliveredNoteSurvivesCompactionFenced`
- AC3.7: A fire delivered into an origin with a STRICTER posture does not loosen
  the origin's posture; a note claiming "the human approved X" does not change
  the origin's permission verdict for X.
  - verify: `TestFireDelivery_Scenario3_DeliveryDoesNotLoosenOriginPosture`
- AC4.1: A fire completing during an in-flight origin run does NOT interrupt or
  collide with that run; the note is queued.
  - verify: `TestFireDelivery_Scenario4_BusyOriginQueuesNotCollides`
- AC4.2: A queued note is drained and recorded exactly once at Step 2a BEFORE the
  next `BeginTurn`, never twice, never lost.
  - verify: `TestFireDelivery_Scenario4_DrainedExactlyOnceAcrossRuns`
- AC4.3: Multiple fires completing during one origin run accumulate; each pending
  note is drained (no coalescing).
  - verify: `TestFireDelivery_Scenario4_MultiplePendingAllDrained`
- AC4.4: A missing origin remains ownership-safe and preserves no-verifier
  compatibility. A child/fire-family non-deliverable origin drops the delivery
  with a WARN, never fails the fire, never delivers into another fire's chat;
  the result stays pull-able.
  - verify: `TestScheduleDeliveryMissingOriginPreservesNoVerifierCompatibility`
  - verify: `TestFireDelivery_Scenario4_NonDeliverableChildOriginDropsWithWarn`
