---
id: 29-weighted-attempt-lifecycle
title: Route weighted admission through the durable attempt worker
blocked_by: [28-automatic-reservation-reconciliation]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Replace the process-local automatic coordinator/controller authority with the same deterministic AttemptRepository lifecycle used by explicit learning. Reserve globally before durable create, then claim, reconstruct evidence, and apply downstream fencing through the existing worker. Preserve weighted scoring and explicit hard admission's distinct bypass rules; remove any second queue or receipt authority.

## Acceptance criteria

- AC6.1: Automatic weighted admission creates the same deterministic durable attempt lifecycle as explicit admission and cannot bypass queue capacity, evidence reconstruction, or downstream authority.
  - verify: `TestCloudNativeLearning_Scenario6_WeightedAdmissionUsesAttemptLifecycle`
