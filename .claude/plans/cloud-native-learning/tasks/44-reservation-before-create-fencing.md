---
id: 44-reservation-before-create-fencing
title: Fence reservations before durable attempt creation
blocked_by: [42-learning-repair-documentation-status]
status: done
branch: plan-cloud-native-learning/44-reservation-before-create-fencing
worktree: .scratch/worktrees/44-reservation-before-create-fencing
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: eliminate the reclaim-versus-late-create race by requiring durable retained charge
authority before `AttemptRepository.Create`. A stale creator whose held reservation was reclaimed
must be unable to create an attempt. Conservatively retain the charge after create failure until its
window expires. Update reconciliation to preserve this fencing and accounting behavior.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing automatic-ledger and reconciliation tasks.


> AC6.2: Concurrent replicas enforce one configured global automatic count/token budget, cooldown,
> and deduplication window without multiplying spend or durable attempts.

> AC6.3: Distributed automatic reservation is tied to deterministic attempt identity. Failure-injection
> covers reserve/create linkage; crashes before and after each boundary; timeout, expiry, reassignment,
