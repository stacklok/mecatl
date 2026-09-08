---
id: 32-durable-work-discovery
title: Storage-neutral durable work discovery and retry loop
blocked_by: [31-documentation-generated-integration]
status: done
branch: "plan-cloud-native-learning/32-durable-work-discovery"
worktree: ".scratch/worktrees/32-durable-work-discovery"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: the remote `AttemptRepository` needs storage-neutral discovery and claiming of
queued work and expired claims. Drive a joined worker loop that discovers work admitted after
startup, renews its claim while work is long-running, cancels work when renewal is lost, and joins
on shutdown. A replacement replica must recover an expired live claim, and a coordinator rejection
must release or otherwise make work discoverable rather than strand it queued. Keep repository
state authoritative; do not add a process-local queue or receipt authority.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing Scenario 3 tasks.

> AC3.1: An explicit imperative request returns a durable queued attempt and, across Build/process replacement, reaches a terminal attempt linked to its authorized proposal and/or skill when downstream capabilities are wired.
>
> - verify: `TestCloudNativeLearning_Scenario3_ExplicitProcedureAttemptSurvivesRestart`

> AC3.5: Crash after claim or after a downstream durable boundary is reconciled idempotently; a retry neither duplicates a proposal/skill nor reports an invented success.
>
> - verify: `TestADR_0259_AttemptReconciliationIsIdempotent`

Add focused offline regression proofs for remote queued discovery after startup, claim-expiry
replacement, renewal-loss cancellation, coordinator rejection recovery, and joined shutdown.
