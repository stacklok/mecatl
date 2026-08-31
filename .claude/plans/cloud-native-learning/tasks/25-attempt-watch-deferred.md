---
id: 25-attempt-watch-deferred
title: Guard the explicit deferral of attempt watch
blocked_by: [24-learning-driver-ownership]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add a structural/source guard and capability assertions proving this plan introduces no attempt-watch endpoint, cursor, process-local substitute, watch envelope, or EventLog-derived feed. Document in code comments where needed that ADR-0250 watches session events only and future advisory attempt notifications require a separate durable attempt-change-feed ADR.

## Acceptance criteria

- AC5.5: Attempt watch is deferred. ADR-0250 session `EventLog` watch is explicitly not an attempt-watch feed; no endpoint, cursor, process-local substitute, or watch envelope is introduced in this plan.
  - verify: none — deferred to a separately specified durable attempt-change feed
