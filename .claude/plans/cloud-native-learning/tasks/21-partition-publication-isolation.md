---
id: 21-partition-publication-isolation
title: Quarantine uncertain learned-skill partitions by generation
blocked_by: [20-replica-catalog-hydration]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Harden the convergence path so repository/publication uncertainty clears or blocks only the affected caller/project partition. Compare durable generations under the publication gate so an older failed hydration cannot revoke a newer active generation. Add concurrent failure injection proving unrelated partitions and external skills remain available.

## Acceptance criteria

- AC4.4: Publication/hydration uncertainty fail-closes only the affected partition and cannot revoke a newer durable active generation.
  - verify: `TestADR_0254_LearnedSkillPartitionPublicationIsolation`
