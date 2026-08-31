---
id: 12-attempt-worker-reconciliation
title: Drive claimed attempts through distinct terminal outcomes
blocked_by: [11-fenced-reflection-boundary]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Implement the composition-owned explicit attempt worker lifecycle: claim, evidence checkpoint, reflection outcome, downstream durable checkpoint, and terminal finalize. Model abstention/no-candidate as its own terminal and preserve separate safe evidence, evaluation, and publication failures. Reconcile each crash boundary using deterministic IDs and opaque CAS so retries never duplicate or invent success.

## Acceptance criteria

- AC3.4: `abstained`/`no_candidate` is a valid distinct terminal, separate from evidence failure, evaluation rejection, and publication failure.
  - verify: `TestADR_0254_AbstentionIsASeparateTerminalOutcome`
- AC3.5: Crash after claim or after a downstream durable boundary is reconciled idempotently; a retry neither duplicates a proposal/skill nor reports an invented success.
  - verify: `TestADR_0254_AttemptReconciliationIsIdempotent`
