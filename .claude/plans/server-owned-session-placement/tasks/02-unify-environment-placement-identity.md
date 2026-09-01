---
id: 02-unify-environment-placement-identity
title: Unify environment and placement identity
blocked_by: [01-placement-foundation]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Add `Revision` to the existing `session.EnvironmentRef` and make it the sole runtime and durable placement identity. Remove task 01's duplicate `PlacementRef` from the binder and provider seams, replacing it with exact `EnvironmentRef` equality. Add ref-aware child seams without widening or removing existing public `Workspace` shapes: engine child constructors obtain identity exclusively from `tool.Environment.Ref()` and never import the server binder. Keep `Session.Workspace` transitional until the later coordinated consumer migration removes it. This enabling task must leave public protobuf contracts and wire adapters unchanged.

Expected focus: `engine/session` environment identity, `engine/tool.Environment`, placement binder/provider seams, and additive engine child-environment seams. Do not remove `Workspace`, migrate snapshots, change public contracts, or alter driver/ACP/schedule wire messages.

## Acceptance criteria

This enabling task owns no standalone acceptance criterion and is required to make later tests implementable.
