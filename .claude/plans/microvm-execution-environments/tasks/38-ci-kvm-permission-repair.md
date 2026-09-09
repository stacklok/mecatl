---
id: 38-ci-kvm-permission-repair
title: Enable KVM access on hosted Linux live runners
blocked_by: []
status: in-progress
branch: "plan-microvm-execution-environments/38-ci-kvm-permission-repair"
worktree: ".scratch/task-microvm-38"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# CI repair

PR #580 Linux amd64/arm64 live cells fail in preflight because `/dev/kvm` exists but is not writable by the hosted runner account. Configure KVM access explicitly and securely before the live gate (for example the standard udev/permission setup with sudo), then retain read/write preflight and the full production E2E. Do not skip or downgrade the live cells.

Verification: both workflow platform definitions contain the required KVM permission setup; action lint/tests pass; local task e2e remains green.
