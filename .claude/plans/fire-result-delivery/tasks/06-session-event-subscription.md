---
id: 06-session-event-subscription
title: Per-session live event subscription seam (in-process, embedded)
blocked_by: [05-delivery-run-and-drain]
status: done
branch: "plan-fire-result-delivery/06-session-event-subscription"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

The mecatui is a thin relay with no server→client push channel: it renders only
the events of a `Converse` stream it opened (one Converse = one run), so a
server-initiated delivery run has no client draining it and never reaches the
screen. Because mecatui hosts mecated IN THE SAME PROCESS (`cmd/mecatui/embed`),
the delivery run CAN surface in-process with no wire change.

Build the **per-session live event subscription** seam (ADR 0075 decision #5):
the Service exposes a per-session live event stream a connected client holds
open — a merged stream over the session's runs, DISTINCT from a single run's
`Run.Events()` (the `relayEventsSSE` merged-stream precedent,
`internal/adapter/server/http.go`). The loop stays storage-agnostic — it emits;
the relay publishes. The delivery run's events are published onto the origin
session's live subscription.

Contract:
- Projection equivalence: a delivery's events flow through the SAME channel a
  user-initiated run's would, so `EventToMsg` projects them identically.
- The delivered note carries a delivery marker (so the client can render it
  distinctly — task 07) AND its `EvUserPrompt` body is available to the
  subscriber (the note the card renders is the recorded note).
- Dead-client drain-to-discard: a subscriber that goes away does not wedge the
  delivery run; the durable log still records the tail.
- The seam is in-process for the embedded server (Wave 2); the gRPC/SSE wire
  transport is Wave 3 (task 08). Keep the loop free of any adapter/proto import.
- Inventory the subscription in ADR 0027 List 1 (a Service-owned outlives-a-call
  resource).

Offline tests: drive a delivery in-process over the embedded composition and
assert the subscriber receives the note event.

## Acceptance criteria

- AC5.1 (the subscription half): with a mecatui connected to a session, a fire's
  delivered note is published onto the session's live subscription AS IT HAPPENS
  — no operator input. (The card render is task 07's AC5.1.)
  - verify: `TestFireDelivery_Scenario5_ConnectedTUIRendersDeliveryLive`
- AC5.3: The delivered note published to the subscriber is the SAME
  fenced-untrusted content the engine recorded (no second render path, no
  un-fenced echo) — the subscriber receives the recorded note, not the fire's raw
  output.
  - verify: `TestFireDelivery_Scenario5_TUIRendersRecordedNoteNotRaw`
- AC6.3 (the in-process drain-discipline half): a dead/disconnected subscriber
  drains-to-discard without wedging the delivery run; the durable log still
  records the tail. (The wire-transport half is task 08's AC6.3.)
  - verify: `TestFireDelivery_Scenario6_DeadClientDrainsWithoutWedging`
