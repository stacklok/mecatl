# ADR 0291 — Server-owned session placement

- Status: Proposed
- Date: 2026-09-01
- Scope: public placement selection; session/snapshot/driver identity; binding and
  reattachment; discovery; successors; delegation; schedules; ACP; and path projections
- Supersedes: [ADR 0237](./0237-listener-scoped-workspace-authority.md); [ADR 0032](./0032-worktree-binding.md) decisions 1, 2, 4, and 5 where they expose or accept filesystem paths; [ADR 0214](./0214-environment-persistence.md) only for duplicate workspace persistence, workspace-derived fallback, and lazy stamping. ADR 0214's exact reattachment and runtime affinity remain authoritative.
- Superseded by: [ADR 0297](./0297-active-clear-cancellation-boundary.md) only for active-source `ClearSession` failure semantics

## Context

A public filesystem path currently acts as both a client preference and server execution
authority. Listener topology does not make that safe: embedded, loopback, and remote
clients cross the same Harness and HTTP contract.

The required invariant is server-exclusive filesystem authority, not the absence of paths
from trusted private state. ADR 0211 already establishes `tool.Environment` as the runtime
unit, and ADR 0214 requires exact reattachment. This ADR removes public path authority and
`Session.Workspace` as a competing durable identity while permitting trusted runtime,
storage, diagnostics, and composition code to retain the physical data needed to perform
exact reattachment. The implementation acceptance is [the server-owned placement plan](../acceptance/server-owned-session-placement.md), whose eight scenarios are cited below.

## Decision

### 1. Public creation has no general placement selector (Scenario 1)

Default `CreateSession` accepts only:

1. omitted placement: bind the deployment default configured by trusted composition; or
2. explicit no-FS: attenuate the session to a filesystem-free environment.

It accepts no general placement ID, token, path, cwd, workspace root, mount, or backend
locator. Public Harness and HTTP requests/responses, generated clients, and public session
inventory neither accept nor return filesystem paths. They also do not expose the exact
durable environment ref. Safe placement metadata is display-only: kind, bounded label,
branch, and revision. It is never reusable authority.

`Bind` remains the sole creation/successor binding operation. For a default/no-FS request,
or for a successor's server-issued selector, it resolves and authorizes against one
provider snapshot and returns a complete `tool.Environment`, exact
`session.EnvironmentRef{Kind, ID, Revision}`, and safe metadata. Selector matching and
environment resolution use the same provider snapshot; inventory drift cannot produce a
mixed generation. This is authorization atomicity, not a check-then-resolve convention.

Old path-bearing requests and clients are unsupported, not translated or ignored.

### 2. EnvironmentRef is the sole durable runtime identity (Scenario 2)

`Session` and `sessnap.Snapshot` persist exactly
`session.EnvironmentRef{Kind, ID, Revision}` as runtime identity. `Session.Workspace`,
snapshot `Workspace`, and every other duplicate physical-workspace identity are removed.
For a local environment, the private `EnvironmentRef.ID` may identify the configured
physical root. That does not make it a public selector: public mappers never expose the
exact ref.

Trusted private runtime, snapshots, storage adapters and their driver transport, operator
diagnostics, and composition may retain or transport physical roots as implementation
data. Driver storage may carry the exact private ref because it is a trusted storage
adapter, not a public client authority surface. Events and public projections remain
path-free.

`Reattach` takes the exact persisted ref/revision and returns a complete environment with
the same identity and runner/workspace namespace. It never chooses a current default.
Missing provider/resolver, authorization drift, unavailable backend, revision mismatch,
nil workspace, or identity mismatch fails with failed precondition before access. There
is no zero-ref fallback, separate workspace inference, lazy create/run-entry stamping,
legacy adoption, or migration sweep. Legacy duplicate placement state is rejected.

This supersedes ADR 0214 only for duplicate workspace persistence, workspace-derived
fallback, and lazy stamping. ADR 0214's exact reattachment and ADR 0211's runtime
environment affinity remain intact.

### 3. Alternate worktrees are scoped ephemeral choices (Scenario 3; Scenario 4)

`ListCommands(session_id)` and `ListWorktrees(session_id)` authorize and reattach the
owned source session, then operate only on its bound placement. Neither accepts a root or
client-provided placement selector. A no-FS session returns its documented empty or
unsupported result without invoking filesystem providers.

Each worktree result contains safe display metadata and an opaque, short-lived selector
token. The token is scoped to the caller and source session and can be used only by
`ClearSession` or `ForkSession` for that source. It is not a durable placement ID, a
bearer path, or reusable display metadata. On use, the server reauthorizes the owner,
re-enumerates the source placement's currently eligible worktrees, and atomically matches
one current choice before binding. An absent, stale, wrong-owner, wrong-source, or
otherwise unauthorized token fails without revealing whether a hidden choice exists.

The local provider issues tokens as HMAC-SHA256 digests over provider-private current
choice identity plus caller/source scope, using a random per-process key. The server never
decodes a token: it re-enumerates current choices and uses constant-time comparison. There
is no selector registry or map, no encoded/reversible path, and no persistence. Tokens do
not survive restart; the client relists. Remote providers may issue equivalent scoped,
ephemeral selectors.

`ClearSession(source_session_id, optional selector)` creates a distinct empty-history
successor. With no selector, ordinary `/clear` inherits and exactly reattaches the source
placement. With a selector, it resolves a current eligible alternate worktree as above.
Clear reauthorizes the source, serializes against source state and leases, and is
non-destructive: failure writes no successor and changes neither source nor client
binding.

`ForkSession(source_session_id, optional selector, model overrides)` copies valid history.
It inherits the exact source ref when the selector is omitted or atomically binds the
selected current worktree when present. Placement and model/provider/effort changes are
validated as one operation; failure creates no partial successor.

### 4. Delegation and handles cannot bypass placement (Scenario 5)

Teams derive the owning session's placement. Subagents and Parallel receive the parent
environment or a server-created fork. No model-facing or delegation operation accepts a
placement selector or path, models cannot choose worktree selectors, and no-FS cannot
upgrade.

Preserved-fork, delegation, and artifact handles are separate typed handles, not worktree
selectors, and cannot be replayed as such. An inspection consumer reauthorizes the durable
owner and original placement scope before access. Delegation events, model-visible
results, and public inspection projections never disclose a physical root.

### 5. Schedules persist resolved placement, never selectors (Scenario 6)

Schedule creation either inherits the source session's exact placement or resolves a
current server-issued selector immediately. A schedule never persists the selector. It
persists the exact private `EnvironmentRef` plus durable owner principal and original
scope. At fire it reauthorizes and exactly reattaches as that owner and scope, never as
daemon identity and never by following a current default. Drift or mismatch creates no
session and performs no filesystem access. Models cannot choose schedule placement
selectors.

### 6. Drivers store private identity; ACP only asserts cwd (Scenario 7)

Trusted driver storage messages may transport the exact private `EnvironmentRef`, including
a provider-private local root in its ID. Driver-returned identity enters runtime only
through exact `Reattach`. Driver storage is not a public Harness/HTTP/client authority
surface, and public mappers never project that ref.

ACP is a local standard boundary and may require a cwd. That cwd is permitted solely as a
local consistency assertion against a placement configured by trusted composition; it
does not select or construct authority. ACP `session/new` and `session/load` bind or
reattach from server state before access and reject a cwd mismatch. Other ACP projections
remain path-free.

### 7. Path surfaces and lifecycle resources are explicit (Scenario 8)

The implementation maintains this path-surface inventory:

| Category | Rule |
|---|---|
| Public Harness/HTTP/client fields and responses | Never accept or return filesystem paths or exact private refs. |
| Public events and projections | Display-only safe metadata; no physical root or reusable authority. |
| Session/snapshot and trusted driver storage | Exact private `EnvironmentRef` allowed; no duplicate `Workspace`. |
| Runtime adapters, operator diagnostics, and composition | Physical roots allowed as private implementation data. |
| ACP cwd | Allowed only as the local consistency assertion in decision 6. |

The local selector HMAC key is a Build-owned, process-lifetime resource. It is generated
randomly at startup, is not persisted, and is reset by design; after restart clients must
relist worktrees. ADR 0027 List 1 inventories its owner, scope, cleanup, and reattach
behavior, and List 2 records the reset-by-design decision. No selector registry, cache, or
process-local placement map is added.

Legacy adoption remains deleted rather than redesigned: no adoption API, compatibility
user, or legacy record may become an alternate placement authority path.

## Consequences

- Every deployment uses the same no-path public contract; embedded mecatui configures its
  launch root only in trusted composition and omits placement over the Harness API.
- Exact reattachment survives because trusted durable state may retain the private data it
  needs; only duplicate `Session.Workspace` and public authority are removed.
- Alternate worktree selection requires an owned source session and a fresh scoped token;
  restart deliberately requires relisting.
- Clear is an explicit, non-destructive successor operation; fork is the history-carrying
  counterpart. Both inherit exact placement unless given a current scoped selector.
- Existing path-bearing clients and legacy duplicate placement records break intentionally.
  A coordinated contract, client, documentation, and deployment rollout is required.
- The Build-owned HMAC key is real lifecycle state and must be inventoried, but it carries
  no durable authority and needs no restart recovery.

## See also

- [Acceptance plan — server-owned session placement](../acceptance/server-owned-session-placement.md)
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md)
- [ADR 0032 — First-class worktree binding for a session](./0032-worktree-binding.md)
- [ADR 0211 — Execution-environment runtime seam](./0211-execution-environment-runtime-seam.md)
- [ADR 0214 — Execution-environment persistence and reattachment](./0214-environment-persistence.md)
- [ADR 0237 — Listener-scoped workspace authority](./0237-listener-scoped-workspace-authority.md)
- [Architecture guide](../architecture.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
