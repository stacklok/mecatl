---
id: 07-scheduled-placement-preparation
title: Scheduled placement preparation
blocked_by: [06-delegation-and-artifact-boundary]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Prepare schedules to retain durable owner, original placement scope, and exact `EnvironmentRef` revision (or explicit no-FS attenuation). Each fire must reauthorize and reattach for the captured owner and scope before creating a session or accessing a filesystem; drift and mismatch record a safe operator-visible failure. Decode transitional `Workspace` only to assert consistency while it remains durable, never as authority. Keep schedule protobuf and generated changes for the atomic public cutover.

Expected focus: schedule domain/store/fire preparation and tests; no protobuf regeneration.

## Acceptance criteria

- AC6.2: Each fire reauthorizes and reattaches with that owner and scope, not daemon
identity. Drift, unavailability, or mismatch records an operator-visible failure without
creating a session or accessing a filesystem.
  - verify: `TestInvariant_scheduled_placement_is_reauthorized_at_fire`
