---
id: 07b-live-subscription-bridge
title: Live-subscription bridge — StreamSessionLive RPC + TUI wiring (embedded + remote)
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

The embedded mecatui dials its in-process server over a real gRPC UNIX socket
(`cmd/mecatui/embed/embed.go`:183-195 registers only HarnessService +
ScheduleService; the TUI connects as a gRPC client), so the in-process
`Service.Subscribe` (task 06) is UNREACHABLE from the TUI. The honest unified
bridge is a server-streaming `StreamSessionLive` RPC (originally Wave 3 / task
08) serving BOTH embedded and remote: one proto, one TUI consumption path. This
task absorbs the Wave-3 mechanism.

Build:
- contracts (`contracts/proto/mecatl/v1/`): a server-streaming
  `StreamSessionLive(session_id) returns (stream Event)` RPC on HarnessService +
  the delivery marker; run `task generate` (never hand-edit contracts/gen).
- adapters (`internal/adapter/server/grpc.go`): the handler publishes each of the
  session's run events (incl. server-initiated delivery runs) onto the stream,
  holding drain-to-discard + projection equivalence. The live-wire log-only skip
  is AMENDED for the delivery case ONLY: relay the delivery's `EvUserPrompt` (the
  note body) so the card has the note; `EvApproval`/`EvCompactionArchive` stay
  skipped. Back it by `Service.Subscribe`/`PublishSessionEvent` (task 06) — the
  gRPC handler is a thin transport over the in-process registry.
- TUI (`cmd/mecatui/client` + `main`): hold `StreamSessionLive` open for the
  active session (embedded AND remote — one path), translate events through the
  SAME `EventToMsg` (projection equivalence), render delivery cards (task 07).
  Reconnect replays missed deliveries via `StreamSessionEvents` (AC5.4).
- embed (`cmd/mecatui/embed`): no separate in-process bridge needed — the TUI
  dials the RPC like any client.
- mecademo: wire `deliverFireResult` as the scheduler's `SetDeliverFireResult`
  callback in composition (`internal/app/build.go`) so the e2e (create → fire →
  deliver → card) runs; `go run ./cmd/mecademo` demonstrates it.

Inventory the RPC's stream registry in ADR 0027 List 1 if it adds an outlives-a-
call resource; run `task docs`.

## Acceptance criteria

- AC5.1: With a mecatui connected to a session (embedded), a fire's delivered
  note renders in the live chat AS IT HAPPENS as a delivery card — no input.
  - verify: `TestFireDelivery_Scenario5_ConnectedTUIRendersDeliveryLive`
- AC5.5: `go run ./cmd/mecademo` demonstrates create → fire → in-chat delivery.
  - verify: demonstration — a mecademo flow captured in the done-gate run.
- AC6.1: A mecatui connected to a REMOTE mecated renders the delivered note live
  as a delivery card — parity with embedded, over the wire.
  - verify: `TestFireDelivery_Scenario6_RemoteTUIRendersDeliveryLive`
- AC6.2: The live subscription relays the delivery's `EvUserPrompt` on the wire
  (the card renders the SAME recorded note); the other two log-only kinds stay
  skipped.
  - verify: `TestFireDelivery_Scenario6_LiveSubscriptionRelaysDeliveryNote`
- AC6.3: A dead/disconnected client's subscription drains-to-discard without
  wedging the delivery run; the durable log records the tail.
  - verify: `TestFireDelivery_Scenario6_DeadClientDrainsWithoutWedging`
- AC6.4: The remote and embedded paths render the SAME delivery card for the SAME
  recorded note (one `EventToMsg` projection, two transports).
  - verify: `TestFireDelivery_Scenario6_TransportProjectionParity`
