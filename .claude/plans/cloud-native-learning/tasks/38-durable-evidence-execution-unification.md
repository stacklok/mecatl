---
id: 38-durable-evidence-execution-unification
title: Unify durable attempt execution on authoritative evidence
blocked_by: [37-partitioned-attempt-quota-retention]
status: done
branch: plan-cloud-native-learning/38-durable-evidence-execution-unification
worktree: .scratch/worktrees/38-durable-evidence-execution-unification
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: execute both initially admitted and restarted durable attempts only from completed,
authoritative `SessionStore`/`EventLog` evidence reconstructed by `learningEvidenceLoader` and its
fenced projection. Remove or bypass the coordinator's retained raw `learning.Input` path for durable
execution. When admission precedes durable terminal-event persistence, bounded discovery must retry
incomplete source evidence instead of prematurely terminalizing the attempt. Keep any process-local
coordinator role limited to explicit synchronous/off legacy reflection, never durable workflow
authority.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing admission, evidence, and worker tasks.

> AC3.2: Durable attempt admission records only the authorized source identity and immutable attempt
> metadata needed for recovery; it does not retain untrusted raw execution input as workflow authority.
>
> AC3.3: Missing, gap-marked, compacted-without-recoverable-archive, unauthorized, or digest/run-mismatched
> evidence terminally fails closed with a safe code and creates no proposal/skill mutation.
>
> AC3.5: Crash after claim or after a downstream durable boundary is reconciled idempotently; a retry
> neither duplicates a proposal/skill nor reports an invented success.
>
> AC6.1: Automatic learning admission remains durable and bounded across process replacement.

Add focused offline proofs that initial and restarted execution produce the same fenced evidence
projection and that a just-admitted, not-yet-terminal source is retried within a bound before
`evidence_unavailable` is finalized.
