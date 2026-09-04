---
id: 09-durable-attempt-admission
title: Bind admitted completions to durable RunID-backed attempts
blocked_by: [06-memattempt-adapter, 07-durable-attempt-store]
status: done
branch: "plan-cloud-native-learning/09-durable-attempt-admission"
worktree: ".scratch/worktrees/09-durable-attempt-admission"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Replace queued-receipt authority at the service/composition admission boundary with durable attempt creation. Require a non-zero durable ADR-0249 RunID, exact source session and canonical digest binding, and successful create before reporting queued. Duplicate admissions must return the deterministic existing attempt. Keep skipped/non-admitted status and metrics immediate and content-free, with no repository write.

## Acceptance criteria

- AC2.1: `queued` is returned only after landed ADR-0249 supplies a non-zero durable `RunID` and durable create succeeds; absent, zero, or non-durable RunID refuses admission before queuing and creates no attempt. A reload from a second process finds the same deterministic attempt in `queued`, `running`, or a terminal state; duplicate admission converges to that attempt.
  - verify: `TestADR_0259_QueuedAttemptRequiresDurableRunIDAndIsIdempotent`
- AC2.5: A skipped or non-admitted completion produces immediate status plus content-free metrics but creates no attempt record.
  - verify: `TestCloudNativeLearning_Scenario2_NonAdmittedWorkIsNotDurable`
