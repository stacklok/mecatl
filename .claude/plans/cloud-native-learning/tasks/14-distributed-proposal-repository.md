---
id: 14-distributed-proposal-repository
title: Add distributed ProposalRepository client and server adapters
blocked_by: [13-explicit-worker-vertical-slice]
status: done
branch: "plan-cloud-native-learning/14-distributed-proposal-repository"
worktree: ".scratch/worktrees/14-distributed-proposal-repository"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add driver protocol messages and root client/server wrappers for the existing ProposalRepository lifecycle. Preserve opaque CAS, deterministic IDs, bounded provenance/evidence, decision history, recovery, and caller/project partition keys. Map only closed typed failures and run proposal conformance through the remote boundary.

## Acceptance criteria

No numbered acceptance criterion is owned by this repository task. It is the proposal half of AC4.1 and feeds task 17's combined proof.
