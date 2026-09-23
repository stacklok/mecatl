# ADR 0311 — ToolHive-owned multi-upstream MCP broker OAuth

- Status: Accepted
- Date: 2026-09-03
- Scope: session-scoped MCP broker OAuth authorization and workspace enrollment
- Supersedes: None
- Superseded by: ADR 0310 (pre-prompt-only static-tool admission decision only)

## Context

The session-scoped broker currently rejects more than one OAuth upstream. That
restriction is duplicated in configuration admission and ToolHive construction.
Yet ToolHive's embedded authorization server accepts an ordered `Upstreams`
configuration, persists each upstream's authorization state, refreshes tokens
by provider, and injects the selected provider's token only into the backend
configured for that provider.

Mecatl must not duplicate that controller. A second implementation of callback
state, PKCE, authorization-code exchange, refresh, provider routing, or
upstream-token custody would create two conflicting sources of truth and make a
single ToolHive authorization chain appear as several model/client-visible
workflows. In particular, copying an outer broker credential into backend
specific mecatl grants is not a valid representation of upstream
authorization.

The broker still needs a mecatl-owned session boundary: the operator configures
the allowed upstreams, a session may be held behind a pre-prompt enrollment
gate, and the authenticated capability catalogue must be admitted and frozen
before it reaches the model. A shared public callback endpoint must not expose
or accept an upstream selector.

## Decision

Allow more than one OAuth MCP upstream in broker mode. **ToolHive owns the
entire upstream authorization chain**; mecatl treats it as one opaque broker
operation.

### ToolHive responsibilities

- ToolHive receives every protected upstream configuration, including the
  provider identity, client configuration, scopes, endpoints, and the backend
  injection binding.
- ToolHive owns ordered upstream presentation, opaque callback-state
  correlation, PKCE, authorization-code exchange, upstream-token storage,
  refresh, and provider-specific injection.
- A ToolHive token for backend `A` is injected only into `A`; it never
  authorizes backend `B`, even if their scopes, issuer, or client configuration
  match.
- ToolHive reports completion only after every configured upstream required for
  the operation has authorized successfully. A failed or expired member makes
  the ToolHive operation fail; ToolHive retains only the token lifecycle state
  it needs to retry or refresh according to its own contract.

### Mecatl responsibilities

- Mecatl validates operator configuration, requires broker/global MCP
  exclusivity, converts the profiles to ToolHive configuration, owns the
  ToolHive process lifetime, and mounts its fixed HTTP handler bundle.
- Mecatl exposes one safe bundle-level session control: begin, observe, and
  cancel. Its public projection contains only the opaque enrollment reference,
  aggregate status, configured-service count, and ephemeral presentation URL.
  It never exposes an upstream name/provider key, OAuth state, code, endpoint,
  access token, or refresh token.
- Mecatl retains the session ownership, lease, and pre-prompt gate while the
  ToolHive operation is pending. It may use a broker-local credential to call
  ToolHive, but it does not interpret that credential as an upstream grant or
  store it per backend.
- On ToolHive completion, mecatl performs authenticated discovery through the
  ToolHive process, collision-checks the resulting capabilities, freezes one
  model-visible catalogue, persists the successful enrollment binding, and
  rebuilds the session engine. Normal mecatl permissions then govern tool use.
- Mecatl does not implement per-upstream pending authorizations, callback
  routing, grant copying, token refresh, or sequential-next-backend workflow.

The callback endpoints have two distinct roles on the same public broker origin:

- The operator-configured callback URL is ToolHive's final redirect to mecatl's
  registered broker client after the upstream chain completes.
- Every upstream provider uses ToolHive's fixed shared callback endpoint under
  the mounted broker handler prefix. ToolHive's opaque state machine consumes
  that callback; neither a URL path nor any model/client-supplied field selects
  a backend.

## Consequences

Users can encounter multiple upstream consent pages during one ToolHive-driven
browser authorization, but mecatl presents one enrollment operation. The
browser's progression and all per-provider state are ToolHive implementation
details; the only mecatl-visible terminal outcomes are the aggregate operation
states.

The multi-upstream implementation in mecatl is deliberately small:

1. remove the local one-protected-upstream rejection;
2. supply all protected profiles to ToolHive; and
3. replace local per-backend OAuth/grant/callback state with a single opaque
   ToolHive-operation binding before admitting its completed catalogue.

Acceptance tests must use two independent fake OAuth/MCP upstreams with the
real ToolHive embedded authorization server and mecatl service boundary. They
must prove that ToolHive completes the chain, mecatl publishes no partial
catalogue, each backend receives only its own upstream token, a single-backend
refresh does not affect the other, and no second mecatl authorization workflow
is created.

The configured broker authority remains exclusive of global programmatic
`MCPServers`. This decision changes neither the in-process-only broker scope
nor the future remote/durable broker boundary.

## Deferred

- ToolHive's detailed retry, retry-after, and multi-tab browser presentation
  policy is owned by ToolHive, not exposed as a mecatl contract.
- Remote broker injection, durable broker ownership, and multi-replica routing
  remain separate decisions.
- Sharing an upstream token across differently configured backends is
  prohibited.

## See also

- [ADR 0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md)
- [ADR 0291 — Server-owned session placement](./0291-server-owned-session-placement.md)
- [Architecture guide](../architecture.md)
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)
