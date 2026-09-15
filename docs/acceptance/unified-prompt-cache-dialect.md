# Claude prompt caching via native Messages routing — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — it adds a wire-stable provider identity, changes default-provider precedence, alters what identifying material mecatl transmits to third-party endpoints, and retires one [ADR 0100](../adr/0100-provider-prompt-caching.md) deferral.
**Decision record:** [ADR 0343](../adr/0343-unified-prompt-cache-dialect.md)
**Phase:** provider prompt caching, follow-on to ADR 0100 and ADR 0334
**Status:** proposed, 2026-09-15. Found in production via [Slack thread](https://stacklok.slack.com/archives/C0ASC2E5G3X/p1789467849806939): a budget exhausted in roughly two hours on Claude Opus 4.8 through the ToolHive gateway with cache reads at zero.
**Delivery:** Split. A new wire-stable provider id, a default-precedence change, and a change to data sent to third parties, so behaviour and interfaces need review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1558](https://github.com/stacklok/mecatl/issues/1558).
**Plan PR:** <added when opened>
**Approved baseline:** <absent until approved>

Make a Claude model land on a caching path by default, on OpenRouter and on the ToolHive
gateway, without new operator configuration and without sending an extension field to an
endpoint mecatl cannot classify. The smallest demonstrable behavior: a session whose default
model is a Claude id reports non-zero `CacheReadTokens` on its second turn, on a deployment
where the only configured credential is an OpenRouter key.

## Human decisions

- [x] Build an operator-declared dialect permission, or route Claude to a native Messages surface? — Decision: route to Messages; drop the declaration surface entirely, and document `api_flavor: anthropic-messages` as the answer for the two residual shapes.
- [x] Should the per-installation cache-key salt be persisted? — Decision: mint per process and do not persist, accepting one sticky-routing lane change per restart in exchange for no durable pseudonymous identifier.
- [x] Should ADR 0100's canonical-OpenAI leak guard survive as a hard refusal? — Decision: yes; with the declaration surface dropped it survives intact as ADR 0100's table, and AC4.4 pins it against regression.
- [x] Register `openrouter-anthropic` always, or only on opt-in? — Decision: always, on OpenRouter credential presence, mirroring ADR 0334's register-on-intent so the default fix needs no operator action.
- [x] What should the picker do when a Claude id is reachable under both ids? — Decision: mark the non-caching row rather than hiding it, preserving deliberate choice while making the expensive option visibly expensive.
- [x] Should `prompt_cache_key` stay gated by the dialect? — Decision: yes; routing fixes the endpoints that previously got nothing, so `CacheDialectNone` stays byte-identical and the only wire delta is the salted key value.

## Interface contract

- **gRPC / protobuf:** one added field, `bool prompt_cached = 7;` on `ModelInfo` (`contracts/proto/mecatl/v1/harness.proto`, next free number; field numbers 1-6 unchanged). True when a session on that (provider, model) pair caches its conversation prefix. Additive and backward compatible: an older client ignores it and reads `false`. Requires `task generate`; `contracts/gen` is never hand-edited. `provider_id` remains an open string and carries `openrouter-anthropic` with no other message, field, method, or number change, exactly as `toolhive-anthropic` did under [ADR 0334](../adr/0334-toolhive-protocol-specific-providers.md).
- **Exported Go APIs / interfaces:** Added, in `provider/openai` (a separately released module, ADR 0093): one `WithCacheKeySalt(string)` Option. A consumer passing no Option keeps today's exact `prompt_cache_key` derivation, so `CacheDialectNone` stays byte-identical and this is Added (minor), not Changed, under `engine/COMPATIBILITY.md`. The `engine/api/*.txt` gate is engine-only, so `task api:update` is not triggered. Everything else reuses `provider/anthropic`'s existing Options and `anthropic.NewLister`; registry family classification and URL derivation stay root-internal composition. `port.LLMRequest` is untouched, preserving the reflect whitelist in `engine/port/llm_neutral_test.go`.
- **Tool schemas:** None — provider routing changes add no model-facing tool and alter no schema.
- **CLI / config:** no new flag or key. One OpenRouter credential registers both `openrouter` and `openrouter-anthropic`; the new id is reserved against custom-provider collisions. `--default-provider` and operator `models.default_provider` may select it explicitly. `--no-prompt-cache` and `--anthropic-cache-ttl` keep their exact current meaning and now reach Claude-over-OpenRouter, which the latter previously could not. No `prompt_cache:` subtree is introduced.
- **Events / persistence:** None — no event or snapshot shape changes and no migration. Existing provider-id strings may persist `openrouter-anthropic`, so it is registered on credential presence to stay resolvable. The cache-key salt is process-local and never persisted or transmitted as itself.
- **Security / authority:** the derived Anthropic base is built with `net/url` per ADR 0334, copying no userinfo, query, or fragment, and never by string concatenation. `gateway_url` remains irrelevant here: OpenRouter's base is a constant, not a detected value. The salted key removes cross-principal correlation of workspace configuration via a stable content fingerprint sent to arbitrary endpoints. Scenario 5 extends ADR 0334's existing refuse-redirects decision to the generic openai-compat constructor, so a 307 cannot relocate a conversation body to an unintended host.
- **Compatibility / migration:** additive. No endpoint's extension wire changes, ADR 0100's dialect table is untouched, and the only wire delta is the salted `prompt_cache_key` value on endpoints that already send it. A Claude id becomes reachable under a second provider id; no migration, no persisted-state change, no proto change.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — Claude on OpenRouter caches, with a TTL

Extends [ADR 0334](../adr/0334-toolhive-protocol-specific-providers.md)'s protocol-specific
pattern to OpenRouter, per
[ADR 0343](../adr/0343-unified-prompt-cache-dialect.md) decision 2. Emitting breakpoints is
not the deliverable; non-zero cache reads are.

**Acceptance:**
- AC1.1: one OpenRouter credential registers both `openrouter` and `openrouter-anthropic`, and the new id is refused to a custom provider definition.
  - verify: `TestUnifiedPromptCache_Scenario1_OpenRouterRegistersBothProtocolEntries`
- AC1.2: the derived Anthropic base replaces a terminal `v1` segment so the SDK's own suffix resolves to `/api/v1/messages`, and copies no userinfo, query, or fragment.
  - verify: `TestADR_0343_OpenRouterAnthropicBaseDerivation`
- AC1.3: a request on `openrouter-anthropic` carries the ADR 0100 breakpoint budget, and `--anthropic-cache-ttl=1h` stamps a uniform ttl on every emitted marker.
  - verify: `TestUnifiedPromptCache_Scenario1_OpenRouterAnthropicCarriesBreakpointsAndTTL`
- AC1.4: a usage payload reporting cached input tokens folds into `session.Usage.CacheReadTokens` as non-zero through this path, closing the loop the plan exists to close.
  - verify: `TestUnifiedPromptCache_Scenario1_MessagesPathReportsCacheReads`
- AC1.5: `openrouter-anthropic` lists Anthropic-family models only, never a non-Anthropic id its endpoint cannot execute.
  - verify: `TestADR_0343_OpenRouterAnthropicListsAnthropicOnly`
- AC1.6: `openrouter`'s own base URL and emitted request bytes are unchanged by this work.
  - verify: `TestADR_0343_OpenRouterResponsesEntryByteIdentical`

### Scenario 2 — a Claude default no longer lands on an uncached surface

The reported user's session reached the uncached entry by default, not by choosing it.
[ADR 0343](../adr/0343-unified-prompt-cache-dialect.md) decision 3 makes that a stated rule
rather than a consequence of sort order in `preferredDefaultProvider`.

**Acceptance:**
- AC2.1: a deployment whose only credential is an OpenRouter key, defaulting to a Claude model, resolves to the Messages entry rather than the Responses entry.
  - verify: `TestUnifiedPromptCache_Scenario2_ClaudeDefaultPrefersMessagesEntry`
- AC2.2: the same holds for a gateway-only deployment, which today sorts `toolhive` ahead of `toolhive-anthropic`.
  - verify: `TestUnifiedPromptCache_Scenario2_GatewayClaudeDefaultPrefersAnthropicEntry`
- AC2.3: a non-Claude default is unaffected and still resolves exactly as it does today, across the existing precedence table.
  - verify: `TestADR_0343_NonClaudeDefaultPrecedenceUnchanged`
- AC2.4: an explicit `--default-provider` or operator `models.default_provider` still wins over this preference.
  - verify: `TestADR_0343_ExplicitDefaultProviderOverridesCachingPreference`
- AC2.5: `ListModels` reports `prompt_cached` false for a Claude id under `openrouter` and true under `openrouter-anthropic`, computed once in composition's single model projection rather than recomputed per sink, per [AGENTS.md](../../AGENTS.md)'s single-intersection rule.
  - verify: `TestADR_0343_PromptCachedProjectedOncePerModel`
- AC2.6: the mecatui picker visibly marks a row whose `prompt_cached` is false, and the row stays selectable.
  - verify: `TestUnifiedPromptCache_Scenario2_PickerMarksUncachedRow`

### Scenario 3 — the cache key carries no cross-principal fingerprint

Per [ADR 0343](../adr/0343-unified-prompt-cache-dialect.md) decision 4. Independently
justified: the two endpoints that receive the key today already receive an unsalted one.

**Acceptance:**
- AC3.1: two `app.Build` instances with identical `Config` and an identical first prompt emit different `prompt_cache_key` values.
  - verify: `TestADR_0343_CacheKeySaltedPerProcess`
- AC3.2: within one process the key stays byte-stable across the turns of a run and across a compaction, and concurrent Subagent children sharing an explorer prefix still differ by anchor.
  - verify: `TestUnifiedPromptCache_Scenario3_KeyStableAcrossTurnsAndCompaction`
- AC3.3: the salt is never persisted, never logged, and never sent as itself.
  - verify: `TestADR_0343_CacheKeySaltNeverPersistedOrLogged`
- AC3.4: a `provider/openai` consumer passing no `WithCacheKeySalt` Option gets today's exact derivation, so the module's zero value is byte-identical.
  - verify: `TestADR_0343_CacheKeyUnsaltedWithoutOption`

### Scenario 4 — the resolved caching posture is visible, and the old guard still holds

ADR 0100's failure mode was silence. Per [AGENTS.md](../../AGENTS.md)'s diagnostics rule this
is `port.Diagnostics` work, since no `session.Event` owns it.

**Acceptance:**
- AC4.1: one build-once INFO line names, per registered provider, the resolved cache posture and its source, emitted from `app.Build` and not from the per-engine deps builders.
  - verify: `TestUnifiedPromptCache_Scenario4_PostureLineNamesPostureAndSource`
- AC4.2: the line is computed from the same resolution whose result reaches the adapter, and the assertion compares it against the emitted request body, so a `SetExtraFields` replacement cannot drop a field while the line still reports it.
  - verify: `TestUnifiedPromptCache_Scenario4_PostureLineMatchesEmittedBody`
- AC4.3: no cache-posture WARN or INFO repeats per session or per heal re-mint, preserving the build-time-not-per-remint discipline `normaliseAnthropicCacheTTL` already enforces.
  - verify: `TestADR_0343_CachePostureLogsOncePerProcess`
- AC4.4: root `cache_control` still never reaches the canonical OpenAI endpoint, and `prompt_cache_retention` still never accompanies the OpenRouter dialect.
  - verify: `TestADR_0343_LeakGuardBothDirectionsStillHolds`

### Scenario 5 — a compatible endpoint cannot silently relocate the conversation

`newOpenAICompatEntry` passes no `CheckRedirect`, so `openai`, `openrouter` and
`openai-codex` follow up to 10 redirects and re-send the body on a 307 or 308. Go strips
`Authorization` cross-domain, so the key does not travel, but the system prompt, file
contents and tool results do. ADR 0334 already decided refuse-redirects for the gateway
entries; this applies that decision consistently.

**Acceptance:**
- AC5.1: an openai-compat entry whose endpoint answers 307 to another host errors instead of delivering the request body to that host.
  - verify: `TestADR_0343_OpenAICompatEntryRefusesRedirect`
- AC5.2: redirect refusal rides every per-session and heal re-mint.
  - verify: `TestUnifiedPromptCache_Scenario5_RedirectRefusalSurvivesRemint`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Operator-declared dialect permission for arbitrary endpoints | rejected | [ADR 0343](../adr/0343-unified-prompt-cache-dialect.md) decision 1: it asks an operator to assert a remote, mutable property, a wrong assertion is a hard outage, and it buys less than routing |
| Custom `openai-responses` definition or base-URL override serving Claude | documentation | The operator registers the same endpoint as `api_flavor: anthropic-messages`, which caches unconditionally today |
| Ungating `prompt_cache_key` on endpoints resolving to no dialect | later | Its motivation was the endpoints that got nothing; routing fixes those, so `CacheDialectNone` stays byte-identical and this plan's wire delta stays limited to the salted key value |
| Model-family narrowing of root `cache_control` | not needed | ADR 0100's dialect table is untouched, so the question does not arise |
| GPT-5.6 `prompt_cache_breakpoint` / `prompt_cache_options` | later | Needs an `input_text` content block, conflicting with the bare-string fast path that keeps the prefix byte-stable; also requires fixing `SetExtraFields`' replace semantics first |
| Gateway-side `cache_control` injection | gateway repo | The gateway knows its backend where mecatl cannot, and would fix every client behind it |
| Low-cache-hit-rate alerting | [#613](https://github.com/stacklok/mecatl/issues/613) | mecatui already renders `cache N%`; alerting is a separate capability |
| `openaichat` cache parity | later | That adapter has no `cache_control` dialect to grant; a Chat-Completions endpoint serving Claude takes the same `anthropic-messages` answer as the row above |
| Fail-closed operator-file parse handling for `guardrails:` / `posture:` / `models:` | later | A pre-existing whole-file fail-open, wider than this plan, and no longer touched now that no new subtree is added |
| Reconciling ADR 0210 `openrouter.order` with the Anthropic surface's first-party-only guarantee | later | An operator-visible configuration conflict, not a mecatl-resolvable one |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, `task generate`, and `task api:check` gates pass, with regenerated `contracts/gen` committed and never hand-edited.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. `user-docs/features/choose-models.md` and the OpenRouter provider guidance are updated in
   the same PR, per [AGENTS.md](../../AGENTS.md)'s same-PR rule for user-facing behaviour, since
   a Claude id now appears under two provider ids and only one caches. The `user-docs` CI job
   cannot detect a missing behaviour update.
5. `provider/openai`'s new Option is classified Added (minor) for that module's next tag per
   `engine/COMPATIBILITY.md`.
6. The implementation PR links the Plan / Interface PR and approved commit and reports
   interface conformance.
7. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- OpenRouter documents the Anthropic surface's base URL and directs callers to the Anthropic
  Messages API for TTL control, which is strong evidence it honours `cache_control` with a
  `ttl`. It is not a first-party contract test, so AC1.3 and AC1.4 are the acceptance gate: if
  the surface silently ignores breakpoints, the plan has not delivered regardless of what the
  request body contains.
- Scenario 5 is arguably its own PR. It is carried here because this plan promotes additional
  compatible endpoints to first-class use and ADR 0334 already decided the policy for a
  sibling constructor. Splitting it out during orchestration is acceptable.
