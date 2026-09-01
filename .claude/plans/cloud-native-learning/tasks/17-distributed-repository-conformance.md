---
id: 17-distributed-repository-conformance
title: Prove distributed proposal and skill repository conformance
blocked_by: [16-learning-driver-composition]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add cross-client, cross-server-instance offline fixtures that exercise the distributed ProposalRepository and SkillRepository implementations through their shared conformance suites. Include opaque CAS, provenance/evaluation/activation, recovery, idempotent reopen, and partition isolation under concurrent callers.

## Acceptance criteria

- AC4.1: Distributed ProposalRepository and SkillRepository implementations satisfy their existing shared conformance suites, including opaque CAS, provenance, evaluation, activation, recovery, and partition isolation.
  - verify: `TestCloudNativeLearning_Scenario4_DistributedRepositoriesConform`
