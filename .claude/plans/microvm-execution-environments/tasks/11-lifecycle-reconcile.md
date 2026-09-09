---
id: 11-lifecycle-reconcile
title: Restart reattachment, deletion, and crash reconciliation
blocked_by: [07-workspace-rpc, 08-exec-rpc, 09-network-egress, 10-lifecycle-create]
status: done
branch: "plan-microvm-execution-environments/11-lifecycle-reconcile"
worktree: ".scratch/task-microvm-11"
issue: "532"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Complete the session-lifetime environment lifecycle: harness and daemon restart, exact generation reattachment, detach versus delete, retention destruction, durable cleanup/tombstones, and crash/race reconciliation. Update ADR 0027 resource and rehydration inventories for the real resources introduced.

## Acceptance criteria

- AC5.2: Restarting mecated reattaches the exact environment generation and preserves
  its worktree state without rebuilding the per-session engine unless the independent
  engine-rehydration rules require it.
  - verify: `TestMicroVMEnvironments_Scenario5_HarnessRestartReattachesExactGeneration`
- AC5.3: Restarting microvmd reconstructs or reconciles its durable registry and either
  reattaches the exact live environment or returns an actionable failed precondition;
  it never mints an empty replacement for the same ref.
  - verify: `TestMicroVMEnvironments_Scenario5_DaemonRestartNeverRecreatesEmptyEnvironment`
- AC5.5: CloseSession detaches process-local handles and leaves the environment
  reattachable; explicit deletion prevents new runs, destroys the VM/endpoints/children,
  and removes or preserves the worktree according to the dirty-state policy.
  - verify: `TestMicroVMEnvironments_Scenario5_DetachAndDeleteAreDistinct`
- AC5.6: Crashes at every lifecycle transition, PID reuse, stale sockets, disk-full,
  and two daemons racing one ref converge without deleting another environment or
  leaking quota indefinitely.
  - verify: `TestMicroVMEnvironments_Scenario5_ReconcilerConvergesAcrossCrashPoints`
