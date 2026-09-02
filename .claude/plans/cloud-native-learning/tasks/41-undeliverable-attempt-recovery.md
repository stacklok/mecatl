---
id: 41-undeliverable-attempt-recovery
title: Bound recovery of undeliverable durable attempts
blocked_by: [37-partitioned-attempt-quota-retention, 38-durable-evidence-execution-unification]
status: done
branch: plan-cloud-native-learning/41-undeliverable-attempt-recovery
worktree: .scratch/worktrees/41-undeliverable-attempt-recovery
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: claim a durable attempt before source or provider setup. Finalize missing or deleted
source evidence as `evidence_unavailable`; persist bounded retry/backoff for transient setup or
provider failures and finalize `retry_exhausted` when the bound is reached. Eliminate the one-second
infinite retry loop while preserving claim fencing and restart recovery.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing evidence and worker tasks.

> AC3.3: Missing, gap-marked, compacted-without-recoverable-archive, unauthorized, or digest/run-mismatched
> evidence terminally fails closed with a safe code and creates no proposal/skill mutation.
>
> AC3.5: Crash after claim or after a downstream durable boundary is reconciled idempotently; a retry
> neither duplicates a proposal/skill nor reports an invented success.

Add offline failure-injection proofs for claim-before-setup, missing-source finalization, persisted
transient retry/backoff across restart, and bounded `retry_exhausted` terminalization.
