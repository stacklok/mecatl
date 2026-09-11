# ADR 0325 — Durable Dynamic Client Registration for direct MCP profiles

- Status: Proposed
- Date: 2026-09-09
- Scope: local direct streaming-HTTP MCP authorization-code client identity, registration, and credential lifecycle
- Supersedes: Proposed narrowing of ADR 0219's Basic-only qualification and ADR 0220's direct-DCR exclusion only; neither is superseded until approval
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
it again using the restored encrypted credential. This establishes the selected
SDK/DCR/PKCE/login/persistence/MCP invocation happy path and deployed loopback-port variation,
but not refresh, wrong-path rejection, concurrency, or uncertain-outcome recovery. No
credential/authorization URL is recorded here. Further live qualification remains human-run,
not CI or implied authorization for an implementing agent to contact the gateway.

Broker DCR is a separate ToolHive-owned authority with explicit OAuth2 `upstream` and
`discovery_url`. Neither that lifecycle, `CallMcpWithQuery`, nor remote mecatui OIDC/keyring
work belongs to direct DCR.

## Decision

This ADR remains proposed and its [acceptance plan](../acceptance/direct-mcp-dcr.md) remains
**proposed**. Every human decision required for implementation is resolved and recorded; neither
document is approved or landed. The following observable policies have explicit user approval:

1. Retain `client.mode: dcr`, validating direct and broker shapes separately after authority
   selection while preserving broker compatibility. Direct requires exact `issuer` and no
   `upstream`; the concrete direct payload proposed in the plan is `dcr: {}`.
2. Persist registration separately from the grant. Grant reset and failed refresh preserve
   registration; replacing registration requires explicit reset.
3. Use existing credential-store CAS winner adoption, accepting possible duplicate upstream
   clients. Do not promise or build upstream exactly-once registration.
4. Reuse `mecated mcp login SERVER [--no-browser]` and `internal/app.LoginMCP`, followed by
   local mecatui consuming saved credentials. No new mecatui command or model-visible login
   tool; startup/reconnect/background refresh never register or launch consent.
5. Bind a stable random callback path to the registration and a fresh ephemeral IPv4 loopback
   port to each authorization. State and PKCE stay fresh; exact path/Host/state validation and
   bounded listener lifetime remain mandatory.
6. Default direct DCR to durable authorization: omitted `request_refresh_token` resolves to true and omitted scopes to `[openid, offline_access]`. Explicit false derives `[openid]` when scopes are omitted; explicit scopes must match the effective setting. Never add implicit `profile`/`email`. These defaults do not change preregistered, CIMD, or broker profiles, and refresh issuance is required only for an effective refresh-enabled profile.
7. An unknown registration POST outcome is recovery-required, with durable evidence and no
   automatic retry. A new POST requires explicit operator retry acknowledging possible orphans.
8. `--reset-dcr-registration` applies only to a valid registration and its grant, while
   `--retry-dcr-registration` applies only to a valid unresolved attempt. Each conditional
   local action proceeds to login and neither deletes or revokes an upstream client; grant-only
   reset remains the internal `ResetCredential` seam with no new CLI modifier.
9. A pending record is written before registration POST and transitions to ready by CAS. A
   competing login adopts a ready winner or stops recovery-required while pending, without
   joining or polling. Explicit retry replaces pending with a fresh generation and fences an
   old publication, though an already-sent upstream request may orphan a client. A valid ready
   winner prevails over a late uncertain loser; retain only current and immediately previous
   attempt metadata, with no lease, clock takeover, lock service, attempt index, or exactly-once
   claim.

The exact proposed APIs, config, persistence formats/hash domains, CLI grammar, state matrix,
and named offline proofs are in the acceptance plan. This avoids independent, drifting copies
of the contract. Registration-binding versus current-endpoint validation and fail-closed
corrupt-state handling are resolved contract choices; arbitrary API names are not human-policy
questions.

### Existing seams, selected public SDK constraints

Add an empty direct DCR arm to `OAuthClientConfig`; do not reinterpret its confidential
`Preregistered` arm. A host-only preparation helper obtains the durable callback path and
private one-POST ticket before the existing runtime binds its listener. Inside the login
callback, controller registration resolution uses the official `oauthex.RegisterClient`
with the actual bound URI, persists ready state, and supplies a resolved public
`oauthex.ClientCredentials` to the existing SDK handler's `PreregisteredClient` seam.
`DynamicClientRegistrationConfig` stays nil. Ordinary construction may restore ready state
but has no ticket to POST. No new store interface or second OAuth stack is introduced.

The callback extension is per-call, not shared mutable runtime configuration. Existing
random-path and fixed-redirect callers retain their behavior. A gateway that rejects port
variation fails where the server exposes the rejection (authorization or exchange); the
client never silently re-registers or claims the rejection was necessarily detected earlier.

Public credentials must have nil `ClientSecretAuth`, no Basic header, no `client_secret`,
and no client assertion on real code/refresh wire requests. Persisted public auth style must
be explicit, not auto-detected. Existing confidential preregistered Basic-only/form-secret
rejection and the broker's hardened Basic token client remain unchanged. SDK API availability
is not qualification: an unsupported public exchange or resource-bound persistent refresh
blocks implementation until an official supported seam is available, not a local replacement
OAuth implementation.

Require exact canonical RFC 9728 resource and sole configured issuer, exact AS issuer,
S256, code, authorization-code and `none`, plus refresh grant/AS `offline_access` support
when requested. No resource-origin discovery fallback or SDK trailing-slash issuer tolerance
may weaken those bindings. HTTPS registration/authorization/token endpoints stay on the exact
issuer origin, with existing DNS pinning, no proxy, TLS, bounds, and redacted errors.
Registration POST has no redirect or automatic retry and no initial access credential.

The effective direct-DCR scope allowlist is `[openid, offline_access]` by default: omitted `request_refresh_token` resolves to true and omitted scopes derive from that setting. Explicit false derives `[openid]` when scopes are omitted; explicit scopes must match the effective setting. Profile decoding therefore preserves refresh-field presence before resolving the existing runtime boolean. These defaults are DCR-only. PRM and AS must advertise `openid`; only AS advertisement is required for `offline_access`. ScopeFilter alone is not a final security gate: the SDK adds offline access and unions step-up scopes afterwards. The final authorization request must still match the admitted set, and authorization, code exchange, and refresh must bind exactly the canonical resource. Missing requested refresh issuance never reports durable-refresh success.

### Proposed bounded CAS lifecycle, not an exactly-once lock

One deterministic identity-keyed registration control record, in the existing encrypted
`mecatl-mcp-oauth` namespace, is either pending or ready. A create-only pending write with
random generation/path precedes any POST. The successful writer gets a one-use in-process
ticket; a competing login adopts a valid ready winner or stops recovery-required if pending.
There is no lease, PID, time-based takeover, polling service, append log, or attempt index.
This proposed lifecycle suppresses ordinary competing pending POSTs but does not establish
exactly-once. Its pending-contender behavior is a resolved contract choice.

POST/save uncertainty leaves durable pending evidence. Explicit retry CAS-replaces pending
with a fresh generation, retaining one predecessor summary; explicit registration reset does
the same from ready. Old processes cannot publish against the replacement generation, though
an already-issued upstream POST may create an orphan. A late error cannot overwrite a ready
winner, and no conflict rebases an old result onto a new version. A validated ready result
in the same generation can be adopted after an uncertain save; otherwise stop, never POST
again automatically. A crash before POST conservatively leaves recovery-required state.

Registration and grant are separate CAS records, not a transaction. The proposed DCR grant
key includes resolved client ID **and registration generation**. Grant-only reset writes a
versioned reset tombstone, including when a grant has not yet been issued; authorization
captures that version before exchange. Conflicts adopt only a valid active current-generation
winner, never a reset tombstone. Registration reset makes old-generation grants unreachable;
a late old-key write is inert, not resurrected current state. Token consumers recheck durable
grant/reset state and registration generation. An already-dispatched request may complete;
local reset does not revoke credentials upstream.

Transient refresh network failure preserves registration and grant evidence; `invalid_grant`
or internal grant reset tombstones the grant without replacing the client. No grant-only
CLI modifier is added. Corrupt/unsupported/identity-mismatched registration payloads remain
recovery-required even if the store supplies an authenticated version. Neither registration
reset nor retry may replace them; generic backend corruption, wrong keys and unavailability
also fail closed, never as a cache miss or a validation bypass. Separate operator repair is
required. The predecessor schema stays exactly generation, attempt timestamp, and reason
`explicit_retry` or `explicit_reset`: there is no corrupt-reset/digest variant or force flag.

The registration-binding fingerprint covers issuer/resource, scopes, authentication method,
grants/response types and redirect policy/path. Cosmetic client name and current registration,
authorization and token endpoints are excluded. Intentional binding changes require explicit
registration reset; same-issuer endpoint rotation or cosmetic name drift alone does not replace
the client or change registration generation. Current endpoints are separately rediscovered and
revalidated under the existing exact issuer-origin policy. Stored grant `refresh.token_url`
still passes existing token credential URL/origin checks and hardened egress validation;
registration fingerprint equality never overrides those checks or blindly rewrites the stored
endpoint. If rotation makes a grant unusable, fail/reauthorize at the grant layer while retaining
registration. Corrupt grants remain recovery-required without a new CLI repair path.

## Consequences

Direct DCR can eventually support this gateway without borrowing broker authority or weakening
confidential-client behavior. It adds encrypted registration control, generation-bound grants,
and reset tombstones to the existing local store dependency. Preregistered/CIMD version-1
credentials and their hash keys remain unchanged. There is no legacy direct DCR migration or
client-ID inference from grants.

The plan records resolved observable policies alongside proposed interfaces and implementation
proofs. It is proposed and awaits human plan/interface review, not approval or implementation.
Its offline fixtures must exercise the real profile loader, SDK, controller, callback runtime,
encrypted store and second-load MCP call; the standalone live probe is not a substitute. Nothing
here changes the remote OIDC keyring, MCP broker/query agent, public protobuf API, or runtime in
this contract-only task.

## See also

- [ADR 0219 — Qualify the official MCP SDK authorization-code profile](./0219-mcp-oauth-sdk-profile.md)
- [ADR 0220 — Adapter-local MCP OAuth controller](./0220-mcp-oauth-controller.md)
- [ADR 0314 — Dynamic Client Registration for MCP broker upstreams](./0314-mcp-broker-dcr-client.md)
- [Direct MCP Dynamic Client Registration acceptance plan](../acceptance/direct-mcp-dcr.md)
- [Architecture — internal credential store](../architecture.md#internal-credential-store)
