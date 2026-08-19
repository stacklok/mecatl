---
id: 02-steer-supersede-contract
title: Steer supersede / cancel single-slot contract + drain echo
blocked_by: [01-engine-steer-inbox]
status: done
branch: "plan-steer-while-running/02-steer-supersede-contract"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
---

# Task brief

Build the single-slot supersedable contract on the steer inbox from task 01.
At most one steer is pending per run: a second `EnqueueSteer` supersedes the
pending one; `CancelSteer` retracts a pending one; the boundary drain commits
it. Outcomes are an enum (`accepted`/`superseded`/`retracted`/`none_pending`/
`too_late`), not booleans (AGENTS.md "a third provenance ⇒ extract an enum").
The drain emits `EvSteer` carrying the committed text (the authoritative
echo). Engine is authoritative; the client renders the echoed truth. All
transitions safe under concurrent access (run goroutine drains while a wire
handler enqueues) — run under `-race`. Offline (`mockllm`/`memfs`).

## Acceptance criteria

- AC2.1: At most one steer is pending per run; a second `steer` supersedes the first, and only the superseding text is ever drained.
  - verify: `TestSteer_SingleSlotSupersede`
- AC2.2: `steer_cancel` on a pending steer retracts it; the run drains nothing and the next turn sees no injected message.
  - verify: `TestSteer_CancelRetractsPending`
- AC2.3: A `steer` that arrives *after* the boundary drained (or after the run went terminal) reports `too_late` / not-live — never silently drained into a finished turn.
  - verify: `TestSteer_TooLateNotDrained`
- AC2.4: The drain emits `EvSteer` carrying the committed text; on a supersede race the echo reflects the version actually drained.
  - verify: `TestSteer_DrainEmitsCommittedEcho`
- AC2.5: Supersede/cancel/drain are safe under concurrent access (the run goroutine drains while a wire handler enqueues) — no data race, no double-drain.
  - verify: `TestSteer_InboxConcurrentSafe` (run under `-race`)
- AC2.6: The recorded steer, the streamed `EvSteer` echo, and the message the model sees are the same text — the recorded == streamed == model-view invariant holds.
  - verify: `TestInvariant_recorded_streamed_model_view`
