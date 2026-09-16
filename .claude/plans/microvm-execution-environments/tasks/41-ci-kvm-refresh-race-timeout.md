---
id: 41-ci-kvm-refresh-race-timeout
title: Refresh KVM access after preparation and budget race CI
blocked_by: []
status: done
branch: "plan-microvm-execution-environments/41-ci-kvm-refresh-race-timeout"
worktree: ".scratch/task-microvm-41"
issue: "535"
retries: 0
last_error: ""
accumulator: acc/microvm-execution-environments
---

# CI repair

PR #580 at `2163d139`:

- hosted Linux amd64 proves `/dev/kvm` open immediately after early ACL setup, but artifact/rootfs preparation later invalidates access; refresh and independently open-probe KVM immediately after preparation and immediately before the live test process. Keep access least-privileged and diagnose inode/owner/mode changes.
- normal race job now exceeds its legacy 15-minute timeout after adding the microVM module; raise the bounded CI timeout enough for root+engine+OIDC+microVM+providers rather than cancelling a healthy run.

Verification: workflow ordering tests pin prepare → KVM refresh/open probe → live test; local repeated prepare+probe+E2E; race timeout contract; action/full gates.
