# Providers — OpenAI adapter & multi-provider

> Part of the [mecatl architecture guide](../architecture.md).

**What this covers:** the OpenAI Responses adapter (request translation, SSE → Chunk translation, cancellation), the Anthropic native Messages adapter, OpenCode Go Chat Completions, multi-provider registry + per-session routing, model resolution (aliases, per-slot models, the `plan` slot), the semantic model router for delegation, and capability single-source intersection.

**Prerequisites:** [the ports](ports.md) — the `LLMProvider` port this adapter implements.

**Follow-on:** [context & compaction](context-and-compaction.md) and [observability](observability.md) — per-model context-window handling, `llmresilience`, and per-model tokenizers.

## The OpenAI Responses adapter (`provider/openai`)

> This section walks one adapter end-to-end. It is **not** the whole LLM story:
> mecatl is provider-agnostic, with the native Anthropic Messages API as a peer
> adapter and a per-session provider/model registry — see the [multi-provider](#multi-provider--registry-per-session-routing--model-inventory) section below.
> The deeper design brief for this adapter is
> [`docs/adr/0017-openai-responses-api.md`](../adr/0017-openai-responses-api.md).

`Provider` implements `port.LLMProvider` over `POST /v1/responses` using
`github.com/openai/openai-go/v3`. It owns its own conversation state
("strategy B"): every request is **stateless** — `Store: false`, no
`previous_response_id` — and resends the full input item slice.

**Request translation** (`request.go`, `buildParams`):
- `LLMRequest.System` (the `prompt.Layered`) is `Render()`-ed into
  `Instructions`.
- `Tools` → function tools, each spec's JSON `Schema` unmarshalled into the
  SDK's parameter map (`Strict: true`; empty schema → empty object).
- `Messages` → the input item array via `buildInput`: system/user/assistant text
  become message items; tool messages become `function_call_output` items keyed
  by `call_id`. An assistant turn expands (`assistantItems`) in the order
  **reasoning item → function_call item(s) → text message**.
- `Store: false` plus `Include: [reasoning.encrypted_content]` so reasoning
  survives across turns statelessly. `Message.Reasoning` is carried verbatim as
  the reasoning item's `EncryptedContent`.

**SSE → Chunk translation** (`stream.go`, `translate` — a pure function driven
directly from recorded fixtures by `decodeSSE` in tests):
- `response.output_text.delta` → `ChunkText`
- `response.reasoning_summary_text.delta` / `response.reasoning_text.delta` →
  `ChunkReasoning` (the DISPLAY summary)
- `response.output_item.done` (reasoning) → `ChunkReasoningItem` (the
  `encrypted_content` REPLAY blob — distinct from the display summary; the two
  must never be conflated)
- `response.output_item.done` (message) → `ChunkPhase` (the opaque phase
  marker, stored on `Message.ProviderPhase` and replayed verbatim on the
  assistant message item — issue #46)
- `response.output_item.done` (function_call) → `ChunkToolCall` (acts on the
  assembled `.done` payload, not concatenated deltas)
- `response.completed` → `ChunkUsage` then `ChunkDone(end_turn)` (cached tokens
  map into `Usage.CacheReadTokens`)
- `response.incomplete` → `ChunkUsage` then `ChunkDone(error)`
- `response.failed` / `error` → a non-nil stream **error** carrying the
  provider's message verbatim (so the real reason reaches the terminal
  `result`, not an opaque "error")

**Cancellation**: `Stream` (`openai.go`) selects on `ctx.Done()` each iteration
and abandons the underlying stream; a deliberate `ctx` cancel is **not** reported
as a stream error.

**The provider-neutral seam**: the loop only ever sees `port.Chunk`; no OpenAI
type crosses the boundary. The fake `mockllm.Provider` (`engine/adapter/mockllm`,
`New(turns...)`, `TextTurn`) implements the same port for offline loop testing.

**Compatible endpoints**: `WithBaseURL(url)` overrides the host (vLLM, LiteLLM,
a local proxy); the SDK appends `/responses`. `WithAPIKey` and
`WithRequestOption` round out the options. `cmd/mecated` plumbs
`--openai-base-url` through to it.

## Multi-provider — registry, per-session routing & model inventory

mecatl can serve more than one LLM provider in one process and bind a **provider +
model per session**. The wiring lives entirely in the composition layer
(`internal/app`); the domain/agent/server never see a registry — they receive a bare
`port.LLMProvider`.

**The registry (`internal/app/registry.go`).** `buildProviderRegistry` constructs,
once at `Build`, the set of AVAILABLE providers — a provider is available iff one of
its credential env vars resolves (the var NAMES come from the embedded models.dev
catalog, `internal/adapter/providercatalog`; `OPENAI_API_KEY`/`OPENROUTER_API_KEY`/`ANTHROPIC_API_KEY` —
and `OPENCODE_API_KEY`, supplied by composition since OpenCode Go is not in the vendored catalog).
Only available providers are held (an unkeyed provider is omitted — its availability
is itself sensitive, CWE-200). OpenRouter rides the SAME stateless openai adapter with
the OpenRouter base URL substituted. **Anthropic (P1) is the first native non-OpenAI
wire adapter** (`provider/anthropic`, on the official MIT `anthropic-sdk-go`):
the native Messages API, also STATELESS full-replay, wired via `newAnthropicEntry`. It
validated the provider abstraction — it shipped with NO domain/agent/server/acp/proto
edit; `engineDepsForProvider`, per-session routing, the capability intersection, and
per-sub-agent-provider switching all treat it as data. Its wire-divergences (the
REQUIRED `max_tokens`, the model-class-dependent extended-thinking config which is ON
and model-aware, and the `(thinking,signature[],redacted)` reasoning-replay list packed
into the opaque `Message.Reasoning` STRING) are absorbed at adapter-construction, not in
the DTO. `UseMock` short-circuits to a single synthetic
`mock` entry (offline). The zero-keys case is the named, actionable `errNoProvider`.

**OpenCode Go (`provider/openaichat`)** is the Chat Completions wire adapter —
the sibling of the openai Responses adapter, built on the same `openai-go` SDK via
`client.Chat.Completions`. It serves provider id `opencode` (base URL
`https://opencode.ai/zen/go/v1`, key `OPENCODE_API_KEY`) and is the generic OpenAI
Chat-Completions protocol adapter (usable by any such endpoint). Reasoning-effort passes
through un-clamped (the endpoint accepts `xhigh`/`max`); reasoning-replay and phase are
dropped (Chat Completions is stateless across turns), so no port/proto/engine-API change
was needed. Live model listing rides `openCodeLister` (the `openaicompat` lister
wrapped to stamp adapter-static text+image modalities, so a live refresh doesn't
flip an uncatalogued model's Image capability to false).

**SSE keepalive survival.** A data-less SSE frame — a keepalive comment
(`: ping - ...`, which OpenCode Go sends on long turns), a bare extra blank line,
an `event:`-only frame, or an empty `data:` value — used to kill the stream
outright once it landed mid-turn: the openai-go `ssestream` decoder dispatches on
every blank line and `json.Unmarshal`s the empty payload into
`unexpected end of JSON input`, a latched error. A rare event on an
otherwise-healthy connection, but terminal every time it hit, since the no-replay
rule can't retry past the first committed chunk. The shared `provider/ssefilter`
package, installed as the OUTERMOST `option.WithMiddleware` on **both** the
`openaichat.New` (Chat Completions) **and** `openai.New` (Responses) constructors,
buffers each SSE frame and drops any data-less frame in its entirety before the
SDK's decoder sees it — whole-frame, not line-at-a-time, so a dropped frame's
`event:`/`id:` lines can never leak into the following frame. It bounds its own
per-frame buffer so it doesn't reopen the unbounded-read hazard it would otherwise
sit in front of. The mechanism is confirmed on Responses-shaped frames too, so
the Responses adapter (`openai`, `openrouter`, and the ToolHive LLM gateway
entries — all the same Responses wire protocol) carries the guard as well as the
`opencode` slot. The Responses adapter additionally fails a stream CLOSED
(`errTruncatedStream`, wrapping `io.ErrUnexpectedEOF`) when it ends with no
terminal event, so a truncated turn is never promoted to a successful
`StopEndTurn`. See
[`docs/adr/0067-openai-chat-completions-adapter.md`](../adr/0067-openai-chat-completions-adapter.md)
and `docs/design/IMPLEMENTATION-NOTES.md` for the exact mechanics.
`buildProvider` returns the registry **and** its default provider so the shared engine
+ every child/fork/team engine keep receiving the single default provider exactly as
before (the default path is byte-identical). A composition-only `providerConstructor`
seam (mirroring `envDetector`) lets the offline e2e back two real provider ids with
mocks; production leaves it nil.

**Intent-driven availability (issue #262, ADR 0064).** Every provider above is
**key-driven** — available iff a credential resolves. The ToolHive LLM gateway proxy
entry (`providerToolhive`, id `"toolhive"`) is **intent-driven** instead: it is
registered when `resolveToolhiveIntent` detects ToolHive's own config file (or an
explicit `--toolhive-llm-base-url`) — no credential required, and NEVER gated by
reachability (register-on-intent; a session persisting `provider_id: "toolhive"` must
survive a restart with the proxy down, never rejected as "unknown or unavailable
provider"). Each `providerEntry` carries an `intentDriven` bit that
`preferredDefaultProvider` reads to place intent-driven providers at an explicit
LOWEST-preference tier (any key-driven provider always wins the default) and that the
`ListModels` `provider_status` projection reads to scope its wire surface to
intent-driven entries only. A BOUNDED (≤1.5s) Build-time probe runs immediately after
registration and drives ONLY the startup diagnostic, the initial live-model snapshot,
and default-model eligibility for a SOLE intent-driven provider — never registration
itself. See `docs/adr/0064-toolhive-llm-gateway-provider.md` for the full design
(including the accepted sole+probe-down boot deviation) and
`internal/adapter/openaicompat` / `internal/adapter/toolhivellm` for the two-layer leaf
split (protocol-generic lister + the one ToolHive-aware config reader).

**Per-session routing (`sessionEngineFactory`).** `CreateSession` carries an OPTIONAL
`provider_id`/`model_id` selector, expressed at the server boundary as the NEUTRAL
`server.ProviderSelector` (the server adapter imports neither the registry nor the
catalog). The widened `SessionEngineFactory func(ctx, sel, specs)` is the ONE seam for
a per-session engine — it serves BOTH a non-default provider/model AND client-provided
MCP servers (orthogonal inputs → ONE engine over ONE catalog). The composition factory
resolves the selector against the registry and builds Deps via
**`engineDepsForProvider`**, which re-derives EVERY provider/model-closing field
(LLM, Compactor, Model, model-keyed TokenCounter, `PromptConfig.Env.Model`, and the
**context-window resolver** `Deps.ContextWindow` — a `func() int` built by
`reg.windowResolver` (override→live→catalog→128k floor) and read live at the point of
use, so the compaction trigger AGREES with the `ListModels`-advertised `context_limit`
and self-corrects after a live-catalog swap with no rebuild; only a genuinely
uncatalogued passthrough model falls back to the 128k default). The DEFAULT model
resolves through the SAME resolver (`baseEngineDeps`, issue #63) — it is no longer
pinned to the 128k floor.
This is the contamination fix: a shallow clone swapping only the
LLM would compact/count through the wrong model. The resolution table:

| `provider_id` | `model_id` | Outcome |
|---|---|---|
| `""` | `""` | **Shared engine** (default provider, no per-session build) — today's path. The default itself resolves `--model` → the server-configured deployment default (`--default-provider`/`--default-model`, issue #21; validated **fail-fast** at build) → the per-provider built-in |
| `""` | set | **InvalidArgument** — a bare model on the env-derived default provider is ambiguous |
| known+available | `""` | per-session engine on that provider's default model |
| known+available | catalogued | per-session engine bound to (provider, model) |
| known+available | NOT catalogued | **passthrough** — the model string reaches the provider verbatim (catalog gates nothing) |
| unknown/unavailable | any | **InvalidArgument** — `"unknown or unavailable provider"`, never a silent fallback |

The provider is **fixed for the session lifetime** (reasoning-replay + the byte-stable
prefix are provider-private; "switch provider" = new session). `session.Session` is
NOT widened — the selector resolves to an ENGINE at create time, registered in the same
`sessionEngines` map (and selected the same way by `StartRunContent`) the client-MCP
path uses; `loadAndReopen` is untouched. That map is **capped** at
`Config.MaxSessionEngines` (default 1024): the gRPC/HTTP surfaces have no
connection-teardown drain, so without a cap a client creating selector sessions and
never calling `CloseSession`/`EndSession` could grow it unbounded (CWE-770). Past the
cap, `createSession` returns `ErrTooManySessionEngines` (gRPC `ResourceExhausted` /
HTTP 429); `CloseSession`/`EndSession` frees a slot. (Keys are NEVER on the wire — only
the provider id.)

**Model inventory (`ListModels` / `internal/app/modelsnapshot.go`).** `modelSnapshot`
joins the registry's AVAILABLE providers to the embedded catalog and projects each
model into the proto `ModelInfo` (public metadata only — id, provider_id, display_name,
image/reasoning flags, context_limit — never a key/env/base-URL). The composition root
injects the snapshot into `server.Config.Models`; the server adapter holds only the
proto slice (mirroring the `ListAgents` idiom). The `mock` provider advertises no
selectable models. `ServerCapabilities.model_selection` is true iff the snapshot is
non-empty, gating the client's model picker the way `agents` gates `/agents`. Provider
key/base-URL flags landed in `cmd/mecated` earlier; the picker UX is a client concern.

**Capability single-source (`internal/app/capability.go`).** A model's true input
capability is the INTERSECTION `catalog-per-model-modalities ∩ adapter-Capabilities()`,
computed by `modelCapability` in composition (the only layer holding both inputs). That
ONE neutral `port.ProviderCapabilities` feeds three sinks so they cannot disagree:
`ModelInfo.image` (ListModels), the `CreateSessionResponse.session_capabilities` echo
(per-session), and the ACP gate (`Service.ProviderCapabilities()`, the default caps).
The server/acp adapters receive only the computed value — no catalog/registry type
crosses inward. Keys are never on the wire — only the provider id.

**Per-sub-agent provider (shipped).** A Subagent agent def or team member may pin a
`provider:` (orthogonal to `model:`) to run its child engine on a DIFFERENT provider
than the parent, and a provider-selected session propagates its provider to the
sub-agents it spawns (which it now CAN — Half B builds it a per-session Subagent/Team
tool). Precedence: `def.Provider > session-selected provider > build-time default`;
every child routes through `engineDepsForProvider` so it never contaminates the
parent's compactor/counter. Composition-only — the registry never reaches the child
engine (a bare `port.LLMProvider` is handed down).

**Full design: see [`docs/adr/0016-multi-provider.md`](../adr/0016-multi-provider.md)** (registry, catalog-as-data, DTO
neutrality, selection primitive, per-session engine, capability intersection,
disclosure posture + per-client key custody, per-sub-agent provider, and the P0→P3
phasing).

## Model resolution — aliases + per-slot models

Model selection layers **on top of** the provider routing above, all in composition
(the domain/agent only ever sees a concrete model string).

**Aliases are the spine ([ADR 0030](../adr/0030-model-selection-heuristics.md)).** A
short semantic name (`cheap`/`fast`/`reasoning`, or the Claude-Code-style
`sonnet`/`opus`/`haiku`) maps to a concrete provider model id through the operator's
`ModelAliases` map (`--model-alias name=id`, or the `models.aliases` YAML map), then the
built-in aliases. The ONE grammar is `lookupModelAlias` in
`internal/app/agentdefs.go` — shared by the forgiving agent-def `model:` path
(`resolveAlias`) and the fail-fast flag path (`normalizeSubagentModel`), so they never
drift.

**Per-slot models (`models.slots`, Phase 1+2).** A **slot** is a named internal
lightweight LLM call. Three are routed to a slot so housekeeping can run on a cheaper
model than the session:

| Slot | Routed call | Resolution choke point |
|------|-------------|------------------------|
| `compaction` | the `CascadeCompactor` tier-4 summary call | `engineDepsForProvider` → `buildCompactor` |
| `ask-reviewer` | the headless child-ask reviewer (issue #31) | `askAdjudicatorDeps` |
| `guardrail` | the LLM content checker (issue #27) | `buildGuardrailsChecker` |

The single choke point is **`resolveSlotModel`** (`internal/app/slots.go`): an explicit
`models.slots[<slot>]` binding wins, else the slot's default **tier** (`compaction`,
`ask-reviewer`, and `guardrail` all default to `cheap`), else `("", false)`. A resolved
selector is mapped THROUGH `lookupModelAlias`, so a slot value is itself an alias or a
literal id. For the compaction slot, ONLY the summary LLM call's model (and its
token-counter) is swapped — the engine's own Model / TokenCounter / PromptConfig /
ContextWindow stay on the session model. For `ask-reviewer` the slot **supersedes the
model** of `--subagent-ask-reviewer`, but that flag stays the **on/off gate** (a slot
alone never enables the reviewer). For `guardrail` the slot **supersedes the model**
of `--guardrails-model` AND **enables** guardrails (ADR 0046 — configure = enable); the
flag is no longer the sole enable gate.

Posture is **fail-soft** and the default is **byte-identical**: with no slot configured
every routed call keeps the session model; a typo'd slot key or an alias meaning inherit
WARNs and degrades to the session model — a broken housekeeping slot never wedges the
call. `models.slots` / `models.aliases` come from the operator tier (user-global
`settings.yaml` + `--model-slot` / `--model-alias`); a project tier may also bind them
**within an operator allowlist** (see below). Team synthesis is **deferred** (it runs on
the lead member's whole engine); the subagent router shipped in Phase 5 (see below).

**Mode→model: the `plan` slot (Phase 3, the opusplan pattern).** A fourth slot, `plan`,
is wired on the **mode axis** rather than the internal-call axis. It does **not** route a
housekeeping call — it re-resolves the **session** model when the session's
`PermissionMode` is plan, so a planning turn runs on a strong-reasoning model and an
executing turn on the session model. Two divergences from the call-slots: its default
**tier** is `reasoning`, not `cheap` (a plan model is a strong-reasoning model); and its
consumer is the **per-session engine factory** (`sessionEngineFactory`), not a per-call
deps builder. It reuses `resolveSlotModel` unchanged — same grammar, different consumer.

The re-resolution is **fixed per turn, re-resolved between turns**: the model is fixed
for the duration of a turn; a mode switch (`SetMode`, rejected mid-turn) takes effect at
the next **run-entry seam** — the SAME seam `rehydrateSession` already rebuilds a
per-session engine on. The provider stays **fixed per session**: the plan slot only
swaps the **model** within the session provider, never the provider. Two rebuild triggers
live in the server (`engineAndWorkspaceFor`): a registered per-session engine whose
`builtForMode` no longer matches the session's `Mode` is **rebuilt** (CASE 1), and a
default-FS session whose mode would change the model — gated by the composition predicate
`server.Config.ModeNeedsEngine` (nil unless a plan slot is active, the **byte-identical**
guard) — is **promoted** to a per-session engine (CASE 2). Both go through the one shared
`buildAndRegisterSessionEngine` helper. `resolved_model` re-emits the new model on the
next `GetSession`/turn echo after the rebuild (a `SetMode` response still carries the
pre-rebuild model — the model is fixed per turn). With no plan slot, a mode flip changes
nothing. See [ADR 0030](../adr/0030-model-selection-heuristics.md) Layer 3.

**Project-overridable model config, capped by an operator allowlist (Phase 4).** A
**trusted** project's `.mecatl/settings.yaml` may re-bind `models.default` / `models.slots`
/ `models.aliases` — but only to entries the operator vetted. The whole layering chain is:

```text
CLI (operator flags) > project-YAML (capped) > operator-YAML (settings.yaml) > built-in
```

The operator's `models.allowlist` (a list of alias names and/or concrete ids) is the
**non-wideable cap**. It is the **opt-in**: with no operator allowlist, a project `models:`
block stays WARN-ignored — byte-identical to before Phase 4. A project-tier
`models.allowlist:` key is always ignored with a WARN (a project cannot widen its own cap).

Two halves enforce it:

- **`permconfig`** (`OperatorModelPolicy()` / `ProjectModelBindings(ws)`) captures the
  project bindings within the **trust gate** (the SAME `TrustProject` gate as project allow
  rules — an untrusted repo's `models:` is dropped) and the opt-in (an operator allowlist
  must exist). The allowlist: key is stripped at capture; the membership cap is left to
  composition (it needs the operator-merged alias map to canonicalize).
- **Composition** (`foldProjectModelBindings`, `internal/app/slots.go`) canonicalizes the
  allowlist to a concrete-id **set** (each entry resolved through the operator-merged alias
  map), then for each project binding does **resolve-then-check**: resolve the value to a
  concrete id, test membership, **accept** (merge) or **drop** (keep the operator/default
  value) with one build-once WARN. Slot bindings are also validated against the known slot
  names. The allowlist applies to **every** config-file binding consumer — the session
  `default`, all slots (including the Phase-3 `plan` slot), and aliases.

Precedence within the cap: a project binding **overrides** the operator-YAML value for the
same key but **skips** any key the operator set on the **CLI** (the CLI flag is a deliberate
per-run override that still wins). The mechanism is a snapshot of the CLI-set keys taken in
`Build` **before** the operator-YAML fold runs (`captureCLIModelKeys`); the project fold
overrides operator-YAML keys yet skips the snapshotted CLI keys. The allowlist and its
canonicalization are always operator-only.

**Out of scope (this slice):** the allowlist caps **config-file** bindings only — a per-def
`AgentDef.Model` literal and the per-session API selector
(`CreateSessionRequest.model_id`) are not capped here. The operator's OWN bindings are
never capped (the operator is authoritative). See
[ADR 0030](../adr/0030-model-selection-heuristics.md).

## The semantic model router (Phase 5)

The **semantic model router** ([ADR 0031](../adr/0031-subagent-model-router.md), enable
model [ADR 0042](../adr/0042-taxonomy-gated-model-router.md), extended by
[ADR 0034](../adr/0034-team-parallel-model-routing.md)) picks which model a delegation runs
on, **per task**, from an operator-defined menu. ADR 0031 shipped it for the `Subagent`
family; ADR 0034 extended it to **agent-team members** and **Parallel branches** — the same
operator taxonomy and enable model govern all three. It is the Phase-5 realisation of ADR
0030's deferred "Layer 3b" — built as a sibling of the headless ask reviewer and the
guardrail checker, not as new architecture.

**Taxonomy enables; a kill-switch disables (ADR 0042).** The operator defines categories
in the user-global `settings.yaml` `models.router:` subtree — each a `name`, a one-line
`description` the classifier reads, and a `model` selector (alias / slot / concrete id).
**Defining a non-empty taxonomy ENABLES the router** — the guardrails-parity model
(configure = enable), replacing ADR 0031's flag-to-enable gate. To keep the taxonomy but
turn routing off, set `disabled: true` in the subtree or pass
`--subagent-model-router=false` (the two combine into `cfg.RouterDisabled`); a bare
`--subagent-model-router` / `=true` is a harmless no-op (it still parses but neither
enables nor disables — the router stays governed by the taxonomy). A project-tier
`models.router:` is **stripped with a WARN**
(operator-tier only). The classifier itself runs on the `router` model slot (default
`cheap` tier; an operator `classifier-slot` overrides) — a tiny one-turn call.

**How it fires.** For a **plain** default delegation only (no per-call `model`, no `agent`,
no `fork`, no `resume` — those already pin the engine), the `Subagent` `run()` hook calls a
composition-built classifier (`RunModelRouter`, role `model-router`, tool-less, one turn,
no-progress nudge disabled). The classifier reads the category descriptions in the clear
and the (untrusted) task prompt inside the `UntrustedFence`, and returns a category by the
**whole-output-single-JSON-object** parse (the hardened parse the ask reviewer uses); a
hallucinated category is a miss. Composition maps the chosen category to its model selector
through the alias machinery (operator targets are **uncapped**) and mints the child via the
**existing per-call engine factory** — **decide-once, commit-for-child-lifetime,
same-provider** (the engine layer stays model-string-only; the chosen model is never a
`port.LLMRequest` field). Both the foreground and background paths route.

**Precedence** (by gating): explicit per-call `model` > agent-def `Model` (incl. explicit
`inherit`) > fork/resume > **router** > `--subagent-model` default > session model. The
router fills the gap; it never overrides pinned intent.

**Named agent-defs too (issue #286, [ADR 0066](../adr/0066-route-unpinned-and-writable-delegations.md)).**
A delegation to a named `agent` that declared **no `model:`** (expressed no model intent) is
ROUTABLE — the router classifies it and rebuilds the def's SCOPED engine (its
catalog/prompt/hooks) on the picked model, fail-soft to the pre-built def engine on a miss.
ANY non-empty `def.Model` — `inherit`, a built-in alias, a concrete id — PINS the def
(routing skips it); **explicit `model: inherit` is how you opt a def OUT of routing**.
Composition excludes a def that switches provider or declares inline MCP from the routable
set (the pick could not mint there), so the classifier is never spent for it. Team members
and Parallel branches are out of scope (unchanged).

**Writable delegations too (issue #285).** A `mode:"read-write"` delegation honours the
same axes: a per-call `model` (or the router pick) rebuilds the WRITABLE explorer on that
model via the writable engine factory (direct-write against the parent tree — no fork). A
writable call with an explicit `model` but no writable factory wired is a LOUD error (never
a silent inherit); a plain writable delegation whose routed pick would be discarded (factory
unwired) does not spend the classifier at all. A writable `resume`/specialist (`agent`) keeps
its own engine, unchanged.

**Fail-soft + breaker.** The router is **never load-bearing**. Any classifier failure,
cancellation, unparseable verdict, unknown category, or unresolvable target → the
delegation inherits the default explorer model. A per-run circuit breaker (default 3
consecutive misses, mirroring the ask-reviewer breaker) opens after repeated misses and
skips the classifier for the rest of the run; a success resets it. Its mutex serialises
classifications within a run, so a Subagent fan-out cannot multiply classifier spend.
**OFF (no taxonomy, or the kill-switch) is byte-identical** — no classifier call, the
inherited model. **No nesting:** a child has no `Subagent` tool, and `childEngineDepsForProvider`
forces `Deps.SubagentModelRouter` nil. It runs in **both** interactive and headless
deployments (it is orthogonal to the ask-review path).

**Observability.** `EvSubagentStart` carries `RoutedCategory`/`RoutedModel` (bare metadata,
gauntlet-#7 safe) when routed; a per-classification INFO rides the existing child
diagnostic chokepoint and a Build-once "router ACTIVE" fact narrates the config. Every
MISS logs an INFO naming the reason (`degenerate-input`/`classifier-error`/`cancelled`/
`bad-verdict`/`unknown-category`/`category-selector-empty`/`category-target-unresolvable`/
`empty-model` — metadata only, issue #287); the breaker-open INFO is unchanged. The
routed fields surface end-to-end: the session struct + the proto/client wire
(`routed_category`/`routed_model` on the `Subagent` event payload), relayed through
the gRPC + HTTP relays and rendered by mecatui (inline card + ctrl+a fleet roster).

**Team members + Parallel branches (ADR 0034).** The same router governs the other two
delegation families, reusing the one `parentCaps.routeTask` closure the dispatcher binds per
run (so a mixed turn shares ONE breaker / miss-counter / classifier-usage fold across all
families). The per-family seam respects each family's engine lifetime:

- A **team member** is classified **once at `AddMember`** (decide-once; the member engine is
  built once and reused across rounds via `Reopen`, never re-routed), off its
  `InitialPrompt` (falling back to its name). Only a **plain undefined** member routes — a
  member with an agent def pins its own model. `AddMember` runs serially on the single
  Team-tool goroutine, so members classify one at a time. The routed model id threads
  through the `MemberEngine` factory's new `routedModel` parameter; the
  `EvTeamStart` roster entry carries `RoutedCategory`/`RoutedModel`. The gRPC `RunTeam`
  direct path is **zero-caps** (no `routeTask`), so it never routes — byte-identical.
- A **Parallel branch** is classified **per-branch in `runBranch`** (each branch routes at
  most once) over an OPTIONAL `engineFactory` (the `WithSubagentEngineFactory` shape); on a
  hit the branch runs on the routed engine, on a miss/unwired the shared branch child. The
  branch_start event carries the routed metadata. The breaker mutex serialises the concurrent
  branch classifications.

Like the Subagent family, the team and parallel routed fields surface **end-to-end on the
proto/client wire**: `routed_category`/`routed_model` on the `TeamMemberSpec` (team.start
roster) and on the `Parallel` event (branch_start), relayed through the gRPC + HTTP relays
and rendered by mecatui (the ctrl+a Teams roster row and the Parallel group-focus branch
row). Bare metadata only — a category label + a model id, never member/branch content
(gauntlet #7).

**The per-delegation model surface (ADR 0035, issue #112).** The routed cue only answers
"the router chose this"; it is empty for the common cases (router off, inherited/default,
agent-def-pinned, per-call `model` override). A generic `model` field now rides ALL three
delegation payloads — `Subagent` (field 13), `TeamMemberSpec` (field 7), `Parallel`
(field 21) — carrying the **concrete model id the child actually ran on, regardless of how
it was chosen**. It is populated at the existing emit sites from the child engine's
resolved model via `Engine.Model()` / `Supervisor.MemberModel(name)`, set unconditionally,
and is bare metadata (a model id, never child content), gauntlet-#7 safe. When routed,
`model == routed_model`. mecatui renders `routed: …` when the router fired (not duplicated
as a `model:` line), else `model: <id>` for the plain case. The gRPC `RunTeam` direct path
emits no EvTeamStart roster, so the only roster projection site is the in-process Team tool.

The category→model→engine mapping stays in composition (`buildMemberEngine` substitutes the
routed model on the undefined branch; the new `buildParallelEngineFactory` mints the branch
engine) — both through the contamination-safe per-provider path (window/compactor/counter
re-derived), never a clone-and-swap. Fail-soft, decide-once, the breaker, and OFF-is-byte-
identical all hold per family.

## Prerequisites

- [The ports — the LLMProvider seam](ports.md)

## Follow-on reading

- [Context & compaction — per-model context window](context-and-compaction.md)
- [Observability & reliability — provider resilience](observability.md)

---

[← Architecture guide](../architecture.md)
