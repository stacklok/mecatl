---
id: 37-partitioned-attempt-quota-retention
title: Bound durable attempt quota and retention per partition
blocked_by: [31-documentation-generated-integration]
status: done
branch: plan-cloud-native-learning/37-partitioned-attempt-quota-retention
worktree: .scratch/worktrees/37-partitioned-attempt-quota-retention
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Repair brief

Repair finding: enforce durable attempt quota and retention independently per caller partition so
one tenant cannot fill a shared attempt document or consume another partition's retention capacity.
Apply quota, retention, deletion, and any cleanup accounting under the authoritative partition
boundary while preserving claim safety and non-disclosure.

## Protected acceptance criteria

Repair proof only; AC ownership remains with the existing durable-attempt and attempt-control tasks.

> AC2.6: Retention/deletion are caller-partitioned, CAS-safe where applicable, and cannot delete a claimed nonterminal attempt.
>
> - verify: `TestCloudNativeLearning_Scenario2_RetentionAndDeletionRespectClaimsAndPartition`

> AC5.1: The owner can get and page through attempt projections, while another caller cannot infer existence, metadata, proposal IDs, skill IDs, counts, cursors, timing, or diagnostics. This non-disclosure holds before locks/signals/diagnostics and pagination/count/cursor construction; foreign and missing requests have the same absence-style result.
>
> - verify: `TestADR_0259_AttemptControlsAreNonDisclosingBeforeSideEffects`

Add offline multi-partition saturation and retention tests proving one partition cannot deny another
partition admission, cleanup capacity, or non-disclosing controls.
