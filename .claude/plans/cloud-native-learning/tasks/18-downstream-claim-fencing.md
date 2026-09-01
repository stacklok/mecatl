---
id: 18-downstream-claim-fencing
title: Fence every proposal skill catalog and finalize mutation by attempt claim
blocked_by: [17-distributed-repository-conformance]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Thread an opaque attempt/claim-generation guard through every downstream proposal create/link, skill create/evaluate/stage/activate/archive/rollback, catalog publish/invalidate, checkpoint, and finalization boundary. Validate the claim immediately before each mutation and make stale, expired, released, retried, or abandoned claims fail closed. Add adversarial two-worker failure injection across repositories and publication.

## Acceptance criteria

- AC2.3: Attempts permit only legal CAS transitions, and an expired or superseded worker claim cannot checkpoint, propose, mutate a skill, publish/invalidate a catalog, or finalize a successor.
  - verify: `TestADR_0254_AttemptFencedClaimGuardsEveryDownstreamBoundary`
- AC4.2: Concurrent replicas converge one proposal/skill lifecycle without lost transitions, duplicate activation, or cross-partition visibility. Failure injection proves that stale worker A cannot propose, mutate a skill, publish/invalidate a catalog, or finalize after worker B owns, retries, or abandons the attempt.
  - verify: `TestADR_0254_StaleClaimCannotMutateAnyDownstreamBoundary`
