---
id: 07-tui-delivery-card
title: TUI delivery card — render the live note distinctly (embedded e2e)
blocked_by: [06-session-event-subscription]
status: done
branch: "plan-fire-result-delivery/07-tui-delivery-card"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/fire-result-delivery
---

# Task brief

The visible product. The TUI holds the session event subscription (task 06) open
and renders the delivered note as a DISTINCT delivery card — a scheduled-task
affordance (icon) + the schedule name + the fenced outcome — NOT as a user-typed
prompt and NOT silently as the model's own text, so the operator can tell
provenance at a glance. The model's one-line acknowledgment (the delivery run's
assistant reply) renders normally after it.

Constraints:
- Reuse the EXISTING projection: a new `tea.Msg` + render mapped through
  `EventToMsg` (`cmd/mecatui/client/msgs.go`), drained via the shared
  `readEventLoop` (`cmd/mecatui/client/stream.go`) — no second translation path.
- The card renders the RECORDED note (the same fenced-untrusted content the
  engine recorded), never the fire's raw output.
- A TUI that reconnects AFTER a delivery sees the note via the existing
  `StreamSessionEvents` replay feed (which relays `EvUserPrompt`,
  `internal/adapter/server/grpc.go`:386) — a delivery is never lost to a
  disconnect.
- mecademo demonstrates the e2e: a schedule created in-chat fires and its result
  appears in the SAME session's transcript as a delivered note, no `sched--`
  session hunt.
- mecatui's `ui`/`theme`/`client` import no `engine/...` or `internal/...` and no
  proto directly beyond the relayed `Event` — respect that boundary (docs/tui.md).

Gates: `task test:golden` if you touch mecatui View/teatest goldens.

## Acceptance criteria

- AC5.1: With a mecatui connected to a session, a fire of that session's schedule
  renders the delivered note in the live chat AS IT HAPPENS — no operator input,
  no manual refresh — as a distinct delivery card naming the schedule.
  - verify: `TestFireDelivery_Scenario5_ConnectedTUIRendersDeliveryLive`
- AC5.2: The delivery card is visually distinct from a user-typed prompt AND from
  the model's own text (a scheduled-task affordance + the schedule name), so the
  operator can tell provenance at a glance.
  - verify: `TestFireDelivery_Scenario5_DeliveryCardDistinctProvenance`
- AC5.4: A TUI that reconnects AFTER a delivery sees the delivered note via the
  replay feed (the `StreamSessionEvents` path relays `EvUserPrompt`), so a
  delivery is never lost to a disconnect.
  - verify: `TestFireDelivery_Scenario5_ReconnectReplaySeesDelivery`
- AC5.5: `go run ./cmd/mecademo` demonstrates the e2e: a schedule created in-chat
  fires and its result appears in the SAME session's transcript as a delivered
  note, with no separate `sched--` session hunt.
  - verify: demonstration — a mecademo flow showing create → fire → in-chat
    delivery, captured in the plan's done-gate run.
