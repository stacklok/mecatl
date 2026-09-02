---
id: 28-automatic-reservation-reconciliation
title: Reconcile reservation and attempt creation crash boundaries
blocked_by: [27-durable-automatic-ledger]
status: done
branch: "plan-cloud-native-learning/28-automatic-reservation-reconciliation"
worktree: ".scratch/worktrees/28-automatic-reservation-reconciliation"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Implement reserve/create linkage and reconciliation keyed by deterministic attempt identity. Exercise crashes before and after reservation and attempt creation, response loss, timeout, expiry, reassignment, worker abandonment, and retained/reclaimed charging. Prove stale workers and retries cannot exceed global maxima or mint duplicate attempts.

## Acceptance criteria

- AC6.3: Distributed automatic reservation is tied to deterministic attempt identity. Failure-injection covers reserve/create linkage; crashes before and after each boundary; timeout, expiry, reassignment, and abandonment; retained versus reclaimed charge; and proves that retries never exceed the configured global maximum.
  - verify: `TestADR_0295_AutomaticReservationsReconcileWithoutExceedingGlobalMaximum`
