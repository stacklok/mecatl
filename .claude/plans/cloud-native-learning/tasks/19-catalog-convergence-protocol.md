---
id: 19-catalog-convergence-protocol
title: Define durable learned-skill catalog generation convergence
blocked_by: [18-downstream-claim-fencing]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/cloud-native-learning
---

# Task brief

Define the authoritative per-caller/project-partition monotonic generation read from durable
repository state. Catalogs are derived caches: publish, hydrate, and invalidate compare that
generation so a delayed old operation cannot replace or revoke a newer generation. Preserve
external-skill precedence and path-free bundles, and fail closed only the uncertain partition. Do
not claim instant invalidation or guard catalog updates with an attempt claim; do not introduce
attempt watch or reuse ADR-0250 session watch as workflow state.

## Acceptance criteria

No numbered acceptance criterion is owned by this protocol task. It enables AC4.3 and AC4.4.
