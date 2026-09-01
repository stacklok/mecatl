---
id: 30-automatic-capability-claims
title: Gate global automatic-learning capability claims on ledger wiring
blocked_by: [29-weighted-attempt-lifecycle]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Make composition capability and posture reporting distinguish explicit durable learning from globally bounded automatic learning. Before the distributed ledger is wired, report ADR-0114 process-local limitations plainly; only advertise global count/token/cooldown/deduplication semantics when the durable ledger is selected and healthy.

## Acceptance criteria

- AC6.4: Documentation and capability reporting do not claim global automatic bounds until AC6.2 is wired; the earlier explicit-only slice says so plainly.
  - verify: `TestCloudNativeLearning_Scenario6_NoPrematureGlobalBoundClaim`
