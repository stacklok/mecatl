---
id: 05-worktree-git
title: Exact session worktree capture and guest Git reconstruction
blocked_by: [01-module-contract]
status: done
branch: "plan-microvm-execution-environments/05-worktree-git"
worktree: ".scratch/task-microvm-05"
issue: "529"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Task brief

Prepare one hardened session branch/worktree and exact source-state capture. Reuse or extract Brood Box's linked-worktree validation and guest-local Git metadata reconstruction, including the common object-store alternates design. This task prepares/mount-describes paths but leaves generic Workspace RPC to task 07.

## Acceptance criteria

- AC3.1: The prepared worktree contains the source's committed files and the full
  declared staged/unstaged/untracked content fidelity; a race or unsupported state
  that prevents exact capture aborts creation instead of silently starting clean.
  - verify: `TestMicroVMEnvironments_Scenario3_SourceStateCaptureIsExactOrFails`
- AC3.3: Guest Git operates with reconstructed local metadata and the common object
  store mounted host-enforced read-only; malicious `.git`/`commondir`, symlink, or
  escape inputs cannot expose another host path.
  - verify: `TestMicroVMEnvironments_Scenario3_WorktreeGitMetadataIsConfined`
