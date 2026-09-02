---
id: 20-replica-catalog-hydration
title: Hydrate and invalidate learned skills across replicas
blocked_by: [19-catalog-convergence-protocol]
status: done
branch: "plan-cloud-native-learning/20-replica-catalog-hydration"
worktree: ".scratch/worktrees/20-replica-catalog-hydration"
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Wire caller-bound Skill-tool selection to reconcile the affected learned-skill partition against durable generation state before serving. Prove cold replica hydration and already-hydrated replica invalidation for activation, archive, rollback, and replacement. Serve wholly old or wholly new path-free metadata/body snapshots and never expose unauthorized partitions.

## Acceptance criteria

- AC4.3: Catalog publication, hydration, and invalidation converge by authoritative per-partition monotonic generation after Active, archive, rollback, or replacement transitions. An authorized session on replica B serves a wholly old or wholly new partition snapshot; delayed old publish/invalidate cannot replace or revoke a newer generation. Instant claim-driven invalidation is not promised, and an unauthorized or non-admitted partition sees nothing.
  - verify: `TestADR_0295_ReplicaHydrationConvergesAcrossReplacementAndRollback`
