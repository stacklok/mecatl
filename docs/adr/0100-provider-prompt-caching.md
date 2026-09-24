# ADR 0100 — Provider-side conversation prompt caching (Anthropic, OpenAI, OpenRouter, openaichat)

- Status: Accepted
- Date: 2026-08-06
- Scope: `provider/anthropic`, `provider/openai`, `provider/openaichat` (the wire-format request builders and usage mapping), and `internal/app`'s provider construction (the `--no-prompt-cache` / `--anthropic-cache-ttl` knobs and the `(providerID, resolvedBaseURL)` cache-dialect gate).
- Supersedes: —
- Superseded by: —

## Context

mecatl cached only the *system* prefix. The anthropic adapter placed a single
`cache_control:{type:"ephemeral"}` breakpoint on the `StablePrefix` system
block, which caches `tools`+`system` and nothing else. Because a breakpoint
writes exactly one cache entry (the hash of the prefix ending at that block),
the message array was never cached — every turn re-paid full input price for
the entire conversation. In an agentic loop the messages *are* the tokens:
tool results, file contents, subagent reports, all growing each turn.

`provider/openai` (Responses) and `provider/openaichat` (Chat Completions) sent
**no** cache hints at all. Since OpenRouter rides the openai Responses adapter,
Claude and Qwen models routed through OpenRouter got **zero** caching, and
OpenAI models missed `prompt_cache_key`, which is effectively mandatory on
GPT-5.6+ for reliable prefix matching.

Two facts made caching the conversation cheap to add:

- `StablePrefix` **and** `VolatileSuffix` are byte-identical across every turn
  of one `Engine.Run` (`Env.Date` is frozen at engine-build time; `Mode`
  cannot change mid-run). Turn-0 fragments are prepended to `req.Messages` once
  per run. So the prefix was already stable enough to cache — only the
  breakpoints were missing.
- `port.LLMRequest` has exactly four fields, machine-guarded by a reflect
  whitelist (`engine/port/llm_neutral_test.go`). Nothing in this change widens
  the port — every new knob is an adapter-construction Option, mirroring
  `WithProviderCapabilities` / `WithReasoningEffort`.

## Decision

### Anthropic: a 4-slot breakpoint budget

Wire prefix order is `tools → system → messages`. Anthropic allows 4
breakpoints; the top-level automatic marker consumes one. Breakpoints
themselves are free — cost is read for the highest-hit breakpoint `A` and
write for `(last breakpoint − A)` — so extra *earlier* markers cost nothing.

| Slot | Position | Kind | Why |
|---|---|---|---|
| 1 | `system[0]` (StablePrefix) | explicit | Unchanged from before this ADR. Tools+system floor; survives compaction. |
| 2 | last block of the last leading turn-0 fragment (conditional) | explicit | Compaction survivor: after `ReplaceHistory` rewrites the tail, `[system][fragments]` is still byte-stable, so the multi-thousand-token AGENTS.md + rules + soul + memory-index block is not re-written on every compaction. |
| 3 | last block of the message at `lastAssistantIdx − 1` (conditional) | explicit | Previous-turn boundary. Guards the 20-block backward lookback: a turn adds `1 thinking + K tool_use + 1 text + K tool_result ≈ 2K+2` blocks, so a wide read-parallel fan-out (K ≥ 9) pushes the previous turn's entry outside the window and collapses the hit to slot 1/2. |
| 4 | `MessageNewParams.CacheControl` | automatic | The self-advancing conversation breakpoint (the SDK "automatically applies a cache_control marker to the last cacheable block"). Turn N writes the conversation, turn N+1 reads it — the actual fix for the stated gap. |

Slot 2 is derived by walking forward from index 0 while the message is
`RoleUser` and matches `prompt.IsInjectedTurn0Fragment`, taking the last index
of that *leading run* (stop at the first non-match — an old persisted session
may carry a fragment mid-history, and only the contiguous leading run is the
byte-stable prefix worth a breakpoint). Slot 3 scans backward for the last
`RoleAssistant` message and marks the index before it (two adjacent assistant
messages are unreachable — `finishTurnNoTools` records an empty assistant turn
then a *user* nudge — so the scan is unambiguous). Slots 2 and 3 are deduped
when they resolve to the same index: a second marker on the same block changes
neither the read-hit index nor the write span, so it is dropped rather than
wasted.

**Trip-wire**: a fifth caching concern must drop slot 1 first — it is pure
insurance whenever fragments exist (slot 2 already covers the same prefix once
fragments are present), and dropping it is the cheapest way to stay inside the
4-breakpoint ceiling.

**Uniform TTL is the rule that kills every documented 400.** The same TTL
applies to slots 1, 2, 3 and the top-level marker:

- "longer TTLs must appear before shorter ones" → vacuous with one TTL class.
- "last block has explicit `cache_control` with a different ttl than the
  automatic marker" → unreachable (slot 3 is never the last block, and its TTL
  equals the top-level one by construction).
- "4 explicit breakpoints + automatic → 400" → at most 3 explicit markers are
  ever emitted, pinned by `TestBuildParamsBreakpointBudget`.

**The mixed-TTL optimum (1h early, 5m late) was considered and rejected.** It
is legal and marginally cheaper (the late, cheap-to-recompute blocks don't pay
for a 1h write), but it reintroduces both traps above for a gain that does not
justify the added classification logic and test surface. One TTL, set via
`--anthropic-cache-ttl`, is the whole knob.

At the default TTL the `TTL` field stays the zero value and is
`omitzero`-dropped, so every marker marshals as exactly
`{"type":"ephemeral"}` — byte-identical *objects* to the pre-ADR wire; only the
*positions* are new. `WithConversationCaching(false)` (wired from
`--no-prompt-cache`) reproduces the pre-ADR wire exactly: no top-level marker,
no message-level markers, only the pre-existing StablePrefix breakpoint.

Below a model's minimum cacheable length (512–4096 tokens) markers silently
no-op with no error; the adapter does not guess a token count to gate on.

### OpenAI / OpenRouter: dialect-gated hints, no port change

OpenAI's implicit caching already places a breakpoint on the most recent
user/tool message and reads across the 50 most recent, longest match wins.
Given mecatl's byte-stable prefix and append-only history, that already caches
the conversation; the gaps were the cache key, retention, and observability.

`provider/openai` gained a `CacheDialect` Option (`WithCacheDialect`):
`CacheDialectNone` (zero value, emits nothing), `CacheDialectOpenAI`
(`prompt_cache_key` + model-gated `prompt_cache_retention`), and
`CacheDialectOpenRouter` (`prompt_cache_key` + the OpenRouter-only top-level
`cache_control` field, via `SetExtraFields` — there is no typed SDK field for
it). Only the *method* `buildParams` (the live `Stream` path) is wired; the
free `buildParams` (the pre-existing default-path form) is untouched.

**The dialect is gated on `(providerID, resolvedBaseURL)`, never providerID
alone.** `cache_control` at the request root is an OpenRouter field — sending
it to real OpenAI 400s. Both `--openai-base-url` and `--openrouter-base-url`
exist, so an operator pointing the `openai` id at a compatible endpoint
(vLLM/LiteLLM) keeps the id `openai` but must not get OpenAI-specific hints.
This repo has already been bitten by a strict-compatible upstream exactly this
way (see `emptyToolOutputPlaceholder`'s rationale in
`provider/openai/request.go`).

| id | baseURL | dialect |
|---|---|---|
| `openai` | canonical (empty) | `OpenAI` |
| `openrouter` | `openRouterDefaultBaseURL` | `OpenRouter` |
| `toolhive` | any | `None` |
| anything else, or any non-canonical baseURL | any | `None` |

`--no-prompt-cache` forces `None` on every row, unconditionally.

**Cache key**, derived in-adapter (the session id does not exist at
construction time):

```
key = "mecatl-" + hex(sha256(StablePrefix))[:12] + "-" + hex(sha256(anchorText))[:8]
```

`anchorText` is the `Text` of the first message that is *not* a leading turn-0
fragment. The anchor answers OpenAI's ~15-req/min-per-key guidance: a
prefix-only key would put the parent plus up to 8 concurrent Subagent children
(all sharing the explorer prefix) on one routing lane, where they would also
evict each other from the shared 50-breakpoint window. Per-conversation keys
fall out for free because each child carries its own task prompt as anchor.
The anchor is stable for the same reason compaction pins the first genuine
user turn; if a degraded compaction ever drops it the key changes once and the
cache rebuilds — fail-soft, no error path. The prefix hash is memoised on the
`Provider` via a single `atomic.Pointer[prefixMemo]` compared by string
equality (a memcmp, far cheaper than re-hashing 10–30 KB); no unbounded map
keyed on 30 KB strings.

**`prompt_cache_retention` model gating** lives in the adapter as an ordered
prefix table (`retentionFor`), the same idiom as the adapter's
`adaptiveThinkingPrefixes`: a wire-protocol fact, not a catalog fact.
`ok=false` omits the field, so a value is never guessed and can never 400. The
high-risk detail: `"gpt-5"` is a prefix of `"gpt-5.6"`, where the feature is
*deprecated*. The matcher (1) normalises and strips a trailing `-YYYY-MM-DD`
snapshot suffix, (2) checks an explicit **deny** list before any allow prefix,
(3) matches allow entries longest-prefix-first. It does **not** strip an
`"<org>/"` routing prefix: an OpenRouter-style id such as `"openai/gpt-5"`
reaching `retentionFor` (only ever called under `CacheDialectOpenAI`, the
canonical endpoint) is itself anomalous, so it classifies as unknown rather
than guessed — `gpt-5.6`, `gpt-5.6-codex`, and `openai/gpt-5` all get explicit
negative rows in the table test.

**`cache_write_tokens` observability**: OpenAI/OpenRouter's Responses surface
reports this at `usage.input_tokens_details.cache_write_tokens`, with no typed
SDK field (`ResponseUsageInputTokensDetails` only types `CachedTokens`).
`cacheWriteTokensFrom` probes `RawJSON()` for the key and folds it into the
existing `session.Usage.CacheWriteTokens` — no new field, no proto change. Any
parse failure or absent key yields 0. Unlike the anthropic fold (which exists
because Anthropic's raw `input_tokens` *excludes* cache tokens), OpenAI's
`InputTokens` already *includes* cache writes, so there is no fold here — only
a clamp: `write` is bounded to `[0, inputTokens]` so `CacheWriteTokens ⊆
InputTokens` holds regardless of how an upstream (mis)reports it.

### openaichat: included, and dormant today

`provider/openaichat` gets `prompt_cache_key` with a **locally-duplicated**
`CacheDialect` — only `None` and `OpenAI` (there is no
Chat-Completions-over-OpenRouter path, and this adapter does not gate a
retention knob). With the `(id, baseURL)` gate, the only registered
Chat-Completions-protocol consumer today is `opencode`
(`https://opencode.ai/zen/go/v1`), which is not the canonical OpenAI endpoint,
so `openaichatCacheDialectFor` always resolves to `None` in production — this
ships fully tested but changes no runtime behaviour until a future
OpenAI-over-Chat-Completions entry appears. Cross-module duplication (the
`CacheDialect` type, `cachekey.go`'s prefix-memo/anchor derivation, the
`(id, baseURL)` gate helper) is the established discipline here (cf.
`isContextOverflowMessage`, duplicated between `provider/anthropic` and
`provider/openaichat` for the identical reason: separate Go modules, and a
shared dependency is worse than ~30 lines).

### Composition: the `(providerID, resolvedBaseURL)` gate lives in `internal/app`

`cacheDialectFor(id, baseURL, cfg)` (and its openaichat sibling,
`openaichatCacheDialectFor`) are pure functions in `internal/app/promptcache.go`,
wired **inside** the three provider-construction closures (`newOpenAICompatEntry`
— shared by openai/openrouter/toolhive — `newOpenCodeEntry`, and
`newAnthropicEntry`), not at the outer call site, so every per-session and
heal re-mint carries the dialect, never just the initial build.
`normaliseAnthropicCacheTTL` validates `--anthropic-cache-ttl` against `"5m"`
/ `"1h"` / `""` (unset, silent) and WARNs through the injected
`port.Diagnostics` on anything else; it is called **once** per registry
build (a build-time, not a per-remint, call site — mirroring the existing
`operatorDefaultEffortFor` discipline), so an unrecognised value WARNs at most
once per process, never once per session/heal re-mint.

## Consequences

Every turn of a long agentic run now caches the growing conversation on all
three provider paths, not just the system prompt — the dominant per-turn cost
in an agentic loop. The cost is a small, well-tested breakpoint-placement
surface in `provider/anthropic` and a dialect-gated hint surface in
`provider/openai`/`provider/openaichat`, all defaulting ON with a single
escape hatch (`--no-prompt-cache`) that reproduces the exact pre-ADR wire for
any operator who needs it (a strict, unfamiliar OpenAI-compatible upstream, or
a live cost/behaviour regression).

The two `CacheDialect` types (`provider/openai` and `provider/openaichat`) are
duplicated by necessity (separate Go modules) and must be kept in lockstep by
hand if the gate logic ever changes — there is no compiler enforcement of
that, only the paired tests in each module plus `internal/app`'s
`TestCacheDialectTable` / `TestOpenAIChatCacheDialectTable`.

**Deferred, not done here:**

- GPT-5.6's explicit `prompt_cache_breakpoint` / `prompt_cache_options` knobs:
  no typed SDK fields exist yet, pre-5.6 upstreams 400 on them, and implicit
  caching already works without them.
- An operator-supplied cache key (letting an operator pin/override the derived
  `prompt_cache_key`).
- A `settings.yaml` `prompt-cache-ttl:` key — this would additionally touch
  `internal/adapter/permconfig/resolve.go` and the `foldOperator*` machinery;
  v1 is CLI-only.
- Anthropic's 1h TTL via OpenRouter: OpenRouter's Responses-compatible path for
  Anthropic models carries no TTL concept at all; supporting it would require
  moving OpenRouter to Chat Completions or the native Messages API for
  Anthropic models specifically, which is out of scope here.

## See also

- [ADR 0016 — Multi-provider architecture](./0016-multi-provider.md) (frozen; the provider registry this ADR extends).
- [ADR 0017 — OpenAI Responses API adapter](./0017-openai-responses-api.md) (frozen).
- [ADR 0055 — Reasoning effort](./0055-reasoning-effort.md) (the sibling adapter-Option-not-port-field discipline this ADR follows).
- [ADR 0093 — Provider modules](./0093-provider-modules.md) (why `provider/anthropic`, `provider/openai`, `provider/openaichat` are separate Go modules, and why duplication across them is the accepted discipline).
- [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md)'s prompt-caching section (the dense per-adapter reference).
