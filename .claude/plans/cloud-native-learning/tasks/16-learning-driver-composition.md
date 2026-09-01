---
id: 16-learning-driver-composition
title: Negotiate and compose distributed learning repositories
blocked_by: [08-attempt-driver-repository, 14-distributed-proposal-repository, 15-distributed-skill-repository]
status: done
branch: "plan-cloud-native-learning/16-learning-driver-composition"
worktree: ".scratch/worktrees/16-learning-driver-composition"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Extend driver capability negotiation and composition so AttemptRepository, ProposalRepository, and SkillRepository are selected as one explicit distributed learning backend without silent local fallback. Reuse the Build-owned driver connection cache and close discipline. Keep raw workspace paths and unauthenticated identity out of the transport; task 24 owns full enforced-ownership startup behavior.

## Acceptance criteria

No numbered acceptance criterion is owned by this composition task. It enables AC4.1 and AC5.4.
