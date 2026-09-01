---
id: 09-coordinated-public-contract-cutover
title: Coordinated public contract cutover
blocked_by: [08-acp-and-driver-preparation]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

Make one atomic public-contract cutover across Harness, HTTP, schedules, and driver protobufs, then regenerate all bindings. Remove every public path-bearing request, response, inventory, event, error, selector, and compatibility path. Wire the prepared server binding, discovery, successor, scheduling, ACP, driver, delegation, and artifact services to the new contract. Add the breaking `ClearSession` RPC and direct Team wire path; update generated clients and mecatui client/UI in the same change. This is the only task that changes public wire contracts or runs protobuf generation.

Expected focus: Harness and driver protobufs, generated contracts, HTTP/gRPC handlers, schedule/driver mappings, mecatui, and integration tests. Do not retain aliases, legacy translation, or public path fallbacks.

## Acceptance criteria

- AC1.1: `CreateSession` accepts only the three tagged selector variants. Omission binds
the deployment default; no-FS attenuates it; an ID is reauthorized. Its response exposes
only `PlacementRef` and safe metadata.
  - verify: `TestADR_0280_CreateSessionPlacementProtocol`
- AC1.4: Harness protobuf/HTTP requests and responses, generated clients, and public
session inventory contain no filesystem path, cwd, workspace root, mount, or
path-shaped environment selector. Old path-bearing requests are unsupported.
  - verify: `TestADR_0280_PublicHarnessContractContainsNoFilesystemPaths`
- AC2.4: Public projections, events, diagnostics, logs, and errors carry only the opaque
ref or safe metadata and never a physical path or secret backend locator.
  - verify: `TestInvariant_physical_placement_paths_never_cross_public_api`
- AC3.1: `ListCommands(session_id)` and `ListWorktrees(session_id)` authorize and
reattach the owned session before discovery; neither accepts a root or selector.
  - verify: `TestADR_0280_DiscoveryIsSessionScopedAndOwnerAuthorized`
- AC3.4: A hidden or unknown session cannot probe another owner's placement or server
layout; mecatui switches by opaque ID and retains its selected session on failure.
  - verify: `TestServerOwnedSessionPlacement_Scenario3_OwnershipAndMecatuiSwitch`
- AC4.1: `ClearSession` is a new breaking RPC: request contains only `source_session_id`;
response returns the distinct successor ID and safe session metadata. It creates an
empty-history successor with the source's exact `PlacementRef`, owner, applicable mode,
provider/model/effort, limits, and permission posture.
  - verify: `TestADR_0280_ClearSessionRPCCreatesEmptyInheritedSuccessor`
- AC6.1: A schedule persists its durable owner principal, original placement scope, and
exact `PlacementRef` revision (or explicit no-FS attenuation), never “follow current
default” intent or a path.
  - verify: `TestADR_0280_SchedulePersistsOwnerScopeAndExactPlacementRef`
- AC7.1: Driver session/environment messages carry only exact opaque placement refs and
capabilities, not paths. Driver results are accepted only through exact `Reattach`.
  - verify: `TestADR_0280_DriverProtocolCarriesExactOpaquePlacementRef`
