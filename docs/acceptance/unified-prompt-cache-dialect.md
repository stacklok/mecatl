# Protocol-native prompt-cache breakpoints — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — it changes what mecatl puts on the wire for every Responses request, supersedes three [ADR 0100](../adr/0100-provider-prompt-caching.md) decisions, adds a wire-stable provider identity, and alters what identifying material reaches third-party endpoints.
**Decision record:** [ADR 0346](../adr/0346-unified-prompt-cache-dialect.md)
**Phase:** provider prompt caching, follow-on to ADR 0100 and ADR 0334
**Status:** proposed, 2026-09-16. Found in production via [Slack thread](https://stacklok.slack.com/archives/C0ASC2E5G3X/p1789467849806939), then measured on staging: the OpenRouter downstream served 175 requests in 3 hours at zero cache-read tokens while every Messages-protocol downstream cached heavily.
**Delivery:** Split. A wire change affecting every Responses request, a new provider id, and a change to data sent to third parties.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1558](https://github.com/stacklok/mecatl/issues/1558).
**Plan PR:** [#1572](https://github.com/stacklok/mecatl/pull/1572)
**Approved baseline:** <absent until approved>

Make an explicit-ask upstream cache on **any** Responses endpoint, without consulting the
model's vendor, the provider id, or the base URL. The smallest demonstrable behavior: a
Claude model on the ToolHive gateway's Responses surface reports non-zero `CacheReadTokens`
on its second turn, with no operator configuration and no new provider selected.

## Human decisions

- [x] Ask for the cache via an endpoint-declared dialect, route to a Messages surface, or ask through the protocol? — Decision: ask through the protocol with `prompt_cache_breakpoint`; keep Messages routing where ids are shared because it additionally carries a TTL and four slots.
- [x] Should the breakpoint be gated on the model's vendor family? — Decision: no. A name-keyed rule breaks when a second vendor ships explicit-ask caching, and staging already exposes one model under four ids, so it is wrong on live data today.
- [x] What about the canonical OpenAI endpoint, where earlier models may not support the field? — Decision: one endpoint-plus-capability carve-out reusing `retentionFor`'s existing table, because OpenAI documents neither ignore nor reject for earlier models and ADR 0100 records this repo being bitten by a strict upstream.
- [x] Keep OpenRouter's root `cache_control` alongside the breakpoint? — Decision: retire it; OpenRouter converts a breakpoint for Anthropic and Google, and two mechanisms for one intent is worse than one.
- [x] Apply the default-provider sibling redirect to the ToolHive pair? — Decision: no, only the OpenRouter pair; the gateway's surfaces use different id namespaces so a provider-only switch cannot resolve.
- [x] Should the per-installation cache-key salt be persisted? — Decision: mint per process and do not persist, accepting one sticky-routing lane change per restart.
- [x] Register `openrouter-anthropic` always, or on opt-in? — Decision: always, on OpenRouter credential presence, mirroring ADR 0334's register-on-intent.
- [x] Picker treatment for a non-caching row? — Decision: mark it, do not hide it; an operator may deliberately want the Responses path.

## Interface contract

- **gRPC / protobuf:** one added field, `bool prompt_cached = 7` on `ModelInfo` (numbers 1-6 unchanged; additive, an older client reads false). Requires `task generate`; `contracts/gen` is never hand-edited.
- **Exported Go APIs / interfaces:** `provider/openai` (separately released, ADR 0093). **Changed:** every dialect now emits one `prompt_cache_breakpoint` on an `input_text` block, and `CacheDialectOpenRouter` no longer emits root `cache_control`, so a consumer's wire changes without changing its code — Changed (breaking) for that module's next tag under `engine/COMPATIBILITY.md`. **Added:** `WithCacheKeySalt(string)`. `CacheDialect` keeps its three constants and gains no value. `port.LLMRequest` untouched, preserving the reflect whitelist in `engine/port/llm_neutral_test.go`.
- **Tool schemas:** None — no tool gains, loses, or changes a field.
- **CLI / config:** no new flag or key. One OpenRouter credential registers both `openrouter` and `openrouter-anthropic`; the new id is reserved against custom-provider collisions. `--no-prompt-cache` keeps its exact meaning and is now the escape hatch for the breakpoint too; `--anthropic-cache-ttl` now reaches Claude-over-OpenRouter via the Messages entry.
- **Events / persistence:** None — this change persists no session state and emits no event. The cache-key salt is process-local and never persisted or transmitted as itself. `session.Usage.CacheReadTokens`/`CacheWriteTokens` already carry the observable effect.
- **Security / authority:** the breakpoint is a fixed protocol constant carrying no user, model, or config value, so it opens no injection surface. The derived Anthropic base is built with `net/url` per ADR 0334, copying no userinfo, query, or fragment. The salted key removes cross-principal correlation of workspace configuration via a stable content fingerprint. Scenario 6 extends ADR 0334's refuse-redirects decision to the generic openai-compat constructor.
- **Compatibility / migration:** the emitted Responses body changes for every request (one breakpoint added, root `cache_control` removed where it was sent), which shifts the cached prefix once and rebuilds it. No persisted-state change, no proto-breaking change, no migration.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — an explicit-ask upstream caches on any Responses endpoint

The general fix, per [ADR 0346](../adr/0346-unified-prompt-cache-dialect.md) decisions 1-4.
Explicit breakpoints are additive to implicit caching, so one marker is legal on every
request and no vendor needs naming.

**Acceptance:**
- AC1.1: every Responses request carries exactly one `prompt_cache_breakpoint` of mode `explicit`, on an `input_text` content block, regardless of provider id, base URL, or model vendor.
  - verify: `TestADR_0346_BreakpointEmittedVendorAgnostic`
- AC1.2: the breakpoint sits at the previous-turn boundary, so turn N writes what turn N+1 reads, and never on top-level `instructions`, which the protocol forbids.
  - verify: `TestADR_0346_BreakpointAtPreviousTurnBoundary`
- AC1.3: a turn-0 request with no previous turn emits no breakpoint rather than marking a boundary that cannot be reused.
  - verify: `TestADR_0346_NoBreakpointWithoutAPreviousTurn`
- AC1.4: root `cache_control` is emitted by no dialect, on any endpoint.
  - verify: `TestADR_0346_RootCacheControlRetired`
- AC1.5: on the canonical OpenAI endpoint a model outside `retentionFor`'s supported set gets no breakpoint, and a supported one does; no other endpoint consults the model at all.
  - verify: `TestADR_0346_CanonicalOpenAICarveOut`
- AC1.6: `--no-prompt-cache` suppresses the breakpoint along with every other hint, reproducing the pre-ADR-0100 wire.
  - verify: `TestADR_0346_NoPromptCacheSuppressesBreakpoint`
- AC1.7: a usage payload reporting cached input tokens folds into `session.Usage.CacheReadTokens` as non-zero through a dialect-less endpoint, which is the reported incident's exact shape.
  - verify: `TestADR_0346_BreakpointCacheReadE2E`

### Scenario 2 — Claude on OpenRouter caches through Messages, with a TTL

Extends [ADR 0334](../adr/0334-toolhive-protocol-specific-providers.md)'s protocol-specific
pattern to OpenRouter, per ADR 0346 decision 5. Complementary to Scenario 1: four breakpoint
slots and a real TTL where the surface exists.

**Acceptance:**
- AC2.1: one OpenRouter credential registers both `openrouter` and `openrouter-anthropic`, and the new id is refused to a custom provider definition.
  - verify: `TestUnifiedPromptCache_Scenario1_OpenRouterRegistersBothProtocolEntries`
- AC2.2: the derived Anthropic base sheds a terminal `v1` so the SDK's suffix resolves to `/api/v1/messages`, copying no userinfo, query, or fragment.
  - verify: `TestADR_0346_OpenRouterAnthropicBaseDerivation`
- AC2.3: a request on `openrouter-anthropic` carries the ADR 0100 breakpoint budget, and `--anthropic-cache-ttl=1h` stamps a uniform ttl on every marker.
  - verify: `TestADR_0346_AnthropicCacheTTLStampedOnOpenRouterAnthropic`
- AC2.4: `openrouter-anthropic` lists Anthropic-family models only, never an id its endpoint cannot execute.
  - verify: `TestADR_0346_OpenRouterAnthropicListsAnthropicOnly`
- AC2.5: `openrouter`'s own base URL is unchanged by this work.
  - verify: `TestADR_0346_OpenRouterResponsesEntryByteIdentical`

### Scenario 3 — a Claude default is redirected only where ids are shared, and marked everywhere else

ADR 0346 decision 6. The ToolHive pair is deliberately excluded: staging shows its surfaces
use different id namespaces, so a provider-only switch cannot resolve.

**Acceptance:**
- AC3.1: an OpenRouter-only deployment defaulting to a Claude model resolves to the Messages entry.
  - verify: `TestUnifiedPromptCache_Scenario2_ClaudeDefaultPrefersMessagesEntry`
- AC3.2: a gateway-only deployment defaulting to a Claude model is NOT redirected, because the sibling does not share the id namespace; it relies on Scenario 1 instead.
  - verify: `TestADR_0346_GatewayPairNotRedirected`
- AC3.3: a non-Claude default resolves exactly as today, and an explicit `--default-provider` or operator `models.default_provider` always wins.
  - verify: `TestADR_0346_NonClaudeDefaultPrecedenceUnchanged`
- AC3.4: `ListModels` reports `prompt_cached` per (provider, model), computed once in composition's single projection per [AGENTS.md](../../AGENTS.md)'s single-intersection rule.
  - verify: `TestADR_0346_PromptCachedProjectedOncePerModel`
- AC3.5: the picker marks a row with no cache breakpoint and the row stays selectable.
  - verify: `TestUnifiedPromptCache_Scenario2_PickerMarksUncachedRow`

### Scenario 4 — the cache key carries no cross-principal fingerprint

[ADR 0346](../adr/0346-unified-prompt-cache-dialect.md) decision 7, independently justified:
the endpoints already receiving the key receive an unsalted one today.

**Acceptance:**
- AC4.1: two processes with identical `Config` and an identical first prompt emit different `prompt_cache_key` values.
  - verify: `TestADR_0346_CacheKeySaltedPerProcess`
- AC4.2: within one process the key is byte-stable across turns and across compaction, and concurrent Subagent children still differ by anchor.
  - verify: `TestUnifiedPromptCache_Scenario3_KeyStableAcrossTurnsAndCompaction`
- AC4.3: the salt is never persisted, never logged, and never sent as itself.
  - verify: `TestADR_0346_CacheKeySaltNeverPersistedOrLogged`
- AC4.4: a consumer passing no salt Option gets ADR 0100's exact derivation.
  - verify: `TestADR_0346_CacheKeyUnsaltedWithoutOption`

### Scenario 5 — the resolved posture is visible

ADR 0100's failure mode was silence. Per [AGENTS.md](../../AGENTS.md)'s diagnostics rule this
is `port.Diagnostics` work, since no `session.Event` owns it.

**Acceptance:**
- AC5.1: one build-once INFO line names, per registered provider, the resolved cache posture and its source, emitted from `app.Build` and not the per-engine deps builders.
  - verify: `TestUnifiedPromptCache_Scenario4_PostureLineNamesPostureAndSource`
- AC5.2: the line is computed from the same resolution the adapter received and is asserted against the emitted body, so a dropped field cannot hide behind a healthy-looking line.
  - verify: `TestADR_0346_BreakpointCacheReadE2E`,
    `TestADR_0346_AnthropicCacheTTLStampedOnOpenRouterAnthropic`
- AC5.3: no cache-posture diagnostic repeats per session or per heal re-mint.
  - verify: `TestADR_0346_CachePostureLogsOncePerProcess`
- AC5.4: `prompt_cache_retention` still never accompanies a non-OpenAI dialect.
  - verify: `TestADR_0346_LeakGuardBothDirectionsStillHolds`

### Scenario 6 — a compatible endpoint cannot silently relocate the conversation

`newOpenAICompatEntry` passed no `CheckRedirect`, so a 307 re-sends the body. This applies
ADR 0334's existing gateway decision to the generic constructor.

**Acceptance:**
- AC6.1: an openai-compat entry whose endpoint answers 307 to another host errors instead of delivering the body there.
  - verify: `TestADR_0346_OpenAICompatEntryRefusesRedirect`
- AC6.2: redirect refusal rides every per-session and heal re-mint.
  - verify: `TestADR_0346_AnthropicProtocolEntryRefusesRedirect`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Operator-declared dialect permission | rejected | [ADR 0346](../adr/0346-unified-prompt-cache-dialect.md) decision 1: asking through the protocol needs no declaration, and a declaration asserts a remote mutable property whose wrong value is a hard outage |
| Vendor-family gating of the breakpoint | rejected | ADR 0346 decision 1: breaks on a second explicit-ask vendor, and staging already exposes one model under four ids |
| ToolHive sibling redirect | rejected | ADR 0346 decision 6: the surfaces use different id namespaces, so a provider-only switch cannot resolve; Scenario 1 covers the case instead |
| `prompt_cache_options.ttl` replacing the deprecated `prompt_cache_retention` | later | OpenAI deprecated the field this repo still sends on its canonical endpoint; a separate migration with its own model gating |
| Multiple breakpoints (the protocol allows three explicit alongside implicit) | later | One at the turn boundary captures the growing conversation; more slots need placement evidence, not guesswork |
| Live probe of a pre-5.6 OpenAI model | later | Would settle whether decision 2's carve-out can be deleted; costs real money on a live endpoint, and the carve-out is the conservative reading either way |
| Gateway-side `cache_control` injection | gateway repo | Unnecessary for mecatl after Scenario 1, but would fix every other client behind that gateway |
| Low-cache-hit-rate alerting | [#613](https://github.com/stacklok/mecatl/issues/613) | mecatui already renders `cache N%` |
| `openaichat` breakpoint parity | later | Chat Completions takes the marker on a `text` block; the same decision applies but no registered consumer needs it yet |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, `task generate`, and `task api:check` gates pass, with regenerated `contracts/gen` committed and never hand-edited.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. `user-docs/features/choose-models.md` is updated in the same PR per [AGENTS.md](../../AGENTS.md)'s same-PR rule, and `task site:build` passes.
5. `provider/openai`'s wire change is classified Changed (breaking) for that module's next tag per `engine/COMPATIBILITY.md`.
6. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
7. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The canonical-OpenAI carve-out rests on undocumented behaviour. OpenAI's guide says only
  "Only implicit caching is supported" for earlier models and states neither ignore nor
  reject. One live probe deletes or justifies the carve-out; until then it is the conservative
  reading of an endpoint this repo has already been bitten by.
- A strict OpenAI-compatible endpoint rejecting unknown fields inside a content block now sees
  the breakpoint for every model, not only an explicit-ask one. That is the accepted price of
  removing the vendor gate, bounded by `--no-prompt-cache` and the per-endpoint dialect.
- A genuine one-shot run writes a breakpoint nothing reads, costing roughly 0.25x on that
  request, because "this will be the only turn" is not knowable before the turn.
- Emitting a breakpoint requires an `input_text` block where the adapter currently emits a
  bare string for text-only messages. That shifts the cached prefix once per deployment, then
  it is stable again.
