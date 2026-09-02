# Server-owned session placement — acceptance plan

**Phase:** public placement capability redesign
**Status:** landed, 2026-09-01
**ADR:** [ADR 0289](../adr/0289-server-owned-session-placement.md)
**Accumulator branch:** `acc/server-owned-session-placement` (off `main`)

This clean break removes client filesystem-path authority from every session lifecycle.
The server binds every environment; no compatibility field, legacy-path adoption, or
listener-topology exception remains. Trusted private runtime and durable storage may keep
physical roots as implementation data required for exact reattachment.

## Placement protocol and scope

Default `CreateSession` accepts only omitted deployment default or explicit no-FS
attenuation. It accepts no general placement ID. Alternate worktree selection starts from
an owned source session: `ListWorktrees(session_id)` returns safe display metadata and an
opaque short-lived selector usable only by `ClearSession` or `ForkSession` for that source.

`session.EnvironmentRef{Kind, ID, Revision}` is the sole durable runtime identity.
`Session.Workspace` is removed, but a local ref's private ID may identify the configured
root. Snapshots and trusted driver storage may carry that exact private ref. Public
Harness/HTTP/client mappers never expose it or any filesystem path; public kind, label,
branch, and revision metadata is display-only and grants nothing.

Local worktree selectors are HMAC-SHA256 digests over provider-private current choice
identity plus caller/source scope, made with a random per-process key. The server never
decodes them: it re-enumerates current eligible choices and constant-time matches in one
provider snapshot. There is no selector registry, encoded path, or persistence. After a
restart the client relists. Remote providers may provide equivalent scoped ephemeral
selectors.

`Bind` atomically resolves the deployment default/no-FS choice or matches a current
selector choice and returns a complete `tool.Environment` plus exact private ref.
`Reattach` accepts only that exact persisted ref/revision and never follows a current
default. ACP cwd remains solely a local assertion against trusted configuration.

## Scenarios and acceptance criteria

### Scenario 1 — Create binds the server-owned default or no-FS

This scenario enforces [ADR 0289](../adr/0289-server-owned-session-placement.md) at the public creation boundary.

- AC1.1: `CreateSession` accepts only omitted deployment default or explicit no-FS
attenuation; it accepts no general placement ID or selector. Its response exposes only a
session ID and bounded display metadata, never the exact private `EnvironmentRef`.
  - verify: `TestADR_0289_CreateSessionAcceptsOnlyDefaultOrNoFS`
- AC1.2: `Bind` constructs the complete environment and exact private ref from one
immutable provider snapshot; an authorization/resolution or inventory revision race fails
closed before persistence or filesystem access, never binding a mixed generation.
  - verify: `TestADR_0289_BindRejectsRebindBetweenAuthorizationAndResolution`
- AC1.3: Unknown, stale, wrong-owner, wrong-source, unauthorized, or unavailable selectors
fail before environment construction, trust evaluation, or persistence; hidden and absent
choices are indistinguishable and never fall back to a default.
  - verify: `TestADR_0289_ScopedWorktreeSelectorsFailClosed`
- AC1.4: Harness protobuf/HTTP requests and responses, generated clients, and public session
inventory contain no filesystem path, cwd, workspace root, mount, exact private
`EnvironmentRef`, or path-shaped selector. Old path-bearing requests are unsupported.
  - verify: `TestADR_0289_PublicHarnessContractContainsNoFilesystemPaths`

### Scenario 2 — Durable sessions reattach exactly

This scenario preserves the exact-reattachment invariant from [ADR 0214](../adr/0214-environment-persistence.md) while removing duplicate workspace identity.

- AC2.1: `Session` and its snapshot persist only
`EnvironmentRef{Kind, ID, Revision}` as runtime identity and contain no duplicate
`Workspace`, live runner, credential, transport, or process handle. A local private ID may
identify its configured physical root.
  - verify: `TestADR_0289_SessionAndSnapshotPersistOnlyEnvironmentRef`
- AC2.2: On load and at run entry, `Reattach` requires the exact persisted ref/revision and
returns a complete environment whose identity and bound runner share its namespace.
Missing resolver/provider, authorization drift, revision mismatch, nil workspace, or
identity mismatch is a failed precondition with no fallback.
  - verify: `TestInvariant_persisted_placement_reattaches_exactly`
- AC2.3: Zero refs, duplicate legacy `Workspace` state, and otherwise invalid snapshots are
rejected; there is no lazy stamping, migration sweep, workspace inference, adoption, or
alternate authority path.
  - verify: `TestADR_0289_LegacyDuplicatePlacementStateIsUnsupported`
- AC2.4: Public projections, events, client-visible logs, and errors expose only bounded
display metadata and never an exact private ref, physical path, or secret backend locator.
Trusted operator diagnostics may retain physical roots.
  - verify: `TestInvariant_physical_placement_paths_never_cross_public_api`

### Scenario 3 — Discovery issues source-scoped ephemeral selectors

This scenario retains the worktree workflow from [ADR 0032](../adr/0032-worktree-binding.md) without public path authority or stable public placement IDs.

- AC3.1: `ListCommands(session_id)` and `ListWorktrees(session_id)` authorize and reattach
the owned session before discovery; neither accepts a root or client-provided selector.
Worktree entries contain an opaque selector plus bounded display-only kind/label/branch/
revision metadata.
  - verify: `TestADR_0289_DiscoveryIsSessionScopedAndOwnerAuthorized`
- AC3.2: For a no-FS session, both discovery calls return the documented empty/unsupported
result without invoking a filesystem/default resolver, command source, or worktree lister.
  - verify: `TestADR_0289_NoFSDiscoveryDoesNotInvokeFilesystemProviders`
- AC3.3: A local selector is an HMAC-SHA256 digest scoped to caller and source session. On
use, the server re-enumerates currently eligible source-placement worktrees and
constant-time matches in one provider snapshot; it never decodes the token or consults a
registry/map.
  - verify: `TestADR_0289_WorktreeSelectorIsScopedAndMatchedAgainstCurrentInventory`
- AC3.4: A hidden/unknown session cannot probe another owner's placement or server layout.
Selectors expire on process restart, so mecatui relists and retains its selected session
on relist or switch failure.
  - verify: `TestServerOwnedSessionPlacement_Scenario3_OwnershipRelistAndSafeSwitch`

### Scenario 4 — Clear and fork create non-destructive successors

This scenario extends the history-carrying successor semantics of [ADR 0065](../adr/0065-conversation-fork.md) with a separate empty-history operation.

- AC4.1: `ClearSession(source_session_id, optional selector)` creates a distinct
empty-history successor. Ordinary `/clear` omits the selector and inherits the exact
source placement, owner, applicable mode, provider/model/effort, limits, and permission
posture; the response exposes no exact private ref or path.
  - verify: `TestADR_0289_ClearSessionCreatesEmptyInheritedSuccessor`
- AC4.2: Clear reauthorizes the source, observes source-state/run-entry serialization and
lease rules, and exactly reattaches inherited placement or atomically resolves a current
source-scoped selector. Failure persists no successor and changes neither source nor
client binding.
  - verify: `TestADR_0289_ClearSessionIsLeaseSafeAndNonDestructive`
- AC4.3: `ForkSession(source_session_id, optional selector, model overrides)` copies valid
history and inherits the exact source ref when selector is omitted. A selector and model/
provider/effort overrides are fully authorized and atomically resolved; failure leaves no
partial successor.
  - verify: `TestInvariant_fork_placement_is_atomic_and_server_authorized`

### Scenario 5 — Delegation and artifact handles do not become selectors

This scenario preserves complete parent/child environment affinity from [ADR 0211](../adr/0211-execution-environment-runtime-seam.md).

- AC5.1: Teams derive placement from their owning session; subagents and Parallel derive or
fork the parent environment. No model-facing or delegation call accepts a worktree
selector, placement ID, or path; models cannot choose selectors and no-FS cannot upgrade.
  - verify: `TestInvariant_delegation_cannot_escalate_placement`
- AC5.2: Preserved-fork, delegation, and artifact handles are distinct typed handles and
are never accepted as worktree selectors. Every inspection consumer reauthorizes the owner
and original placement scope before access.
  - verify: `TestADR_0289_ArtifactHandlesCannotReplayAsPlacementSelectors`
- AC5.3: Delegation results, inspection surfaces, events, and model-visible summaries expose
no fork root, placement path, exact private ref, or reusable selector.
  - verify: `TestADR_0289_DelegationObservabilityContainsNoPlacementPath`

### Scenario 6 — Deferred creation persists resolved owner and placement

This scenario applies the deferred-session contract from [ADR 0059](../adr/0059-scheduled-tasks.md) without granting the scheduler ambient placement authority.

- AC6.1: Schedule creation inherits the source placement or immediately resolves a current
source-scoped selector, then persists the durable owner principal, original scope, and
exact private `EnvironmentRef`. It never persists a selector or “follow current default”
intent; models cannot choose schedule selectors.
  - verify: `TestADR_0289_ScheduleResolvesSelectorBeforePersistingExactEnvironmentRef`
- AC6.2: Each fire reauthorizes and exactly reattaches with that owner and scope, not daemon
identity. Drift, unavailability, or mismatch records an operator-visible failure without
creating a session or accessing a filesystem.
  - verify: `TestInvariant_scheduled_placement_is_reauthorized_at_fire`

### Scenario 7 — Driver storage and ACP preserve the binding boundary

This scenario keeps remote environment reattachment aligned with [ADR 0214](../adr/0214-environment-persistence.md) and the composition boundary in [`architecture.md`](../architecture.md).

- AC7.1: Trusted driver storage messages carry the exact private `EnvironmentRef`, which
may include a local physical root, and driver results enter runtime only through exact
`Reattach`. No public Harness/HTTP/client mapper projects that ref.
  - verify: `TestADR_0289_DriverStorageCarriesExactPrivateEnvironmentRef`
- AC7.2: ACP `session/new` and `session/load` bind or reattach from server state before
access. A required ACP cwd is checked only for consistency with trusted configured
placement; mismatch is rejected and cwd cannot select or construct authority.
  - verify: `TestADR_0289_ACPBindAndLoadAssertConfiguredPlacement`
- AC7.3: Apart from the local cwd assertion, ACP-visible sessions, discovery, errors, and
events obey the same public no-path and no-exact-ref projection rule.
  - verify: `TestServerOwnedSessionPlacement_Scenario7_ACPProjectsNoPhysicalPaths`

### Scenario 8 — Composition owns private paths and selector lifecycle

- AC8.1: The path-surface inventory distinguishes public Harness/HTTP/client fields and
public projections from private session/snapshot/driver storage, runtime adapters,
operator diagnostics, composition, and ACP cwd. Public surfaces are path-free; private
storage may retain the exact ref, `Session.Workspace` is removed, and ACP cwd is assertion
only.
  - verify: `TestADR_0289_PathSurfaceInventoryEnforcesPublicBoundary`
- AC8.2: Trusted composition configures and validates the local deployment default and
placement providers before serving; no stable public placement-ID registry is required.
  - verify: `TestADR_0289_CompositionConfiguresProviderOwnedPlacements`
- AC8.3: ADR 0027 List 1 inventories the random HMAC key as a Build-owned,
process-lifetime resource, and List 2 records its reset-by-design restart semantics.
Selectors are not persisted, restart requires relisting, and no selector registry/map is
introduced.
  - verify: `TestADR_0289_PlacementReauditInventoriesEphemeralSelectorKey`

## Out of scope

| Item | Decision |
|---|---|
| Compatibility aliases, legacy path migration, or adoption | Deleted, not redesigned; no compatibility users are supported. |
| Client-selected host paths, including embedded/loopback | Permanently excluded by [ADR 0289](../adr/0289-server-owned-session-placement.md). |
| Filesystem CAS, read ledger, runner affinity, or fork/merge mechanics | Preserved by [ADR 0208](../adr/0208-execution-environment.md) and [ADR 0211](../adr/0211-execution-environment-runtime-seam.md). |
| Stable public placement IDs, selectors as bearer grants, encoded paths, or portable deployment aliases | Permanently excluded. |

## Cross-cutting deliverables

- Remove `Session.Workspace` and public path/ref projections while retaining exact private
  `EnvironmentRef` in snapshot and trusted driver storage.
- Update public contracts, generated SDKs, API compatibility records, architecture, usage,
  implementation notes, `AGENTS.md`, and `user-docs/` for the breaking change.
- Add structural public-path guards and the named scenario tests above. Every AC has one
  observable verify line; `task ac-trace-strict` must resolve all 26 ACs.
- Re-audit ADR 0027 List 1/List 2 and inventory the Build-owned random HMAC key, including
  its reset-by-design/relist-after-restart decision.

## Definition of done

1. `task lint`, `task test`, `task docs`, `task generate`, `task api:check`, and
   `task site:build` pass.
2. `task ac-trace-strict` resolves all 26 AC proofs and every named scenario test is green.
3. gRPC, HTTP/SSE, generated SDK, mecatui, mecak8s, driver, ACP, schedule, clear, fork,
   delegation, and no-FS tests demonstrate the corresponding scenarios above.
4. ADR 0027 inventories the selector HMAC key and its reset-by-design restart semantics
   before the accumulator is complete.

## Unresolved decisions

- Safe display labels may be refined by provider but must remain bounded, non-secret,
  non-path, and unusable as authority.
- Final Go names for internal binding requests and public selector fields may follow local
  conventions; the source-scoped ephemeral-token and exact-private-ref protocol is fixed.
