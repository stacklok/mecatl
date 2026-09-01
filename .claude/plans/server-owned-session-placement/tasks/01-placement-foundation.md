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

Introduce the core `PlacementRef{Kind, ID, Revision}` and closed three-variant selector vocabulary, plus the composition/server placement-provider seam whose single `Bind` operation atomically authorizes and resolves an immutable provider record into a complete `tool.Environment`, exact ref, and bounded safe metadata. Implement the trusted local default and provider-owned worktree/remote extension points without a registry, signer, cache, path-derived public ID, or possession-as-authority shortcut. Reject hidden, stale, wrong-scope, unauthorized, unavailable, and revision-raced records before constructing an environment. Keep provider roots private to adapters/composition and make startup validate the configured default before serving. This is the foundation task; consumers must use this seam rather than reimplementing check-then-resolve logic.

Expected focus: `engine/session` placement values, `internal/adapter/server` placement interfaces/binder and focused fakes/tests, and `internal/app` default/provider composition. Avoid public protobuf migration, snapshots, discovery, successors, schedules, ACP, and documentation; later tasks own those surfaces.

## Acceptance criteria

- AC1.2: `Bind` atomically authorizes and resolves one immutable provider record/version;
a rebind or inventory revision between authorization and resolution fails closed before
filesystem access or persistence, never binding a mixed generation.
  - verify: `TestADR_0280_BindRejectsRebindBetweenAuthorizationAndResolution`
- AC1.3: Unknown, stale, wrong-scope, unauthorized, or unavailable IDs fail before
environment construction, trust evaluation, or persistence; hidden and absent IDs are
indistinguishable and never default.
  - verify: `TestInvariant_server_owned_placement_ids_fail_closed`
- AC8.2: Trusted composition configures the local default; worktree and remote providers
provide stable opaque IDs/revisions. Startup rejects invalid configuration before serving.
  - verify: `TestADR_0280_CompositionConfiguresProviderOwnedPlacements`
