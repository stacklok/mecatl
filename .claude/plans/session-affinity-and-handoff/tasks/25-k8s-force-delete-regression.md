---
id: 25-k8s-force-delete-regression
title: Restore force-delete lease TTL exclusion
blocked_by: [24-integration-finalization]
status: done
branch: "plan-session-affinity-and-handoff/25-k8s-force-delete-regression"
worktree: ".scratch/task-session-affinity-25"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Integration regression brief

Investigate and fix the branch regression in `e2e/k8s/failover_test.go`: after a hard force-delete with no graceful shutdown/release, a survivor immediately acquires and returns 200 instead of remaining blocked until the 30s lease TTL. Main passes this contract. Preserve graceful-delete immediate handoff and all new mutation-capability/drain semantics. Add the smallest deterministic regression test at the appropriate layer and run the kind e2e if feasible.
