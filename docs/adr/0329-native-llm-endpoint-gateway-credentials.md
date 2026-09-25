# ADR 0329 — Native LLM endpoint gateway credentials

- Status: Accepted
- Date: 2026-09-10
- Scope: operator-defined native LLM endpoint configuration, gateway credential custody, and lifecycle
- Supersedes: the native ownership/configuration portions of ADR 0238

## Context

ADR 0238 established generic operator-defined LLM providers with `none` or API-key
authentication. An organizational OpenAI Responses gateway instead needs a durable,
interactive OIDC credential lifecycle while retaining the existing composition-owned
registry and durable provider/model selector. ToolHive already owns a distinct gateway
provider lifecycle (ADR 0064 and ADR 0102); replacing or aliasing it would break
existing modes, MCP discovery, and sessions.

## Decision

Use the operator-only `llm.endpoints.ID` facade for native LLM endpoints. Normalize it
once into the existing provider-definition and registry pipeline. An endpoint has one
deployment-scoped gateway credential under the explicit `llm.credential_home`; its
protected record is bound to the canonical gateway, exact issuer, OIDC identity,
normalized scopes, redirect, and independent issuer/gateway trust identities.

Embedded local mecatui alone performs Authorization Code + PKCE S256 enrollment. A
standalone mecated only uses/refreshes an existing record. The endpoint lifecycle holds
an endpoint-scoped transaction lock across record load, exchange, and CAS commit, while
access tokens remain process-local. Status is passive and local; logout makes local
delete authoritative before best-effort revocation. V1 accepts only the authorization
code exchange access token, never an ID token or inbound caller bearer.

Inventory and endpoint availability are deployment-wide. Caller OIDC verifies a
principal for ownership, then drops the raw bearer; it neither entitles nor filters
endpoints and never forwards or retains caller credentials. Each admitted caller shares
the endpoint's gateway identity, quota, gateway-side audit/retention posture, and model
availability. Operators use a dedicated deployment/service identity. Mutually untrusted
or per-user upstream authorization requires separate deployments until a future explicit
forwarded-token or RFC 8693-style token-exchange contract.

Keep ToolHive's identity, modes, persisted sessions, and lifecycle intact. Native and
ToolHive credentials never migrate, discover, copy, or fall back to one another. The
one-release bare login alias remains ToolHive-only; neither lifecycle prints a token.

## Consequences

The native facade has a strict but intentionally narrow schema and host boundary. An
identity change makes its durable selections unavailable rather than silently rebinding.
A crash after provider-side refresh rotation and before local CAS may require login.
There is no remote login RPC, per-principal endpoint inventory, generic opaque-token
heuristic, device flow, DCR, client secret, TLS bypass, or credential migration.

This supersedes only ADR 0238's native ownership/configuration extension. ADR 0238
continues to govern its generic API-key/none provider definitions; ADRs 0064 and 0102
continue to govern ToolHive LLM lifecycle and direct mode respectively.

## See also

- [ADR 0016](./0016-multi-provider.md) — composition-owned registry
- [ADR 0064](./0064-toolhive-llm-gateway-provider.md) and [ADR 0102](./0102-toolhive-direct-mode.md)
- [ADR 0218](./0218-credential-store.md) — encrypted CAS store
- [Architecture: providers](../architecture/providers.md)
- [Run mecated standalone](../../user-docs/building/deployment/mecated.md) — native LLM endpoint operation
