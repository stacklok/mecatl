---
id: 03-atomic-server-owned-placement-cutover
title: Atomic server-owned placement cutover
blocked_by: [02-unify-environment-placement-identity]
status: done
branch: "plan-server-owned-session-placement/03-atomic-server-owned-placement-cutover"
worktree: ".scratch/worktrees/03-atomic-server-owned-placement-cutover"
issue: ""
retries: 0
last_error: ""
accumulator: acc/server-owned-session-placement
---

# Task brief

This intentionally atomic cutover has no test-green intermediate: exact private
`EnvironmentRef` Bind/Reattach conflicts with path-based public creation until every
consumer changes together. Remove `Session.Workspace` without removing the private root
from snapshots or trusted driver storage; cut public Harness/HTTP/client surfaces to
omitted-default or explicit no-FS creation and path-free display metadata. Add owned-source
`ListWorktrees` selectors, selector-aware Clear/Fork, and the Build-owned random HMAC-SHA256
key: tokens are caller/source scoped, matched in constant time against one freshly
enumerated provider snapshot, never decoded, mapped, or persisted, and require relisting
after restart. Resolve schedule selectors before persistence, preserve exact reattachment,
keep ACP cwd assertion-only, remove legacy adoption, and cut over discovery, delegation,
artifacts, schedules, ACP, composition, mecatui, contracts, generated bindings, API
snapshots, changelog, and structural tests together. Trusted runtime, snapshot, driver
storage, operator-diagnostic, and composition paths remain private implementation data.

Strict TDD by vertical sub-commit is encouraged, but the final task branch must be green
for every named acceptance criterion below.

## Acceptance criteria

- AC1.1: `CreateSession` accepts only omitted deployment default or explicit no-FS
attenuation; it accepts no general placement ID or selector. Its response exposes only a
session ID and bounded display metadata, never the exact private `EnvironmentRef`.
  - verify: `TestADR_0280_CreateSessionAcceptsOnlyDefaultOrNoFS`
- AC1.3: Unknown, stale, wrong-owner, wrong-source, unauthorized, or unavailable selectors
fail before environment construction, trust evaluation, or persistence; hidden and absent
choices are indistinguishable and never fall back to a default.
  - verify: `TestADR_0280_ScopedWorktreeSelectorsFailClosed`
- AC1.4: Harness protobuf/HTTP requests and responses, generated clients, and public session
inventory contain no filesystem path, cwd, workspace root, mount, exact private
`EnvironmentRef`, or path-shaped selector. Old path-bearing requests are unsupported.
  - verify: `TestADR_0280_PublicHarnessContractContainsNoFilesystemPaths`
- AC2.1: `Session` and its snapshot persist only
`EnvironmentRef{Kind, ID, Revision}` as runtime identity and contain no duplicate
`Workspace`, live runner, credential, transport, or process handle. A local private ID may
identify its configured physical root.
  - verify: `TestADR_0280_SessionAndSnapshotPersistOnlyEnvironmentRef`
- AC2.2: On load and at run entry, `Reattach` requires the exact persisted ref/revision and
returns a complete environment whose identity and bound runner share its namespace.
Missing resolver/provider, authorization drift, revision mismatch, nil workspace, or
identity mismatch is a failed precondition with no fallback.
  - verify: `TestInvariant_persisted_placement_reattaches_exactly`
- AC2.3: Zero refs, duplicate legacy `Workspace` state, and otherwise invalid snapshots are
rejected; there is no lazy stamping, migration sweep, workspace inference, adoption, or
alternate authority path.
  - verify: `TestADR_0280_LegacyDuplicatePlacementStateIsUnsupported`
- AC2.4: Public projections, events, client-visible logs, and errors expose only bounded
display metadata and never an exact private ref, physical path, or secret backend locator.
Trusted operator diagnostics may retain physical roots.
  - verify: `TestInvariant_physical_placement_paths_never_cross_public_api`
- AC3.1: `ListCommands(session_id)` and `ListWorktrees(session_id)` authorize and reattach
the owned session before discovery; neither accepts a root or client-provided selector.
Worktree entries contain an opaque selector plus bounded display-only kind/label/branch/
revision metadata.
  - verify: `TestADR_0280_DiscoveryIsSessionScopedAndOwnerAuthorized`
- AC3.2: For a no-FS session, both discovery calls return the documented empty/unsupported
result without invoking a filesystem/default resolver, command source, or worktree lister.
  - verify: `TestADR_0280_NoFSDiscoveryDoesNotInvokeFilesystemProviders`
- AC3.3: A local selector is an HMAC-SHA256 digest scoped to caller and source session. On
use, the server re-enumerates currently eligible source-placement worktrees and
constant-time matches in one provider snapshot; it never decodes the token or consults a
registry/map.
  - verify: `TestADR_0280_WorktreeSelectorIsScopedAndMatchedAgainstCurrentInventory`
- AC3.4: A hidden/unknown session cannot probe another owner's placement or server layout.
Selectors expire on process restart, so mecatui relists and retains its selected session
on relist or switch failure.
  - verify: `TestServerOwnedSessionPlacement_Scenario3_OwnershipRelistAndSafeSwitch`
- AC4.1: `ClearSession(source_session_id, optional selector)` creates a distinct
empty-history successor. Ordinary `/clear` omits the selector and inherits the exact
source placement, owner, applicable mode, provider/model/effort, limits, and permission
posture; the response exposes no exact private ref or path.
  - verify: `TestADR_0280_ClearSessionCreatesEmptyInheritedSuccessor`
- AC4.2: Clear reauthorizes the source, observes source-state/run-entry serialization and
lease rules, and exactly reattaches inherited placement or atomically resolves a current
source-scoped selector. Failure persists no successor and changes neither source nor
client binding.
  - verify: `TestADR_0280_ClearSessionIsLeaseSafeAndNonDestructive`
- AC4.3: `ForkSession(source_session_id, optional selector, model overrides)` copies valid
history and inherits the exact source ref when selector is omitted. A selector and model/
provider/effort overrides are fully authorized and atomically resolved; failure leaves no
partial successor.
  - verify: `TestInvariant_fork_placement_is_atomic_and_server_authorized`
- AC5.1: Teams derive placement from their owning session; subagents and Parallel derive or
fork the parent environment. No model-facing or delegation call accepts a worktree
selector, placement ID, or path; models cannot choose selectors and no-FS cannot upgrade.
  - verify: `TestInvariant_delegation_cannot_escalate_placement`
- AC5.2: Preserved-fork, delegation, and artifact handles are distinct typed handles and
are never accepted as worktree selectors. Every inspection consumer reauthorizes the owner
and original placement scope before access.
  - verify: `TestADR_0280_ArtifactHandlesCannotReplayAsPlacementSelectors`
- AC5.3: Delegation results, inspection surfaces, events, and model-visible summaries expose
no fork root, placement path, exact private ref, or reusable selector.
  - verify: `TestADR_0280_DelegationObservabilityContainsNoPlacementPath`
- AC6.1: Schedule creation inherits the source placement or immediately resolves a current
source-scoped selector, then persists the durable owner principal, original scope, and
exact private `EnvironmentRef`. It never persists a selector or “follow current default”
intent; models cannot choose schedule selectors.
  - verify: `TestADR_0280_ScheduleResolvesSelectorBeforePersistingExactEnvironmentRef`
- AC6.2: Each fire reauthorizes and exactly reattaches with that owner and scope, not daemon
identity. Drift, unavailability, or mismatch records an operator-visible failure without
creating a session or accessing a filesystem.
  - verify: `TestInvariant_scheduled_placement_is_reauthorized_at_fire`
- AC7.1: Trusted driver storage messages carry the exact private `EnvironmentRef`, which
may include a local physical root, and driver results enter runtime only through exact
`Reattach`. No public Harness/HTTP/client mapper projects that ref.
  - verify: `TestADR_0280_DriverStorageCarriesExactPrivateEnvironmentRef`
- AC7.2: ACP `session/new` and `session/load` bind or reattach from server state before
access. A required ACP cwd is checked only for consistency with trusted configured
placement; mismatch is rejected and cwd cannot select or construct authority.
  - verify: `TestADR_0280_ACPBindAndLoadAssertConfiguredPlacement`
- AC7.3: Apart from the local cwd assertion, ACP-visible sessions, discovery, errors, and
events obey the same public no-path and no-exact-ref projection rule.
  - verify: `TestServerOwnedSessionPlacement_Scenario7_ACPProjectsNoPhysicalPaths`
- AC8.1: The path-surface inventory distinguishes public Harness/HTTP/client fields and
public projections from private session/snapshot/driver storage, runtime adapters,
operator diagnostics, composition, and ACP cwd. Public surfaces are path-free; private
storage may retain the exact ref, `Session.Workspace` is removed, and ACP cwd is assertion
only.
  - verify: `TestADR_0280_PathSurfaceInventoryEnforcesPublicBoundary`
