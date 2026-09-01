---
id: 26-automatic-ledger-contract
title: Define distributed automatic admission reservation semantics
blocked_by: [25-attempt-watch-deferred]
status: done
branch: "plan-cloud-native-learning/26-automatic-ledger-contract"
worktree: ".scratch/worktrees/26-automatic-ledger-contract"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Define the storage-neutral automatic admission ledger contract and shared conformance: deterministic attempt-linked reservation IDs, global/per-principal count and token windows, cooldown, deduplication, expiry/reassignment, retained versus reclaimed charge, and opaque CAS/fencing. Keep explicit hard admission policy distinct and do not add a second work queue.

## Acceptance criteria

No numbered acceptance criterion is owned by this domain task. It enables AC6.1, AC6.2, and AC6.3.
