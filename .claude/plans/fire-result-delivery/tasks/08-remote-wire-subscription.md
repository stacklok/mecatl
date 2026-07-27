---
id: 08-remote-wire-subscription
title: Remote wire subscription — server-streaming RPC (Wave 3, the bigger change)
blocked_by: [07-tui-delivery-card]
status: done
branch: "plan-fire-result-delivery/07b-live-subscription-bridge"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

Wave 3 — the bigger change, last. Scenario 5/6's live card rides the embedded
in-process path; a REMOTE mecated (TUI connects over gRPC to a separate daemon)
has no in-process shortcut, so the per-session live event subscription must cross
the wire. This extends the SAME live-card behaviour to a remote origin — additive
to a working embedded product (Waves 1+2 already deliver the operator's e2e).

Build a per-session **server-streaming event subscription RPC** — a
`StreamSessionLive` sibling to the existing `StreamSessionEvents` replay feed
(`internal/adapter/server/grpc.go`:378) — that a connected TUI holds open for its
active session, onto which the Service publishes every run's events for that
session (including a server-initiated delivery run). This is a proto + wire
change: edit `contracts/proto/mecatl/v1/` and run `task generate` (never
hand-edit `contracts/gen/`).

Invariants (same as the in-process path):
- Projection equivalence: a delivery projects identically to a user-initiated run.
- Dead-client drain-to-discard: a disconnected subscriber does not wedge the
  delivery run; the durable log still records the tail.
- The live-wire log-only-kind skip is AMENDED for the delivery case ONLY: the
  subscription relays the delivery's `EvUserPrompt` (the note body) on the live
  wire — the replay feed already does (`grpc.go`:386) — while the OTHER two
  log-only kinds (`EvApproval`/`EvCompactionArchive`) stay skipped.
- TUI: when connected to a REMOTE server, hold the subscription open and render
  delivery cards from it — the SAME `EventToMsg` path as the embedded case (one
  projection, two transports).

## Acceptance criteria

- AC6.1: A mecatui connected to a REMOTE mecated renders a fire's delivered note
  in the live chat as it happens, as a delivery card — parity with the embedded
  AC5.1, over the wire.
  - verify: `TestFireDelivery_Scenario6_RemoteTUIRendersDeliveryLive`
- AC6.2: The live subscription relays the delivery's `EvUserPrompt` (the note
  body) on the wire — the delivery card renders the SAME recorded note, not a
  placeholder (the live-wire log-only skip is amended for the delivery case only;
  the other two log-only kinds stay skipped).
  - verify: `TestFireDelivery_Scenario6_LiveSubscriptionRelaysDeliveryNote`
- AC6.3: A dead/disconnected client's subscription drains-to-discard without
  wedging the delivery run (the durable log still records the tail; the fire and
  the recorded note are unaffected).
  - verify: `TestFireDelivery_Scenario6_DeadClientDrainsWithoutWedging`
- AC6.4: The remote and embedded paths render the SAME delivery card for the SAME
  recorded note (one `EventToMsg` projection; a delivery is indistinguishable
  across transports).
  - verify: `TestFireDelivery_Scenario6_TransportProjectionParity`
