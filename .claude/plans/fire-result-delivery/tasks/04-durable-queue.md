---
id: 04-durable-queue
title: Durable per-session pending-delivery queue (ADR 0027 List 1+2)
blocked_by: [01-spec-field-and-validation]
status: done
branch: "plan-fire-result-delivery/04-durable-queue"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

The pending-delivery queue is a new DURABLE per-session structure keyed on the
SESSION, not the per-Run `noticed`/`delivered` registry (which dies with its
run). It survives restart (persist-in-snapshot — an ADR 0027 List 1 resource-inventory
row AND a List 2 rehydrate-fidelity decision). The fire path enqueues to it; the
loop's turn-boundary drain (task 05) and the run-entry funnel dequeue from it.

Design constraints:
- Keyed on the origin session id; holds the rendered note text (from task 03) +
  a monotonic per-session sequence for the exactly-once ledger.
- Durable: backed by the session store / a sidecar (the same durability the
  session snapshot has) so a process restart does not lose pending notes. Follow
  the existing persistence patterns (jsonlstore sidecar precedent, `.events.jsonl`).
  A store with no persistence degrades honestly (in-memory, byte-identical to a
  no-delivery path for the memstore default).
- Exactly-once ledger survives the run it was queued during: a note queued in
  run N drains in run N+1 if run N ends first (the ledger is session-scoped).
- Bounded backlog: a cap drops the OLDEST pending note with a WARN rather than
  growing unboundedly on an overloaded origin.
- Add the ADR 0027 List 1 + List 2 inventory rows (docs/adr/0027-cloud-native.md)
  in THIS branch (the inventory discipline). Run `task docs` for the markdown.

Use the reference adapters (memstore / jsonlstore over a temp dir) — never a
mock-framework mock of a port. The queue is a composition/adapter structure, not
a domain type.

## Acceptance criteria

- AC4.2 (ledger half): the exactly-once ledger survives the end of the run a note
  was queued during — a note queued in run N drains in run N+1 if run N ends
  first. (The drain-side recording is task 05's AC4.2; THIS task owns the durable
  session-scoped ledger that makes it possible.)
  - verify: `TestFireDelivery_Scenario4_DrainedExactlyOnceAcrossRuns`
- AC4.3 (backlog half): multiple pending notes accumulate; a bounded backlog cap
  drops the oldest with a WARN rather than growing unboundedly. (The drain-side
  "each is drained, no coalescing" is task 05's AC4.3.)
  - verify: `TestFireDelivery_Scenario4_MultiplePendingAllDrained`
- AC4.5: The pending-delivery queue is durable: a process restart with notes
  still pending drains them on the origin's next run-entry (the
  persist-in-snapshot List 2 decision), it does not lose them.
  - verify: `TestFireDelivery_Scenario4_PendingQueueSurvivesRestart`
