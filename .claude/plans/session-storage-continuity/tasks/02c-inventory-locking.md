---
id: 02c-inventory-locking
title: Inventory lock decomposition and reconciliation
blocked_by: [02b-bounded-pagination]
status: done
branch: "plan-session-storage-continuity/02c-inventory-locking"
worktree: ""
issue: "587"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Finish indexed inventory by decomposing the store-wide lock: catalog/rebuild work cannot block unrelated session Save/Load/EventLog.Append/ToolCall, while every same-family snapshot/sidecar/delete/migration mutation uses the existing cross-process family identity. Route stale-session reconciliation and retention discovery through the cheap catalog path without weakening lease/liveness behavior.

## Acceptance criteria

- AC2.4: A blocked inventory or catalog rebuild does not delay Save, Load, EventLog.Append, or ToolCall for a different session; Save, Delete, migration promotion/removal, EventLog.Append, and ToolCall for the same family coordinate under one cross-process mutation identity so snapshot-last deletion or migration cannot race a sidecar append.
  - verify: `TestSessionStorageContinuity_Scenario2_UnrelatedMutationNotBlocked`, `TestSessionStorageContinuity_Scenario2_SameFamilyMutationSerialized`
