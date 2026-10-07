# Providers and model routing

How Mecatl talks to model providers, binds a session to a provider and model, and
picks models through aliases, slots, and the semantic model router. Provider retries
and circuit breaking are in [observability](observability.md).

## What a provider adapter does

A provider adapter implements `port.LLMProvider`: it turns a provider-neutral
`port.LLMRequest` into one wire request and streams the reply back as `port.Chunk`
values. The engine never sees a provider type, so the loop is tested offline against
`engine/adapter/mockllm`.

Two invariants hold every adapter in place:

- **The request stays provider-neutral.** Wire-specific settings, such as
  Anthropic's required `max_tokens` or its thinking budget, are adapter construction
  options, not `LLMRequest` fields. Adding a provider never changes the port, the
  engine, or the wire API.
- **Replay is stateless.** Mecatl owns the conversation. Every request resends the
  full history; the Responses adapter sends `Store: false` and no previous response
  ID. Opaque reasoning that a provider needs back (encrypted reasoning items,
  Anthropic thinking signatures) travels in the opaque `session.Message.Reasoning`
  string, packed by the adapter as an ordered envelope and replayed untouched.
  Because the session log holds everything, a session can be persisted, resumed,
  forked, or rebuilt from events without provider-side state.

Reasoning blobs mean something only to the provider that issued them, so a session's
provider is fixed for its lifetime; switching provider means a new session.

Adapters fail closed on ambiguous streams: a Responses stream that ends without a
terminal event is an error, not an end of turn. If the provider rejects replayed
encrypted reasoning before any chunk is emitted, the adapter retries once without it
and marks a second failure not retryable. `provider/ssefilter` drops data-less SSE
frames (keepalives) before the OpenAI SDK decoder can choke on them. Upstream error
text maps to a closed set of display categories and never reaches clients raw.

## Wire adapters and registry identities

A wire adapter speaks one protocol. There are three, each its own Go module:
`provider/openai` (OpenAI Responses), `provider/anthropic` (Anthropic Messages), and
`provider/openaichat` (OpenAI Chat Completions, which has no reasoning replay across
turns). A registry identity is a `provider_id` a client can select; several reuse one
adapter with a different base URL, credential, or transport policy. The registry in
`internal/app/registry.go` holds only available providers:

| Identity | Adapter | Available when |
| --- | --- | --- |
| `openai` | Responses | an OpenAI key or bearer-token file is configured |
| `openai-codex` | Responses | a manual ChatGPT Codex token snapshot is configured (experimental) |
| `openrouter` | Responses | an OpenRouter key resolves |
| `openrouter-anthropic` | Messages | same OpenRouter key; Anthropic models only |
| `anthropic` | Messages | an Anthropic key resolves |
| `opencode` | Chat Completions | an OpenCode Go key resolves |
| `toolhive` | Responses | ToolHive gateway config is detected (intent, not a key) |
| `toolhive-anthropic` | Messages | same ToolHive gateway config |
| operator-defined | any of the three | declared in operator settings |
| `mock` | `mockllm` | offline mode only; replaces every other entry |

The two OpenAI identities are separate billing and entitlement boundaries: `openai`
uses a public API key against the supported Responses API, `openai-codex` a ChatGPT
subscription token against OpenAI's undocumented private Codex backend, and neither
enables the other. `internal/adapter/openaicodex` adds only credential, header, and
model-list policy around the same `provider/openai.Provider`, so request building,
replay, stream translation, and resilience stay single-sourced. Codex inventory
comes only from the account's live model list, never the public catalog.

ToolHive composes two wire adapters as two identities over one detected gateway
configuration: `toolhive` (Responses) and `toolhive-anthropic` (Messages). Their
inventories and health are independent, and model IDs are never merged across them.
In direct mode they share one in-process OIDC token source; in proxy mode they use
ToolHive's local proxy. OpenRouter follows the same pattern with `openrouter-anthropic`, which exists
because Anthropic prompt caching needs the Messages surface. The `openrouter` entry
also passes OpenRouter's downstream-provider preferences and reports which
downstream served a turn as a `provider.route` event.

Operator-defined providers pick a wire flavor and authenticate with an API key, no
credential, or OIDC (Responses only). Selecting an unenrolled OIDC provider fails
with a `mecatui providers login` hint instead of falling back.

Without a configured default, `preferredDefaultProvider` prefers `openai`, then other
key-driven providers, then intent-driven gateways. With no provider, Build fails with
an error listing accepted credentials and the `--mock` escape hatch.

## Per-session routing

`CreateSession` takes an optional `provider_id`/`model_id` selector, which the server
passes on as a neutral `server.ProviderSelector` for composition to resolve.

| Selector | Outcome |
| --- | --- |
| both empty | the shared engine on the deployment default |
| model without provider | invalid argument: a bare model is ambiguous |
| provider only | per-session engine on that provider's default model |
| provider and model | per-session engine bound to that pair; an uncatalogued model is passed through verbatim |
| unknown or unavailable provider | invalid argument, never a silent fallback |

A per-session engine is built by `sessionEngineFactory` through
`engineDepsForProvider`. That function re-derives everything that depends on the
provider or model: the LLM, the compactor, the model-keyed token counter, the
prompt's model fields, and the context-window resolver. It exists because a shallow
clone of the default deps with only the LLM swapped would count and compact through
the wrong model. Child engines for subagents, team members, and Parallel branches use
the same path. The server caps live per-session engines and returns
`ResourceExhausted` (HTTP 429) past the cap.

A subagent definition may pin its own `provider`, which beats the session's provider
and then the build default. A child on a different provider takes the definition's
model or that provider's default, never the parent's model string.

## Model inventory

`internal/app/provider_discovery.go` owns live model listing for one Build: one
server, or one replica in a multi-replica deployment. Inventory is not shared
across replicas. Listing requests from startup, the model picker, the
`DiscoverModels` tool, and context-window admission join one bounded attempt per
provider, followed by a short cooldown. A successful, non-empty list replaces that
provider's models; a failed or empty one keeps the last good list. Native
authenticated providers list on demand only. Published inventory carries only public
metadata (ID, provider, display name, capabilities, context limit), never keys or
endpoints.

A model's context window resolves at the point of use: global override, then exact
per-model configuration, then live metadata, then the embedded catalog, then a
128,000-token floor. If nothing is known and discovery for an eligible provider
hasn't succeeded, the window is unknown and the request is rejected before inference
(see [context and compaction](context-and-compaction.md)).

## Model resolution: aliases and slots

The engine only sees concrete model IDs. Aliases and slots resolve in composition.

An **alias** is a short name mapped to a model ID in operator settings.
`lookupModelAlias` in `internal/app/agentdefs.go` is the one grammar used by agent
definitions, flags, and slots. Built-in `sonnet`, `opus`, and `haiku` mean "inherit"
unless the operator maps them, so shared agent files never break startup.

A **slot** routes one internal call to its own model. `resolveSlotModel` in
`internal/app/slots.go` uses the slot's explicit binding, else the binding of the
slot's default tier (`cheap`, `fast`, or `reasoning`), else nothing.

| Slot | Routes | Default tier |
| --- | --- | --- |
| `compaction` | the compactor's summary call only | `cheap` |
| `ask-reviewer` | the headless child-ask reviewer | `cheap` |
| `guardrail` | the LLM guardrail checker; a binding, even via its tier, enables guardrails | `cheap` |
| `reflection` | the learning reflection call | `cheap` |
| `router` | the semantic router's classifier | `cheap` |
| `title` | server-side session titles | none (opt-in) |
| `plan` | the session model while in plan mode | `reasoning` |

Slot resolution is fail-soft: an unknown slot key or an unresolvable selector logs
one warning at Build and the call keeps the session model. The `plan` slot is the
one slot on the mode axis. It swaps the model, never the provider, and takes effect
at the next run entry: the server rebuilds or promotes the session's engine when the
session's mode differs from the one the engine was built for.

Model settings come from the operator tier. A trusted project may rebind the default
model, slots, and aliases only to entries in the operator's allowlist; values outside
it are dropped with a warning, and without an allowlist project model settings are
ignored.

## Semantic model router

The router picks the model for a delegated task from an operator-defined taxonomy of
categories, each with a description and a model selector. Defining categories
enables it; a kill switch disables it. It applies to `Subagent` calls, team members
(classified once when added), and Parallel branches (each classified once).

It decides only when nothing else has: an explicit per-call model, a definition's
model (including `inherit`), fork, and resume all skip it. The routed child stays on the parent's provider and
keeps that model for its lifetime. The chosen model never enters `port.LLMRequest`;
composition builds the child through the same per-provider factory.

Two classifier backends exist. The `llm` backend runs one tool-less turn on the
`router` slot model; the task text is fenced as untrusted, and the reply must be a
single JSON object naming an offered category. The `jev` backend asks the Typesafe
Jev service the same question with bounded input, time, and concurrency. Both return
the engine-owned `ModelRouteResult`.

The router is never load-bearing. A classifier error, timeout, cancellation,
unparseable verdict, unknown category, low confidence, or unresolvable target keeps
the model the delegation would have used anyway. A per-run breaker opens after three
consecutive misses and skips classification for the rest of the run; its mutex also
serializes classifications so a fan-out can't multiply classifier spend. Children
get no router, so routing never nests.

Delegation start events record the model that ran, the routed category and model, a
bounded routing reason (such as `pinned-model` or a miss code), and an optional
decision snapshot: metadata only, never task content.

## Related

- [Ports](ports.md)
- [Context and compaction](context-and-compaction.md)
- [Observability](observability.md)
- [Subagents and teams](subagents-and-teams.md)
- [Choose models](../../user-docs/features/choose-models.md) and the
  [configuration reference](../../user-docs/reference/configuration.md)
