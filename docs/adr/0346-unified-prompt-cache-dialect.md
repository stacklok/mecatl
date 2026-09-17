# ADR 0346 — Prompt-cache breakpoints are protocol-native, never vendor-keyed

- Status: Proposed
- Date: 2026-09-15
- Scope: `provider/openai` (explicit prompt-cache breakpoints, the retired root `cache_control`, the salted cache key) and `internal/app` (the OpenRouter registry entries, default-provider precedence, the prompt-cache posture line).
- Supersedes: [ADR 0100](./0100-provider-prompt-caching.md) for three decisions: its deferral of `prompt_cache_breakpoint`, its OpenRouter-only root `cache_control` dialect arm, and its deferral of "Anthropic's 1h TTL via OpenRouter". Extends [ADR 0334](./0334-toolhive-protocol-specific-providers.md)'s protocol-specific-provider pattern to OpenRouter. ADR 0100's `prompt_cache_key` derivation and its `prompt_cache_retention` model gating stand.
- Superseded by: —

## Context

Some upstreams cache a prompt prefix only when the caller explicitly asks; the rest cache
implicitly. ADR 0100 gated the openai (Responses) adapter's cache hints on a two-row allowlist
of endpoint identities, so every other endpoint emitted nothing. That is free for an upstream
that caches implicitly and roughly 10x for one that does not, since Anthropic cache reads bill
at 0.1x input.

Found in production: a user exhausted a budget in roughly two hours of agentic coding on
Claude Opus 4.8 through the ToolHive gateway's Responses surface, cache reads at zero
(stacklok/mecatl#1558). Three endpoint shapes reach `CacheDialectNone` while serving such a
model: the gateway's Responses surface (`toolhive`), a built-in id under a base-URL override,
and a custom `providers:` definition with `api_flavor: openai-responses`.

### Measured on staging, not reasoned about

Three hours of one staging gateway's usage records, grouped by the downstream it routed to:

| downstream | requests | Messages shape | Responses shape | cache-read tokens |
|---|---|---|---|---|
| `anthropic` | 4,749 | 4,749 | 0 | 1,444,863,623 |
| `openai` | 646 | 0 | 643 | 65,454,701 |
| `bedrock-anthropic` | 193 | 192 | 0 | 77,457,203 |
| `openrouter` | 175 | 0 | 21 | **0** |

The Messages protocol caches heavily wherever it is used, and the OpenRouter downstream is the
only one that never caches, across every request rather than one session's.

Model ids are **not** stable across those routes. The same Claude Opus 4.8 appears as
`anthropic/claude-opus-4.8` on the OpenRouter downstream, `claude-opus-4-8` and
`claude-opus-5` on the Anthropic downstream, and `us.anthropic.claude-opus-4-8` on Bedrock.
Two consequences, both load-bearing below: a rule keyed on a model name is already wrong on
live data, and a provider-only switch between two surfaces of one gateway cannot work, because
those surfaces do not share an id namespace.

### What the protocol already offers

`ResponseInputTextParam.PromptCacheBreakpoint` is a typed field in openai-go v3.61.0, the
version this repo resolves. ADR 0100 deferred it on the stated grounds that "no typed SDK
fields exist yet"; that reason is stale. It is part of the Responses protocol rather than any
vendor's extension, and OpenRouter documents converting such a block into an Anthropic
`cache_control` breakpoint when it routes to Anthropic or Google.

`newAnthropicEntryFor` separately applies `WithConversationCaching` on every endpoint,
including unknown ones, because Anthropic's `cache_control` is native to the Messages API.
ADR 0334 used that to give the ToolHive gateway a protocol-specific `toolhive-anthropic`
entry. OpenRouter exposes the same shape: its documented Anthropic base is
`https://openrouter.ai/api`, so the SDK's `/v1/messages` suffix lands correctly, and
OpenRouter's own caching docs send callers to Chat Completions or Anthropic Messages when they
need a cache `ttl`, which Responses cannot express.

### What was not derivable

A prior draft of this ADR claimed `toolhivellm.Config.GatewayURL` is never used to construct a
request URL. That is **proxy-mode-scoped and not true in general**: in direct mode (ADR 0102)
`directBaseURL(detected.GatewayURL)` is, by its own comment, "the ONE derivation of a request
URL from gateway_url", validated as HTTPS and paired with `RefuseRedirects`.

## Decision

### 1. Ask for the cache through the protocol, and never consult the vendor

Emit one explicit `prompt_cache_breakpoint` on an `input_text` content block of every
Responses request. Do not look at the model's vendor, the provider id, or the base URL to
decide whether to ask.

This is expressible because explicit breakpoints are defined as **additive** to implicit
caching: with `prompt_cache_options.mode` at its `implicit` default, the API "creates one
implicit breakpoint and writes up to the latest three explicit breakpoints in the request".
So one marker is legal on every request, and three outcomes exhaust the space:

- an upstream that caches implicitly gets its implicit breakpoint plus ours;
- an upstream that caches only on an explicit ask gets the ask — Anthropic today, Qwen on
  Alibaba, and any future vendor with the same semantics;
- an upstream that does not implement the field ignores it, which is the pre-ADR wire.

**Vendor identity is never an input.** A matcher on `anthropic`/`claude` was considered and
rejected: it breaks the moment a second vendor ships explicit-ask caching under another
name, and it breaks when a gateway renames its routes. That is not hypothetical. One staging
gateway exposes the same Claude Opus 4.8 under four ids at once — `anthropic/claude-opus-4.8`
(OpenRouter downstream, Responses, zero cache reads), `claude-opus-4-8` and `claude-opus-5`
(Anthropic downstream, Messages), and `us.anthropic.claude-opus-4-8` (Bedrock downstream,
Messages) — so any name-keyed rule is already wrong on live data.

The failure direction is what makes a vendor-free rule affordable. `prompt_cache_breakpoint`
is part of the Responses protocol, so a wrong guess yields an ignored field. ADR 0100's root
`cache_control` was an OpenRouter extension, so a wrong guess yielded a 400, and
`llmresilience` treats 4xx outside 408/429 as non-retryable — a hard outage. Being wrong is
now cheap, which is precisely why the gate can be removed instead of widened.

### 2. One endpoint carve-out, on documented strictness, not on vendor

On the **canonical OpenAI endpoint only**, emit the breakpoint only for a model known to
support it. OpenAI documents explicit breakpoints as GPT-5.6-and-later and says of earlier
models only "Only implicit caching is supported"; it does **not** document whether an
earlier model ignores the field or rejects the request. ADR 0100 records this repo already
being bitten by a strict upstream on an unrecognised cache field, so the most-used endpoint
does not ride on undocumented behaviour.

This is an endpoint-plus-capability carve-out, not a vendor-family matcher, and it reuses the
model table `retentionFor` already maintains for exactly this endpoint. It costs nothing for
the problem this ADR exists to solve: canonical OpenAI never serves Anthropic models.

Revisit it with one live API probe; if an earlier model ignores the field, the carve-out
disappears and decision 1 becomes unconditional.

### 3. Root `cache_control` is retired

OpenRouter converts a `prompt_cache_breakpoint` block into an Anthropic `cache_control`
breakpoint when it routes to Anthropic or Google, so the breakpoint subsumes the root field.
Emitting both would be two mechanisms describing one intent — the root field self-advances to
the last cacheable block while explicit markers name specific ones — so only the breakpoint
is sent.

That removes the last vendor-private field from the cache path. `CacheDialect` shrinks to
`prompt_cache_key` plus OpenAI's `prompt_cache_retention`, and the
`(providerID, resolvedBaseURL)` gate stops being load-bearing for whether caching happens at
all, which is the property that caused the reported incident.

### 4. Placement: the previous-turn boundary

A cache write that is never read costs more than sending the prompt uncached (Anthropic
writes bill at 1.25x), so placement is a cost decision, not a detail. The marker goes at the
**previous-turn boundary**, the analogue of ADR 0100's anthropic slot 3: turn N writes, turn
N+1 reads, reused by construction in an agent loop.

Top-level `instructions` cannot carry a breakpoint, so the system prefix is not markable and
relies on implicit caching plus the byte-stable prefix, as before.

The honest residual: a genuine one-shot run (`mecatequi`) writes a breakpoint nothing reads,
losing roughly 0.25x on that request. Accepted rather than special-cased, because detecting
"this will be the only turn" before the turn is not possible.

### 5. `openrouter-anthropic`, mirroring ADR 0334

Register a second, protocol-specific entry for the OpenRouter identity, following
[ADR 0334](./0334-toolhive-protocol-specific-providers.md) rather than inventing a parallel
mechanism:

- `openrouter` retains its current OpenAI Responses behaviour and its base URL byte-for-byte.
- `openrouter-anthropic` uses `provider/anthropic` exclusively. Its base is derived with
  `net/url` by replacing a terminal `v1` segment, never by string concatenation; the SDK then
  appends `/v1/messages`.
- The id is wire-stable and reserved against operator-defined provider collisions.
- It lists **Anthropic-family models only**. OpenRouter's Anthropic surface does not support
  non-Anthropic models, so a broader listing would advertise ids that cannot execute.

Because `newAnthropicEntryFor` is already unconditional, this entry inherits the full 4-slot
breakpoint budget and `--anthropic-cache-ttl` with no new caching code. That is what retires
ADR 0100's "1h TTL via OpenRouter is out of scope" deferral: the deferral's own stated
precondition was moving OpenRouter to Chat Completions or native Messages, which this does.

### 6. Default-provider precedence, narrowed to shared-id siblings

A deployment whose only providers are protocol-specific siblings must not default to the
sibling that cannot cache its default model. Sorted-order-among-intent-driven stops being a
sufficient rule once two entries front one identity with different caching capability.

The redirect applies **only where the two surfaces share a model-id namespace**, which today
means the OpenRouter pair. It is deliberately NOT applied to the ToolHive pair: the ids
measured above differ per downstream route, so switching the provider while carrying the id
verbatim yields a pair that cannot resolve. That fails safe rather than misrouting, but it
delivers nothing, and claiming it would be false comfort. The gateway case is covered by
decision 1, which needs no id translation at all.

### 7. The prompt cache key is salted per process

`prompt_cache_key` is derived from a hash of the prompt prefix plus a conversation anchor, with
no installation-specific input. Two unrelated installations sharing harness version, soul,
agent def and tool inventory therefore emit an identical key, letting a recipient correlate
sessions across principals and credentials and confirm guessed configuration. Routing metadata
also frequently lives in a longer retention class than prompt bodies, so the key outlives the
content it fingerprints — the one place the "it is already in the request body" argument does
not hold.

The prefix hash is salted with a random value minted once per process in `app.Build` and passed
as an adapter Option. It is **not persisted**: no durable pseudonymous identifier on disk, at
the cost of one sticky-routing lane change per restart, which the derivation already treats as
fail-soft for anchor changes. Byte-stability within a run, prefix bucketing, and
per-conversation separation of concurrent Subagent children are all preserved.

A consumer passing no Option keeps today's exact derivation, so this is Added, not Changed,
for `provider/openai` (ADR 0093).

## Consequences

Caching now works for an explicit-ask upstream on **any** Responses endpoint, including ones
composition cannot classify, with no operator configuration and no vendor list to maintain.
The reported incident is fixed directly: a Claude model on the gateway's Responses surface
gets a breakpoint, the gateway forwards it, and OpenRouter translates it. A future vendor with
the same semantics is covered with no code change, which is the property a name-keyed rule
could not provide.

The Messages path remains preferred where it exists, on evidence rather than preference: four
breakpoint slots, a real TTL, and 1.44B cache-read tokens against the Responses path's zero.
Decision 1 is the floor, not a replacement.

Two costs are accepted rather than engineered away. A strict OpenAI-compatible endpoint that
rejects unknown fields inside a content block now sees the field for **every** model, not only
an explicit-ask one; that is the price of removing the vendor gate, bounded only by
`--no-prompt-cache`. The per-endpoint dialect is deliberately not a second bound: decision 1
detaches the breakpoint from the dialect, so an unclassified endpoint cannot be exempted
without disabling caching harness-wide. And a genuine one-shot run writes a breakpoint nothing
reads, losing roughly 0.25x on that request, because "this will be the only turn" is not
knowable before the turn.

The canonical-OpenAI carve-out in decision 2 rests on undocumented behaviour and should not
outlive a live probe. If an earlier model ignores the field, delete the carve-out; if it
rejects the request, the carve-out is load-bearing and should say so with a citation.

One further asymmetry is now recorded rather than left implicit: a model can appear under two
provider ids for one identity with materially different cache quality, four breakpoints and a
TTL on Messages against one breakpoint on Responses. Decision 6 and the posture line exist so
that is visible instead of silent. The picker's `prompt_cached` marker is narrower than that
asymmetry: after decision 1 both siblings ask for a cache, so it marks only a Claude row under
`--no-prompt-cache`.

Unchanged from ADR 0100: `prompt_cache_key`'s derivation (now salted), and
`prompt_cache_retention`'s model gating on the canonical OpenAI endpoint — though OpenAI has
since deprecated that field in favour of `prompt_cache_options.ttl`, which is a follow-up this
ADR does not take. Note also that the SDK's `SetExtraFields` **replaces** rather than merges,
which is no longer a trap for this path now that the root field is retired.

Not decided here: whether the ToolHive gateway should inject `cache_control` for its own
Anthropic backends. Decision 1 makes that unnecessary for mecatl, but it would fix every other
client behind that gateway, and the 175-request, zero-cache-read row above is the case for it.

## See also

- [ADR 0334 — Protocol-specific ToolHive gateway providers](./0334-toolhive-protocol-specific-providers.md) (the pattern this extends, and the URL-derivation rules it reuses).
- [ADR 0100 — Provider-side conversation prompt caching](./0100-provider-prompt-caching.md) (the TTL deferral this retires; its dialect table stands).
- [ADR 0210 — OpenRouter downstream-provider steering](./0210-openrouter-downstream-provider-steering.md) (the `order` interaction, and the sticky-routing role of the cache key).
- [ADR 0093 — Provider modules](./0093-provider-modules.md) (why the salt Option is Added for `provider/openai`).
- [Acceptance plan](../acceptance/unified-prompt-cache-dialect.md).
