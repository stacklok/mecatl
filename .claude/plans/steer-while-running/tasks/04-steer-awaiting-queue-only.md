---
id: 04-steer-awaiting-queue-only
title: Steer while awaiting an ask — queue-only, drain on resume
blocked_by: [01-engine-steer-inbox]
status: done
branch: "plan-steer-while-running/04-steer-awaiting-queue-only"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
---

# Task brief

Loop behaviour: while a run is parked `awaiting` on a permission or plan ask,
the loop is suspended in `PauseForApproval` and is not iterating `runLoop`, so
a steer cannot be drained until the ask resolves. The inbox holds the pending
steer across the parked state; `driveFromAwaiting`/`resumeFromAwaiting`
re-enters `runLoop`, whose Step 2a drain picks it up at the resumed run's
first turn boundary. The ask still requires an explicit verdict — the steer is
purely additive input (the "yes-and"/"no-and" case), never a verdict and never
a bypass. See the awaiting run-entry seam (AGENTS.md) and ADR-0069.

## Acceptance criteria

- AC4.1: A steer submitted while a run is parked `awaiting` is held, not rejected and not delivered early.
  - verify: `TestSteer_AwaitingAskIsHeld`
- AC4.2: On verdict-driven resume, the held steer is drained at the resumed run's first turn boundary and replayed to the model.
  - verify: `TestSteer_AwaitingResumeDrains`
- AC4.3: The held steer does not resolve, modify, or bypass the pending ask — the ask still requires an explicit verdict.
  - verify: `TestSteer_AskStillRequiresVerdict`
