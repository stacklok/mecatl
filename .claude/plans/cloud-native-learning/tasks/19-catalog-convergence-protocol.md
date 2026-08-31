---
id: 19-catalog-convergence-protocol
title: Define durable learned-skill catalog generation convergence
blocked_by: [18-downstream-claim-fencing]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Add the smallest durable generation/change protocol needed for replica-safe Active, archive, rollback, and replacement convergence. Bind generations to caller/project partitions and authoritative repository state, preserve external-skill precedence and path-free bundles, and ensure catalog updates remain guarded by the current attempt claim. Do not introduce attempt watch or reuse ADR-0250 session watch as workflow state.

## Acceptance criteria

No numbered acceptance criterion is owned by this protocol task. It enables AC4.3 and AC4.4.
