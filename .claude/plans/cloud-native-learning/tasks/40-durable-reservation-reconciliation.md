---
id: 40-durable-reservation-reconciliation
title: Reconcile expired durable automatic reservations
blocked_by: [37-partitioned-attempt-quota-retention, 39-backend-authoritative-attempt-time]
status: done
branch: plan-cloud-native-learning/40-automatic-reservation-discovery
worktree: .scratch/worktrees/40-automatic-reservation-discovery
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: add bounded, backend-time-authoritative discovery and cleanup for expired held
automatic reservations. A Build-owned reconciliation loop must join on shutdown, retain a reservation
linked to an attempt, reclaim one with no linked attempt, and record quota/retention consistently so
held orphans cannot fill the durable document. Do not replay admission during reconciliation.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing automatic-ledger and reconciliation tasks.

> AC6.2: Concurrent replicas enforce one configured global automatic count/token budget, cooldown,
> and deduplication window without multiplying spend or durable attempts.
>
> AC6.3: Distributed automatic reservation is tied to deterministic attempt identity. Failure-injection
> covers reserve/create linkage; crashes before and after each boundary; timeout, expiry, reassignment,
> and abandonment; retained versus reclaimed charge; and proves that retries never exceed the configured
> global maximum.

Add offline cross-Build crash proofs after reserve and after attempt creation, plus bounded cleanup
proofs for retained linked and reclaimed unlinked reservations without a second admission.
