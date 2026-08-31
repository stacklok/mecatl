---
id: 20-replica-catalog-hydration
title: Hydrate and invalidate learned skills across replicas
blocked_by: [19-catalog-convergence-protocol]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Wire caller-bound Skill-tool selection to reconcile the affected learned-skill partition against durable generation state before serving. Prove cold replica hydration and already-hydrated replica invalidation for activation, archive, rollback, and replacement. Serve wholly old or wholly new path-free metadata/body snapshots and never expose unauthorized partitions.

## Acceptance criteria

- AC4.3: After a durable Active, archive, rollback, or replacement transition on replica A, an authorized session on replica B hydrates or invalidates the affected partition before serving the Skill tool. Already-hydrated replicas converge and serve neither stale body nor stale metadata; an unauthorized or non-admitted partition sees nothing.
  - verify: `TestADR_0254_ReplicaHydrationConvergesAcrossReplacementAndRollback`
