---
id: 13-explicit-worker-vertical-slice
title: Wire the explicit durable learning worker vertical slice
blocked_by: [08-attempt-driver-repository, 12-attempt-worker-reconciliation]
status: done
branch: "plan-cloud-native-learning/13-explicit-worker-vertical-slice"
worktree: ".scratch/worktrees/13-explicit-worker-vertical-slice"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Wire opt-in composition from genuine explicit admission through durable attempt creation, process replacement, claim, evidence reconstruction, reflection/materialization, proposal/skill linkage, and terminal state. Own worker startup/cleanup in Build and leave the engine loop storage-agnostic. Preserve byte-identical ordinary runs and zero attempt repository/worker allocation when learning is off or unwired.

## Acceptance criteria

- AC3.1: An explicit imperative request returns a durable queued attempt and, across Build/process replacement, reaches a terminal attempt linked to its authorized proposal and/or skill when downstream capabilities are wired.
  - verify: `TestCloudNativeLearning_Scenario3_ExplicitProcedureAttemptSurvivesRestart`
- AC3.6: With learning unwired or off, engine composition and ordinary runs remain byte-identical and allocate no attempt repository, worker, or durable attempt.
  - verify: `TestCloudNativeLearning_Scenario3_UnwiredLearningIsByteIdentical`
