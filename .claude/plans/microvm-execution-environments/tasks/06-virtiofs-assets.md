---
id: 06-virtiofs-assets
title: RW worktree and read-only asset virtio-fs mounts
blocked_by: [03-artifact-verification, 05-worktree-git]
status: done
branch: "plan-microvm-execution-environments/06-virtiofs-assets"
worktree: ".scratch/task-microvm-06"
issue: "529"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Wire the prepared worktree RW at guest `/workspace`, adopt libkrun's host-enforced read-only virtio-fs API through go-microvm for Git objects and materialized skill assets, and prove bidirectional host/guest visibility. Keep mount inputs explicit and confined.

## Acceptance criteria

- AC3.2: A guest Write/Edit or Bash mutation under `/workspace` is immediately visible
  in the prepared host worktree, and a host edit to that worktree is visible to the
  guest.
  - verify: `TestMicroVMEnvironments_Scenario3_WorktreeIsBidirectionallyVisible`
- AC3.6: An out-of-repository skill asset reaches the guest only through explicit
  materialization or a host-enforced read-only mount; Bash cannot discover an arbitrary
  user home/config directory through that capability.
  - verify: `TestMicroVMEnvironments_Scenario3_SkillAssetsAreExplicitAndReadOnly`
