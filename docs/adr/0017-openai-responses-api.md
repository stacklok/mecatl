# ADR 0017 — OpenAI Responses API adapter

- Status: Accepted (research/implementation brief; the adapter shipped)
- Date: 2026-05-29
- Scope: the stateless Responses API adapter for OpenAI and OpenRouter, including reasoning-blob replay, phase capture, prompt-cache strategy, and streaming assembly

## Context

mecatl needed a production Go adapter for OpenAI's Responses API (`POST /v1/responses`) — the successor to Chat Completions and the Assistants API, with richer reasoning-item support and better cache utilisation. The primary design questions were: client-owned state vs server-side chaining, how to handle encrypted reasoning items across turns in a stateless harness, and how to preserve the `phase` marker that GPT-5.x requires to avoid treating preambles as final answers.

## Decision

The adapter uses the stateless, client-owned strategy (`store:false`, no `previous_response_id`): the harness owns the full `input` item slice, compacts it, and replays reasoning items verbatim each turn via `include:["reasoning.encrypted_content"]`. The `phase` marker on assistant message items is captured as an opaque string and replayed byte-for-byte. The prompt-prefix cache strategy puts static content (instructions, tools, stable context) before volatile per-turn content and tool outputs.

## Consequences

The adapter is portable across OpenAI-compatible endpoints; server-side state chaining is not used, so all compaction, persistence, and multi-provider portability remain in the harness. The reasoning blob and phase replay are regression-pinned by tests. OpenAI-only features (encrypted reasoning, `prompt_cache_key`, hosted tools) degrade gracefully on compatible endpoints. Current behaviour is described in `docs/architecture.md`; shipped/deferred state is tracked in [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md).

---

> Scope: building a Go harness that owns its own context window, compaction, and tool loop, talking to OpenAI and OpenAI-compatible `/v1/responses` endpoints via `github.com/openai/openai-go/v3`.
> Captured 2026-05-29 by the `responses-researcher` agent. Treat SDK field/constructor names as "verify against pinned `api.md`".

---

## 1. Responses API vs Chat Completions

The **Responses API** (`POST /v1/responses`) is OpenAI's unified, agent-oriented interface. It is a **superset of Chat Completions** and the official successor to both Chat Completions (still supported) and the deprecated **Assistants API**. Cited benefits: better reasoning-model performance (reasoning items preserved across turns), 40–80% better cache utilization, lower cost, native agentic tooling.

**Messages vs. items.** Chat Completions sends `messages: [{role, content}]` and returns `choices[0].message`. Responses sends an `input` (string *or* array of typed **Items**) and returns a flat `output` array of typed items. Items are a discriminated union: `message`, `function_call`, `function_call_output`, `reasoning`, plus hosted-tool items.

Key request fields: `model`, `input`, `instructions` (NOT carried forward with `previous_response_id` — resend each turn), `tools`/`tool_choice`, `previous_response_id`, `store` (default true on OpenAI; set false for stateless/ZDR), `reasoning` (`{effort, summary}`), `text` (replaces `response_format`; `text.format` carries structured-output schema), `include` (e.g. `["reasoning.encrypted_content"]`), `max_output_tokens`, `parallel_tool_calls`, `prompt_cache_key`.

Response: `id`, `status` (`completed`/`incomplete`/`failed`), `output[]`, `usage`, `output_text` convenience.

**Internalize:** there is no single "assistant message string." A turn's result is an ordered list of items (maybe a `reasoning` item, then `function_call` items, then a `message`). To continue, append those output items *plus* your `function_call_output` items to the next request's `input`. Tool config is **internally-tagged** (`{"type":"function","name":...,"parameters":...}`) and `strict` defaults to true.

## 2. Streaming (SSE)

`stream: true` → server emits **semantic, typed SSE events** with `type` + monotonic `sequence_number`.

- Lifecycle: `response.queued` → `response.created` → `response.in_progress`; terminal `response.completed` (carries final `response` incl. `usage`), `response.incomplete`, `response.failed`, transport `error`.
- Output assembly (add→delta→done): `response.output_item.added` (gives `output_index`, item stub incl. `call_id`), `response.content_part.added`, `response.output_text.delta`, `response.output_text.done`, `response.content_part.done`, `response.output_item.done`.
- Function-call streaming: `output_item.added` (type `function_call`, `call_id`, `name`) → `response.function_call_arguments.delta` (repeated; append in `sequence_number` order) → `response.function_call_arguments.done` (full args JSON — parse THIS, not your concatenation) → `output_item.done`.
- Reasoning: `response.reasoning_summary_part.added`, `response.reasoning_summary_text.delta/done` (raw reasoning not streamed in plaintext).

**Assembly rule:** stream deltas to the client for responsiveness, but act on `.done`/`response.completed` payloads as authoritative (especially before invoking a tool). `usage` appears ONLY on `response.completed`.

**Single visible text part:** the harness assembles exactly ONE visible assistant text part per turn, matching `session.Message.Text` being a single string. The adapter pins the part identity (`item_id`/`output_index`/`content_index`) on the first `response.output_text.delta` and **loud-errors** on any later text delta with a different identity (a second message item, or a second `output_text` content part) rather than silently fusing two distinct parts into one buffer. Ordering is not at risk (the stream is serial and `sequence_number`-monotonic) — only part identity is, so this is a tripwire for a shape that essentially never occurs. Reasoning summary deltas are exempt (display-only, keyed by `summary_index`, legitimately concatenated).

**Cancellation:** (a) cancel the `context.Context`/close the stream (normal foreground path; abandons partial output); (b) server-side `POST /v1/responses/{id}/cancel` (only meaningful with `store:true`/background).

## 3. Function / Tool Calling

Declaration (internally tagged), model emits `function_call` items with `call_id` (the correlation key, distinct from item `id`) and `arguments` as a JSON **string**. Submit results as `function_call_output` input items keyed by `call_id`.

**The loop:** send → receive output items → for each `function_call`, execute locally → append the original `function_call` item(s) AND your `function_call_output` item(s) AND any `reasoning` items to `input` → resend → repeat until a `message` with no further calls.

`parallel_tool_calls` (default true): model may emit multiple calls per turn; return one `function_call_output` per `call_id`. Set `false` to force ≤1 tool call/turn — often desirable for ordered/serial tool effects in a coding harness.

`tool_choice`: `"auto"`/`"required"`/`"none"`/named. `allowed_tools` restricts the active subset while keeping the full tool list in the prefix for cache stability.

**Strict mode:** every object needs `additionalProperties:false` and every property in `required` (model-optional → nullable union `["string","null"]`). Structured *final* output uses `text.format` with a JSON schema.

## 4. Conversation State

- **A. Server-side chaining** — `previous_response_id` + `store:true`. Minimal payloads, automatic reasoning-item preservation, best cache. But state lives on OpenAI, you don't control/compact it, not portable across compatible endpoints, `instructions` not inherited.
- **B. Stateless / client-owned** — `store:false`, no `previous_response_id`, resend full `input` each turn. You own the transcript, can compact/persist/replay, portable. Must manually carry forward reasoning items (with `include:["reasoning.encrypted_content"]` when `store:false`).

**Recommendation: strategy B (own your own message list).** A harness doing its own compaction, persistence, checkpointing, multi-provider support must not delegate state. Maintain an ordered input-item slice as source of truth; append model output + `function_call_output` + reasoning items each turn; compact over your own slice; keep system prompt + tool defs as a stable prefix for prefix-cache hits.

## 5. Prompt Caching

Automatic prefix caching, no opt-in: prompts ≥ **1024 tokens** eligible; hits grow in 128-token increments; only **exact prefix matches** hit (any byte change before a point invalidates everything after). ~50% discount on cached input + latency win.

**Structure static-first, dynamic-last:** (1) `instructions`/system prompt; (2) `tools` array (identical ordering/content every turn — use `allowed_tools` to gate, don't reorder/conditionally include); (3) long stable context; (4) volatile per-turn content + tool outputs last. Keep tool JSON schemas byte-stable, no timestamps/IDs in the prefix, append don't splice.

`prompt_cache_key`: optional string to bias routing per session/workspace (>~15 req/min per (prefix,key) overflows). Read usage via `usage.input_tokens_details.cached_tokens`; log cached/total ratio. TTL ~5–10 min idle up to ~1h; some models support `prompt_cache_retention:"24h"`.

## 6. Reasoning Models

`reasoning.effort` (`none`…`xhigh`; GPT-5.5-class defaults `medium`) — expose as a per-task knob. Reasoning items are hidden; billed as output tokens, reported under `usage.output_tokens_details.reasoning_tokens` (the engine now surfaces this on `session.Usage.ReasoningTokens` as a subset of `OutputTokens` — additive observability, the budget brake is unchanged). `reasoning.summary` (`auto`/`detailed`) returns a human-readable summary item (streamed via `reasoning_summary_text.delta`).

**Encrypted reasoning for stateless use:** with `store:false`, add `include:["reasoning.encrypted_content"]`; reasoning items carry `encrypted_content`; pass back verbatim. OpenAI decrypts in-memory only.

**Critical multi-turn rule:** preserve and pass back `reasoning` items alongside `function_call`/`function_call_output`. Dropping them degrades reasoning quality and can trigger premature stops. Input ordering each turn: prior reasoning item(s) → function_call(s) → function_call_output(s) → continue. Automatic with server-side state; manual with stateless.

**Verified: mecatl's replay carries the REAL blob, and `Include` is load-bearing.** A standing concern held that the adapter replayed the human-readable reasoning *summary* as `encrypted_content` rather than the real blob. That is NOT true of the current code, verified end to end: `request.go` sets `Include=[reasoning.encrypted_content]` + `Store=false` (asks the API to return the real blob); `stream.go` emits the reasoning item carrying `item.EncryptedContent` (the blob) on `response.output_item.done`, distinct from the display-only `reasoning_summary_text.delta` path (`ChunkReasoning`); `assistantItems` replays `Message.Reasoning` verbatim as `ResponseReasoningItemParam.EncryptedContent` with an empty `Summary`. The blob round-trips. The ONE residual hazard: replay only works while `Include` is set — a future provider entry that drops `reasoning.encrypted_content` from `Include` silently makes the blob `""` and degrades replay to a no-op (quality loss / premature stops, NOT a 400). Guarded by the pinning test `TestReasoningReplayUsesRealBlobNotSummary` (asserts the streamed reasoning item equals the blob NOT the summary, AND that `Include` is set with `Store=false`); the Anthropic analogue `TestReasoningEnvelopeRoundTripsSignature` pins the thinking *signature* survives pack→unpack. These are regression tripwires, not a fix — there is no replay bug to fix.

**Assistant-message `phase` is captured and replayed too (issue #46), the same discipline as the reasoning blob.** The Responses API tags an assistant output `message` item with a `phase` marker — `commentary` for intermediate preambles, `final_answer` for the answer. For GPT-5.3-codex and beyond, store:false manual-replay apps **must preserve and resend phase on every assistant message item**, or the model treats prior commentary as a final answer and stops early. The seam, mirroring the reasoning route: `stream.go` lifts `item.Phase` off the assembled `message` item on `response.output_item.done` and emits a neutral `ChunkPhase` (the visible text still arrives via `output_text.delta`, untouched); the loop stores it on `session.Message.ProviderPhase`; `assistantItems` (`request.go`) sets `EasyInputMessageParam.Phase` back verbatim on the replayed assistant message item. Phase is treated as an **opaque string** — never validated against the `commentary`/`final_answer` enum, so unknown future values pass through (forward-compat). An empty phase (non-tagging models) is wire-omitted (`json:"phase,omitzero"`), so the byte-stable prompt prefix is unchanged. Guarded by `TestPhaseCapturedAndReplayed` (capture + replay + byte-stability) and `TestPhaseThreadedOntoAssistantMessage` (the loop threads it without interpreting it).

## 7. The `openai-go` SDK

- **Module:** `github.com/openai/openai-go/v3` (the `/v3` is mandatory). Latest at research: **v3.37.0**. Go 1.22+.
- **Packages:** root `openai` (client, `openai.String/Int/Bool/Float`, errors), `option`, `responses`, `param` (`param.Opt`, `param.IsOmitted`, `param.Null[T]()`), `shared`/`shared/constant`.

Non-streaming with a function tool:
```go
client := openai.NewClient(option.WithAPIKey(os.Getenv("OPENAI_API_KEY")))
tool := responses.ToolUnionParam{OfFunction: &responses.FunctionToolParam{
    Name: "read_file", Description: openai.String("Read a file."), Strict: openai.Bool(true),
    Parameters: map[string]any{"type":"object","properties":map[string]any{"path":map[string]any{"type":"string"}},"required":[]string{"path"},"additionalProperties":false},
}}
params := responses.ResponseNewParams{
    Model: "gpt-5.2", Instructions: openai.String("You are a coding agent."),
    Input: responses.ResponseNewParamsInputUnion{OfString: openai.String("Open main.go")},
    Tools: []responses.ToolUnionParam{tool}, Store: openai.Bool(false),
    Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
}
resp, err := client.Responses.New(ctx, params) // iterate resp.Output, switch item.Type
```
To continue: switch `Input` to the array form (`OfInputItemList`) with prior output items + `function_call_output` (constructor name varies — check `api.md`).

Streaming:
```go
stream := client.Responses.NewStreaming(ctx, params)
acc := responses.ResponseStreamAccumulator{}
for stream.Next() {
    event := stream.Current(); acc.AddChunk(event)
    switch event.Type {
    case "response.output_text.delta": fmt.Print(event.Delta)
    case "response.completed": /* acc holds assembled final response */
    }
}
if err := stream.Err(); err != nil { /* handle */ }
final := acc.Response
```
Use `responses.ResponseStreamAccumulator.AddChunk` rather than hand-rolling. (Verify accumulator field/method names against v3.37.0 `api.md`; the `JustFinished*` helpers surfaced in search are the **Chat Completions** accumulator.)

**Idioms:** optional scalars via `openai.String/Int/Bool/Float` → `param.Opt[T]`; check `param.IsOmitted`; explicit null `param.Null[T]()`. Options: `option.WithAPIKey/WithBaseURL/WithHeader/WithMaxRetries/WithRequestTimeout/WithMiddleware`.

**Gotchas:** `/v3` mandatory; unions use `OfXxx` pointer fields (set exactly one); `arguments` is raw JSON string (unmarshal yourself); don't confuse item `id` vs `call_id`; pass model **strings** for forward-compat and compatible endpoints.

## 8. OpenAI-Compatible Endpoints

`option.WithBaseURL("http://localhost:8000/v1")` overrides the host; SDK appends `/responses` etc.

**Reality:** `/v1/chat/completions` is broadly supported; `/v1/responses` support is thin (llama.cpp: none yet; vLLM: partial/version-dependent; LiteLLM proxies translate). Commonly missing on compatible servers: `previous_response_id`/`store`, hosted tools, automatic caching/`prompt_cache_key`, encrypted reasoning, reasoning summaries. Strict mode & `parallel_tool_calls` vary.

**Harness implication:** design for the **stateless, client-owned** path + a **plain `function` tool loop** as baseline; treat `previous_response_id`, hosted tools, caching, encrypted reasoning as OpenAI-only enhancements you feature-detect and degrade gracefully on. Prefer model strings.

## 9. Usage / Limits & Error Handling

Usage (on response, and `response.completed` when streaming): `usage.input_tokens`/`output_tokens`/`total_tokens`, `input_tokens_details.cached_tokens`, `output_tokens_details.reasoning_tokens` (the latter two map to `session.Usage.CacheReadTokens` and `session.Usage.ReasoningTokens` respectively — both subsets of their inclusive totals).

Rate-limit headers: `x-ratelimit-{limit,remaining,reset}-{requests,tokens}`.

Retry/backoff: SDK auto-retries connection errors, 408/409/429/5xx 2× by default with exp backoff + jitter, honoring `Retry-After`. Tune `option.WithMaxRetries`, `option.WithRequestTimeout`. Don't retry non-idempotent tool side effects — retry the model call, not the executed tool.

Typed errors:
```go
var apierr *openai.Error
if errors.As(err, &apierr) { _ = apierr.StatusCode /* 401/400/429/5xx */ }
```
Context cancellation surfaces as a context error, not `*openai.Error` — handle both.

## Bottom-line for this harness

1. Responses API + `function` tools, **stateless** (`store:false`, no `previous_response_id`), own the `input` item slice as source of truth.
2. **Preserve reasoning items every turn** (`include:["reasoning.encrypted_content"]` on OpenAI).
3. **Preserve & resend `phase` on assistant messages** (`commentary`/`final_answer`, opaque) — dropping it makes GPT-5.x treat preambles as final answers / stop early.
4. Byte-stable prefix (instructions + tools + stable context first, volatile last); per-session `prompt_cache_key`; monitor `cached_tokens`.
5. Use `responses.ResponseStreamAccumulator`; act on `.done`/`response.completed`.
6. Feature-gate by endpoint; prefer model strings.
7. Pin `github.com/openai/openai-go/v3`; verify param/constructor names against that version's `api.md`.

### Verify before coding
- Exact Go names for `function_call_output` input items and `ResponseStreamAccumulator` in v3.37.0 (`api.md`).
- `reasoning.effort` enum and which models accept `prompt_cache_retention:"24h"`.


---

*Part of the [design docs](../design/README.md). Related: [Multi-provider / multi-model (Phase 0)](0016-multi-provider.md).*
