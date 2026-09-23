# ADR 0220 — Adapter-local MCP OAuth controller

- Status: Accepted
- Date: 2026-08-14
- Scope: streaming-HTTP MCP authorization-code lifecycle, persistence, and egress policy
- Supersedes: none
- Superseded by: none

## Context

ADR 0219 qualified the official MCP Go SDK's public authorization-code behavior and
identified limits that cannot be corrected safely through its current hooks. The MCP
adapter still needed one owner for durable tokens, concurrent authorization challenges,
reconnect reuse, and OAuth-specific network policy without implementing OAuth discovery
or token exchange itself.

The SDK does not expose validated discovery metadata before presentation, durable DCR
registration hooks, or automatic reauthorization after refresh `invalid_grant`. Parsing
those protocol responses locally would create a second OAuth implementation and falsely
suggest a broader production profile than the dependency supports.

## Decision

Add an optional `OAuthController` in `internal/adapter/mcp`. It wraps and retains exactly
one official `auth.AuthorizationCodeHandler` per `Server`; initial dial and reconnect use
the same controller. The controller supports preregistered confidential clients and CIMD
only. DCR has no configuration surface.

The controller borrows an explicitly injected credential store. A versioned strict
credential envelope is keyed by profile, principal, canonical resource, exact issuer,
registration kind, and client identifier. New grants and lazy refresh rotation use the
store's compare-and-swap operations; conflicts adopt the validated winner. Terminal
`invalid_grant` conditionally deletes the stale record and returns a typed, redacted
login-required error. `ResetCredential` uses the same conditional-delete discipline.
`Close` is idempotent, closes only the controller's HTTP idle pool, and never closes the
borrowed store.

Authorization challenges are coalesced per controller. The completed safe outcome is
retained under a secret-free digest of the request credential plus response status and
challenge, so callers that were concurrently issued but arrive after the leader completed
still share that outcome. A changed credential/challenge or `ResetCredential` invalidates
that equivalence and may start a new flight. Waiters retain their own context bounds; the
first live caller may take over after a cancelled leader while peers join that replacement.
A nil presenter is the noninteractive posture: close the challenge response and immediately
return the typed login-required error. This ADR adds no presenter implementation.

OAuth protocol traffic uses a dedicated no-proxy HTTP client. Exact configured origins
are checked before DNS, every DNS answer is validated, mixed safe/unsafe answers fail,
and the validated addresses are pinned into the dial. Private addresses require an exact
per-origin opt-in. TLS, dial, response-header, total-time, idle-pool, and redirect bounds
are explicit. Resource and additional origins may receive only credential-free discovery
GET/HEAD requests. Authorization presentation is accepted only at the canonical configured
issuer origin; every protocol POST, `Authorization` header, authorization code, refresh
token, client assertion, or token-exchange request is rejected before dialing unless its
destination is that issuer origin. Preregistered confidential clients additionally require
HTTP Basic, and `client_secret_post` is always rejected before dialing. The MCP resource
transport has a separate exact-resource capability for its audience-bound bearer; merely
appearing in the discovery allowlist does not grant that capability. Redirects are
same-origin GET/HEAD only and reject credential-bearing or POST redirects. In OAuth mode
the separate MCP client also rejects cross-origin redirects before a bearer can be
reattached. OAuth-disabled and static-header behavior remains the existing path; static
`Authorization` and OAuth are mutually exclusive.

Continue delegating protected-resource discovery, authorization-server metadata parsing,
PKCE, challenge interpretation, browser URL construction, callback validation inputs,
code exchange, and refresh HTTP to the official SDK and `x/oauth2`. The SDK can select an
authorization server from protected-resource metadata or fallback behavior without exposing
validated metadata first; therefore the configured issuer is enforced at the presenter and
HTTP egress boundaries rather than treated as proof of SDK selection. CIMD remains supported
only because this gate prevents a selected foreign server from receiving a code or token;
DCR remains disabled because no equivalent durable-registration/lifecycle gate exists. No
profile may weaken the exact-issuer egress rule. Do not claim broad production readiness
until the ADR 0219 metadata-profile blockers are fixed upstream and requalified.

## Consequences

Embeddings inside the root module can supply a presenter, identity, store, and constrained
client profile without rebuilding OAuth protocol logic. Reconnect retains credential and
singleflight state, and returned errors never retain SDK, store, endpoint, or credential
causes.

There is deliberately no browser/callback listener, CLI or settings schema, composition
wiring, ACP surface, engine port, or protobuf change. DCR and automatic refresh-failure
login are unsupported. Operator-facing deployment remains deferred.

## See also

- [ADR 0219 — Qualified official SDK profile](./0219-mcp-oauth-sdk-profile.md)
- [ADR 0218 — Internal encrypted credential store](./0218-credential-store.md)
- [Extensibility architecture](../architecture/extensibility.md)
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md)
