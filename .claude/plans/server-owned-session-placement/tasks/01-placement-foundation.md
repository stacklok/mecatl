---
id: 01-placement-foundation
title: Provider-owned placement protocol and atomic binding
blocked_by: []
status: done
branch: "plan-server-owned-session-placement/01-placement-foundation"
worktree: ".scratch/worktrees/01-placement-foundation"
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Introduce the core `EnvironmentRef{Kind, ID, Revision}` and the composition/server
placement-provider seam whose single `Bind` operation resolves an immutable provider
snapshot into a complete `tool.Environment`, exact private ref, and bounded safe metadata.
Implement and validate the trusted local deployment default plus provider extension points
without making private roots public. This foundation task owns provider-snapshot atomicity
and composition startup validation; Task 03 owns the revised public default/no-FS creation
surface and source-scoped ephemeral worktree-selector authorization.

Expected focus: `engine/session` placement values, `internal/adapter/server` placement
interfaces/binder and focused fakes/tests, and `internal/app` default/provider composition.
Avoid public protobuf migration, snapshots, discovery, successors, schedules, ACP, and
documentation; later tasks own those surfaces.

## Acceptance criteria

- AC1.2: `Bind` constructs the complete environment and exact private ref from one
immutable provider snapshot; an authorization/resolution or inventory revision race fails
closed before persistence or filesystem access, never binding a mixed generation.
  - verify: `TestADR_0280_BindRejectsRebindBetweenAuthorizationAndResolution`
- AC8.2: Trusted composition configures and validates the local deployment default and
placement providers before serving; no stable public placement-ID registry is required.
  - verify: `TestADR_0280_CompositionConfiguresProviderOwnedPlacements`
