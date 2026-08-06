## 14. OpenAI & compatible endpoints

The OpenAI provider talks to the **Responses API** (`POST /v1/responses`) via
`github.com/openai/openai-go/v3`. The harness owns its own conversation state:
every request is stateless (`store:false`, no `previous_response_id`) and
resends the full input slice, carrying reasoning items forward.

| Setting | How |
| --- | --- |
| API key | `OPENAI_API_KEY` env var (selects `--openai` automatically) |
| Base URL | `--openai-base-url https://your-host/v1` (SDK appends `/responses`) |
| Model | `--model <id>` |

**OpenRouter** rides this same stateless Responses adapter: set `OPENROUTER_API_KEY`
and the `openrouter` provider is auto-detected against `https://openrouter.ai/api/v1`
(override with `--openrouter-base-url`). It accepts an `OPENAI_API_KEY` by convention
when no dedicated key is set.

```console
# OpenAI
$ OPENAI_API_KEY=sk-... go run ./cmd/mecated serve --openai --model gpt-5

# An OpenAI-compatible endpoint (vLLM / LiteLLM / local proxy)
$ OPENAI_API_KEY=token go run ./cmd/mecated serve --openai \
    --openai-base-url http://127.0.0.1:8000/v1 --model my-model
```

### What compatible servers may lack

`/v1/chat/completions` is broadly supported, but `/v1/responses` support is thin
and version-dependent (llama.cpp: none yet; vLLM: partial; LiteLLM proxies
translate). Per `docs/adr/0017-openai-responses-api.md` §8, features **commonly
missing** on compatible servers include:

- `previous_response_id` / `store` (the harness already avoids these by design —
  it keeps state itself, so it is portable),
- hosted tools,
- automatic caching / `prompt_cache_key` (expect a lower or zero `cacheHitRate`),
- encrypted reasoning content and reasoning summaries,
- strict mode and `parallel_tool_calls` (these vary).

If a compatible endpoint behaves oddly, suspect missing Responses-API support
before suspecting the harness.

### Prompt-cache hints are OFF for a non-canonical base URL

mecatl sends OpenAI-specific prompt-cache hints — `prompt_cache_key` and the
model-gated `prompt_cache_retention` — only when the `openai` provider id is
talking to the **canonical** OpenAI endpoint (no `--openai-base-url`
override). The gate is on `(provider id, resolved base URL)`, never the id
alone: a strict-compatible upstream (vLLM/LiteLLM, an Azure OpenAI proxy via
`--openai-base-url`) can 400 on `prompt_cache_retention` or reject an
unrecognised field outright, so pointing `openai` at a non-canonical URL
degrades the cache dialect to `None` — no hints sent at all, byte-identical
to the pre-caching wire. If your requests are unexpectedly missing
`prompt_cache_key` and you *do* want it, that is almost always why. The same
gate applies to `openrouter`: hints are sent only at OpenRouter's default base
URL (`https://openrouter.ai/api/v1`); an `--openrouter-base-url` override also
degrades to `None`. See [`--no-prompt-cache` / `--anthropic-cache-ttl`](./mecated.md)
and [ADR 0100](../adr/0100-provider-prompt-caching.md) for the full rationale.

---

See also: [model routing](model-routing.md) (slots, aliases, the router); the
[ToolHive LLM gateway](../usage.md#toolhive-llm-gateway) section (a zero-config,
auto-detected OpenAI-compatible endpoint — no `--openai-base-url` needed); or
the [operator guide index](../usage.md).

