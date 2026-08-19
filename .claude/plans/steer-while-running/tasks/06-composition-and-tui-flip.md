---
id: 06-composition-and-tui-flip
title: Composition gate + capability flip + mecatui steer mode
blocked_by: [05-wire-grpc-steer-frame]
status: done
branch: "plan-steer-while-running/06-composition-and-tui-flip"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
---

# Task brief

The composition gate + the only client-facing piece. The steer inbox is armed
by a posture/run-level knob resolved in `internal/app` (posture ladder), wired
through `app.Build` into the engine deps AND the `ServerCapabilities.steer` bit;
default ON. mecatui reads the `steer` capability: when present, `enter` mid-run
sends a `steer` frame and the queue card reflects the authoritative echoed/acked
state (pending → sent → too-late-promoted); when absent, keep the #228 local
merge-queue byte-identical. The client-side merge (join staged lines with a
blank line) happens BEFORE send so the engine sees one frame; `↑` edits the
combined message; the engine's single-slot supersede is the replace path for an
already-sent-but-un-drained steer (do NOT conflate the two). Golden files shift
for the card → `task test:golden`.

## Acceptance criteria

- AC6.1: With steer enabled (default), the capability is advertised and a mid-run input reaches the model without a separate follow-up run.
  - verify: `TestSteer_EnabledByDefaultEndToEnd`
- AC6.2: With steer disabled, the engine inbox is inert and the TUI falls back to the client-side terminal queue unchanged.
  - verify: `TestSteer_DisabledFallsBackToLocalQueue`
- AC6.3: The TUI renders the merged pending message as a single item, reflects the echoed/acked state honestly (pending vs sent vs promoted-after-race), and `↑` edits the combined message.
  - verify: `TestSteer_TUIRendersAuthoritativeState` (+ `task test:golden` for the card)
- AC6.4: `go run ./cmd/mecademo` still prints a full offline session (no behavioural regression with steer unused).
  - verify: demonstration — `go run ./cmd/mecademo`
