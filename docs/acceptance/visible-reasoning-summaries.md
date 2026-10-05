# Visible reasoning summaries across provider protocols — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — restores the existing display-summary contract across two provider adapters and model discovery without changing a durable public, authority, persistence, or module boundary.
**Decision record:** None — [ADR 0016](../adr/0016-multi-provider.md) already defines unknown versus known thinking metadata and prefix fallback; [ADR 0017](../adr/0017-openai-responses-api.md) already distinguishes requested display summaries from encrypted reasoning replay. No new durable architectural choice is needed.
**Phase:** provider request and discovery correctness
**Status:** proposed, 2026-10-04. The operator chose `reasoning.summary:"auto"` on every OpenAI Responses request; unsupported endpoints remain visible errors rather than triggering speculative fallback.
**Delivery:** Split. Discovery, Anthropic fallback, and Responses request behavior span separate adapters and compatibility cases that merit plan/interface review before implementation.
**Expected tasks:** 3
**Issue:** [stacklok/mecatl#2084](https://github.com/stacklok/mecatl/issues/2084); the Claude 5 fallback slice overlaps [community PR #2100](https://github.com/stacklok/mecatl/pull/2100).
**Plan PR:** [stacklok/mecatl#2118](https://github.com/stacklok/mecatl/pull/2118)
**Approved baseline:** Absent until plan approval.

This contract requires **three distinct Mecatl fixes**, each with its own scenario and acceptance proofs:

1. **Anthropic discovery (Scenario 1):** preserve missing thinking-capability metadata as *unknown* rather than marking the model incapable; explicit unsupported metadata remains authoritative.
2. **Anthropic fallback (Scenario 2):** select adaptive, summarized thinking for Claude 5 when live metadata is unknown, including supported bare and OpenRouter-namespaced IDs. Reuse or credit #2100's bare-ID prefix fix; that PR alone does not satisfy fix 1.
3. **OpenAI Responses requests (Scenario 3):** send `reasoning.summary:"auto"` on every Responses route while keeping display summaries separate from encrypted replay; surface an incompatible endpoint's rejection without an automatic retry.

All three are required for this plan to land. The owning living guide is [provider architecture](../architecture/providers.md); the public [model-selection guide](../../user-docs/features/choose-models.md) owns the operator-facing caveat that summaries depend on provider/model support and may not appear on every turn. Update those pages only with verified implemented behavior, in the implementation PR.

Live evidence behind [#2084](https://github.com/stacklok/mecatl/issues/2084): an internal gateway lists Claude Opus 5.5 with `capabilities` absent; a direct Messages call succeeds with zero summary when thinking is omitted and returns a summary when adaptive + summarized is requested. Without a live descriptor, current Claude 5 prefix fallback returns HTTP 400. On the same gateway's Responses route, GPT-5-mini returned no summary with the current request and a summary with `reasoning.summary:"auto"`; OpenRouter Responses already returned one without an explicit ask. These probes validate specific endpoints and models, not universal model support.

## Human decisions

- [x] Request summaries across the OpenAI Responses adapter — Decision: send `reasoning.summary:"auto"` by default on every Responses request, including canonical OpenAI, OpenRouter, and compatible custom providers, with or without an explicit reasoning effort. Do not add an opt-in setting or retry without the field on rejection; surface an unsupported-field error normally. If compatibility failures arise, investigate and separately approve a narrowly scoped exception rather than add a speculative fallback now.

## Interface contract

- **gRPC / protobuf:** None — the existing `reasoning.delta` event and transport projection already carry display summaries; no new field or wire event is proposed.
- **Exported Go APIs / interfaces:** None — model capability presence is handled inside the existing Anthropic lister/composition projection and request adapter; the engine `LLMRequest`, `Chunk`, and provider ports remain unchanged.
- **Tool schemas:** None — tools and their request/result schemas are unaffected.
- **CLI / config:** None — `reasoning.summary:"auto"` is unconditional for the shared OpenAI Responses adapter. No provider opt-in, new setting, CLI flag, per-session selector, or precedence change is introduced; Anthropic Messages and Chat Completions continue using their own request contracts.
- **Events / persistence:** Existing `ChunkReasoning` → `reasoning.delta` display path remains unchanged, as does `ChunkReasoningItem` replay and durable event handling. A summary is not saved as opaque provider replay data; when upstream sends no summary, no synthetic summary event is created.
- **Security / authority:** Model listing stays authenticated with the existing resolved credential; its discovery and diagnostic projections must not expose credentials, raw listings, provider error bodies, or thinking text. Provider-supplied summaries remain visible *only* through the existing display `reasoning.delta` path, never as permission or routing signals. A provider rejection of `reasoning.summary` is surfaced normally; the adapter does not automatically resend the prompt with a changed request.
- **Compatibility / migration:** Existing sessions and snapshots need no migration. Claude 5 fallback selection extends the current matrix; explicit live unsupported stays authoritative. Every OpenAI Responses endpoint receives `reasoning.summary:"auto"`; a compatible endpoint that rejects the field now surfaces its error, requiring an explicitly scoped follow-up rather than an implicit retry or disabling summaries for all providers. The community PR's prefix change can be reused or merged independently, but cannot close #2084 while the missing-capabilities path remains.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — incomplete native-model discovery does not suppress reasoning

The per-field `Known=false` fallback in [ADR 0016](../adr/0016-multi-provider.md) applies to missing thinking metadata, including a listed model whose `capabilities` object or thinking types are absent. Treat absent `capabilities`, absent `thinking`, absent `thinking.types`, and `thinking.supported:true` with empty/unreported type support as **unknown**. Treat an explicit `thinking.supported:false` as **known unsupported** even if types are absent; explicit `types.adaptive.supported` / `types.enabled.supported` values select supported modes. An empty types object without explicit `thinking.supported:false` is *not* an explicit negative. [Custom-provider composition](../architecture/providers.md) uses the same lister as the native entry.

**Acceptance:**
- AC1.1: a custom Anthropic-protocol model listing with no `capabilities`, no `thinking`, no `types`, or a present-but-empty `types` object without explicit `thinking.supported:false` leaves thinking **unknown**. A request for a prefix-recognized adaptive model therefore sends `thinking:{type:"adaptive",display:"summarized"}`; streamed thinking deltas reach `reasoning.delta`.
  - verify: `TestADR_0016_MissingThinkingCapabilityUsesModelFallback`
- AC1.2: a listing with explicit `thinking.supported:false` remains *known unsupported* and sends no thinking config; a listing explicitly reporting adaptive or manual support selects that mode with summarized display. An older manual-only model does not receive adaptive thinking.
  - verify: `TestVisibleReasoning_Scenario1_ExplicitCapabilitiesWin`
- AC1.3: sparse listing metadata does not change model ID, output/context limits where supplied, the credential boundary, or the distinction between display summary and signed replay blocks.
  - verify: `TestVisibleReasoning_Scenario1_SparseListingPreservesOtherFields`

### Scenario 2 — Claude 5 fallback uses a supported request shape

On missing, failed, or nonmatching live capability lookup, [the Anthropic request builder](../../provider/anthropic/request.go) must not send legacy manual-budget thinking to adaptive-only Claude 5. This implements [ADR 0016's documented prefix fallback](../adr/0016-multi-provider.md) and includes the cases already covered by [PR #2100](https://github.com/stacklok/mecatl/pull/2100); incorporate that contribution instead of duplicating it. The separately registered `openrouter-anthropic` entry advertises `anthropic/claude-...` IDs through its [OpenRouter listing](../../internal/app/modellister.go), which supplies no thinking-type descriptor. The same fallback must recognize that explicit namespace rather than match only bare `claude-...` IDs.

**Acceptance:**
- AC2.1: the Opus 5, Sonnet 5, Fable 5, and Mythos 5 families, including 5.5/5.1 point releases and dated bare-ID snapshots, select adaptive thinking with `display:"summarized"` on missing live metadata; no `budget_tokens` is sent. [Anthropic's model table](https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting) lists these as adaptive-only; unlike #2100, this also covers Mythos 5 without requiring a curated catalog entry. Older Opus/Sonnet/Haiku 4.5 stay manual-only and incapable Claude 3.5 stays without thinking.
  - verify: `TestVisibleReasoning_Scenario2_Claude5AdaptiveFallback`
- AC2.2: a known live descriptor still takes precedence over any prefix; a `claude-opus-5-5` request lacking capability fields from its listed model takes the adaptive fallback, while an explicitly unsupported record omits thinking.
  - verify: `TestVisibleReasoning_Scenario2_LivePrecedenceAndFallback`
- AC2.3: with an authenticated listing failure or timeout, or when a successful listing omits the exact requested model ID, the Opus 5.5 request still uses the adaptive prefix fallback. A failed or sparse refresh does not erase the embedded catalog floor where one exists or the configured custom-provider default model ID.
  - verify: `TestVisibleReasoning_Scenario2_ListingFailureAndModelMiss`
- AC2.4: the Anthropic Messages adapter recognizes an exact OpenRouter `anthropic/claude-...` namespace for the same family classification as its bare Claude ID, including adaptive Opus 5.5, manual-only Opus 4.5, and incapable Claude 3.5; it sends the original namespaced ID unchanged on the wire. Unknown vendor namespaces do not gain Anthropic thinking merely because their ID contains `claude`.
  - verify: `TestVisibleReasoning_Scenario2_NamespacedAnthropicFallback`

### Scenario 3 — Responses asks for display summaries without losing continuity

[ADR 0017](../adr/0017-openai-responses-api.md) documents the `reasoning.summary` request and the independent `reasoning.encrypted_content` replay path. Every provider constructed with the shared OpenAI Responses adapter requests summaries on every model call; a gateway may return none, or reject the request, without the adapter inventing text or retrying with weaker options.

**Acceptance:**
- AC3.1: every Responses route, including canonical OpenAI, OpenRouter, and operator-defined compatible providers, sends `reasoning.summary:"auto"` with or without explicit reasoning effort; configured effort and existing `store:false` plus `include:["reasoning.encrypted_content"]` remain unchanged, and no summary request is sent by Anthropic Messages or Chat Completions adapters.
  - verify: `TestVisibleReasoning_Scenario3_SummaryRequestAndReplayFields`
- AC3.2: streamed `response.reasoning_summary_text.delta` and `response.reasoning_text.delta` yield display-only reasoning through the engine event boundary, while encrypted reasoning items remain opaque and round-trip unchanged both on the next request and after a persisted session reload. Neither the next Responses input nor the persisted opaque replay field contains the display summary. An upstream turn without a summary still completes normally without fabricated reasoning text.
  - verify: `TestVisibleReasoning_Scenario3_SummaryEventsStayDisplayOnly`
- AC3.3: an endpoint/model that rejects `reasoning.summary` returns its provider error to the caller without an adapter retry or a request stripped of the summary option; an unrelated 400 remains an unrelated error. An offline HTTP fixture counts exactly one request per rejected call and confirms no later output or tool side effect is fabricated.
  - verify: `TestVisibleReasoning_Scenario3_UnsupportedSummaryCompatibility`

## Out of scope

| Item | Defer-to | Decision |
| --- | --- | --- |
| Gateway's omitted Anthropic capability metadata | Gateway issue | Fix listing at the source separately; the harness must remain safe when a compatible endpoint omits optional fields. |
| Universal adaptive default for every unknown model | Separate model-support decision | Manual-only older models reject adaptive; keep the existing prefix fallback until a documented replacement exists. |
| Synthetic summaries when the model emits none, or full chain-of-thought exposure | Not supported by this plan | Adaptive models may skip reasoning; summaries are provider text, not guaranteed raw reasoning. |
| Historical summary rehydration from session snapshots | Separate persistence decision | No snapshot or replay contract is changed. |

## Definition of done

1. Focused offline lister, composition, request, and streaming/event tests cover each AC; fixture endpoints never read ambient operator settings, `auth.yaml`, or live network.
2. The implemented behavior replaces stale wording in [provider architecture](../architecture/providers.md); the public [model-selection guide](../../user-docs/features/choose-models.md) is updated by the `user-docs` and `tech-writer` skills for the operator-facing behavior. Run `task docs` and `task site:build` for those changes.
3. `task lint`, `task test:race`, `task api:check`, `task ac-trace-strict`, and `go run ./cmd/mecademo` pass before the implementation PR. Live calls are optional validation with sanitized reporting, not part of the offline gate.
4. The implementation PR credits/reuses #2100 if applicable, links the approved Plan / Interface baseline, and runs `/panel-review` with no ship blocker. A partial prefix-only PR must not prematurely close #2084.

## Deferred decisions and known risks

- Responses-compatible endpoints may reject `reasoning.summary:"auto"` or omit summary deltas. The operator chose to try the universal request without a speculative fallback: a rejection stays visible, and an evidence-backed provider-specific accommodation would require separate human review. Existing gateway model inventories do not reliably identify reasoning support, so using a `reasoning` picker flag alone would silently disable a supported model.
