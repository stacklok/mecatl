# Server-owned session placement — acceptance plan

**Phase:** public placement capability redesign  
**Status:** draft, 2026-09-01  
**ADR:** [ADR 0280](../adr/0280-server-owned-session-placement.md)  
**Accumulator branch:** `acc/server-owned-session-placement` (off `main`)

This clean break removes client filesystem-path authority from every session lifecycle.
The server binds every environment; no compatibility field, legacy-path adoption, or
listener-topology exception remains.

## Placement protocol and scope

The public placement selector is a tagged value with exactly three variants:

1. **omitted** — use the trusted composition-configured deployment default;
2. **no-FS** — explicit attenuation of the applicable default or inherited placement;
3. **ID** — an opaque server-advertised placement ID.

It is neither a path nor a bearer capability. Possession authorizes nothing. The server
reauthorizes every ID for the durable owner principal, operation, placement scope, and
current provider inventory. It atomically binds authorization and resolution to one
immutable provider record/version, returning a complete `tool.Environment`, persisted
`PlacementRef{Kind, ID, Revision}`, and bounded safe metadata. `Reattach` accepts only
that exact ref/revision and never follows a current default.

V1 uses stable opaque IDs and revisions owned by the placement provider: a local default
is configured by trusted composition, a git-worktree provider supplies a stable non-path
identity plus current inventory/fingerprint, and remote providers supply their IDs. V1
adds no placement registry, signer, token, path hash, encoded/encrypted path, or
process-local map. IDs need not be secret because possession is never authority.

Physical workspace is removed from the session aggregate, snapshot, public/session
inventory, driver records, and event sources. Runtime adapter roots and operator
composition paths remain private. ACP's standard local boundary may require a cwd only
as a consistency assertion against the composition-configured placement; it never
constructs or selects authority.

## Scenarios and acceptance criteria

### Scenario 1 — Create binds one server-owned placement

This scenario enforces [ADR 0280](../adr/0280-server-owned-session-placement.md) at the public creation boundary.

- AC1.1: `CreateSession` accepts only the three tagged selector variants. Omission binds
the deployment default; no-FS attenuates it; an ID is reauthorized. Its response exposes
only `PlacementRef` and safe metadata.
  - verify: `TestADR_0280_CreateSessionPlacementProtocol`
- AC1.2: `Bind` atomically authorizes and resolves one immutable provider record/version;
a rebind or inventory revision between authorization and resolution fails closed before
filesystem access or persistence, never binding a mixed generation.
  - verify: `TestADR_0280_BindRejectsRebindBetweenAuthorizationAndResolution`
- AC1.3: Unknown, stale, wrong-scope, unauthorized, or unavailable IDs fail before
environment construction, trust evaluation, or persistence; hidden and absent IDs are
indistinguishable and never default.
  - verify: `TestInvariant_server_owned_placement_ids_fail_closed`
- AC1.4: Harness protobuf/HTTP requests and responses, generated clients, and public
session inventory contain no filesystem path, cwd, workspace root, mount, or
path-shaped environment selector. Old path-bearing requests are unsupported.
  - verify: `TestADR_0280_PublicHarnessContractContainsNoFilesystemPaths`

### Scenario 2 — Durable sessions reattach exactly

This scenario preserves the exact-reattachment invariant from [ADR 0214](../adr/0214-environment-persistence.md) while removing path-derived identity.

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

### Scenario 3 — Discovery is owned-session scoped

This scenario retains the worktree workflow from [ADR 0032](../adr/0032-worktree-binding.md) without its path-bearing authority.

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

### Scenario 4 — Clear and fork create non-destructive successors

This scenario extends the history-carrying successor semantics of [ADR 0065](../adr/0065-conversation-fork.md) with a separate empty-history operation.

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

### Scenario 5 — Delegation and artifact handles do not become selectors

This scenario preserves complete parent/child environment affinity from [ADR 0211](../adr/0211-execution-environment-runtime-seam.md).

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

### Scenario 6 — Deferred creation preserves owner and exact placement

This scenario applies the deferred-session contract from [ADR 0059](../adr/0059-scheduled-tasks.md) without granting the scheduler ambient placement authority.

- AC6.1: A schedule persists its durable owner principal, original placement scope, and
exact `PlacementRef` revision (or explicit no-FS attenuation), never “follow current
default” intent or a path.
  - verify: `TestADR_0280_SchedulePersistsOwnerScopeAndExactPlacementRef`
- AC6.2: Each fire reauthorizes and reattaches with that owner and scope, not daemon
identity. Drift, unavailability, or mismatch records an operator-visible failure without
creating a session or accessing a filesystem.
  - verify: `TestInvariant_scheduled_placement_is_reauthorized_at_fire`

### Scenario 7 — Driver and ACP preserve the binding boundary

This scenario keeps remote environment reattachment aligned with [ADR 0214](../adr/0214-environment-persistence.md) and the composition boundary in [`architecture.md`](../architecture.md).

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

### Scenario 8 — Composition owns allowed paths without new hidden state

- AC8.1: The complete path-surface inventory classifies public Harness/HTTP fields,
durable aggregate/snapshot/driver fields, runtime adapter/operator-composition paths,
and ACP cwd assertions. The first two are removed; only the last two are allowed.
  - verify: `TestADR_0280_PathSurfaceInventoryHasNoPublicOrDurableWorkspacePath`
- AC8.2: Trusted composition configures the local default; worktree and remote providers
provide stable opaque IDs/revisions. Startup rejects invalid configuration before serving.
  - verify: `TestADR_0280_CompositionConfiguresProviderOwnedPlacements`
- AC8.3: The ADR 0027 List 1/List 2 re-audit records any actual added resource or durable
state. V1 introduces none of a registry, signer, cache, or process-local placement map.
  - verify: `TestADR_0280_PlacementReauditFindsNoV1RegistryOrState`

## Out of scope

| Item | Decision |
|---|---|
| Compatibility aliases, legacy path migration, or adoption | Deleted, not redesigned; no compatibility users are supported. |
| Client-selected host paths, including embedded/loopback | Permanently excluded by [ADR 0280](../adr/0280-server-owned-session-placement.md). |
| Filesystem CAS, read ledger, runner affinity, or fork/merge mechanics | Preserved by [ADR 0208](../adr/0208-execution-environment.md) and [ADR 0211](../adr/0211-execution-environment-runtime-seam.md). |
| Placement IDs as bearer grants, encoded paths, or portable deployment aliases | Permanently excluded. |

## Cross-cutting deliverables

- Remove physical `Workspace` from aggregate, snapshot, public/session inventory, driver,
and event-source types; keep runtime adapter roots private.
- Update public contracts, generated SDKs, API compatibility records, architecture,
usage, implementation notes, `AGENTS.md`, and `user-docs/` for the breaking change.
- Add structural path-surface guards and the named scenario tests above. Every AC has one
observable verify line; `task ac-trace-strict` must resolve all 26 ACs.
- Re-audit ADR 0027 List 1/List 2 after implementation. Add rows only for an actual
outlives-a-call resource or restart-relevant state; V1's provider-owned protocol adds
neither by design.

## Definition of done

1. `task lint`, `task test`, `task docs`, `task generate`, `task api:check`, and
   `task site:build` pass.
2. `task ac-trace-strict` resolves all 26 AC proofs and every named scenario test is
   green.
3. gRPC, HTTP/SSE, generated SDK, mecatui, mecak8s, driver, ACP, schedule, clear, fork,
   delegation, and no-FS tests demonstrate the corresponding scenarios above.
4. ADR 0027 contains the implementation re-audit before the accumulator is complete.

## Unresolved decisions

- Safe display metadata may be refined by provider, but must remain bounded, non-secret,
  non-path, and non-reversible.
- The final Go names for the tagged selector and `PlacementRef` fields may follow local
  conventions; their three-variant/exact-revision protocol is fixed.
