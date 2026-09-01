---
id: 39-backend-authoritative-attempt-time
title: Make attempt repository time backend-authoritative
blocked_by: [37-partitioned-attempt-quota-retention]
status: pending
branch: plan-cloud-native-learning/39-backend-authoritative-attempt-time
worktree: .scratch/worktrees/39-backend-authoritative-attempt-time
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: make `AttemptRepository` time backend-authoritative. Clients may provide claim
duration, expected version, and fence, but never `now` or an absolute expiry. The backend clock must
own discovery, acquire, renew, checkpoint, release, finalize, retry, abandonment, and retention
comparisons. Align the memory, file, and driver contracts and their conformance fixtures to this
boundary.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing attempt repository and worker tasks.

> AC2.3: Attempt lifecycle transitions are versioned and fenced so stale clients cannot overwrite a
> newer claim or terminal state.
>
> AC3.5: Crash after claim or after a downstream durable boundary is reconciled idempotently; a retry
> neither duplicates a proposal/skill nor reports an invented success.

Add offline adversarial skew tests across memory, file, and driver-backed repositories proving client
clock values cannot expire, retain, retry, or reclaim an attempt incorrectly.
