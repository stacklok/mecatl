# ADR 0296 — Opt-in local session context service

- Status: Proposed
- Date: 2026-09-03
- Scope: privileged local-client workspace-context projection; gRPC service registration; placement reattachment; and Mecatui status-command integration
- Supersedes: none
- Superseded by: none

## Context

ADR 0291 makes placement server-owned. A public client cannot create a session at an
arbitrary path, receive a physical root, or receive an exact `EnvironmentRef`. That
removes a client-controlled filesystem-authority path and preserves exact
reattachment across restarts.

A session's workspace can nevertheless matter to a client that is local to the
workspace. A terminal UI, editor integration, or local status command may need to
identify the actual directory behind the session in order to report local state.
That state is not necessarily Git: it may be an SVN checkout, a document corpus, or
an ordinary directory. The existing safe `PlacementMetadata` (`Kind`, `Label`,
`Branch`, and `Revision`) identifies a placement for ordinary display, but cannot
provide a locally usable physical root.

Mecatui previously supplied a local session path to its status-command input. The
server-owned-placement implementation correctly removed that path from the ordinary
client projection, but consequently made a selected worktree indistinguishable from
the launch directory to a local status command. Recovering a path through a private
Go reference from Mecatui to an embedded server would work, but would create a hidden
transport bypass and bind the capability to one TUI implementation.

A physical path is read-only information, not placement authority. It is still
sensitive: it can disclose host layout, user names, project names, or tenant
structure. A daemon-local workspace is also not necessarily local to a network
client. Consequently, this is not an ordinary extension of `PlacementMetadata`, and
must not be exposed by the universal Harness or HTTP APIs.

## Decision

### 1. Add a separately registered local session context gRPC service

Add an optional gRPC service for local clients to retrieve a narrowly defined,
privileged projection of an already-owned session's active environment. Version one
has one operation:

```text
GetLocalSessionContext(session_id) -> { workspace_path }
```

The service is separate from the universal Harness service. It is not exposed through
the HTTP gateway, public session inventory, public events, or ordinary placement
metadata. Its response must not include an `EnvironmentRef`, placement selector,
command runner, provider configuration, credentials, mount details, or filesystem
inventory.

The service name and protobuf field names are an explicit privileged-contract
exception to ADR 0291's ordinary public path-projection rule. They must be documented
as such; callers must not treat `workspace_path` as authority to create, rebind,
fork, or otherwise select a session placement.

### 2. Make exposure opt-in and local-listener constrained

The service is disabled by default. Composition may register it only when both of the
following are true:

1. an operator explicitly enables local session context at server startup; and
2. composition attests that the listener is a local-client-trusted transport, such as
   a user-owned Unix socket with restrictive filesystem permissions.

A boolean that merely registers this service on a normal TCP/HTTP listener is not an
acceptable implementation. Loopback address alone is insufficient evidence of a
single trusted local client. The capability must remain unavailable unless both the
explicit opt-in and the listener constraint hold.

Embedded Mecatui enables the service on its existing private Unix-socket listener.
Other local clients and local TUIs may use the same service when an operator has
explicitly configured an eligible listener. A standard `mecated` network deployment
leaves it unregistered. An unavailable service returns the normal gRPC
`UNIMPLEMENTED` result rather than an ambiguous empty context.

Service registration is a composition concern. The engine, session domain, tool
domain, and placement-provider interfaces remain unaware of gRPC and of this
client-projection policy.

### 3. Reattach exactly; never derive a root from metadata or a ref

For `GetLocalSessionContext`, the server must:

1. authenticate and owner-authorize the caller for the requested session;
2. load the session's persisted exact `EnvironmentRef`;
3. exact-reattach through the configured placement provider;
4. return `Environment.Workspace().Root()` only when the reattached ref has local
   environment kind and a non-empty root; and
5. close any provider-owned provisional binding resource.

The implementation must not parse `EnvironmentRef.ID`, derive a root from a label,
re-list a client's local Git worktrees, or fall back to the daemon's current default
workspace. Reattachment mismatch, provider unavailability, no-FS placement, and
non-local placement fail without returning a path. A selected worktree that moved or
was replaced therefore fails closed instead of silently naming a different directory.

The response is a read-only observation of the environment that the session already
uses. It creates no alternate placement authority and does not alter session state.

### 4. Keep universal placement metadata useful and path-free

`PlacementMetadata` remains the all-client, display-safe answer to “what workspace is
this session associated with?” It continues to carry only bounded `Kind`, `Label`,
`Branch`, and `Revision` data. Local providers should supply meaningful provider-owned
labels where available, but a label is not required to be a directory basename and
must never be treated as one.

A local client which needs an actual root uses the opt-in local session context
service. A client which needs only an identifier uses ordinary placement metadata.
The two data classes remain distinct.

### 4.1 Status-command input protocol

The local session path is included in the raw JSON input of a configured direct
local status command and in the StatusML-escaped template projection after the
privileged RPC successfully returns an eligible local root. `Workspace.Name` is
provider-supplied display metadata, not a filesystem basename. This is status input
protocol v3; there is no `Basename` compatibility alias.

### 4.2 Direct status-command CWD

The direct command uses the active session local root when it is available. Otherwise
it uses the cleaned absolute parent directory of the configured helper executable. If
that parent cannot be determined, it retains the launch-directory fallback; it never
selects `HOME` implicitly.

### 5. Defer new-session placement selection

This decision does not add a general `PlacementRequest`, workspace inventory, or
workspace selector to public `CreateSession`. ADR 0291's default/no-FS root-session
creation rule remains in force.

A future product need may add a principal-scoped server workspace inventory and an
opaque server-issued selection token usable at creation. That is a separate decision.
It must preserve provider-owned authorization, atomic inventory-match-and-bind,
exact-ref persistence, and the prohibition on client-provided paths and refs. The
local session context service is observational only and must not become an implicit
selection mechanism.

## Consequences

- Local clients can reliably identify and operate relative to the exact local
  directory of an already-bound session, including selected worktree successors and
  non-VCS directories.
- Mecatui status commands and templates can receive a local session root after an
  eligible lookup: a command receives it in raw JSON and as CWD; a template receives
  it in its StatusML-escaped projection. This does not make `PlacementMetadata`
  path-bearing or expose paths through universal Harness/HTTP/event/placement
  projections.
- Status input protocol v3 replaces the misleading `Workspace.Basename` field with
  provider-supplied display metadata at `Workspace.Name`; no alias is retained.
- The default public Harness/HTTP/client contract remains path-free. Remote clients
  cannot obtain server filesystem paths merely because a session's provider uses a
  local filesystem.
- The service adds a deliberate privileged API and startup configuration surface.
  Registration, listener admission, authentication/ownership, non-local rejection,
  reattachment failure, and the absence of an HTTP route all need direct tests.
- Implementations must inventory any new listener, registration state, or retained
  provider resource in the cloud-native resource ledger when one outlives a call.
- The API is intentionally narrow. Adding fields beyond the exact local workspace
  root requires a separately stated disclosure need and review; “local impact” is not
  a blanket authorization to expose environment internals.

## See also

- [ADR 0291 — Server-owned session placement](./0291-server-owned-session-placement.md)
- [ADR 0211 — Execution-environment runtime seam](./0211-execution-environment-runtime-seam.md)
- [ADR 0214 — Execution-environment persistence and reattachment](./0214-environment-persistence.md)
- [ADR 0247 — Mecatui generated status lines](./0247-mecatui-status-line.md)
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md)
- [Architecture guide](../architecture.md)
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
