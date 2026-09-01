---
id: 10-remove-durable-workspace
title: Remove durable workspace
blocked_by: [09-coordinated-public-contract-cutover]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

After every consumer has migrated, remove transitional durable `Workspace` state from Session, snapshots, stores, event sources, and schedules. Remove every zero/path fallback, legacy adoption API or state, inference, migration, and compatibility path. Durable state is the exact `EnvironmentRef{Kind, ID, Revision}` only; private runtime adapter roots remain outside durable and public surfaces. Update the exported engine API record and compatibility changelog, and add structural guards for the deleted shapes.

Expected focus: aggregate/snapshot/store/schedule cleanup, API compatibility records, changelog, and structural tests. Do not change public wire contracts except to remove code already superseded by task 09.

## Acceptance criteria

- AC2.1: `Session` and its snapshot persist only `PlacementRef{Kind, ID, Revision}`;
they contain no `Workspace`, workspace path, live runner, credential, transport, or
process handle.
  - verify: `TestADR_0280_SessionAndSnapshotPersistOnlyPlacementRef`
- AC2.3: Legacy zero, path-bearing, or otherwise invalid placement snapshots are
rejected; there is no lazy stamping, migration sweep, inference, or alternate authority
path.
  - verify: `TestADR_0280_LegacyPlacementStateIsStructurallyUnsupported`
- AC8.1: The complete path-surface inventory classifies public Harness/HTTP fields,
durable aggregate/snapshot/driver fields, runtime adapter/operator-composition paths,
and ACP cwd assertions. The first two are removed; only the last two are allowed.
  - verify: `TestADR_0280_PathSurfaceInventoryHasNoPublicOrDurableWorkspacePath`
