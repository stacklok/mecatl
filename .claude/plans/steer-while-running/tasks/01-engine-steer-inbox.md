---
id: 01-engine-steer-inbox
title: Engine steer inbox + Step 2a turn-boundary drain
blocked_by: []
status: done
branch: "plan-steer-while-running/01-engine-steer-inbox"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
---

# Task brief

Implement the engine core of steer-while-running: a `Run`-scoped, in-memory
steer inbox on the agent loop, drained at the existing Step 2a turn-boundary
seam in `Engine.runLoop` (`engine/agent/loop.go`) — the same provider-legal
point `injectBackgroundNotice`/`drainPendingDelivery` use (history always ends
on a user prompt / tool result / nudge, never inside a tool_use pair). The
drained steer is recorded via `recordContinuation` (`RecordUserPrompt` + the
log-only `EvUserPrompt`), so it replays cleanly, survives
`session.ValidateToolPairing` and compaction, and rehydrates under ADR-0038.
This task is the injection + recording path only; the supersede/cancel
contract, the wire frame, and composition are later tasks. Offline tests via
`engine/adapter/mockllm` + `engine/adapter/memfs`.

## Acceptance criteria

- AC1.1: A steer enqueued while a run is mid-flight is recorded as an ordinary user message at the next turn boundary, appears in `Conversation.Messages`, and is replayed to the model on the following turn.
  - verify: `TestSteer_InjectedAtTurnBoundary`
- AC1.2: The steer is recorded via `recordContinuation`, so the durable log captures it (log-only `EvUserPrompt`) and `eventsource.Fold` reconstructs the user turn across rehydration.
  - verify: `TestSteer_RecordedAndRehydrated`
- AC1.3: The steer is appended *after* the settled tool results, never inside a `tool_use` pair — `session.ValidateToolPairing` holds on the post-injection history.
  - verify: `TestSteer_PreservesToolPairing`
- AC1.4: The injected conversation keeps the message prefix byte-stable within the run (fragments assembled once, conversation appended), so the steer costs no prompt-cache rebuild beyond normal history growth.
  - verify: `TestSteer_KeepsPromptPrefixByteStable`
- AC1.5: A run with an empty inbox drives exactly as before (the drain is a no-op) — no behavioural change when steer is unused.
  - verify: `TestSteer_EmptyInboxNoOp`
- AC1.6: A steer drained at a boundary where compaction also fires is still replayed **verbatim** to the model on the following turn — the compaction recent-user back-snap keeps the just-drained user message out of the summarized head.
  - verify: `TestSteer_SurvivesCompactionBoundary`
- AC1.7: A pending steer that trips the turn-limit / token-budget brake at the next boundary is recorded (durable history) and the run terminates `StopMaxTurns`/`StopBudget` normally; the recorded steer is addressed by the *next* run — it is never silently dropped and never bypasses `BeginTurn`'s bound.
  - verify: `TestSteer_BrakeTerminalKeepsRecorded`
- AC1.8: A pending (un-drained) steer is **best-effort, in-memory, and lost with the run** on a process crash/session `Abandon` — the explicit honest contract; it is NOT persisted across restart.
  - verify: `TestSteer_PendingSteerLostOnRestart`
