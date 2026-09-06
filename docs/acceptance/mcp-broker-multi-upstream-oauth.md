# ToolHive-owned multi-upstream MCP broker OAuth — acceptance plan

**Phase:** Session MCP broker reconstruction — multiple protected upstreams
**Status:** draft, 2026-09-03
**ADR:** [ADR 0311](../adr/0311-per-upstream-mcp-broker-oauth-grants.md) — ToolHive owns upstream OAuth; mecatl admits its completed broker catalogue.
**Accumulator branch:** `acc/mcp-broker-multi-upstream-oauth` (off `main`).

This is a vertical proof of the ownership boundary. ToolHive, not mecatl,
performs the sequential upstream OAuth chain, owns callbacks and per-provider
tokens, and injects the matching token into each upstream. Mecatl configures
and hosts that process, exposes one opaque session enrollment operation, and
freezes the resulting catalogue only when ToolHive has completed the chain.

The fixture is entirely offline: two independent OAuth/MCP upstream fakes, the
real ToolHive embedded authorization server and vMCP handler, a real
`mcpbroker.Process`, and the real `server.Service` control paths. It records
browser exchanges, token refreshes, discovery, and tool invocation. A direct
adapter fake is not sufficient evidence.

## Scope and sources

- [ADR 0311](../adr/0311-per-upstream-mcp-broker-oauth-grants.md) — ToolHive is the sole upstream OAuth controller.
- [ADR 0312](../adr/0312-confidential-toolhive-broker-client.md) — the generated ToolHive-facing broker client is confidential and authenticates only with HTTP Basic at the token endpoint.
- [ADR 0220](../adr/0220-mcp-oauth-controller.md) — callback and credential custody require exact correlation and constrained traffic.
- [`architecture.md § Session-scoped MCP broker`](../architecture.md) — mecatl owns the process/session boundary and callback mount, not upstream authorization state.

## In scope — three vertical scenarios

### Scenario 1 — One opaque mecatl enrollment completes ToolHive's two-upstream chain

An operator configures two protected streaming-HTTP MCP backends with distinct
OAuth clients, scopes, endpoints, and provider identities. The normal authority
resolver, composition projection, `mcpbroker.Process`, and ToolHive embedded
authorization server receive both profiles. A client begins one workspace
enrollment from the real mecatl server control surface and follows the
presentation URL. ToolHive drives the required consent pages and callbacks;
mecatl only observes the one opaque operation.

On completion, mecatl performs authenticated discovery through ToolHive,
freezes one catalogue, rebuilds the session engine, and invokes one tool on
each backend. The upstream recordings demonstrate that each received only its
own bearer token.

**Acceptance:**
- AC1.1: Two protected profiles boot through the normal authority/composition/process path with distinct ToolHive provider and injection bindings; no global MCP fallback or mecatl one-upstream rejection occurs.
  - verify: `TestADR_0298_ToolHiveTwoUpstreamBrokerBootsE2E`
- AC1.2: `ConnectWorkspaceServices` exposes one opaque pending enrollment and presentation. ToolHive completes both configured upstream authorizations through its real callback chain; mecatl does not publish an intermediate catalogue or create a second backend-specific authorization control.
  - verify: `TestADR_0298_ToolHiveCompletesTwoUpstreamEnrollmentE2E`
- AC1.3: Completion publishes one frozen catalogue containing both backends' tools. Invoking each wrapper uses only that backend's issued token.
  - verify: `TestADR_0298_ToolHiveInjectsIsolatedUpstreamTokensE2E`

---

### Scenario 2 — A ToolHive-chain failure remains one mecatl failure and admits no partial tools

The real ToolHive fixture completes both upstream authorizations and then makes
the second authenticated discovery fail. ToolHive owns the error and any
internal retry state. Mecatl observes only the terminal aggregate operation,
clears its pre-prompt gate, exposes no backend-specific control data, and never
admits a partial catalogue. A later fresh mecatl enrollment delegates a new
operation to ToolHive; it does not revive or inspect Mecatl-held per-upstream
grants because none exist.

ToolHive v0.45 does not correlate an internally received upstream
`access_denied` response to mecatl's outer callback. The real-ToolHive proof
therefore does not claim that case: it remains pending inside ToolHive until its
own expiry. Focused outer-adapter coverage separately proves that an
`access_denied` delivered to mecatl's correlated callback projects as aggregate
`denied`; other correlated OAuth errors project as `failed`.

**Acceptance:**
- AC2.1: A correlatable aggregate failure, outer callback denial/error, expiry, callback mismatch, or authenticated-discovery error produces one terminal mecatl enrollment outcome, clears the gate, and publishes no partial workspace catalogue. The real-ToolHive E2E proves the authenticated-discovery failure boundary; focused adapter tests prove outer callback denial/error and expiry projection. ToolHive v0.45's internally uncorrelated upstream `access_denied` remains explicitly outside this claim.
  - verify: `TestADR_0298_ToolHiveChainFailureDoesNotPublishPartialCatalogueE2E`, `TestWorkspaceEnrollmentTerminalObservationIsRetryable`, `TestOAuthErrorCallbackConsumesStateAndMapsStatus`
- AC2.2: The mecatl gRPC and HTTP controls reveal only aggregate reference/status/count/presentation data; provider identity, callback state, endpoint, code, access token, and refresh token never cross the public boundary.
  - verify: `TestADR_0298_ToolHiveEnrollmentControlsRedactUpstreamStateE2E`
- AC2.3: Retrying begins a fresh opaque ToolHive operation. Mecatl neither selects a “next backend” nor reuses/copies a per-backend OAuth grant.
  - verify: `TestADR_0298_ToolHiveChainRetryHasNoMecatlGrantStateE2E`

---

### Scenario 3 — ToolHive refreshes one backend without affecting the other

After Scenario 1 has admitted the frozen catalogue, the fixture expires one
upstream credential and invokes only that backend. ToolHive refreshes the
selected provider credential and reinjects it. The other backend remains
usable with its own valid credential. This proves the only meaningful
per-upstream state is inside ToolHive, while mecatl's frozen catalogue and
single enrollment binding stay unchanged.

**Acceptance:**
- AC3.1: Expiring backend A's token causes ToolHive to refresh A before A's invocation; B receives no A token, refresh, callback, or authorization prompt.
  - verify: `TestADR_0298_ToolHiveRefreshIsProviderScopedE2E`
- AC3.2: The refresh changes neither the frozen membership/catalogue nor mecatl's enrollment binding, and it creates no new mecatl authorization continuation.
  - verify: `TestADR_0298_ToolHiveRefreshDoesNotReenrollMecatlSessionE2E`

### Scenario 4 — The generated ToolHive broker client is confidential and secret-custodied

For a protected workspace enrollment, process construction generates one
high-entropy broker-client secret. ToolHive registers a confidential client that
stores only the ToolHive-required hash and requires `client_secret_basic` at its
token endpoint. Mecatl keeps the raw value only in private process-owned
OAuth-route/grant state: it is passed in an HTTP Basic header for the
authorization-code exchange and a subsequent refresh, never in a browser URL,
form body, public control, session snapshot, event log, deployment setting, or
upstream request. The returned bearer credential, not the client secret,
authenticates requests to ToolHive's vMCP endpoint.

This is a new unreleased branch capability. It requires no migration or
compatibility read path for an earlier public registration; disposable preview
state may be cleared when it is replaced.

**Acceptance:**
- AC4.1: A protected ToolHive process registers its generated broker client as confidential with a `client_secret_basic` token-endpoint method and a stored hash that verifies the private generated secret but is not the raw secret.
  - verify: `TestToolHiveProtectedClientIsConfidential`
- AC4.2: The real embedded ToolHive authorization-code flow accepts the generated confidential client, while both the authorization-code exchange and refresh send exactly one HTTP Basic credential with the client ID and secret and no `client_secret` form parameter.
  - verify: `TestADR_0298_ToolHiveEnrollmentUsesRealIdentityMiddleware`, `TestProtectedCallCallbackSingleUseAndRefreshCustody`
- AC4.3: Browser presentation, public enrollment controls, durable session/event state, and ToolHive upstream calls expose no generated broker-client secret; only the issued broker bearer token reaches the vMCP request.
  - verify: `TestADR_0299_BrokerClientSecretNeverCrossesPublicBoundary`

---

## Fixture contract

The common fixture must:

1. pass two profiles through `ResolveMCPAuthority`, `toolHiveBrokerConfig`,
   `NewToolHiveProcess`, and `server.Service`;
2. use ToolHive's real embedded authorization/vMCP handlers and callback state;
3. use independent fake upstreams that record token exchange, refresh,
   authenticated discovery, and tool calls;
4. drive the public mecatl controls and the actual ToolHive browser callback
   chain, never an upstream selector supplied to mecatl; and
5. assert negative traffic as well as successful calls, so a shared token,
   hand-written mecatl callback controller, skipped upstream authorization, or
   partial catalogue cannot make the test pass.

Focused unit tests may pin parsing and process-local state transitions, but the
fixture above is the acceptance proof.

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| ToolHive browser retry and multi-tab policy | ToolHive contract | ADR 0311 |
| Correlating ToolHive v0.45's internally received upstream `access_denied` to mecatl's outer enrollment | ToolHive capability/version follow-up | The current real-ToolHive proof intentionally uses a correlatable authenticated-discovery failure |
| Remote or durable broker implementation | Remote broker boundary decision | Ledger H-D4/H-D5/H-D6 |
| Multi-replica broker routing/affinity | Deployment decision | Ledger H-D7 |
| Sharing upstream tokens across backends | Explicitly prohibited | ADR 0311 |
| Unrelated TUI enrollment work | Separate work item | Ledger H-K12/H-K13/M-K8–M-K11 |

## Sequencing recommendation

Make Scenario 1 green first. It proves the intended boundary all the way from
operator configuration to two isolated upstream calls. Reuse that fixture for
failure and refresh behavior. Delete local per-backend OAuth state rather than
adding tests that preserve it.

## Definition of done

1. `task lint`, `task test`, and `task api:check` pass.
2. `task docs` passes, including the generated configuration reference and strict link gate.
3. `task ac-trace-strict` resolves every acceptance proof after the plan lands.
4. All four scenarios pass offline against real ToolHive handlers and two independent fake upstreams; no test calls a live OAuth server, ToolHive deployment, or model provider.
5. No mecatl public/session API stores or projects an upstream OAuth token, refresh token, authorization code, callback state, provider key, or backend selection.
6. Public operator documentation explains that ToolHive handles multi-upstream authorization transparently while mecatl exposes one enrollment operation.
7. `go run ./cmd/mecademo` still prints a complete offline session.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is satisfied.
