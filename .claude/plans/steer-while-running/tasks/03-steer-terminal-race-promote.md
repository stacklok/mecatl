---
id: 03-steer-terminal-race-promote
title: Lost terminal race → auto-promote to a fresh follow-up run
blocked_by: [01-engine-steer-inbox]
status: done
branch: "plan-steer-while-running/03-steer-terminal-race-promote"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
---

# Task brief

Service/loop behaviour: when a steer arrives for a session whose run is
already terminal (the user's "still running" belief lagged the real state),
auto-promote the steer into a fresh follow-up run through the existing hardened
run-entry funnel — `loadAndReopen` + the lease + recover-if-terminal
(`internal/adapter/server/service.go` `StartRunContent`) — never silently
dropping the text. Promotion reuses the reopen/recover discipline; it never
starts a run on an unrepaired terminal session. A live run → enqueue to its
inbox (task 01). The `too_late`/not-live signal is what the service promotes
on. Note: task 05 owns the wire frame; this task owns the Service routing
decision helper (`IsLive`/state → enqueue vs promote) and its tests, callable
from the wire handler.

## Acceptance criteria

- AC3.1: A steer arriving on a live run is enqueued to that run's inbox (not promoted).
  - verify: `TestSteer_LiveRunEnqueues`
- AC3.2: A steer arriving after the run went terminal is promoted to a fresh follow-up run that drives through `loadAndReopen` (recovering the terminal state) and is *not* silently dropped.
  - verify: `TestSteer_TerminalRacePromotes`
- AC3.3: Promotion reuses the run-entry funnel (recover-if-completed / interrupt-if-cancelled / recover-if-failed) — it never starts a run on an unrepaired terminal session.
  - verify: `TestSteer_PromotionUsesRunEntryFunnel`
