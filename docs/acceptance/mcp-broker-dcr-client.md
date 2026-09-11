# MCP broker DCR client — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** Session MCP broker reconstruction — third upstream client mode
**Status:** proposed, 2026-09-08
**Delivery:** Split. Adds a new closed-union arm to operator-facing config
(permconfig schema, Helm chart schema/templates) and a new trust posture
(runtime-minted client identity); a config-surface and security-relevant
addition, not a compact one-task change.
**Expected tasks:** 4
**Issue:** none yet — discovered ad hoc while manually verifying PR #1072
against a live `mecak8s` Kind deployment.
**Plan PR:** <added when opened>
**Approved baseline:** <merged plan commit; absent until approved>

Add `dcr` as a third `mcp.servers[].auth.oauth.client.mode`, alongside the
existing `preregistered` and `cimd` modes, so an operator can point mecak8s
at a protected MCP upstream whose authorization server supports RFC 7591
Dynamic Client Registration (the trigger case: a ToolHive-hosted connector
gateway advertising a public `registration_endpoint` and PKCE-only auth)
without hand-registering a client or hosting a CIMD document. ToolHive's
embedded authorization server already implements DCR as an upstream client
(`authserver.OAuth2UpstreamRunConfig.DCRConfig`, resolved via
`pkg/authserver/runner/dcr_adapter.go`); this plan only wires mecatl's
existing config surface through to that already-shipped capability.

## Human decisions

- [x] Should mecatl's schema plumb ToolHive's optional DCR initial-access-token support (`DCRUpstreamConfig`'s bearer gating registration) in this iteration, or defer it as a follow-on once a real gated upstream needs it? — Decision: defer. The existing ToolHive runner resolves an initial-access-token reference while constructing its DCR request for an embedded-auth-server construction attempt; the DCR resolver then receives that resolved value, not the source. A re-registration occurs only when a new ToolHive process constructs against shared DCR storage, so a single-use initial-access-token upstream would require the operator to rotate the referenced file/env value before that construction attempt. This is an upstream-provisioning-policy concern, not a ToolHive caching gap: the only registration credential ToolHive caches is the RFC 7592 `RegistrationAccessToken`, used solely for update/read/delete and never as an initial access token. Deferred purely because nothing in this plan's scope exercises a gated registration endpoint.
- [x] Should the new `dcr` client object require exactly one of `discovery_url` (RFC 8414 metadata document) or an explicit `registration_endpoint`, mirroring the existing `upstream` OIDC-vs-explicit-OAuth2 mutual-exclusion pattern in `MCPOAuthUpstreamProfile`, or should `discovery_url` be the only supported form for v1 (simpler, matches the trigger case, defers explicit-endpoint DCR to a follow-on)? — Decision: `discovery_url` only for v1. The trigger upstream publishes full RFC 8414 metadata; an explicit-endpoint escape hatch is deferred until a real no-discovery DCR upstream is known to exist.
- [x] `DCRConfig` exists only on ToolHive's `OAuth2UpstreamRunConfig`, not `OIDCUpstreamRunConfig` — should `client.mode: dcr` therefore require `upstream: {mode: oauth2, ...}` and reject a bare `issuer` (OIDC-default) declaration, or should `dcr` be allowed to layer on top of OIDC discovery too (which ToolHive's library does not support today, so that path would fail at conversion time rather than at config validation)? — Decision: require `upstream: {mode: oauth2, ...}`; reject a bare `issuer` at config-validation time. ToolHive's library has no DCR-on-OIDC-upstream path, so this is a schema-level guard against a mismatch that would otherwise only surface as a construction-time failure.
- [x] Must v1's real ToolHive DCR integration proof use a TLS fixture despite ToolHive v0.45.0 exposing no testable custom-CA/client seam for its internally constructed DCR clients, or may it use ToolHive's loopback-HTTP development exception while HTTPS remains mandatory for the public `discovery_url` configuration contract? — Decision: use loopback HTTP only for the hermetic integration fixture. AC1.1 continues to reject non-HTTPS operator configuration; an end-to-end TLS DCR transport proof is deferred until ToolHive exposes a narrow CA/client injection seam.

## Interface contract

- **gRPC / protobuf:** None — this is operator-tier YAML/Helm configuration
  only; no session, event, or wire message changes.
- **Exported Go APIs / interfaces:** `internal/adapter/permconfig.MCPOAuthClientProfile`
  gains a third variant (new field, e.g. `DCR *MCPDCRClientProfile`) and a
  new exported `MCPDCRClientProfile` type
  (`internal/adapter/permconfig/schema.go:496-504`); `internal/adapter/mcpbroker.ToolHiveOAuth`
  gains the fields needed to carry a DCR declaration through to
  `internal/adapter/mcpbroker/toolhive_construction.go`'s conversion into
  `authserver.OAuth2UpstreamRunConfig.DCRConfig`. That conversion
  (`toolHiveUpstream`, `toolhive_construction.go:161-163`) currently rejects
  any protected upstream with an empty `ClientID` unconditionally, before
  branching on OAuth2/OIDC endpoint shape — this is a genuine restructuring
  of that validation (a new `dcr`-branch check ahead of the existing
  ClientID-required guard), not an additive field on an unchanged code path.
- **Tool schemas:** None — the MCP tool catalogue shape presented to the
  model (`oauth.tools` static declarations) is unaffected by which client
  mode obtained the credential.
- **CLI / config:** New `settings.yaml` shape
  `mcp.servers[].auth.oauth.client.mode: dcr` with a `dcr: {discovery_url}`
  payload (RFC 8414 metadata document URL only — no explicit
  `registration_endpoint` form in v1) and a required
  `upstream: {mode: oauth2, oauth2: {authorizationEndpoint, tokenEndpoint}}`
  (a bare `issuer`/OIDC-default declaration is rejected for `dcr` at
  validation time, since ToolHive's `DCRConfig` does not exist on the OIDC
  upstream path). No secret-shaped field is introduced (initial-access-token
  support is deferred — see Human decisions). New Helm value shape
  `mcp.servers[].auth.oauth.client.dcr` mirroring it in
  `deploy/helm/mecak8s/values.schema.json` and rendered by
  `deploy/helm/mecak8s/templates/_helpers.tpl`'s `mecak8s.mcpOAuthSettings`
  template. `internal/configgen/build.go`'s reflection-based settings.yaml
  reference generator (`case "client":` around line 518) gains a third
  branch so `user-docs/reference/configuration.md` documents it.
- **Events / persistence:** None — DCR-obtained credentials are held and
  cached entirely inside ToolHive's existing embedded-authorization-server
  storage (`mcpbroker.ToolHiveConfig.AuthRedisClient` / in-memory fallback,
  the same store the other two client modes already use); mecatl persists
  no new record.
- **Security / authority:** A `dcr` client's identity is minted by the
  upstream authorization server at registration time rather than asserted by
  the operator (unlike `preregistered`/`cimd`). This is a narrower
  reinstatement of DCR than [ADR 0219](../adr/0219-mcp-oauth-sdk-profile.md)/[ADR 0220](../adr/0220-mcp-oauth-controller.md)
  excluded for the sibling MCP OAuth controller — that exclusion was
  specifically about the go-sdk's per-flow, non-durable DCR; ToolHive's
  `pkg/auth/dcr` resolver has its own durable registration cache and reuse,
  which is the gate ADR 0219/0220 said was missing (see
  [ADR 0314](../adr/0314-mcp-broker-dcr-client.md)). Mecatl exposes
  neither ToolHive's per-upstream `InsecureAllowHTTP` nor
  `AllowPrivateIPs` knobs: a configured non-loopback DCR target therefore
  uses ToolHive's zero-default HTTPS and private-IP protections. ToolHive's
  localhost-development exception remains outside this schema. The hermetic
  integration fixture uses that loopback exception only because ToolHive
  v0.45.0 exposes no custom-CA/client seam for its internally constructed DCR
  clients; an end-to-end TLS transport proof is deferred to a ToolHive fix.
  No new secret-shaped value is introduced in this iteration
  (initial-access-token support is deferred — see Human decisions).
- **Compatibility / migration:** Additive only. `preregistered` and `cimd`
  remain unchanged in shape and behavior; the closed union widens from two to
  three variants and existing configs continue to parse and render
  identically.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — An operator declares a `dcr` MCP server end to end

An operator writes `mcp.servers[].auth.oauth.client.mode: dcr` with a
discovery/registration reference (no `preregistered`/`cimd` payload) in
either `settings.yaml` directly or the Helm `mcp.servers` value. The value
survives permconfig's strict decode, the Helm chart's JSON-schema validation
and template rendering into the same `settings.yaml` shape, and
`internal/app`'s composition-layer conversion
(`toolHiveBrokerConfig` in `internal/app/mcp_broker_toolhive.go:53-79`) into
`mcpbroker.ToolHiveOAuth`, which `internal/adapter/mcpbroker/toolhive_construction.go`
turns into a `DCRConfig`-bearing `authserver.OAuth2UpstreamRunConfig` with an
empty `ClientID` — never both. See
[ADR 0314](../adr/0314-mcp-broker-dcr-client.md) and the existing
`preregistered`/`cimd` precedent in `user-docs/building/deployment/mecak8s.md`.

**Acceptance:**
- AC1.1: `permconfig` strictly decodes a `dcr` client declaration (a single
  HTTPS `discovery_url`, no `preregistered`/`cimd` payload) and rejects a
  non-HTTPS or missing discovery URL, one combined with `preregistered` or
  `cimd`, and either an omitted upstream or `upstream.mode: oidc`. These
  rejections preserve the closed union and enforce the explicit OAuth2-only
  upstream decision before ToolHive construction.
  - verify: `TestMcpBrokerDCRClient_Scenario1_PermConfigClosedUnion`
- AC1.2: the Helm chart's `values.schema.json` accepts a well-formed `dcr`
  client value, rejects a malformed one (missing or non-HTTPS
  `discovery_url`, `dcr` combined with `preregistered`/`cimd`, or an OIDC
  upstream), and `mecak8s.mcpOAuthSettings` renders it into the same
  `settings.yaml` `dcr:` shape permconfig parses.
  - verify: `deploy/helm/mecak8s/chart_test.go` (extend the existing
    `mcp.servers` fixture coverage with a `dcr` case)
- AC1.3: `toolHiveBrokerConfig` converts a `dcr`-mode server into a
  `ToolHiveOAuth` value with no `ClientID`, and the restructured
  `toolHiveUpstream` in `internal/adapter/mcpbroker/toolhive_construction.go`
  builds a `DCRConfig`-bearing `authserver.OAuth2UpstreamRunConfig` from it
  instead of rejecting it for a missing client identity. The conversion pins
  `AllowPrivateIPs` and `InsecureAllowHTTP` to their false defaults.
  - verify: `TestADR_0314_ToolHiveConversionCarriesDCRConfig`
- AC1.4: the `dcr` conversion is selected only for the explicit OAuth2
  upstream shape; invalid DCR/OIDC combinations cannot reach ToolHive as an
  OIDC configuration and fail only later at construction.
  - verify: `TestADR_0314_DCRRequiresExplicitOAuth2Upstream`
- AC1.5: `user-docs/reference/configuration.md` documents the `dcr` client shape
  after `task docs` regeneration, and `user-docs/building/deployment/mecak8s.md` carries one
  worked `dcr` example alongside the existing `preregistered`/CIMD examples.
  - verify: inspection — generated-doc + prose-example presence is not a
    behavioral assertion `task ac-trace` can name a Go test for

### Scenario 2 — A configured `dcr` upstream registers, enrolls, and reuses its durable registration

Using an offline loopback-HTTP fixture (an authorization server standing in
for the protected upstream, exposing RFC 8414 metadata whose
`registration_endpoint` is an RFC 7591 `/register` endpoint — no live
network, matching ToolHive's localhost-development exception), a real
`mcpbroker.Process` and ToolHive embedded authorization server retrieve the
fixture discovery document and perform actual DCR while the Process is
constructed. The fixture returns the confidential-client registration shape
ToolHive v0.45.0 requests, completes the browser-facing enrollment, and
accepts one resulting tool invocation. A second Process against the same
ToolHive auth storage proves the resolver's durable registration-cache reuse.
This proves the wiring reaches ToolHive's real `pkg/auth/dcr` resolver, not
only that mecatl's config types compile. The separately-tested operator
configuration still requires HTTPS; end-to-end TLS DCR transport proof is
deferred until ToolHive exposes a custom-CA/client seam.

**Acceptance:**
- AC2.1: construction of a `dcr`-mode `mcpbroker.Process` retrieves the
  loopback fixture's RFC 8414 metadata, uses its advertised `/register`
  endpoint, and issues exactly one DCR registration. The resulting request
  has the fixed broker callback, authorization-code and refresh-token grants,
  response type `code`, and ToolHive v0.45.0's negotiated confidential-client
  authentication method; its completed enrollment can invoke a declared tool
  and the protected upstream observes the resulting bearer token.
  - verify: `TestMcpBrokerDCRClient_Scenario2_RegistersAndEnrolls`
- AC2.2: construction of a second Process against the same DCR credential
  storage reuses the cached registration and does not issue another
  `/register` call; it remains capable of completing enrollment and invoking
  the declared tool.
  - verify: `TestMcpBrokerDCRClient_Scenario2_ReusesCachedRegistration`
- AC2.3: an RFC 7591 registration error fails Process/broker construction
  cleanly and prevents creation of an unauthenticated protected route; it does
  not silently fall back to an unauthenticated call or surface as a later
  enrollment failure.
  - verify: `TestADR_0314_RegistrationFailureNeverFallsBackUnauthenticated`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| DCR initial-access-token support | Follow-on once a real gated upstream needs it | See Human decisions — confirmed clean deferral, no ToolHive caching gap |
| Explicit `registration_endpoint` (no-discovery DCR upstream) | Follow-on once a real no-discovery DCR upstream is known | See Human decisions |
| DCR layered on OIDC (`issuer`) upstream discovery | Follow-on if ToolHive ever adds `DCRConfig` to `OIDCUpstreamRunConfig` | See Human decisions — `dcr` requires explicit `upstream: {mode: oauth2, ...}` |
| `AllowPrivateIPs`/`InsecureAllowHTTP` exposure for DCR calls | Follow-on ADR if an in-cluster DCR upstream needs it | [ADR 0314](../adr/0314-mcp-broker-dcr-client.md) — mecatl exposes neither per-upstream override; ToolHive's localhost-development exception and process-wide compatibility setting remain outside this schema |
| Manually wiring the live `mecak8s-dev` Kind cluster to `connector-gateway.example.com` | Manual verification after this plan lands | Operator action, not part of the automated acceptance surface |
| Global (non-broker) OAuth mode DCR support | Not requested; broker mode is the only place `mcp.servers[].auth.oauth.client` currently exists | N/A — same union, no separate global-mode client concept exists to extend |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR reports interface conformance against this plan.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- Exact Go field/YAML key names for `MCPDCRClientProfile` (e.g.
  `discovery_url` vs `discoveryURL`'s YAML-side snake_case convention) are an
  implementation detail following the existing `snake_case` YAML convention
  visible in `MCPPreregisteredClientProfile`/`MCPCIMDClientProfile` — not a
  material decision requiring human sign-off.
- Scenario 2's fake upstream fixture may be built as a small extension of
  the existing `mcp-broker-multi-upstream-oauth.md` fixture helpers rather
  than a wholly new one, at the implementing task's discretion, provided the
  real `pkg/auth/dcr` resolver is exercised end to end (a direct adapter
  fake standing in for that resolver is not sufficient evidence, matching
  the sibling plan's own fixture-fidelity rule).
