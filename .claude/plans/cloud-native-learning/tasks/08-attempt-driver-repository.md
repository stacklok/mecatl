---
id: 08-attempt-driver-repository
title: Add driver-backed AttemptRepository transport
blocked_by: [05-attempt-conformance]
status: done
branch: "plan-cloud-native-learning/08-attempt-driver-repository"
worktree: ".scratch/worktrees/08-attempt-driver-repository"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Extend the streaming-HTTP-compatible deployment's gRPC driver protocol with an AttemptRepository service plus root client/server wrappers. Carry only bounded domain metadata, opaque versions, claim generations, and typed safe errors. Follow generated-contract workflow and existing driver conformance patterns; ownership enforcement is added in task 24.

## Acceptance criteria

No numbered acceptance criterion is owned by this transport task. It enables AC4.1 and AC5.4 without claiming ownership enforcement prematurely.
