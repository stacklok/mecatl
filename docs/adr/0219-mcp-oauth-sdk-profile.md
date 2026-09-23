# ADR 0219 — Qualify the official MCP SDK authorization-code profile

- Status: Accepted
- Date: 2026-08-14
- Scope: official MCP Go SDK authorization-code client qualification
- Supersedes: none
- Superseded by: none

## Context

mecatl's MCP client is streaming-HTTP only and currently has no OAuth controller. Before
adding one, issue #520 qualified the public authorization-code surface of
`github.com/modelcontextprotocol/go-sdk`: `auth.AuthorizationCodeHandler` attached to
`mcp.StreamableClientTransport`. The root module pins
[`0d0cdbc943f396aa973023a7dd6440398d3df87e`](https://github.com/modelcontextprotocol/go-sdk/commit/0d0cdbc943f396aa973023a7dd6440398d3df87e).
The available upstream snapshot
[`64e454e35c23c473e1fcf1e1c3a6f623260ca773`](https://github.com/modelcontextprotocol/go-sdk/commit/64e454e35c23c473e1fcf1e1c3a6f623260ca773)
has byte-identical authorization-code implementation; its intervening streamable
transport changes do not correct the discrepancies below. No SDK bump is justified by
this qualification.

The hermetic public-API contract lives in
`internal/adapter/mcp/oauth_sdk_fixture_test.go` and
`internal/adapter/mcp/oauth_sdk_qualification_test.go`. It exercises real loopback
Streamable HTTP, protected-resource and authorization-server endpoints without a live
identity provider or network dependency.

## Decision

Qualify a deliberately constrained profile:

1. Require RFC 9728 protected-resource metadata. Its `resource` must exactly equal the
   canonical MCP endpoint, and it must advertise exactly one authorization server. The
   SDK falls back to treating the protected-resource origin as an authorization server
   when metadata is absent; future mecatl production wiring **must reject that fallback
   and require RFC 9728 metadata before calling the SDK**. The qualification test retains
   evidence that the SDK deterministically selects only the first advertised entry.
   Future mecatl production wiring **must reject multiple authorization servers before
   calling the SDK**; this dependency-only issue does not add either product-policy gate.
2. Require PKCE S256 and RFC 8707 `resource` on both authorization and token requests.
   The authorization server must advertise **only `client_secret_basic`** and enforce it;
   a confidential client must never downgrade its secret into the token form. The caller's
   `AuthorizationCodeFetcher` owns exact callback URI validation and must sanitize
   transport failures so its authorization URL and query are absent from returned errors.
   The handler validates state and RFC 9207 issuer responses.
3. Treat scopes as caller policy. `ScopeFilter` narrows discovered scopes; a step-up may
   add only challenge scopes while preserving scopes actually granted previously.
4. The qualified registration matrix is client-ID metadata document (CIMD) when the AS
   advertises support, preregistration when CIMD is unavailable, and DCR only when the AS
   advertises a registration endpoint. CIMD wins over simultaneously configured
   preregistration and DCR; preregistration wins over DCR; a preregistered issuer mismatch
   is terminal; and no AS-supported mechanism fails before browser or token work. Prefer
   preregistration or CIMD. DCR is qualified for one flow only, not as durable
   registration.
5. Retain one handler for the transport lifetime. Restore persisted credentials through
   `InitialTokenSource`; wrap and persist newly exchanged/refreshed tokens only through
   `NewTokenSource`. Qualification covers an accepted restored token, a rejected restored
   token followed by authorization, and a subsequent MCP request lazily refreshing and
   using the replacement token after the original authorization context is cancelled. A
   returned token source owns no untracked goroutine or closeable resource.
6. Supply one caller-owned bounded HTTP client to the handler and transport: total and
   response-header timeouts, an outbound-origin policy, and redirects disabled unless a
   same-allowlisted-origin policy explicitly permits them. Public-transport tests cover
   origin rejection, cross-origin and loop rejection for protected-resource metadata,
   response-header timeout during AS metadata, and total-timeout stalls during resource
   metadata, authorization, token exchange, and registration. Redirect behavior at every
   other endpoint is a production-client requirement, not a separately qualified SDK
   path. The SDK's 1 MiB metadata decoder bound is additive, not an SSRF or timeout policy.

This ADR qualifies dependency behavior only. It adds no production OAuth wiring,
configuration, callback listener, browser UX, credential store, or ACP behavior. A
future controller under parent issue #518 owns those decisions.

Confirmed pinned-SDK discrepancies are constrained rather than reimplemented locally:

- An initialize request whose authorized retry is rejected again is replayed once at
  the JSON-RPC connection layer: four protected-resource attempts and two authorization
  flows are observed for repeated 401. Repeated eligible `insufficient_scope` 403 is
  separately bounded to one step-up authorization and one rejected step-up request. These
  tests are upper-bound evidence, not support for repeated rejection; production excludes
  both paths pending an upstream correction. The relevant public transport implementation
  is the
  [upstream streamable transport source](https://github.com/modelcontextprotocol/go-sdk/blob/0d0cdbc943f396aa973023a7dd6440398d3df87e/mcp/streamable.go).
- With `client_secret_basic` alone advertised, the handler uses HTTP Basic. When the AS
  also advertises `client_secret_post`, however, the pinned SDK sends the confidential
  client secret in the token form instead. Production must accept an exclusively
  `client_secret_basic` metadata value and reject a mixed-method response before SDK
  invocation; the fixture records the form-secret request without including credential
  material in any failure. This is an SDK discrepancy, not a local protocol behavior to
  reimplement. An official fix must be consumed as a pinned revision and requalified.
- When RFC 9728 protected-resource metadata is unavailable, the pinned SDK falls back to
  the protected-resource origin as the authorization server. The fixture characterizes
  the fallback using a metadata-less resource endpoint. Future mecatl wiring must require
  RFC 9728 metadata and reject this legacy fallback before production use; do not add a
  local replacement discovery flow.
- Authorization-server metadata validation accepts a non-empty PKCE-method list that
  omits S256. The fixture authorization server rejects the resulting request; production
  support requires an AS that advertises and enforces S256. See the
  [upstream authorization-code handler](https://github.com/modelcontextprotocol/go-sdk/blob/0d0cdbc943f396aa973023a7dd6440398d3df87e/auth/authorization_code.go).
- For a 403 other than `insufficient_scope`, the handler starts no authorization but the
  transport-level operation is retried once; the initialize replay makes that four
  protected-resource attempts in total through public `Connect`. The suite reproduces
  this with an `invalid_token` challenge solely as pinned-SDK evidence. Arbitrary 403
  handling is excluded from production until corrected or explicitly mediated in the
  official SDK; the observed retries are not desired behavior.
- A lazy refresh returning `invalid_grant` fails the MCP request before it reaches the
  resource and does not trigger reauthorization through the public transport. Automatic
  refresh-failure reauthorization is therefore not qualified; future production wiring
  must either consume an upstream disposition or explicitly re-enter authorization at
  the controller boundary.
- DCR resolution occurs inside each authorization flow and has no registration
  persistence/reuse hook. Durable DCR is excluded.
- Redirect and destination-origin policy is delegated to the supplied `http.Client`.
  The default client is insufficient for future production wiring.
- The public `oauthex.ClientRegistrationResponse.MarshalJSON` emits duplicate timestamp
  fields in this pinned revision, producing a response its paired decoder rejects when
  zero timestamps are present. The qualification AS emits the RFC 7591 JSON object
  directly; this server-helper defect does not alter the client profile. See the
  [upstream registration source](https://github.com/modelcontextprotocol/go-sdk/blob/0d0cdbc943f396aa973023a7dd6440398d3df87e/oauthex/registration.go).

No upstream issue/PR URL was available in the offline implementation environment. These
source links and the pin-to-upstream comparison are the disposition record; filing or
locating upstream reports is required before production OAuth wiring begins. Missing
protocol behavior must be fixed in the official SDK and consumed by pinning a fixed
revision, never copied into mecatl.

## Consequences

The repository now detects drift in the SDK's public OAuth wire behavior while remaining
fully offline and adding no production attack surface. Future controller work has a
precise interoperability and HTTP-hardening floor, plus explicit persistence seams.

The profile is intentionally narrower than everything the SDK currently attempts.
Servers without RFC 9728 metadata, with multiple authorization servers, advertising
`client_secret_post` alongside `client_secret_basic`, without enforced S256, or requiring
durable DCR are unsupported until an explicit design and upstream support exist. The
fixture must evolve when the official SDK fixes a discrepancy; tests must not preserve
unsafe behavior merely for compatibility.

## See also

- [Extensibility architecture](../architecture/extensibility.md)
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md)
- [ADR 0056 — MCP client reconnect](./0056-mcp-client-reconnect.md)
- [MCP authorization specification](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)
