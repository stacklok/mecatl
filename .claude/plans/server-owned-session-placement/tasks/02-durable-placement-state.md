---
id: 02-durable-placement-state
title: Placement-only session aggregate and snapshot persistence
blocked_by: [01-placement-foundation]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Replace the aggregate's path-bearing `Workspace` and legacy `EnvironmentRef` identity with the exact non-zero `PlacementRef` from the foundation. Change session construction/restoration so invalid, zero, path-bearing legacy state is structurally rejected rather than inferred or lazily stamped. Migrate `sessnap`, event-source metadata/folding, session metadata stores, conformance fixtures, and public engine API records so durable state contains only the placement ref and never live workspace/runner/credential/transport/process handles. Preserve the runtime `tool.Environment` affinity seam while separating its private workspace root from durable identity. Update every existing session-construction caller to obtain a placement through the task-01 binder; the legacy public wire may remain temporarily visible until task 03, but it must not be promoted into durable authority. Adapt server/app creation and legacy-adoption call sites as necessary so the repository compiles and all tests remain green without path-to-placement inference. Update engine compatibility records and changelog for intentional exported API changes.

Expected focus: `engine/session`, `engine/adapter/sessnap`, `engine/adapter/eventsource`, `engine/port/store.go`, reference stores/conformance, plus the minimum `internal/adapter/server` and `internal/app` creation-path changes required to migrate all constructor callers to `Bind`; update `engine/api/*.txt` and `engine/CHANGELOG.md`. Do not change Harness protobuf shapes, discovery, successors, schedules, ACP, or user documentation; later tasks own those surfaces.

## Acceptance criteria

- AC2.1: `Session` and its snapshot persist only `PlacementRef{Kind, ID, Revision}`;
they contain no `Workspace`, workspace path, live runner, credential, transport, or
process handle.
  - verify: `TestADR_0280_SessionAndSnapshotPersistOnlyPlacementRef`
- AC2.3: Legacy zero, path-bearing, or otherwise invalid placement snapshots are
rejected; there is no lazy stamping, migration sweep, inference, or alternate authority
path.
  - verify: `TestADR_0280_LegacyPlacementStateIsStructurallyUnsupported`
