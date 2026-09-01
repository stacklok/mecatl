---
id: 09-driver-and-acp-boundary
title: Opaque driver reattachment and ACP consistency assertions
blocked_by: [04-exact-reattachment]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Migrate driver session/environment contracts and mappings from workspace paths/legacy environment refs to exact opaque placement refs plus capabilities; accept a driver result only through exact `Reattach`, including revision and identity/namespace checks. Update ACP `session/new` and `session/load` so server state binds or reattaches first. ACP's protocol-required cwd remains only a local consistency assertion against the trusted composition-configured placement: mismatch fails, and cwd never becomes a selector, ref, authority source, or environment constructor input. Move ACP command discovery to the owned session-id seam and scrub all ACP session/event/error projections of physical roots while preserving editor-buffer runtime behavior inside the private adapter.

Expected focus: driver protobuf session-store messages and regenerated driver bindings, `internal/adapter/grpcdriver`, `internal/adapter/acp/agent.go` and private workspace setup/tests. Avoid Harness protobuf, central reattach changes, discovery UI, successors, delegation, schedules, and docs.

## Acceptance criteria

- AC7.1: Driver session/environment messages carry only exact opaque placement refs and
capabilities, not paths. Driver results are accepted only through exact `Reattach`.
  - verify: `TestADR_0280_DriverProtocolCarriesExactOpaquePlacementRef`
- AC7.2: ACP `session/new` and `session/load` bind or reattach from server state before
access. A required ACP cwd is checked only for consistency with the configured placement;
a mismatch is rejected and ACP cannot select or construct placement authority.
  - verify: `TestADR_0280_ACPBindAndLoadAssertConfiguredPlacement`
- AC7.3: ACP-visible sessions, discovery, errors, and events obey the same no-path
projection rule.
  - verify: `TestServerOwnedSessionPlacement_Scenario7_ACPProjectsNoPhysicalPaths`
