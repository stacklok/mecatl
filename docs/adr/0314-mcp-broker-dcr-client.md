# ADR 0314 — Dynamic Client Registration for MCP broker upstreams

- Status: Proposed
- Date: 2026-09-08
- Scope: the OAuth client identity mecatl/ToolHive presents to a protected MCP
  upstream's own authorization server (`mcp.servers[].auth.oauth.client`)
- Supersedes: None
- Superseded by: None

## Context

`mcp.servers[].auth.oauth.client` is a closed tagged union of exactly two
modes: `preregistered` (an operator-obtained client id + secret) and `cimd`
(an HTTPS Client ID Metadata Document the operator hosts, whose URL doubles
as the client identifier — [ADR 0311](./0311-per-upstream-mcp-broker-oauth-grants.md),
[ADR 0312](./0312-confidential-toolhive-broker-client.md)). Both require the
operator to either register a client by hand with the upstream, or stand up
and host a static metadata document before mecatl can enroll a workspace
against that upstream.

Some protected upstreams — the trigger case is a ToolHive-hosted connector
gateway exposing an MCP endpoint behind an authorization server that
advertises a `registration_endpoint` (RFC 8414) and accepts public clients
(`token_endpoint_auth_methods_supported: ["none"]`, PKCE) — support RFC 7591
Dynamic Client Registration instead. Nothing needs to be hosted or
preregistered: the client registers itself against the upstream at first use
and receives ephemeral (from mecatl's perspective) credentials in return.

ToolHive's embedded authorization server, which mecatl already depends on for
the broker (`github.com/stacklok/toolhive/pkg/authserver`), already
implements DCR as an upstream client:
`authserver.OAuth2UpstreamRunConfig.DCRConfig` (mutually exclusive with
`ClientID`) resolves through ToolHive's dcr_adapter.go into its `pkg/auth/dcr`
registration/caching resolver. This is shipped, tested
library behavior mecatl does not need to reimplement — only expose.

This is not the same DCR [ADR 0219](./0219-mcp-oauth-sdk-profile.md) and
[ADR 0220](./0220-mcp-oauth-controller.md) excluded for the sibling
`internal/adapter/mcp` OAuth controller. That decision was scoped narrowly:
the go-sdk's DCR "resolution occurs inside each authorization flow and has
no registration persistence/reuse hook" (ADR 0219), so it was "qualified for
one flow only, not as durable" client identity, and ADR 0220 left it
disabled entirely because "no equivalent durable-registration/lifecycle gate
exists." `pkg/auth/dcr` is that gate: it carries its own `CredentialStore`
with cache-hit reuse, RFC 7591 §3.2.1 expiry-aware invalidation, and
singleflight-coalesced registration (ToolHive's dcr resolver and store) —
durable registration and reuse across calls, not
a per-flow throwaway client. The prior exclusion stands for the go-sdk path
it was written against; it does not apply to this different, already
durable ToolHive resolver.

Unlike `preregistered` or `cimd`, a DCR-obtained client identity is not
operator-asserted: whatever the upstream's authorization server hands back at
registration time is what mecatl/ToolHive uses. The operator is trusting the
upstream's own registration policy (which may itself require an initial
access token, or may be open) rather than vouching for a specific client
identity themselves. This is a genuine change in trust posture worth
recording, not an incidental config addition.

## Decision

Add `dcr` as a third `mcp.servers[].auth.oauth.client.mode`, mutually
exclusive with `preregistered` and `cimd` (the union stays closed at three
variants, not open-ended). A `dcr` client declares the upstream's
discovery/registration endpoints and mecatl passes them straight through to
ToolHive's existing `DCRUpstreamConfig` — mecatl performs no registration,
caching, or credential storage of its own; that is ToolHive's job, using the
same storage backend already selected for the broker's other OAuth state
(`mcpbroker.ToolHiveConfig.AuthRedisClient` / in-memory fallback).

DCR discovery and registration calls are HTTPS-only, with no per-server
escape hatch — the same invariant that already applies to every other OAuth
upstream call in this surface (`docs/usage/mecak8s.md`: "OAuth always
requires HTTPS and cannot use this escape hatch"). ToolHive's
`DCRUpstreamConfig.AllowPrivateIPs` / `InsecureAllowHTTP` knobs are not
exposed through mecatl's schema in this iteration; a future ADR can widen
that only if a concrete in-cluster DCR upstream needs it.

## Consequences

- An operator can point mecatl at a DCR-capable protected upstream (public
  client, RFC 7591) without hosting a CIMD document or hand-registering a
  confidential client.
- Client identity for a `dcr` server is decided by the upstream at runtime,
  not by the operator. A compromised or misbehaving upstream registration
  endpoint affects only that upstream's own credential, not mecatl's other
  configured servers or the separate confidential client ADR 0312 covers.
- mecatl's config schema, Helm chart schema/templates, and the
  `internal/app` → `internal/adapter/mcpbroker` conversion path each grow one
  more closed-union arm; existing `preregistered`/`cimd` behavior and tests
  are unaffected.
- Whether to plumb ToolHive's optional DCR initial-access-token support
  through mecatl's schema in this iteration is a scope decision recorded in
  the acceptance plan, not settled here.

## See also

- [ADR 0311 — Per-upstream MCP broker OAuth grants](./0311-per-upstream-mcp-broker-oauth-grants.md)
- [ADR 0312 — Confidential ToolHive broker client credentials](./0312-confidential-toolhive-broker-client.md)
- [ADR 0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md) — the DCR exclusion this ADR narrows, not reverses (different resolver, different durability guarantee)
- [ADR 0219 — MCP OAuth SDK profile](./0219-mcp-oauth-sdk-profile.md) — the "no registration persistence/reuse hook" rationale `pkg/auth/dcr` does not share
- [MCP broker DCR client acceptance plan](../acceptance/mcp-broker-dcr-client.md)
