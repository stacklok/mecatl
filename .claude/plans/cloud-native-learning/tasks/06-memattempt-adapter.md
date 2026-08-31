---
id: 06-memattempt-adapter
title: Implement the in-memory AttemptRepository reference adapter
blocked_by: [05-attempt-conformance]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Implement `engine/adapter/memattempt` as the small reference repository and run the full shared conformance suite against it, including deterministic clocks, claims, CAS, retention, and concurrency. Use it for later offline composition tests; do not wire production composition.

## Acceptance criteria

No numbered acceptance criterion is owned by this reference-adapter task. It enables the Scenario 2 and Scenario 3 named integration tests.
