# ADR 0210 — OpenRouter downstream-provider steering + routing echo

- Status: Accepted
- Date: 2026-08-12
- Scope: the openrouter provider entry (composition + the shared OpenAI Responses adapter), the operator config surface, and the client event stream.

## Context

OpenRouter is a *meta-provider*: one model id (e.g. `anthropic/claude-sonnet-4-6`)
is served by several **downstream** inference providers (Anthropic, Amazon Bedrock,
Google Vertex, DeepInfra, …). By default OpenRouter load-balances across them on
price. mecatl had no way to (a) steer which downstream serves a model or (b) see
which downstream actually served a request — an operator sensitive to cost,
compliance, or latency was flying blind.

Two constraints shaped the design:

- `port.LLMRequest` is provider-neutral (guarded by `llm_neutral_test.go`); a
  provider-private knob must be an adapter-construction Option, minted per model in
  the registry entry's `construct`/`remint` closure (the reasoning-effort
  precedent), never a request field.
- mecatl already overloads "provider" to mean the wire adapter (`openai` /
  `openrouter` / …). To avoid collision we call OpenRouter's per-model inference
  providers **downstream providers** everywhere (config, code, docs).

OpenRouter mechanics (verified against its OpenAPI spec + provider-routing docs):
the `provider` request-body object (`ProviderPreferences`) is honoured on **POST
/responses** (the same schema as Chat Completions — no dialect change); the routed
downstream is reported via an opt-in `X-OpenRouter-Metadata: enabled` header whose
`openrouter_metadata` block arrives on the final streaming `response.completed`
event, and is **stripped on cache hits**.

## Decision

**Steering (config → request).** A new operator-tier-only `openrouter:` settings
subtree (`permconfig.OpenRouterSection`) carries a per-model `order: []string` +
`allow_fallbacks: *bool` (v1 surface only). It is parsed strictly, operator-tier
only (a project-tier block is WARN-ignored — steering is a spend/compliance
decision, the same gate as `default_provider`/`allowlist`/`router`). Composition
(`foldOperatorOpenRouter`) validates fail-soft (invalid slug / empty order / empty
or unresolvable model key WARN-dropped) into `Config.openRouterRoutes` — resolving
an alias KEY to its concrete model id via `lookupModelAlias`, so a route written
against an alias lands under the id the request actually carries — and the
openrouter registry entry alone passes two `openai` Options —
`WithOpenRouterProviderPreferences` (a model-keyed resolver) +
`WithOpenRouterMetadata(true)` —
via the existing `extra` channel so every mint (default + per-session remint)
carries them. The adapter injects the `provider` key onto the marshalled body with
`option.WithJSONSet` (verified to work on the Responses POST body) and adds the
metadata header — both **per-request options**, so `buildParams` and the
byte-stable prompt-cache prefix are untouched. No other entry passes these
Options, so the key/header can never leak to a non-OpenRouter endpoint.

**Observability (response → echo).** The adapter parses the terminal
`response.completed` event's `RawJSON()` for `openrouter_metadata` and emits a new
`port.ChunkProviderRoute` (Text = OpenRouter's selected downstream display label,
fail-empty). The loop relays it verbatim onto a new client-visible
`session.EvProviderRoute` (`"provider.route"`) event — a string passthrough (no
proto enum), metadata-only, never recorded to the conversation, never a
diagnostics line (the event taxonomy owns the fact). On a cache hit (metadata
stripped) nothing is emitted — never a fabricated value. mecatui briefly shows
`via <display-name>` in the footer and retains `<model>/<display-name>` in the
header for the current turn; the next turn clears the header before metadata can
arrive, so a cache hit cannot display stale routing.
`ChunkProviderRoute` is classified non-committing in `llmresilience`.

**Rejected alternatives.** Folding the routed downstream into `Service.ResolvedModel`
— wrong shape: that is per-session *identity* resolved at engine construction; the
routed downstream is per-turn and post-hoc. A diagnostics line — operator-only,
invisible to clients, while the client-visible event already owns the fact.
Stamping the params
struct via `param.Override` — brittle against SDK bumps; `WithJSONSet` is the SDK's
own escape hatch.

## Consequences

- An operator can steer a model to a preferred downstream and see which downstream
  served each turn. Setting an `order` disables OpenRouter's default price
  load-balancing (an explicit choice, surfaced in the config docs);
  `allow_fallbacks: false` pins hard.
- **Hard pin ⇒ hard fail (live-verified).** With `allow_fallbacks: false`, if
  OpenRouter can't satisfy *any* downstream in `order` for the model (out of
  policy on the account, unlisted, or down), the request 404s with
  `No endpoints found for <model>` and the turn errors — no silent fallback. A
  downstream can be *listed* healthy on a model's endpoints and still be
  unroutable on the account. Documented in `docs/usage/model-routing.md`.
- Two exported engine constants were Added (minor): `port.ChunkProviderRoute`,
  `session.EvProviderRoute`. Recorded in `engine/CHANGELOG.md`; the api-compat
  snapshots regenerated.
- The echo is best-effort: a cache hit or an OpenRouter change to the metadata
  shape degrades to silence, never an error. The echo carries OpenRouter's
  DISPLAY name for the downstream (e.g. "Google"), which differs in vocabulary
  from the lowercase-kebab slug the steering `order` uses ("google-vertex") —
  two different OpenRouter surfaces. We relay the display name verbatim and
  deliberately do NOT map it back to a slug (a guessed slug can be wrong, so the
  echo is a human readout, not a round-trippable identifier).
- v1 scope: `order` + `allow_fallbacks` only, config-only (no CLI flag). The rest
  of `ProviderPreferences` (`sort`, `only`/`ignore`, `quantizations`, `max_price`,
  …) can follow as additional `openrouter:` keys without reshaping the seam.
- The e2e proof (`internal/app/openrouter_route_e2e_test.go`) is the strict guard
  on `WithJSONSet` surviving the SDK's body marshalling — a silent SDK-behaviour
  change fails it.

## See also

- `docs/usage.md` (operator config), `docs/configuration-reference.md` (generated).
- The provider-neutral request discipline in [ADR 0016](./0016-multi-provider.md);
  the adapter-Option-not-request-field rule this follows.
- Issue [#480](https://github.com/stacklok/mecatl/issues/480).
