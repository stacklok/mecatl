---
id: 39-ci-platform-reliability-repair
title: Make microVM platform CI viable and deterministic
blocked_by: []
status: in-progress
branch: "plan-microvm-execution-environments/39-ci-platform-reliability-repair"
worktree: ".scratch/task-microvm-39"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# CI follow-up

PR #580 failures:

- Linux amd64 live job leaves root/user-namespace-owned rootfs files that the next E2E preparation cannot clean; cleanup must be confined, privilege-aware, and deterministic.
- GitHub-hosted Linux arm64 has no `/dev/kvm`; do not pretend the hosted runner can execute libkrun. Keep hosted arm64 compile/static and move live arm64 to an explicit controlled self-hosted KVM cell that does not block ordinary PRs when no runner is configured.
- macOS compile/static test creates an overlong Unix socket path; use the repository's short Darwin socket strategy or an explicit short safe base.

Verification: local Linux KVM full E2E; workflow contract tests for hosted/static vs controlled/live cells; Darwin tests with path-length guard; action lint/test and full gates.
