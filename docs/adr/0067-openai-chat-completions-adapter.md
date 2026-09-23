# ADR 0067 — OpenAI Chat Completions adapter (OpenCode Go provider)

- Status: Accepted
- Date: 2026-07-17
- Scope: `provider/openaichat` (new request adapter), `internal/app` provider registry, `internal/cliconfig`, the cmd mains
- Related: [ADR 0016](./0016-multi-provider.md) (multi-provider registry), [ADR 0064](./0064-toolhive-llm-gateway-provider.md) (config-detected gateway), [ADR 0055](./0055-reasoning-effort.md) (reasoning effort)

## Context

We want to use **OpenCode Go** — an OpenAI-compatible subscription LLM gateway at
`https://opencode.ai/zen/go/v1` (key env `OPENCODE_API_KEY`) — as an alternative
provider. Its wire protocol is **Chat Completions**, uniformly, for every model it
serves (glm-5.2, deepseek-v4-*, kimi-*, qwen*, grok-4.5, …). This was confirmed by
probing the live API on 2026-07-17.

Before this change mecatl had exactly two request adapters: `openai` (the OpenAI
**Responses** API, `POST /v1/responses`) and `anthropic` (the native Messages
API). `internal/adapter/openaicompat` is a model-*lister* only, not a request
adapter. Neither existing adapter speaks Chat Completions, so OpenCode Go's models
could not be driven at all — a Responses request to that endpoint does not work.

The Chat Completions protocol is spoken by many gateways (Groq, Together, DeepSeek
direct, Fireworks, vLLM/Ollama chat mode), so the new adapter is named for the
**protocol** (`openaichat`), not the vendor — mirroring the `openai` package's own
"names the protocol, not a single vendor" stance. OpenCode Go (registry provider
id `opencode`) is its first consumer.

### What the live probe established (and corrected)
- Base URL `https://opencode.ai/zen/go/v1`; Bearer auth; **bare** model ids.
- `GET /models` exists and is OpenAI-shaped, but carries only `id`/`object`/`created`/`owned_by` — no display name, context, or capability fields.
- Streaming is standard `chat.completion.chunk`; tool-call arguments arrive keyed by `index` (the provider currently sends a whole call in one fragment, but the adapter accumulates by index for spec-compliance). `finish_reason` lands on a trailing empty-delta chunk. `stream_options.include_usage:true` yields a terminal `usage`.
- `reasoning_effort` is **accepted**; the endpoint's set is `low|medium|high|xhigh|max|none|adaptive`. Unlike the OpenAI Responses path there is **no xhigh/max→high clamp** for this provider. mecatl's neutral vocabulary (ADR 0055) only exposes `low|medium|high|xhigh|max`, so `none`/`adaptive` are endpoint-accepted but not reachable through composition — the adapter passes the neutral set verbatim and omits anything else.
- `reasoning_content` streams (GLM/DeepSeek "thinking") but is not in the openai-go typed delta — it is read from the raw-JSON extra fields and surfaced as `ChunkReasoning` (display-only). This is NOT for replay (Chat Completions is stateless — no `ChunkReasoningItem`); it is load-bearing so `llmresilience`'s idle watchdog observes progress during a thinking phase (a dropped reasoning stream would look like a stall).
- Two non-standard SSE frames appear: an `x-opencode-type:"inference-cost"` frame (empty choices, cost + `normalizedUsage`) and a frame **after** `data: [DONE]`. A spike proved the openai-go `ssestream` decoder tolerates both — it skips post-`[DONE]` frames and only errors on a top-level `error` key — so the SDK stream is used directly, with no bespoke decoder.

## Decision

Add `provider/openaichat`, a `port.LLMProvider` over Chat Completions built
on the already-vendored `github.com/openai/openai-go/v3` SDK via
`client.Chat.Completions` (no new dependency). It mirrors the `openai` package's
shape — `Provider`/`New`/`Stream` + pure `buildParams` + pure `translate` driven
from recorded SSE fixtures — with the protocol differences:

- Requests map the neutral `LLMRequest` to `ChatCompletionNewParams`: the layered
  system prompt to a leading system message; user/assistant/tool turns to the
  `messages` array (assistant tool calls as function `tool_calls`, results as
  `role:"tool"` messages); tools as non-strict function tools;
  `stream_options.include_usage:true`; `reasoning_effort` passed **verbatim** (no
  clamp), omitted for unknown/empty.
- The stream translator accumulates tool-call fragments by index and defers the
  terminal (tool calls, usage, `ChunkDone`) to a `finalize` flushed at clean
  stream end — so a usage frame that trails `finish_reason` is counted. It **fails
  closed**: a clean EOF WITHOUT a `finish_reason` (the SDK's `ssestream` returns a
  nil error on a plain mid-stream EOF — `[DONE]` is not proven) surfaces a
  truncation error (wrapping `io.ErrUnexpectedEOF`, retryable when pre-commit)
  rather than fabricating a terminal, so a dropped connection never becomes a
  successful tool-executing turn.
- **Deliberately dropped** (not gaps): the reasoning-replay blob and the phase
  marker (both Responses-only) and any server-side conversation state. This is the
  proof the neutral `Chunk`/`Message`/`LLMRequest` seam already fits — **no port,
  proto, or engine-API change**.

Compose it as provider id `opencode`: key-driven (`OPENCODE_API_KEY`), default base
URL `https://opencode.ai/zen/go/v1`, default model `glm-5.2`, live model listing
via `openCodeLister` (the `openaicompat` lister wrapped to stamp the adapter-static
modalities text+image, so a live refresh resolves the /models envelope's missing
modality metadata to the documented adapter-static fallback STABLY — an
unstamped nil would flip an uncatalogued model's Image capability true→false the
instant a refresh landed). Because OpenCode Go is not in the vendored
models.dev subset, `providerEnvVars` carries an explicit `opencode` arm, and its
default model cannot fall back to the catalog. Reasoning effort is left in the
identity (no-clamp) case of `clampEffortForProvider`.

## Consequences

- Every OpenCode Go model is drivable, and a reusable generic Chat Completions
  adapter now exists for any OpenAI-compatible chat endpoint.
- A third request adapter to maintain, and the tool-call-fragment assembly is new
  stream logic covered by fixture tests (real single-frame + a synthesized
  multi-fragment case).
- OpenCode Go models are uncatalogued: capability detection falls to adapter static
  caps + the 128k context floor, and `--default-model opencode/<x>` won't pass the
  catalogued-model validation (per-session `--model` passthrough still works).
- Deferred (with upgrade paths): a `max_completion_tokens` resolver (the endpoint
  does not require it), and parsing the richer `x-opencode-type` cost frame for
  cache-token accounting. (Display-only reasoning streaming — initially deferred —
  is now implemented, since dropping it blinded the resilience idle watchdog.)

## See also

- [ADR 0016](./0016-multi-provider.md), [ADR 0064](./0064-toolhive-llm-gateway-provider.md), [ADR 0055](./0055-reasoning-effort.md)
- Living reference: [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) (provider registry, capability intersection)
