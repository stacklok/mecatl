---
id: 24-learning-driver-ownership
title: Fail closed on unsupported multi-tenant learning drivers
blocked_by: [23-attempt-control-api, 16-learning-driver-composition]
status: done
branch: "plan-cloud-native-learning/24-learning-driver-ownership-repair"
worktree: ".scratch/worktrees/24-learning-driver-ownership-repair"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Apply ADR-0213's fail-closed staging rule to the shipped raw Attempt/Proposal/Skill driver services. They do not yet have workload-authentication middleware, a private durable owner registry, or separately authenticated maintenance RPCs, so application ownership enforcement must reject every configured remote learning store regardless of self-advertised capability. Permit a capability-complete remote repository set only as explicitly trusted single-tenant infrastructure when `OwnershipEnforced=false`, and carry opaque project namespace digests rather than workspace paths.

## Acceptance criteria

- AC5.4: The shipped raw learning repository RPCs do not claim workload-authenticated ownership enforcement. With `OwnershipEnforced=true`, configuring `--learning-store-url` fails closed until ADR-0213 middleware, a private owner registry, and separated maintenance RPCs exist, regardless of a driver's self-advertised `enforced` value. Only a capability-complete driver explicitly trusted as single-tenant infrastructure may compose when `OwnershipEnforced=false`; proposal and skill project namespaces are opaque rather than raw workspace paths.
  - verify: `TestADR_0254_LearningDriversEnforceOwnershipOrFailClosed`
