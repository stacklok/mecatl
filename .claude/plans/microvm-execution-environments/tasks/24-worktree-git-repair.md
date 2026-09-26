---
id: 24-worktree-git-repair
title: Mount reconstructed Git metadata correctly and harden every host Git call
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/24-worktree-git-repair"
worktree: ".scratch/task-microvm-24"
issue: "529"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# Panel repair brief

Cross-confirmed Spec/Security blocker: reconstructed metadata is mounted at `/run/mecatl/git-metadata` while Git from `/workspace` still reads the original host-path `.git` file. Duplication blocker: retention Dirty/Remove Git calls bypass the hardened environment used by worktree preparation. Reuse review also found non-hermetic tests invoking ambient external diff.

Make every guest Git command resolve only the reconstructed guest-local metadata—mount it at `/workspace/.git` or set an unavoidable confined `GIT_DIR` contract. Preserve host-enforced read-only common objects. Extract one narrow hardened Git invoker used by prepare, dirty inspection, and cleanup; disable hooks, fsmonitor, pager, external diff, and ambient unsafe config. Make tests hermetic.

Protects AC3.1–AC3.3, AC6.2, AC8.1.

## Verification

- Real/fake guest `git -C /workspace status` and `rev-parse --git-dir` use guest-local metadata.
- Base/sibling refs/config/hooks remain unchanged.
- Ambient `diff.external`, fsmonitor, hooks, pager cannot execute in production or tests.
- Existing worktree/virtiofs tests plus lint/test pass.
