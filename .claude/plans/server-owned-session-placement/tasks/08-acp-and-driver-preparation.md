---
id: 08-acp-and-driver-preparation
title: ACP and driver preparation
blocked_by: [07-scheduled-placement-preparation]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Prepare ACP `session/new` and `session/load` to bind or exactly reattach server state before access. ACP cwd remains only a consistency assertion against the configured placement and can neither select nor construct authority. Prepare driver helpers to carry and consume exact opaque environment refs through reattachment, while retaining existing protobufs and generated bindings for task 09. Ensure ACP-visible projections have no physical placement paths.

Expected focus: ACP boundary and driver helper seams with tests; no protobuf regeneration.

## Acceptance criteria

- AC7.2: ACP `session/new` and `session/load` bind or reattach from server state before
access. A required ACP cwd is checked only for consistency with the configured placement;
a mismatch is rejected and ACP cannot select or construct placement authority.
  - verify: `TestADR_0280_ACPBindAndLoadAssertConfiguredPlacement`
- AC7.3: ACP-visible sessions, discovery, errors, and events obey the same no-path
projection rule.
  - verify: `TestServerOwnedSessionPlacement_Scenario7_ACPProjectsNoPhysicalPaths`
