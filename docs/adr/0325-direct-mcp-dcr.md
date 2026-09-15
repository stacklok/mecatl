# ADR 0325 — Durable Dynamic Client Registration for direct MCP profiles

- Status: Accepted
- Date: 2026-09-09
- Amended: 2026-09-10 — direct DCR v1 is no-refresh; complete public-client refresh is deferred to issue #1355
- Scope: local direct streaming-HTTP MCP authorization-code client identity, registration, and no-refresh access-grant lifecycle
- Supersedes: ADR 0219's Basic-only qualification and ADR 0220's direct-DCR exclusion only for the constrained public no-refresh profile defined here
- Superseded by: None

## Context

Direct MCP OAuth already has an adapter-local controller, hardened HTTP transport, official
SDK authorization-code/PKCE integration, host-owned loopback login, and encrypted credential
persistence for preregistered confidential and CIMD clients. Token identity includes a known
client ID. The SDK's per-authorization DCR hook supplies no durable registration/reuse hook;
using it directly would create registration identity too late and could re-register on login.

The connector gateway's canonical resource is
`https://connector-gateway.example.com/gw/mcp`. Discovery advertised public DCR, code,
authorization-code and refresh grants, S256, and `none`. An explicitly authorized standalone
Python probe registered one public client and completed two browser authorizations/code
exchanges on the same random callback path with two ports, both different from the registered
port. It requested only `openid` and `authorization_code`, used S256 and no secret, and
discarded tokens. A subsequent operator-driven Go spike used `mecated mcp login` and local
mecatui against the deployed gateway, discovered the real connector tool catalog, invoked the
harmless Excalidraw `read_me` tool, restarted mecatui without another registration, and invoked
it again using the restored encrypted access credential. This establishes the selected
SDK/DCR/PKCE/login/persistence/MCP invocation happy path and deployed loopback-port variation,
but not wrong-path rejection, concurrency, uncertain-outcome recovery, or refresh.

Implementation qualification found two dependency gaps. The pinned SDK delegates persistent
refresh to `oauth2.Config.TokenSource`, which cannot attach the required RFC 8707 `resource`.
It also maps a restored public client to `AuthStyleAutoDetect`, whose first token request uses
Basic with an empty secret before retrying with `client_id` in form parameters. The human
explicitly selected a no-refresh first delivery and deferred complete resource-bound
public-client refresh to [issue #1355](https://github.com/stacklok/mecatl/issues/1355).

Broker DCR is a separate ToolHive-owned authority with explicit OAuth2 `upstream` and
`discovery_url`. Neither that lifecycle, `CallMcpWithQuery`, nor remote mecatui OIDC/keyring
work belongs to direct DCR.

## Decision

1. Retain `client.mode: dcr`, validating direct and broker shapes separately after authority
   selection while preserving broker compatibility. Direct requires exact `issuer`, no
   `upstream`, and `dcr: {}`.
2. Persist registration separately from the access grant. Grant reset and access-token expiry
   preserve registration; replacing registration requires explicit reset.
3. Use existing credential-store CAS winner adoption, accepting possible duplicate upstream
   clients. Do not promise or build upstream exactly-once registration.
4. Reuse `mecated mcp login SERVER [--no-browser]` and `internal/app.LoginMCP`, followed by
   local mecatui consuming saved credentials. No new mecatui command or model-visible login
   tool; startup/reconnect/background work never registers, refreshes, or launches consent.
5. Bind a stable random callback path to the registration and a fresh ephemeral IPv4 loopback
   port to each authorization. State and PKCE stay fresh; exact path/Host/state validation and
   bounded listener lifetime remain mandatory.
6. Direct DCR v1 is no-refresh. Omitted `request_refresh_token` resolves false; explicit false
   is accepted and explicit true is rejected. Omitted scopes resolve `[openid]`; explicit
   scopes must equal exactly `{openid}`. Never request or accept `offline_access`, the
   refresh-token grant, or an issued refresh token. Broker, preregistered, and CIMD behavior
   remains unchanged.
7. An unknown registration POST outcome is recovery-required, with durable evidence and no
   automatic retry. A new POST requires explicit operator retry acknowledging possible orphans.
8. `--reset-dcr-registration` applies only to a valid registration and its grant, while
   `--retry-dcr-registration` applies only to a valid unresolved attempt. Each conditional
   local action proceeds to login and neither deletes nor revokes an upstream client; grant-only
   reset remains the internal `ResetCredential` seam with no new CLI modifier.
9. A pending record is written before registration POST and transitions to ready by CAS. A
   competing login adopts a ready winner or stops recovery-required while pending. Explicit
   retry replaces pending with a fresh generation and fences an old publication. Retain only
   current and immediately previous attempt metadata; add no lease, clock takeover, lock
   service, attempt index, or exactly-once claim.

The exact APIs, config, persistence formats/hash domains, CLI grammar, state matrix, and named
offline proofs live in the [acceptance plan](../acceptance/direct-mcp-dcr.md).

### Official SDK boundary and public token exchange

Add an empty direct DCR arm to `OAuthClientConfig`; do not reinterpret its confidential
`Preregistered` arm. A host-only preparation helper obtains the durable callback path and
private one-POST ticket before the runtime binds its listener. Controller registration uses
the official `oauthex.RegisterClient`, persists ready state, and supplies the resolved public
client through the SDK handler's `PreregisteredClient` seam. The SDK's per-flow
`DynamicClientRegistrationConfig` stays nil. Ordinary construction may restore ready state
but has no ticket to POST. No new store interface or second OAuth stack is introduced.

Public credentials have nil `ClientSecretAuth`. Initial authorization uses S256 and exactly
one canonical RFC 8707 `resource`. The pinned SDK maps a restored public client to
`AuthStyleAutoDetect`; for direct DCR only, the hardened transport rejects the SDK's Basic
probe locally before dialing. It does not strip and forward the malformed request. The SDK
then performs its parameter fallback, which is admitted only with the exact expected public
`client_id` and no Basic header, `client_secret`, client assertion, or other authentication
field. A real-wire fixture must prove zero Basic requests reached upstream and one valid
parameter-form exchange succeeded. This bounded transport mediation is accepted reuse of the
official SDK flow, not a generic local OAuth implementation. Existing confidential
preregistered Basic-only/form-secret rejection remains unchanged.

Require exact canonical RFC 9728 resource and sole configured issuer, exact AS issuer, S256,
code, authorization-code and `none`. AS support for `refresh_token` or `offline_access` is not
required. No resource-origin discovery fallback or SDK trailing-slash issuer tolerance may
weaken those bindings. HTTPS registration/authorization/token endpoints stay on the exact
issuer origin, with existing DNS pinning, no proxy, TLS, bounds, and redacted errors.
Registration POST has no redirect or automatic retry and no initial access credential.

### Bounded CAS lifecycle

One deterministic identity-keyed registration control record in the encrypted
`mecatl-mcp-oauth` namespace is pending or ready. A create-only pending write with random
generation/path precedes any POST. The successful writer gets a one-use in-process ticket; a
competing login adopts a valid ready winner or stops recovery-required if pending. POST/save
uncertainty leaves durable pending evidence. Explicit retry or registration reset CAS-replaces
the old record with a fresh generation, retaining one predecessor summary. Old processes
cannot publish against the replacement generation, though an already-issued request may
create an upstream orphan.

Registration and grant are separate CAS records, not a transaction. The DCR grant key includes
resolved client ID and registration generation. Grant-only reset writes a versioned reset
tombstone, including before the first grant; authorization captures that version before code
exchange. Conflicts adopt only a valid active current-generation winner, never a reset
tombstone. Registration reset makes old-generation grants unreachable. Token consumers
recheck durable grant/reset state and registration generation before returning a token.

A DCR version-2 active grant contains only an access token and strictly named authorization
metadata needed to validate the non-refresh credential. It forbids `refresh_token`. Runtime
restoration uses a DCR-specific non-refresh token source and never constructs
`oauth2.Config.TokenSource`. Expiry returns the existing safe login-required category without
network refresh or browser launch. Explicit login reuses the registration/path with a fresh
port/state/PKCE. An unsolicited refresh token makes login fail before persistence.

Corrupt, unsupported, or identity-mismatched registration/grant payloads remain
recovery-required. Neither registration reset nor retry may reinterpret or replace corrupt
state. Generic backend corruption, wrong keys, and unavailability fail closed. Local reset is
not upstream revocation, and no cross-record atomicity is claimed.

## Consequences

Direct DCR supports explicit public-client registration, authorization, restart-before-expiry,
and reauthorization without borrowing broker authority or weakening confidential-client
behavior. It adds encrypted registration control, generation-bound access grants, and reset
tombstones. Preregistered/CIMD version-1 credentials and hash keys remain unchanged. There is
no legacy direct-DCR migration or client-ID inference from grants.

An expired direct-DCR access token requires explicit `mecated mcp login`; ordinary mecatui
startup reports login-required and performs no refresh or hidden consent. Complete
resource-bound public-client refresh, including rotation and restart qualification, is deferred
to [issue #1355](https://github.com/stacklok/mecatl/issues/1355).

## See also

- [ADR 0219 — Qualify the official MCP SDK authorization-code profile](./0219-mcp-oauth-sdk-profile.md)
- [ADR 0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md)
- [ADR 0314 — Dynamic Client Registration for MCP broker upstreams](./0314-mcp-broker-dcr-client.md)
- [Direct MCP Dynamic Client Registration acceptance plan](../acceptance/direct-mcp-dcr.md)
- [Issue #1355 — resource-bound public-client OAuth refresh](https://github.com/stacklok/mecatl/issues/1355)
- [Architecture — internal credential store](../architecture.md#internal-credential-store)
