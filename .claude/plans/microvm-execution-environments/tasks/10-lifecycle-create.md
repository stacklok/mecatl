---
id: 10-lifecycle-create
title: Transactional environment provisioning and fail-closed resolution
blocked_by: [02-profile-paths, 03-artifact-verification, 04-control-protocol, 05-worktree-git]
status: done
branch: "plan-microvm-execution-environments/10-lifecycle-create"
worktree: ".scratch/task-microvm-10"
issue: "532"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Add the internal provisioning/lifecycle seam and creation transaction across worktree, artifact, VM, protocol, environment ref/generation, and session persistence. Extend the existing resolver without widening tool.Environment into lifecycle management. Fail closed for every invalid ref.

## Acceptance criteria

- AC5.1: After a successful creation response, the persisted session, driver registry,
  prepared worktree, VM, guest endpoint, owner, profile, generation, and artifact
  identities agree; a failure before persistence rolls back or durably queues every
  provisional resource for cleanup.
  - verify: `TestMicroVMEnvironments_Scenario5_CreateTransactionIsAllOrReconciled`
- AC5.4: A nil resolver, unknown, foreign, stale, destroyed, incompatible, or
  generation-mismatched ref fails before tool execution with no local fallback.
  - verify: `TestInvariant_remote_environment_resolution_fails_closed`
