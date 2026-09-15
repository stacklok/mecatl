# ADR 0343 — Claude prompt caching routes through a native Messages surface

- Status: Proposed
- Date: 2026-09-15
- Scope: `internal/app` (the OpenRouter registry entries, default-provider precedence, the prompt-cache posture line) and `provider/openai` (the salted cache key).
- Supersedes: [ADR 0100](./0100-provider-prompt-caching.md), for its deferral of "Anthropic's 1h TTL via OpenRouter" only. Extends [ADR 0334](./0334-toolhive-protocol-specific-providers.md)'s protocol-specific-provider pattern to OpenRouter. Every other ADR 0100 decision, including its `(providerID, resolvedBaseURL)` dialect table, stands unchanged.
- Superseded by: —

## Context

Anthropic and Alibaba are the only OpenRouter upstreams that cache solely on an explicit
ask; every other upstream caches implicitly. ADR 0100 gated the openai (Responses) adapter's
cache hints on a two-row allowlist of endpoint identities, so any other endpoint emits
nothing — free for a GPT or Gemini model, and roughly 10x for a Claude model, since Anthropic
cache reads bill at 0.1x input.

Found in production: a user exhausted a budget in roughly two hours of agentic coding on
Claude Opus 4.8 through the ToolHive gateway's Responses surface, with cache reads at zero
(stacklok/mecatl#1558).

Three endpoint shapes reach `CacheDialectNone` while serving Claude: the ToolHive gateway's
Responses surface (`toolhive`), a built-in id under a base-URL override, and a custom
`providers:` definition with `api_flavor: openai-responses`.

Two facts decide the shape of the fix.

First, `newAnthropicEntryFor` already applies `WithConversationCaching` on **every** endpoint,
including unknown ones, because Anthropic's `cache_control` is native to the Messages API
rather than an extension. ADR 0334 used that to give the ToolHive gateway a second,
protocol-specific entry (`toolhive-anthropic`) whose models always travel as Messages. A
caching path for Claude-through-the-gateway therefore already exists.

Second, OpenRouter exposes an Anthropic Messages-compatible endpoint. Its documented client
base URL is `https://openrouter.ai/api`, so the SDK's own `/v1/messages` suffix lands on
`https://openrouter.ai/api/v1/messages` — the same strip-`v1`-then-append derivation ADR 0334
already specified. OpenRouter's caching documentation directs callers to the Chat Completions
or **Anthropic Messages** API specifically when they need a cache `ttl`, which the Responses
path cannot express at all.

So the gap is not that mecatl lacks a way to cache Claude. It is that the paths which do cache
are not the ones a Claude model lands on. `preferredDefaultProvider` treats intent-driven
entries as a last resort and then takes the first from `reg.Available()` in sorted order,
where `toolhive` sorts before `toolhive-anthropic`, so a gateway-only deployment defaults to
the uncached Responses entry; the picker then offers Claude under it because the gateway's
`/v1/models` lists Claude.

## Decision

### 1. Claude-family caching is a routing decision, not an extension-field decision

Route Claude-family models to a native Messages surface. Do not make Claude work over an
OpenAI-Responses endpoint by sending extension fields.

**The rejected alternative was an operator-declared dialect permission**: a config surface
letting an operator assert that a given endpoint tolerates OpenRouter's root `cache_control`,
which would have widened ADR 0100's allowlist to cover all three shapes. It was rejected
because:

- It asks the operator to assert a property of a **remote, mutable** routing decision. A
  proxy's upstream can change server-side with no local config change, so every such
  declaration is stale-by-default.
- A wrong declaration is a hard outage, not a cost regression: `llmresilience` treats 4xx
  outside 408/429 as non-retryable, so a rejected root field fails every request.
- It buys strictly less than routing. The Messages path additionally carries per-block
  breakpoints and the 5m/1h TTL, neither of which the Responses path can express.
- The Messages path needs no endpoint classification at all, which is the property that lets
  `newAnthropicEntryFor` be unconditional today.

ADR 0100's dialect table is therefore left exactly as it stands, and no endpoint gains a way
to be declared OpenRouter-compatible.

### 2. `openrouter-anthropic`, mirroring ADR 0334

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

### 3. Default-provider precedence must not prefer an uncached surface

A deployment whose only providers are protocol-specific siblings must not default to the
sibling that cannot cache its default model. Sorted-order-among-intent-driven is not a
sufficient rule once two entries front the same identity with different caching capability.

### 4. The prompt cache key is salted per process

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

Claude caches on OpenRouter and on the ToolHive gateway with no new operator configuration, no
novel wire field, and no endpoint classification, and gains a TTL knob it never had. The two
remaining uncovered shapes — a custom `openai-responses` definition and a base-URL override
serving Claude — are addressed by documentation: an operator in that position registers the
same endpoint as `api_flavor: anthropic-messages`, which caches unconditionally today.

The cost is a second wire-stable provider id per identity, which is the cost ADR 0334 already
accepted, plus the fact that a Claude model can now appear under two ids for one identity and
only one of them caches. Decision 3 and the posture line exist to keep that from being silent.

OpenRouter documents its Anthropic surface as guaranteed only against the Anthropic
first-party downstream, which interacts with ADR 0210's `openrouter.order` steering: an
operator pinning a non-Anthropic downstream for a Claude id may find the two settings in
tension. That is an operator-visible configuration conflict, not a mecatl-resolvable one.

Unchanged from ADR 0100: GPT-5.6's `prompt_cache_breakpoint` stays deferred, and the
Responses path still cannot express a TTL. Note also that the SDK's `SetExtraFields`
**replaces** rather than merges, so a future second root extension added naively would
silently drop `cache_control`.

Not decided here: whether the ToolHive gateway should inject `cache_control` for its own
Anthropic backends. It knows its backend where mecatl cannot, and doing so would fix every
client behind it. That belongs to the gateway.

## See also

- [ADR 0334 — Protocol-specific ToolHive gateway providers](./0334-toolhive-protocol-specific-providers.md) (the pattern this extends, and the URL-derivation rules it reuses).
- [ADR 0100 — Provider-side conversation prompt caching](./0100-provider-prompt-caching.md) (the TTL deferral this retires; its dialect table stands).
- [ADR 0210 — OpenRouter downstream-provider steering](./0210-openrouter-downstream-provider-steering.md) (the `order` interaction, and the sticky-routing role of the cache key).
- [ADR 0093 — Provider modules](./0093-provider-modules.md) (why the salt Option is Added for `provider/openai`).
- [Acceptance plan](../acceptance/unified-prompt-cache-dialect.md).
