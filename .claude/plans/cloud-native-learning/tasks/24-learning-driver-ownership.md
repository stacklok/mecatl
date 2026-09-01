---
id: 24-learning-driver-ownership
title: Enforce workload-authenticated ownership in learning drivers
blocked_by: [23-attempt-control-api, 16-learning-driver-composition]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Apply ADR-0213 to Attempt/Proposal/Skill driver services: accept caller claims only from authenticated mecatl workload peers, separate ordinary caller RPCs from infrastructure worker/retention RPCs, use private durable owner bindings, and carry opaque project namespace digests rather than workspace paths. Negotiate enforced ownership explicitly and make configured startup fail closed when the driver cannot enforce it.

## Acceptance criteria

- AC5.4: Driver-backed learning enforces workload-authenticated claims; caller and infrastructure RPCs are separated; project namespaces are opaque rather than raw workspace paths; and startup fails closed when the driver cannot enforce ownership.
  - verify: `TestADR_0254_LearningDriversEnforceOwnershipOrFailClosed`
