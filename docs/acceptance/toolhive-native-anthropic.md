# ToolHive native Anthropic gateway support — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this adds a wire-stable provider identity and extends the direct-mode OIDC authentication boundary across a second protocol adapter.
**Decision record:** [ADR 0334](../adr/0334-toolhive-protocol-specific-providers.md)
**Phase:** protocol-specific ToolHive AI gateway providers
**Status:** in-progress, 2026-09-10. The directing user explicitly waived the acceptance-plan spine for this named implementation; no plan approval or merge is claimed.
**Delivery:** Split. The directing user explicitly waived the separate Plan / Interface checkpoint for this local implementation; final human code review and merge remain required.
**Expected tasks:** implementation and aggregate verification in progress under the explicit workflow waiver
**Issue:** None assigned.
**Plan PR:** Absent until explicitly authorized and opened.
**Approved baseline:** absent; implementation was explicitly authorized without one.

Expose the ToolHive AI gateway's native Anthropic catalog and Messages endpoint as a distinct
Mecatl provider without changing the existing OpenAI Responses-backed `toolhive` provider. Both
entries derive from the same ToolHive intent and routing mode, while catalog health and
last-known-good state remain independent.

## Human decisions

None — the requested contract fixes the provider ID, protocol separation, URL roots, authentication behavior, compatibility requirements, tests, documentation, and downstream exclusion.

## Interface contract

- **gRPC / protobuf:** None — existing open-string `provider_id` and current model/status messages carry `toolhive-anthropic`; no message, field, method, or field number changes.
- **Exported Go APIs / interfaces:** None — reuse `provider/anthropic` options and `anthropic.NewLister`; all new family classification, URL derivation, shared-client construction, probing, and status logic remains root-internal composition.
- **Tool schemas:** None — provider routing changes do not add or alter model-facing tools.
- **CLI / config:** Add no flag or key. One resolved ToolHive LLM intent in existing `auto|proxy|direct` mode registers both `toolhive` and `toolhive-anthropic`. Reserve `toolhive-anthropic` against custom-provider collisions. Existing `--default-provider` and operator `models.default_provider` may explicitly select it; `toolhive` remains the implicit first intent-driven provider.
- **Events / persistence:** None — no event or snapshot shape changes and no migration. Existing provider-ID strings may persist `toolhive-anthropic`; register-on-intent ensures it remains resolvable when either endpoint is temporarily unavailable.
- **Security / authority:** Proxy requests stay loopback-bound and redirect-refusing. The existing OpenAI proxy path stays byte-identical; the native Anthropic proxy transport removes its SDK `x-api-key`/conflicting auth and sends only `Authorization: Bearer thv-proxy` to loopback, where ToolHive replaces it, so no placeholder reaches the upstream gateway. Direct listing and inference for both protocols share one non-interactive ToolHive token source and bearer base transport/client policy; protocol listers may shallow-clone the client to add response caps/timeouts but retain that same transport/source. Every direct outbound attempt removes `Authorization` and `x-api-key`, invokes the token source once, and sets only the authoritative bearer before transport. Fetch both ToolHive endpoints concurrently under the existing operation-wide bounds (Build 1.5 seconds, background refresh 10 seconds, on-demand stale refresh 2 seconds). Preserve HTTPS enforcement with the literal-loopback exception, bounded model bodies, sanitized token errors, secret-free URL/status/diagnostics, no credential logging/persistence, and both SDKs' ambient-credential suppression.
- **Compatibility / migration:** `toolhive` keeps its explicit OpenAI base URL byte-for-byte, `/v1/models`, `/v1/responses`, default precedence, zero-selector model, last-known-good behavior, and regression coverage. `toolhive-anthropic` is additive and uses `/anthropic/v1/models` plus `/anthropic/v1/messages`. Its proxy base replaces only a terminal `v1` path segment with `anthropic`, otherwise appending `anthropic`; its direct base appends `anthropic` to `gateway_url`. Path prefixes are preserved and trailing slashes normalized; userinfo/query/fragment are not copied into the native base or diagnostics. Model/status ordering is deterministic. Mecatui classifies both as the same ToolHive gateway family and suppresses duplicate same-family availability notices while retaining both picker/status rows.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — One gateway intent exposes two truthful protocol catalogs

The registry follows the protocol-specific identity decision in [ADR 0334](../adr/0334-toolhive-protocol-specific-providers.md) while retaining the register-on-intent behavior from [ADR 0064](../adr/0064-toolhive-llm-gateway-provider.md).

**Acceptance:**
- AC1.1: Proxy and direct intent each register sorted provider IDs `toolhive` and `toolhive-anthropic`; absent/disabled intent registers neither, and the new ID cannot be redefined as an operator custom provider.
  - verify: `TestToolhiveNativeAnthropic_Scenario1_Registration`
- AC1.2: The existing OpenAI base stays verbatim and discovery retains its current `<base>/models` behavior (`/v1/models` for detected/default gateway bases). Native discovery requests `/anthropic/v1/models`. Native base derivation covers origin, `/v1`, `/prefix/v1`, trailing-slash, and explicit non-`/v1` bases; it preserves path prefixes, never copies userinfo/query/fragment, and never moves path segments into query data.
  - verify: `TestToolhiveNativeAnthropic_Scenario1_DiscoveryPaths`
- AC1.3: Native rows retain context/output limits, image capability, and adaptive/manual thinking metadata from `anthropic.NewLister`; matching embedded Anthropic metadata remains a fallback without becoming unverified gateway inventory. Tests carry output/thinking through `liveMetaStore.outputLimitFor`/`thinkingFor` into emitted Anthropic `max_tokens` and thinking mode, not only picker-visible fields.
  - verify: `TestToolhiveNativeAnthropic_Scenario1_Metadata`

### Scenario 2 — Native selections use Messages with one authoritative credential

Direct authentication extends [ADR 0102](../adr/0102-toolhive-direct-mode.md) without widening the SDK or engine boundaries described in the [provider architecture](../architecture/providers.md).

**Acceptance:**
- AC2.1: Selecting a `toolhive-anthropic` model sends Anthropic Messages JSON to `/anthropic/v1/messages` and never sends that selection to `/v1/responses`; selecting `toolhive` retains Responses JSON and `/v1/responses`.
  - verify: `TestADR_0325_ProtocolSpecificWireRouting`
- AC2.2: Direct-mode listing and inference for both entries construct one token source and share its bearer base transport/client policy; each outbound attempt invokes the source once, removes placeholder or conflicting `Authorization` and `x-api-key`, and sends only the authoritative bearer. Protocol lister client clones retain the shared bearer transport. A token error forwards no request and remains credential-classified and sanitized.
  - verify: `TestToolhiveNativeAnthropic_Scenario2_DirectAuthentication`
- AC2.3: Native proxy listing/inference converts the Anthropic SDK placeholder `x-api-key` into a loopback-only placeholder bearer; a mock proxy/upstream boundary proves no `x-api-key` or placeholder reaches upstream. Proxy and direct inference refuse redirects, direct mode rejects non-HTTPS non-loopback gateway URLs through the existing gate, reminted providers retain the correct transport, and no credential-bearing URL component or header enters inventory, status, or diagnostics.
  - verify: `TestToolhiveNativeAnthropic_Scenario2_TransportSecurity`

### Scenario 3 — Inventory failures are independent and defaults do not move

The existing provider-keyed outcome store in the [provider architecture](../architecture/providers.md) remains the source of live status and last-known-good catalogs.

**Acceptance:**
- AC3.1: Success, honest empty, unauthorized, unreachable, refresh, and last-known-good transitions are independent for `toolhive` and `toolhive-anthropic`. Both family fetches start concurrently under each existing overall deadline (Build 1.5 seconds, background 10 seconds, on-demand stale 2 seconds); failure or delay of either endpoint does not starve, erase, suppress, or relabel the healthy protocol's outcome or inventory, and results publish in deterministic provider order.
  - verify: `TestToolhiveNativeAnthropic_Scenario3_IndependentOutcomes`
- AC3.2: With both entries present, `toolhive` remains the implicit default and retains its existing first-listed zero-selector model. An honest empty catalog for the resolved default remains a fatal actionable Build error for `toolhive` and applies consistently when the operator explicitly defaults to `toolhive-anthropic`; transport/auth failure of the resolved default and every empty/failure of a non-default sibling remain non-fatal. An explicit native selection works without automatic cross-protocol failover.
  - verify: `TestToolhiveNativeAnthropic_Scenario3_DefaultCompatibility`
- AC3.3: Model inventory and provider status remain sorted and secret-free and report separate counts/states. Hints are routing-aware: proxy-unreachable says to start the proxy, direct-unreachable says to check gateway connectivity or use proxy mode, unauthorized says to re-authenticate, and empty retains setup/admin guidance. Mecatui gives both IDs ToolHive gateway provenance/disclosure, retains both picker/status rows, suppresses duplicate footer/welcome availability notices when either same-family provider is active, and does not change unrelated-provider rendering.
  - verify: `TestToolhiveNativeAnthropic_Scenario3_StatusAndPresentation`
- AC3.4: Existing ToolHive proxy/direct discovery, Responses inference, rehydration, default healing, and no-intent behavior retain their regression coverage; living architecture, implementation notes, usage, and TUI documentation explain the two IDs and shared gateway identity.
  - verify: inspection — run the existing ToolHive suites plus `task docs`; documentation is the operator-facing proof.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Catalog merging or automatic protocol failover | separate architectural proposal | Provider identity selects the wire adapter; keep catalogs and failures independent. |
| New ToolHive flags, credentials, or login flows | not planned | Reuse the existing intent, routing mode, and OIDC token source. |
| Changes to native `anthropic` provider defaults | not planned | Reuse its adapter/lister without changing key-driven Anthropic behavior. |

## Definition of done

1. Focused offline tests use mock transports/servers and require no live gateway credential.
2. `task lint`, `task test`, `task api:check`, `task docs`, and `task ac-trace-strict` pass; `go run ./cmd/mecademo` remains green.
3. Model inventory/status output is deterministic and contains no credential or placeholder material.
4. The implementation PR links the Plan / Interface PR and approved commit and reports conformance to every interface clause.
5. `/panel-review` reports no ship blockers or unwaived failures.

## Deferred decisions and known risks

- None — any implementation need for a new public surface, credential source, routing mode, protocol failover, or inventory merge is contract drift and returns to human review.
