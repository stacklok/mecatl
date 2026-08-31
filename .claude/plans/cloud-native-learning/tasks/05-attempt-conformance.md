---
id: 05-attempt-conformance
title: Build shared AttemptRepository lifecycle conformance
blocked_by: [04-attempt-repository-contract]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Create an importable shared conformance suite for deterministic idempotent creation, opaque CAS, legal states, claim expiry/supersession, partition isolation, pagination, reconciliation checkpoints, retention, and deletion. Make the suite reusable by memory, local durable, and driver-backed implementations. Pin claimed-nonterminal deletion refusal and partition-safe cleanup here.

## Acceptance criteria

- AC2.6: Retention/deletion are caller-partitioned, CAS-safe where applicable, and cannot delete a claimed nonterminal attempt.
  - verify: `TestCloudNativeLearning_Scenario2_RetentionAndDeletionRespectClaimsAndPartition`
