---
id: 44-actionlint-kvm-shell-repair
title: Fix KVM workflow shell portability lint
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/44-actionlint-kvm-shell-repair"
worktree: ".scratch/task-microvm-44"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# CI repair

Fix actionlint/ShellCheck SC2155 and SC2016 in the KVM group identity step without changing the proven `sg kvm` execution behavior. Separate assignment from export so lookup failure propagates; pass/log the resolved GID into the inner shell without relying on accidental outer expansion.

Verification: actionlint, action tests, local KVM E2E, full gates.
