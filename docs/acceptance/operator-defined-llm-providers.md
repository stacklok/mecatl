# Operator-defined LLM providers — acceptance plan

**Phase:** operator-local provider configuration
**Status:** landed, 2026-08-25. Extended to make provider-credential loading composition-owned before provider OAuth is introduced.
**ADR:** [ADR 0238](../adr/0238-operator-defined-llm-providers.md) — pins provider identity, protocol, auth, endpoint overrides, and composition-owned provider-credential loading.
**Accumulator branch:** `acc/operator-defined-llm-providers` (off `main`).

The smallest set of work that lets an operator register a truthfully named LLM gateway,
select it as the deployment default, and keep its endpoint and one API key in the correct
operator-local files. It reuses the three existing wire adapters rather than adding a
new provider protocol, and leaves OAuth, arbitrary auth headers, and transport tuning
for a later decision.

## Why these scope cuts

- [ADR 0238](../adr/0238-operator-defined-llm-providers.md) — an explicit closed
  protocol/auth vocabulary is safer than a generic provider-plugin configuration DSL.
- [ADR 0016](../adr/0016-multi-provider.md) — provider construction belongs only in
  composition; the engine continues to consume a bare `port.LLMProvider`.
- [ADR 0215](../adr/0215-openai-subscription-manual-token.md) — Codex keeps its
  policy-owned endpoint and credential path rather than becoming a generic override.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — operator configuration is validated before provider construction

A user-global `settings.yaml` declares custom provider identities under `providers:` and
eligible built-in endpoint overrides under `provider_overrides:`. The resolver admits
these only from operator sources, validates all values before startup, and never lets a
project repository redirect model traffic. This preserves the composition-only registry
and project-trust boundary described in [ADR 0016](../adr/0016-multi-provider.md) and
[`AGENTS.md` — project trust](../../AGENTS.md).

**Acceptance:**

- AC1.1: a valid operator `providers.<id>` entry with a lower-case stable ID, HTTPS base
  URL, required `default_model`, one supported `api_flavor`, and `none` or `api_key`
  authentication resolves as a custom provider definition.
  - verify: `TestOperatorDefinedLLMProviders_Scenario1_ValidDefinition`
- AC1.2: reserved built-in IDs, duplicate custom IDs, invalid IDs, non-HTTPS URLs,
  URL userinfo/query/fragment, unknown entry fields, unsupported protocol/auth values, and
  absent `default_model` fail configuration validation without printing a secret.
  - verify: `TestOperatorDefinedLLMProviders_Scenario1_InvalidDefinitionsFailClosed`
- AC1.3: all custom definitions and `provider_overrides` from project-tier settings are
  ignored with a value-free warning; an operator definition remains authoritative.
  - verify: `TestInvariant_custom_providers_operator_tier_only`
- AC1.4: `provider_overrides` accepts only `openai`, `openrouter`, `anthropic`, and
  `opencode`; Codex and ToolHive endpoint policies remain unchanged.
  - verify: `TestADR_0238_BuiltinOverrideAllowlist`

---

### Scenario 2 — credentials remain separate and one-to-one

A custom `api_key` provider reads exactly its same-ID API-key record from the
operator-local strict `auth.yaml`; a no-auth provider needs no record. Environment
fallbacks, OAuth, arbitrary headers, and n:n credential references remain absent. This
extends the existing auth-file separation described in
[`docs/usage/mecated.md`](../usage/mecated.md#credentials-file-authyaml) without placing
secrets in settings, following the value-free credential boundary in
[ADR 0238](../adr/0238-operator-defined-llm-providers.md).

**Acceptance:**

- AC2.1: two custom entries may share a URL and flavor while authenticating with distinct
  `auth.yaml` API-key records; neither key appears in diagnostics, errors, model lists,
  or wire responses.
  - verify: `TestOperatorDefinedLLMProviders_Scenario2_SeparateAuthFileKeys`
- AC2.2: an `api_key` custom provider without its matching auth-file record is unavailable,
  while a `none` provider registers without an auth-file record.
  - verify: `TestOperatorDefinedLLMProviders_Scenario2_AvailabilityFollowsAuthMethod`
- AC2.3: every server-owning command root first resolves the operator definition set,
  derives its accepted custom auth-file IDs, then parses `auth.yaml` once into the
  immutable credential snapshot passed to composition; no custom credential has an
  environment fallback.
  - verify: `TestInvariant_custom_provider_auth_bootstrap_single_source`
- AC2.4: unknown auth-file provider IDs and malformed API-key records remain strict,
  value-free failures rather than silently creating an available provider.
  - verify: `TestInvariant_custom_provider_authfile_strict`

---

### Scenario 3 — each declared protocol mints the established adapter

A configured custom entry selects one existing adapter explicitly: OpenAI Responses,
OpenAI Chat Completions, or Anthropic Messages. Each construction and per-session remint
retains the existing resilience, capabilities, request shaping, and neutral
`port.LLMRequest` invariants from [ADR 0016](../adr/0016-multi-provider.md) and the
provider-module boundary from [ADR 0093](../adr/0093-provider-modules.md).

**Acceptance:**

- AC3.1: an `openai-responses` custom provider sends its configured default model to its
  configured base URL through the existing Responses adapter, and an explicit session
  model overrides that default.
  - verify: `TestOperatorDefinedLLMProviders_Scenario3_ResponsesAdapter`
- AC3.2: an `openai-chat-completions` custom provider uses the existing Chat Completions
  adapter, and an `anthropic-messages` provider uses the native Messages adapter with its
  established API-key headers and request requirements.
  - verify: `TestOperatorDefinedLLMProviders_Scenario3_DeclaredFlavorSelectsAdapter`
- AC3.3: custom providers do not inherit OpenRouter routing metadata, canonical-provider
  prompt-cache hints, Codex policy headers, ToolHive discovery, or provider-private
  behavior not declared by their flavor.
  - verify: `TestInvariant_custom_provider_has_no_builtin_private_options`
- AC3.4: no engine/domain/port/proto public API changes are needed for custom provider
  registration.
  - verify: inspection — `task api:check` and the layering gate prove the core boundary.

---

### Scenario 4 — model discovery is opportunistic and conservative

A custom provider begins with its required default model as a composition-owned synchronous
inventory floor. It uses a flavor-appropriate live lister only when the existing adapter
can list at its configured base URL; such a request uses the resolved authentication,
bounded timeout/body handling, and a redirect-refusing client. A listing fault, malformed
payload, or empty result does not make an otherwise configured provider unusable. This
follows the live-catalog fallback and capability-truth discipline in
[ADR 0016](../adr/0016-multi-provider.md) and
[`architecture/providers.md`](../architecture/providers.md).

**Acceptance:**

- AC4.1: a custom Responses or Chat-Completions provider can use the compatible `/models`
  lister at its configured base URL, and a custom Anthropic provider can use the native
  lister at its configured base URL; each request is authenticated as configured,
  redirect-refusing, and bounded before successful results expand inventory.
  - verify: `TestOperatorDefinedLLMProviders_Scenario4_LiveListing`
- AC4.2: the configured default model is the synchronous registry inventory floor for
  empty-model selection, deployment-default validation, live-list fallback, and
  child/provider reminting; it is usable even without a static custom model catalog.
  - verify: `TestInvariant_custom_provider_default_model_inventory_floor`
- AC4.3: a failed, timed-out, malformed, or empty live listing preserves that default-model
  floor and does not discard a valid provider registration.
  - verify: `TestOperatorDefinedLLMProviders_Scenario4_ListingFallback`
- AC4.4: unknown live model metadata never overclaims image, audio, reasoning, output, or
  context-window capabilities.
  - verify: `TestInvariant_custom_provider_live_metadata_conservative`

---

### Scenario 5 — every composition root honors the same selection rules

Mecated and embedded mecatui use the operator definition and its auth-file key; mecatui
connect mode remains a pure client of the remote server. Mecatequi and mecak8s receive the
same custom-provider configuration through shared command wiring where their existing
provider policy permits it. The default-provider semantics remain server-owned, as
specified by [`docs/configuration-reference.md`](../configuration-reference.md#models)
and [ADR 0238](../adr/0238-operator-defined-llm-providers.md).

**Acceptance:**

- AC5.1: `models.default_provider` accepts an available custom ID, a zero-selector session
  uses that provider and its configured default model, and an unavailable or unknown
  custom default fails startup rather than silently falling back.
  - verify: `TestOperatorDefinedLLMProviders_Scenario5_DefaultProvider`
- AC5.2: explicit CLI base-URL flags override matching `provider_overrides` settings,
  settings override built-in defaults, custom provider definitions remain unaffected by
  built-in override settings, and each override retains the matching existing flag's
  inventory and private-option behavior.
  - verify: `TestOperatorDefinedLLMProviders_Scenario5_OverridePrecedence`
- AC5.3: an explicitly selected custom provider/model persists and rehydrates through the
  same ID; a removed or renamed ID fails loudly rather than selecting a different provider.
  - verify: `TestOperatorDefinedLLMProviders_Scenario5_PersistedSelector`
- AC5.4: mecated, embedded mecatui, mecatequi, and mecak8s construct the same configured
  custom API-key and no-auth providers; mecatui connect mode does not require local
  provider credentials.
  - verify: `TestOperatorDefinedLLMProviders_Scenario5_CompositionRoots`

---

### Scenario 6 — provider credentials are composition-owned

`app.Build` resolves the single operator provider-definition snapshot, invokes an injected
provider-credential loader once, and owns any future credential lifecycle. Command-root
`appConfig` helpers construct declarative configuration and inject the shared loader; tests
use a fake loader rather than bypassing provider loading. This follows the MCP profile
ownership pattern in [`internal/app/build.go`](../../internal/app/build.go) and is pinned
by [ADR 0238](../adr/0238-operator-defined-llm-providers.md).

**Acceptance:**

- AC6.1: `app.Build` supplies its resolved operator provider definitions to one injected
  provider-credential loader before provider default selection and registry construction.
  - verify: `TestADR_0238_BuildLoadsProviderCredentialLoaderOnce`
- AC6.2: a provider-credential loader error aborts Build, and Build closes an acquired
  provider-credential lifecycle exactly once on every later Build failure or normal close.
  - verify: `TestADR_0238_BuildOwnsProviderCredentialLifecycle`
- AC6.3: API-key provider credentials retain one strict `auth.yaml` snapshot and the existing
  CLI endpoint override precedence; custom-provider auth IDs are validated only after
  definitions resolve.
  - verify: `TestInvariant_provider_credentials_auth_snapshot`
- AC6.4: all four command roots inject the same shared provider-credential resolver, while
  mecatequi/mecak8s policy and mecatui connect-mode no-local-provider behavior remain
  unchanged.
  - verify: `TestOperatorDefinedLLMProviders_Scenario6_CredentialLoaderRoots`

---

### Scenario 7 — non-secret endpoint overrides remain command configuration

Command roots map `--*-base-url` values directly into a non-secret endpoint-override map.
`app.Build` merges that command map over settings-derived `provider_overrides` before registry
construction. The registry consumes only the effective map: CLI > settings > built-in default;
custom `ProviderDefinition.BaseURL` remains independent, while empty OpenAI and Anthropic
entries retain their SDK defaults.

**Acceptance:**

- AC7.1: credential loader results contain credentials only, not built-in endpoint fields.
  - verify: `TestInvariant_provider_credentials_auth_snapshot`
- AC7.2: command endpoint overrides win over settings, settings-only entries survive, custom
  provider URLs remain unchanged, and OpenAI/Anthropic preserve empty SDK-default endpoints.
  - verify: `TestOperatorDefinedLLMProviders_Scenario7_EndpointOverridePrecedence`
- AC7.3: all command roots use named declarative config helpers; mecatequi/mecak8s policies and
  mecatui connect behavior remain unchanged.
  - verify: `TestOperatorDefinedLLMProviders_Scenario6_CredentialLoaderRoots`

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| OAuth, token refresh, multiple credentials, arbitrary request headers | later provider-auth decision | [ADR 0238](../adr/0238-operator-defined-llm-providers.md) |
| Custom CA bundles, mTLS, proxies, non-HTTPS endpoints | later transport decision | [ADR 0238](../adr/0238-operator-defined-llm-providers.md) |
| User/project-defined providers and client-supplied credentials | later multi-tenant credential custody | [ADR 0016](../adr/0016-multi-provider.md) |
| Static custom model catalogs and periodic model refresh | later catalog enhancement | [ADR 0016](../adr/0016-multi-provider.md) |
| Automatic protocol detection or fallback | never implicit; a future explicit decision if needed | [ADR 0238](../adr/0238-operator-defined-llm-providers.md) |

## Sequencing recommendation

First add the strict operator configuration and auth-file correlation, then construct the
registry entries behind the existing adapter/remint closures. Add live-listing fallback
only after the entry has a truthful default-model inventory. Finally thread the shared
configuration through each command root and prove CLI-over-settings precedence and
session rehydration.

## Named tests landing in this plan

- `TestInvariant_custom_providers_operator_tier_only`
- `TestADR_0238_BuiltinOverrideAllowlist`
- `TestInvariant_custom_provider_authfile_strict`
- `TestInvariant_custom_provider_has_no_builtin_private_options`
- `TestInvariant_custom_provider_live_metadata_conservative`
- `TestOperatorDefinedLLMProviders_Scenario1_ValidDefinition`
- `TestOperatorDefinedLLMProviders_Scenario2_SeparateAuthFileKeys`
- `TestOperatorDefinedLLMProviders_Scenario3_DeclaredFlavorSelectsAdapter`
- `TestOperatorDefinedLLMProviders_Scenario4_LiveListing`
- `TestOperatorDefinedLLMProviders_Scenario5_CompositionRoots`
- `TestOperatorDefinedLLMProviders_Scenario6_CredentialLoaderRoots`
- `TestOperatorDefinedLLMProviders_Scenario7_EndpointOverridePrecedence`
- `TestADR_0238_BuildLoadsProviderCredentialLoaderOnce`
- `TestADR_0238_BuildOwnsProviderCredentialLifecycle`

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` regenerates `llms.txt` and passes the strict link gate.
3. `task api:check` passes without a core API change; if an exported engine API becomes
   necessary, `task api:update` and the required `engine/CHANGELOG.md` note are included.
4. `task ac-trace-strict` resolves every acceptance proof after the plan is marked
   `landed`.
5. The named tests above are green and grep-locatable by identifier.
6. `go run ./cmd/mecademo` still prints a complete offline session.
7. A manually configured custom provider can be selected by its public `provider_id` while
   credentials and credentialed URLs never reach the API or logs.

## Deferred decisions and known risks

- **Gateway-specific model inventory.** The Stacklok gateway has confirmed both
  Responses (`/v1/models`) and Anthropic (`/anthropic/v1/models`) listings, but the
  implementation remains protocol-generic rather than encoding those host-specific paths.
- **Custom gateway defaults.** The required `default_model` makes zero-selector behavior
  deterministic; operators must update it when their gateway retires that model.
- **Chat Completions gateway support.** The configuration supports the existing adapter,
  but the gateway's invalid request only confirms route handling, not an end-to-end
  streaming request. It remains an operator compatibility check, not an auto-detected
  capability.

## Exit criteria

When every point under *Definition of done* holds on the accumulator, this plan is
satisfied.
