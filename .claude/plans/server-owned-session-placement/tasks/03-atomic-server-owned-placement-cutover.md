---
id: 03-atomic-server-owned-placement-cutover
title: Atomic server-owned placement cutover
blocked_by: [02-unify-environment-placement-identity]
status: in-progress
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

This intentionally atomic cutover has no test-green intermediate: exact `Bind`/`Reattach` conflicts with path-based public creation until every consumer changes together. Implement exact Bind/Reattach, remove `Session.Workspace` and every legacy adoption/path fallback, and update Harness, HTTP, schedules, and driver protobufs once. Cut over creation, clear, fork, discovery, delegation, artifacts, schedules, ACP, composition, and mecatui; regenerate bindings; update API snapshots and changelog; and add structural tests. Runtime adapter/operator-composition roots and ACP cwd assertions remain the narrow allowed path uses.

Strict TDD by vertical sub-commit is encouraged, but the final task branch must be green for every named acceptance criterion below.

## Acceptance criteria

- AC1.1: `CreateSession` accepts only the three tagged selector variants. Omission binds
the deployment default; no-FS attenuates it; an ID is reauthorized. Its response exposes
only `PlacementRef` and safe metadata.
  - verify: `TestADR_0280_CreateSessionPlacementProtocol`
- AC1.4: Harness protobuf/HTTP requests and responses, generated clients, and public
session inventory contain no filesystem path, cwd, workspace root, mount, or
path-shaped environment selector. Old path-bearing requests are unsupported.
  - verify: `TestADR_0280_PublicHarnessContractContainsNoFilesystemPaths`
- AC2.1: `Session` and its snapshot persist only `PlacementRef{Kind, ID, Revision}`;
they contain no `Workspace`, workspace path, live runner, credential, transport, or
process handle.
  - verify: `TestADR_0280_SessionAndSnapshotPersistOnlyPlacementRef`
- AC2.2: On load and at run entry, `Reattach` requires the exact persisted ref/revision
and returns a complete environment whose identity and bound runner share its namespace.
Missing resolver/provider, authorization drift, revision mismatch, nil workspace, or
identity mismatch is a failed precondition with no fallback.
  - verify: `TestInvariant_persisted_placement_reattaches_exactly`
- AC2.3: Legacy zero, path-bearing, or otherwise invalid placement snapshots are
rejected; there is no lazy stamping, migration sweep, inference, or alternate authority
path.
  - verify: `TestADR_0280_LegacyPlacementStateIsStructurallyUnsupported`
- AC2.4: Public projections, events, diagnostics, logs, and errors carry only the opaque
ref or safe metadata and never a physical path or secret backend locator.
  - verify: `TestInvariant_physical_placement_paths_never_cross_public_api`
- AC3.1: `ListCommands(session_id)` and `ListWorktrees(session_id)` authorize and
reattach the owned session before discovery; neither accepts a root or selector.
  - verify: `TestADR_0280_DiscoveryIsSessionScopedAndOwnerAuthorized`
- AC3.2: For a no-FS session, both discovery calls return the documented empty/
unsupported result without invoking a filesystem/default resolver, command source, or
worktree lister.
  - verify: `TestADR_0280_NoFSDiscoveryDoesNotInvokeFilesystemProviders`
- AC3.3: Worktree entries advertise only provider-owned opaque IDs and bounded safe
metadata. Creating or forking from one reauthorizes and atomically binds it; listing is
not a grant.
  - verify: `TestADR_0280_WorktreePlacementIsReauthorizedOnUse`
- AC3.4: A hidden or unknown session cannot probe another owner's placement or server
layout; mecatui switches by opaque ID and retains its selected session on failure.
  - verify: `TestServerOwnedSessionPlacement_Scenario3_OwnershipAndMecatuiSwitch`
- AC4.1: `ClearSession` is a new breaking RPC: request contains only `source_session_id`;
response returns the distinct successor ID and safe session metadata. It creates an
empty-history successor with the source's exact `PlacementRef`, owner, applicable mode,
provider/model/effort, limits, and permission posture.
  - verify: `TestADR_0280_ClearSessionRPCCreatesEmptyInheritedSuccessor`
- AC4.2: Clear authorizes the source, observes source-state/run-entry serialization and
lease rules, reattaches/reauthorizes the inherited exact placement, and is
non-destructive: failure persists no successor and changes neither source nor client
binding.
  - verify: `TestADR_0280_ClearSessionIsLeaseSafeAndNonDestructive`
- AC4.3: `ForkSession` copies valid history and either inherits the exact source ref or
uses one of the three selector variants. A changed placement/provider/model/effort is
fully authorized and atomically bound; failure leaves no partial successor.
  - verify: `TestInvariant_fork_placement_is_atomic_and_server_authorized`
- AC5.1: Teams derive placement from their owning session (or a direct API's authorized
selector); subagents and Parallel derive or fork the parent environment. No model-facing
or delegation call accepts an unrelated placement ID or path, and no-FS cannot upgrade.
  - verify: `TestInvariant_delegation_cannot_escalate_placement`
- AC5.2: Preserved-fork, delegation, and artifact handles are distinct typed handles,
never accepted as placement selectors. Every inspection consumer reauthorizes the owner
and original placement scope before access.
  - verify: `TestADR_0280_ArtifactHandlesCannotReplayAsPlacementSelectors`
- AC5.3: Delegation results, inspection surfaces, and model-visible summaries expose no
fork root or placement path.
  - verify: `TestADR_0280_DelegationObservabilityContainsNoPlacementPath`
- AC6.1: A schedule persists its durable owner principal, original placement scope, and
exact `PlacementRef` revision (or explicit no-FS attenuation), never “follow current
default” intent or a path.
  - verify: `TestADR_0280_SchedulePersistsOwnerScopeAndExactPlacementRef`
- AC6.2: Each fire reauthorizes and reattaches with that owner and scope, not daemon
identity. Drift, unavailability, or mismatch records an operator-visible failure without
creating a session or accessing a filesystem.
  - verify: `TestInvariant_scheduled_placement_is_reauthorized_at_fire`
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
- AC8.1: The complete path-surface inventory classifies public Harness/HTTP fields,
durable aggregate/snapshot/driver fields, runtime adapter/operator-composition paths,
and ACP cwd assertions. The first two are removed; only the last two are allowed.
  - verify: `TestADR_0280_PathSurfaceInventoryHasNoPublicOrDurableWorkspacePath`
