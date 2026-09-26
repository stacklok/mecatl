---
id: 12-multi-session
title: Same-repository concurrency and resource admission
blocked_by: [11-lifecycle-reconcile]
status: done
branch: "plan-microvm-execution-environments/12-multi-session"
worktree: ".scratch/task-microvm-12"
issue: "533"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Prove and harden concurrent local sessions from one repository. Each session gets independent worktree, Git metadata, VM, endpoint, generation, and quotas while sharing only verified immutable caches/object data. Add mecatui projections for each resolved session.

## Acceptance criteria

- AC6.1: Two concurrent session creates from one repository succeed with distinct
  worktree roots, branches, indexes, environments, endpoints, and writable files; each
  UI displays its own resolved paths.
  - verify: `TestMicroVMEnvironments_Scenario6_SameRepoSessionsUseDistinctWorktreesAndVMs`
- AC6.2: File and Git mutations in session A do not alter session B's working files,
  index, refs, config, hooks, or guest-local metadata; both can read the shared
  host-enforced read-only object store.
  - verify: `TestMicroVMEnvironments_Scenario6_SiblingSessionsCannotMutateEachOther`
- AC6.3: VM/worktree/image-pull admission observes per-user and deployment limits for
  booting/active VMs, CPU, RAM, disk/inodes, execs, forks, pulls, and boot rate; a
  rejection is stable and actionable.
  - verify: `TestMicroVMEnvironments_Scenario6_ResourceAdmissionIsBounded`
- AC6.4: Concurrent cache, worktree, endpoint, and cleanup operations do not collide
  when session IDs or repository names share prefixes or hostile characters.
  - verify: `TestMicroVMEnvironments_Scenario6_ConcurrentResourceNamesAreOpaqueAndConfined`
