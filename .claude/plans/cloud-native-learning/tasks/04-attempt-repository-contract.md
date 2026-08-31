---
id: 04-attempt-repository-contract
title: Add the AttemptRepository CAS and fenced-claim contract
blocked_by: [03-attempt-domain-values]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Define the storage-neutral `learning.AttemptRepository` interface, opaque versions, legal transition table, expiring claim acquire/renew/release semantics, checkpoints, terminal finalization, typed conflicts, and idempotent create contract. Keep the engine loop storage-agnostic. This task establishes the contract later downstream fencing tasks consume; it does not yet claim complete end-to-end fencing.

## Acceptance criteria

No numbered acceptance criterion is owned by this contract task. It enables AC2.1, AC2.3, AC2.6, and the Scenario 3 worker tasks without duplicating their ownership.
