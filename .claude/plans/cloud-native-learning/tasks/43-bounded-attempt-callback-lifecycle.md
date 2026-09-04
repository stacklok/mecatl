---
id: 43-bounded-attempt-callback-lifecycle
title: Bound attempt callback lifecycle and recovery
blocked_by: [42-learning-repair-documentation-status]
status: done
branch: plan-cloud-native-learning/43-bounded-attempt-callback-lifecycle
worktree: .scratch/worktrees/43-bounded-attempt-callback-lifecycle
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: make Build-owned recovery run every attempt callback under the configured bounded
context, covering prepare, evidence, reflection, and publish. A callback timeout must persist a
transient retry/backoff and eventually terminalize as `retry_exhausted`; it must not leave an
unbounded callback running. Claim renewal stops when the callback is no longer active. `Built.Close`
remains bounded and cooperative, and joins the recovery lifecycle.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing evidence and worker tasks.

> AC3.5: Crash after claim or after a downstream durable boundary is reconciled idempotently; a retry
> neither duplicates a proposal/skill nor reports an invented success.

Add offline blocked-callback timeout and shutdown/join proofs, including timeout persistence, retry
