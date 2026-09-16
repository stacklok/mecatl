---
id: 14-child-merge-lifecycle
title: Conflict-aware child merge and durable cleanup
blocked_by: [13-child-environments]
status: done
branch: "plan-microvm-execution-environments/14-child-merge-lifecycle"
worktree: ".scratch/task-microvm-14"
issue: "534"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Implement exact fork-base tracking, additions/replacements/deletions merge, per-parent serialization, conflict preservation, and durable child cleanup/reconciliation. Preserve current cancellation, timeout, background drain, inspection, and resume behavior.

## Acceptance criteria

- AC7.4: Merge compares additions, replacements, and deletions against the immutable
  fork base, serializes per parent, applies no uncertain partial result, and preserves
  inspectable child state on conflict.
  - verify: `TestMicroVMEnvironments_Scenario7_MergeIsConflictAwareAndPreservesChild`
- AC7.5: Daemon/harness crash, cancellation, timeout, background drain, and resume do not
  cross-attach or leak child environments and retain the existing model-visible stop and
  resume semantics.
  - verify: `TestMicroVMEnvironments_Scenario7_ChildLifecycleSurvivesTerminationPaths`
