---
id: 13-child-environments
title: Direct-write and isolated child environment selection
blocked_by: [07-workspace-rpc, 08-exec-rpc, 11-lifecycle-reconcile]
status: done
branch: "plan-microvm-execution-environments/13-child-environments"
worktree: ".scratch/task-microvm-13"
issue: "534"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Route delegation by EnvironmentRef kind. Keep direct-write Subagent on the parent Environment and mint complete child VM/worktree Environments for isolated Subagent, Parallel, and Team paths. Preserve dispatcher serialization and existing child concurrency bounds.

## Acceptance criteria

- AC7.1: A direct-write Subagent uses the parent's Environment, so its filesystem and
  Bash mutations appear in the parent worktree and remain parent-mutate-serial.
  - verify: `TestMicroVMEnvironments_Scenario7_DirectWriteUsesParentEnvironment`
- AC7.2: A read-only Subagent, Parallel branch, or Team member receives a complete child
  Environment whose Workspace and runner share a child namespace and cannot mutate the
  parent before merge.
  - verify: `TestMicroVMEnvironments_Scenario7_IsolatedChildrenUseCompleteEnvironments`
- AC7.3: Concurrent branches share no writable worktree, Git metadata, guest endpoint,
  or environment generation; child creation is bounded by the existing delegation gates
  plus driver fork quotas.
  - verify: `TestMicroVMEnvironments_Scenario7_ConcurrentChildrenAreIsolatedAndBounded`
