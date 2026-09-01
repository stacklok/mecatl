# ADR 0280 — Server-owned session placement

- Status: Proposed
- Date: 2026-09-01
- Scope: public placement selection; session/snapshot/driver identity; binding and
  reattachment; discovery; successors; delegation; schedules; ACP; and path projections
- Supersedes: [ADR 0237](./0237-listener-scoped-workspace-authority.md); [ADR 0032](./0032-worktree-binding.md) decisions 1, 2, 4, and 5 where they expose or accept filesystem paths; [ADR 0214](./0214-environment-persistence.md) only for zero/path-bearing refs, workspace-derived local resolution, and lazy stamping. ADR 0214's exact reattachment and runtime affinity remain authoritative.
- Superseded by: none

## Context

A public filesystem path currently acts as both a client preference and server execution
authority. Listener topology does not make that safe: embedded, loopback, and remote
clients cross the same contract. Paths also leak server layout through snapshots,
inventory, worktree discovery, drivers, events, and alternate creation paths.

ADR 0211 already establishes `tool.Environment` as the runtime unit, and ADR 0214
requires exact reattachment. This ADR makes the server's placement decision durable and
removes physical workspace as a competing aggregate, snapshot, or public authority.
The implementation acceptance is [the server-owned placement plan](../acceptance/server-owned-session-placement.md), whose eight scenarios are cited in the decisions below.

## Decision

### 1. Public placement is a closed tagged protocol (Scenario 1)

A public selector has exactly three variants:

1. omitted: use the deployment default configured by trusted composition;
2. explicit no-FS: attenuate the applicable default or inherited placement; or
3. opaque ID: select an advertised placement.

No variant is a path, cwd, workspace root, mount, backend locator, or path-shaped
environment identity. Possession of an ID is never authority. The server reauthorizes an
ID for the caller, operation, deployment, placement scope, and current provider
inventory; absent and authorization-hidden IDs are indistinguishable. Invalid selection
never falls back to a default.

`Bind` is the sole creation/successor binding operation. It atomically authorizes and
resolves one immutable provider placement record/version, then returns a complete
`tool.Environment`, its `PlacementRef`, and safe metadata. The persistent ref is
`PlacementRef{Kind, ID, Revision}` (final implementation names may vary). A rebind or
inventory revision between authorization and resolution must fail rather than produce a
mixed generation. This is authorization atomicity, not a check-then-resolve convention.

Public Harness and HTTP fields/responses, generated clients, and public session inventory
remove all filesystem-path and workspace fields. This is an intentional breaking change:
old path-bearing requests and clients are unsupported, not translated or ignored.

### 2. V1 placement IDs are provider-owned, stable, and non-authorizing (Scenario 1; Scenario 8)

V1 does not create a placement registry, signer/key, self-authenticating token, path
hash, encoded/encrypted path, or process-local map. Providers own stable opaque IDs and
revisions: trusted composition configures the local default; a git-worktree provider
returns a stable non-path ID with its current inventory/fingerprint; remote providers
supply their IDs and revisions. IDs need not be secret because use always reauthorizes.

Provider inventory and authorization are evaluated at binding time. The ADR 0027 List 1
and List 2 re-audit remains mandatory for any resource or durable state an implementation
actually adds; V1's specified protocol adds none.

### 3. Exact placement replaces workspace persistence (Scenario 2)

`Session` and `sessnap.Snapshot` persist exactly `PlacementRef{Kind, ID, Revision}` as
the placement identity. They remove `Session.Workspace`, snapshot `Workspace`, and every
other physical-workspace duplicate. Public/session inventory, drivers, and event sources
also remove physical workspace fields. Runtime adapter roots may remain private to their
adapter and trusted operator/composition inputs may remain paths.

`Reattach` takes an exact persisted ref/revision and returns a complete environment with
the same identity and runner/workspace namespace. It never chooses a current default.
Missing provider/resolver, authorization drift, unavailable backend, revision mismatch,
nil workspace, or identity mismatch fails with failed precondition before access. There
is no zero ref, workspace-derived local resolution, lazy create/run-entry stamping,
legacy inference, or migration sweep. Legacy placement state is rejected.

This supersedes ADR 0214 only for its zero/path-bearing `EnvironmentRef` convention,
workspace-derived local resolution, and lazy stamping. ADR 0214's exact reattachment and
ADR 0211's runtime environment affinity remain intact.

### 4. Discovery and successors use the bound placement (Scenario 3; Scenario 4)

`ListCommands(session_id)` and `ListWorktrees(session_id)` authorize and reattach the
owned session, then operate only on its bound placement. They accept neither a root nor a
selector. A no-FS session returns its documented empty/unsupported result without calling
a filesystem/default resolver, command source, or worktree lister. Worktree results carry
only provider-owned opaque IDs and safe metadata; using one invokes `Bind` again.

`ClearSession` is a new breaking RPC. Its request contains only `source_session_id`; its
response contains the new successor ID and safe session metadata. It authorizes and
serializes against the source, observes source-state and lease rules, and creates a
distinct empty-history successor inheriting the exact placement ref, owner, and applicable
settings. It reauthorizes/reattaches that exact placement. It is non-destructive: a
failure writes no successor and changes neither source nor client binding.

`ForkSession` copies valid history and inherits the exact ref when its selector is
omitted, or invokes `Bind` for explicit no-FS or an opaque ID. Changed model/provider/
effort and placement are validated as one operation; failure creates no partial successor.

### 5. Delegation, handles, and schedules cannot bypass placement (Scenario 5; Scenario 6)

Teams derive the owning session's placement (or a direct API's newly authorized selector).
Subagents and Parallel receive the parent environment or a server-created fork. No
model-facing or delegation operation selects an unrelated placement; no-FS cannot upgrade.

Preserved-fork, delegation, and artifact handles are separate typed handles, not placement
IDs, and cannot be replayed as selectors. An inspection consumer reauthorizes the durable
owner and original placement scope before access. Their projections never disclose a path.

A schedule persists the durable owner principal, original placement scope, and exact
placement ref/revision (or explicit no-FS attenuation). At fire it reauthorizes and
reattaches as that owner and scope, never as daemon identity and never by following a
current default. Drift or mismatch creates no session and performs no filesystem access.

Legacy adoption is deleted rather than redesigned: no adoption API, compatibility user, or
legacy record may become an alternate placement authority path.

### 6. Drivers and ACP reattach; they do not select authority (Scenario 7)

Driver messages carry exact opaque placement refs and capabilities, never paths. A
driver-returned environment enters only through exact `Reattach`.

ACP is a local standard boundary and may require a cwd. That cwd is permitted solely as a
consistency assertion against a placement configured by trusted composition; it does not
select or construct authority. Both ACP `session/new` and `session/load` bind or reattach
from server state before access and reject a cwd mismatch. ACP projections follow the same
no-public-path rule.

### 7. Path-surface inventory and lifecycle are explicit (Scenario 8)

The implementation maintains a complete path-surface inventory with four categories:

| Category | Rule |
|---|---|
| Public Harness/HTTP fields and responses | Removed. |
| Durable aggregate, snapshot, driver, and event-source fields | Removed. |
| Runtime adapter roots and operator/composition configuration | Allowed privately. |
| ACP cwd | Allowed only as the local consistency assertion in decision 6. |

Any actual new outlives-a-call resource or restart-relevant state is re-audited in ADR
0027 Lists 1 and 2. This decision does not pre-authorize a hidden registry, cache, signer,
or process-local map.

## Consequences

- Every deployment uses the same non-path public contract; embedded mecatui configures its
  launch root only in trusted composition and omits placement over the Harness API.
- Exact reattachment and runtime affinity survive, but physical workspace is no longer a
  durable or public alternative authority source.
- Clear is an explicit breaking RPC with defined safe successor semantics; fork remains the
  intentional history-carrying re-selection seam.
- Existing path-bearing clients and records break intentionally. A coordinated contract,
  client, documentation, and deployment rollout is required.
- V1 has no unresolved registry-versus-token choice. Provider implementations may differ
  only within the fixed stable-ID/revision, bind-time-reauthorization protocol.

## See also

- [Acceptance plan — server-owned session placement](../acceptance/server-owned-session-placement.md)
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md)
- [ADR 0032 — First-class worktree binding for a session](./0032-worktree-binding.md)
- [ADR 0211 — Execution-environment runtime seam](./0211-execution-environment-runtime-seam.md)
- [ADR 0214 — Execution-environment persistence and reattachment](./0214-environment-persistence.md)
- [ADR 0237 — Listener-scoped workspace authority](./0237-listener-scoped-workspace-authority.md)
- [Architecture guide](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
