---
id: 03-server-binding-and-exact-reattachment
title: Server binding and exact reattachment
blocked_by: [02-unify-environment-placement-identity]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Bind and persist the exact `EnvironmentRef` for every new session, then make load and run entry reattach that exact ref through the server-owned provider seam. A reattached complete environment must match the persisted ref exactly and its bound runner must share the environment namespace. During the transitional `Session.Workspace` period, retain it only as an equality assertion against the reattached environment; it must never supply authority or a fallback. Keep all public contracts unchanged and add no protobuf work.

Expected focus: server creation, load, run-entry, persistence, and exact-reattachment tests. Do not migrate discovery, successors, delegation, schedules, ACP, driver wire types, or public projections.

## Acceptance criteria

- AC2.2: On load and at run entry, `Reattach` requires the exact persisted ref/revision
and returns a complete environment whose identity and bound runner share its namespace.
Missing resolver/provider, authorization drift, revision mismatch, nil workspace, or
identity mismatch is a failed precondition with no fallback.
  - verify: `TestInvariant_persisted_placement_reattaches_exactly`
