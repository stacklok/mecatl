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
  by `call_id`. An assistant turn expands (`assistantItems`) with its **reasoning
  items INTERLEAVED with its function_call items in the model's original emission
  order** (rs_a → fc_1 → rs_b → fc_2 …), then the text message. `session.Message`
  holds reasoning and tool calls in two fields with no relative order, so each
  packed reasoning entry records how many calls preceded it and the adapter
  rebuilds the sequence — the stateless-replay rule is to pass prior output items
  back untouched.
- `Store: false` plus `Include: [reasoning.encrypted_content]` so reasoning
  survives across turns statelessly. A turn may carry SEVERAL reasoning items,
  each with `encrypted_content` bound to its own `rs_…` item id, so the adapter
  packs the ordered `(id, blob)` list into the opaque `Message.Reasoning` STRING
  (`reasoning.go`) — the same envelope discipline as the anthropic adapter — and
  replays one input item per entry, each under its own id. Fusing the blobs
  under a single id is what the provider rejects as `invalid_encrypted_content`;
  see [ADR 0101](../adr/0101-openai-reasoning-multiplicity.md).
- A rejected replay (`invalid_encrypted_content`) is REPAIRED once per request,
  pre-commit only: `Stream` retries with `withoutEncryptedReasoning` — the
  reasoning envelopes removed, visible history, tool calls/results, phase markers
  and function-call item ids all kept. A failed repair is terminal for that
  Stream (`Retryable() == false`, honoured by `llmresilience`) so the outer
  wrapper cannot replay the pair and spend the repair twice.

**SSE → Chunk translation** (`stream.go`, `translate` — a pure function driven
directly from recorded fixtures by `decodeSSE` in tests):
- `response.output_text.delta` → `ChunkText`. Every non-empty visible delta is
  projected in serial SSE arrival order, even when item, output, or content
  identities differ. Those provider identities are deliberately discarded at the
  adapter boundary; the engine concatenates the chunks into the one
  `Message.Text` string without synthetic separators or text-part metadata
  ([ADR 0302](../adr/0302-openai-visible-text-delta-projection.md)).
- `response.reasoning_summary_text.delta` / `response.reasoning_text.delta` →
  `ChunkReasoning` (the DISPLAY summary)
- `response.output_item.done` (reasoning) → BUFFERED into `streamState.reasoning`
  as an `(item id, encrypted_content)` pair. The REPLAY blob is distinct from the
  display summary; the two must never be conflated.
- `response.output_item.done` (message) → `ChunkPhase` (the opaque phase
  marker, stored on `Message.ProviderPhase` and replayed verbatim on the
  assistant message item — issue #46)
- `response.output_item.done` (function_call) → `ChunkToolCall` (acts on the
  assembled `.done` payload, not concatenated deltas)
- `response.completed` → the packed `ChunkReasoningItem` (the turn's buffered
  reasoning items, flushed here because only now is the full ordered list known),
  then `ChunkUsage` and `ChunkDone(end_turn)` (cached tokens map into
  `Usage.CacheReadTokens`)
- `response.incomplete` → `ChunkUsage` then `ChunkDone(error)`
- `response.failed` / `error` → a non-nil stream **error** carrying the
  provider's in-band message verbatim; HTTP API rejections instead render only
  their structured `code` (or `type`) and message as `code: message`, never the
  SDK's raw response body, request URL, or correlation ID.

**Cancellation**: `Stream` (`openai.go`) selects on `ctx.Done()` each iteration
and abandons the underlying stream; a deliberate `ctx` cancel is **not** reported
as a stream error.

**Per-request session correlation.** The shared run-entry path binds the exact active
`session.SessionID` to the context passed through `LLMProvider.Stream`. The OpenAI
Responses, OpenAI Chat Completions, and Anthropic adapters project it as
`X-Mecatl-Session-ID` on each HTTP request. It is correlation-only: child/member/
auxiliary engines bind their own IDs, while compaction inherits the parent run's ID.
Provider clients never hold it globally, so concurrent sessions cannot cross-stamp.
An absent or Go-illegal HTTP field value omits the header without failing inference;
the value is otherwise byte-exact. It is not auth, tracing, idempotency, provider
state, safety/user identity, or a cache key. See
[ADR 0216](../adr/0216-provider-session-correlation-header.md).

**The provider-neutral seam**: the loop only ever sees `port.Chunk`; no OpenAI
type crosses the boundary. The fake `mockllm.Provider` (`engine/adapter/mockllm`,
`New(turns...)`, `TextTurn`) implements the same port for offline loop testing.

**Compatible endpoints**: `WithBaseURL(url)` overrides the host (vLLM, LiteLLM,
a local proxy); the SDK appends `/responses`. `WithAPIKey` and
`WithRequestOption` round out the options. Command roots map `--openai-base-url`
into the non-secret built-in endpoint override map that composition folds over
operator settings.

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

### Experimental `openai-codex` subscription provider

`openai-codex` is a distinct, credential-driven registry entry for a ChatGPT
Codex subscription. It is not public OpenAI API credit and does not reuse
`OPENAI_API_KEY`: the operator supplies an immutable access-token/account
snapshot through `auth.yaml`. The entry targets the undocumented private
`https://chatgpt.com/backend-api/codex` compatibility surface and is explicitly
experimental; OpenAI may change or withdraw that surface independently of the
supported public API.

The implementation is an adjunct, not a second Responses adapter.
`internal/adapter/openaicodex` owns credential validation, request-time expiry,
redirect refusal, the fixed `originator: mecatl`/account headers, error
normalization, and the private `/codex/models` envelope. Composition passes those
options to the same `provider/openai.Provider` constructor used by `openai` and
OpenRouter. The shared request builder, stateless full replay, successful SSE
translator, retry decorator, and provider-neutral `port.LLMRequest` therefore
remain unchanged.

The models endpoint is the account entitlement authority. Before the first
successful live response, unauthorized/unreachable discovery publishes no Codex
inventory and a successful empty response remains empty; the public OpenAI catalog
never invents subscription entitlements. After a success, the process-local
last-known-good list may remain visible across refresh failures, but inference
still reports the current credential/service error. Opaque entitled slugs are
preserved; embedded OpenAI metadata may only enrich a matching entitled slug.
The private endpoint treats `client_version` as a protocol-compatibility gate,
not merely client identity: malformed values return HTTP 400 and semver values
below its current compatibility floor return a successful empty inventory. The
lister therefore sends the live-probe-verified compatibility value `1.0.0`;
honest client identity remains in `originator: mecatl` and `User-Agent: mecatl`.
When no model is configured and Codex is the sole/default provider, a bounded
startup discovery selects the first entitled slug or fails honestly.

An explicit `(provider_id: openai-codex, model_id: ...)` selector is persisted
and rehydrates through the same provider. An empty selector retains the existing
floating semantics and follows the deployment default after restart. The manual
credential is read once at startup; every request rechecks that snapshot's expiry,
but there is no refresh, login, or auth-file writer. Replacing an expired/rejected
token requires restarting the process. The plaintext and same-UID threat boundary
is documented in the [operator setup](https://mecatl.dev/docs/building/deployment/settings#configure-provider-credentials).

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

### Operator-defined providers

Operator-local `settings.yaml` may declare first-class `providers.<id>` entries with an
HTTPS base URL, required default model, one wire flavor (`openai-responses`,
`openai-chat-completions`, or `anthropic-messages`), and either `auth.method: none`
or `api_key` (ADR 0238). `provider_overrides` changes only the base URLs of the
eligible built-ins; it does not turn a built-in into a custom provider. Custom IDs cannot
collide with any built-in or the reserved offline `mock` ID.

`app.Build` resolves the operator definition set once, passes that set to the injected
`ProviderCredentialLoader` once, then applies the returned immutable `ProviderCredentials`
before default selection and registry construction. Command roots project their non-secret
base-URL flags into `ProviderOverrides`; Build merges those over settings-derived overrides
before registry construction. The command roots inject the local `cliconfig` resolver but do
not construct custom-provider registry state; Build owns and closes any credential lifecycle
after later build failures and at normal shutdown. This keeps a
future refreshable credential implementation at the composition boundary without exposing it
to the engine or wire API. `mecatui connect` does not embed a server and performs no local
provider-credential I/O. Custom API keys remain file-only records keyed by provider ID; `none`
has no ambient credential fallback. The configured default model is
available synchronously even when live listing fails or returns no models. Live `/models`
requests use the declared flavor and resolved authentication, have bounded time/body
envelopes, and refuse redirects. Inference uses the same redirect-refusing transport:
a redirect never forwards a request body or credentials to its target. That transport
policy is retained when default or live-model capability reminting rebuilds an adapter.

**OpenRouter downstream-provider routing (issue #480, ADR 0210).** OpenRouter is a
*meta-provider* — one model id is served by several **downstream** inference
providers (Anthropic, Amazon Bedrock, Google Vertex, …) that OpenRouter
load-balances across on price. mecatl exposes both halves: **steering** via the
operator-tier-only `openrouter:` settings subtree (a per-model `order:` +
`allow_fallbacks:`, folded by `foldOperatorOpenRouter` into `Config.openRouterRoutes`),
and **observability** of which downstream actually served a turn. The openrouter
registry entry ALONE passes two `openai` adapter Options —
`WithOpenRouterProviderPreferences` (a model-keyed resolver) +
`WithOpenRouterMetadata(true)` —
over the shared `extra` channel, so every mint (default + per-session remint)
carries them and no other entry can leak the `provider` body key or the
`X-OpenRouter-Metadata` header. Both are stamped as PER-REQUEST options
(`option.WithJSONSet` for the body key), leaving `buildParams` and the byte-stable
prompt-cache prefix untouched. The routed downstream echoes back as
`port.ChunkProviderRoute` (parsed from the terminal `response.completed` raw JSON's
`openrouter_metadata`, fail-empty) → the client-visible `session.EvProviderRoute`
(`"provider.route"`), absent on a cache hit — never fabricated. See
[`docs/adr/0210-openrouter-downstream-provider-steering.md`](../adr/0210-openrouter-downstream-provider-steering.md).

**Intent-driven availability (issue #262, ADR 0064).** Every provider above is
**key-driven** — available iff a credential resolves. The ToolHive LLM gateway proxy
entry (`providerToolhive`, id `"toolhive"`) is **intent-driven** instead: it is
registered when `resolveToolhiveIntent` detects ToolHive's own config file (or an
explicit `--toolhive-llm-base-url`) — no credential required, and NEVER gated by
reachability (register-on-intent; a session persisting `provider_id: "toolhive"` must
survive a restart with the proxy down, never rejected as "unknown or unavailable
provider"). Each `providerEntry` carries an `intentDriven` bit that
`preferredDefaultProvider` reads to place intent-driven providers at an explicit
LOWEST-preference tier (any key-driven provider always wins the default).
`ListModels` `provider_status` projects operator-actionable live-listing outcomes
for intent-driven gateways, Codex entitlements, and operator-defined custom
providers (identified by their configured custom default model); it exposes only
safe provider ID/state/hint metadata, never an endpoint, credential, or raw
listing error/body. `intentDriven` alone controls the TUI's `org` tier and gateway
availability notices. A BOUNDED (≤1.5s) Build-time probe runs immediately after
registration and drives ONLY the startup diagnostic, the initial live-model snapshot,
and default-model eligibility for a SOLE intent-driven provider — never registration
itself. See `docs/adr/0064-toolhive-llm-gateway-provider.md` for the full design
(including the accepted sole+probe-down boot deviation) and
`internal/adapter/openaicompat` / `internal/adapter/toolhivellm` for the two-layer leaf
split (protocol-generic lister + the one ToolHive-aware config reader).

**DIRECT mode (issue #265, ADR 0102).** The gateway entry can also talk DIRECTLY to the
real `gateway_url` with no local proxy hop: mecatl imports ToolHive as a Go library (one
file, `internal/adapter/toolhivellm/tokensource.go`, the package's sole toolhive-importing
file alongside the stdlib-only `detect*.go`) and builds an in-process OIDC token source
— the SAME `llm.NewTokenSource` `thv llm token` uses — so the bearer is minted and
refreshed in-process. The token rides a custom `http.RoundTripper` inside the
`*http.Client` passed to `openai.WithHTTPClient` (`bearerRoundTripper` in
`internal/app/registry.go`), which strips the SDK's placeholder `Authorization` header
and sets `Bearer <real-token>` per request — mirroring the ToolHive proxy's own `Rewrite`.
The `WithHTTPClient` option rides every per-session/heal re-mint (the
`newOpenAICompatEntry` closure appends it to every `construct()` call), so the token
injection cannot drift off a re-minted adapter. A new `--toolhive-llm-mode
auto|proxy|direct` flag (default `auto`) drives the routing in
`resolveToolhiveIntent`: `auto` selects direct when the OIDC trio
(`gateway_url`+`issuer`+`client_id`) is configured AND the `gateway_url` is HTTPS
(`http://localhost`/`http://127.0.0.1` carve-out; a non-HTTPS gateway would send the
bearer over cleartext), else falls back to the loopback proxy with a WARN; `proxy`
forces the loopback path; `direct` forces the gateway path and Build-fails when OIDC
is absent. The direct base URL is derived (`gateway_url + "/v1"`), never hand-set. The
token never enters a log, an error string, or an env var (OS keyring; only its
reference is persisted; errors are sanitised via `llm.SanitizeTokenError`). `mecatui
llm login` runs the interactive OIDC flow in-process; a headless `mecated` cache-miss
surfaces an actionable error naming `thv llm setup` / `mecatui llm login` /
`--toolhive-llm-mode proxy`. `tls_skip_verify` is NOT honored in direct mode (upstream
gap) — a self-signed gateway must use `--toolhive-llm-mode proxy`. See
`docs/adr/0102-toolhive-direct-mode.md` for the full design.

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
image/reasoning flags, context_limit — never a key/env/base-URL). Composition stores
that projection in one atomic resolved inventory shared by `ListModels` and the
read-only `DiscoverModels` tool. Live refresh swaps that same inventory, so both views
retain the existing floor/last-known-good/empty semantics without a second lister or
probe. `DiscoverModels` exact-filters only `provider_id` and `model_id`, returns at most
50 complete provider/model handles (20 by default), and has a 32 KiB output ceiling.
The pair is the exact selection handle; a model id never implies its provider. The tool
is registered through the common catalog assembly, including no-FS sessions, and
receives no workspace or shell input. The `mock` provider advertises no selectable
models. `ServerCapabilities.model_selection` is true iff the inventory is non-empty or
a refresh source is available, gating the client's model picker. Provider key/base-URL
flags landed in `cmd/mecated` earlier; the picker UX is a client concern.

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
live in the server (`engineAndEnvironmentFor`): a registered per-session engine whose
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

**How it fires.** For a default delegation with no per-call `model`, `fork`, or `resume`,
the `Subagent` `run()` hook calls a composition-built classifier (`RunModelRouter`, role
`model-router`, tool-less, one turn, no-progress nudge disabled). A named `agent` remains
eligible when its definition declared no `model:` and composition can rebuild its scoped
engine on a same-provider routed model; a pinned, provider-switched, or inline-MCP definition
bypasses classification. The classifier reads the category descriptions in the clear
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

**Named agent-defs too (issue #286, [ADR 0066](../adr/0066-route-unpinned-and-writable-delegations.md),
extended for writable specialists by [ADR 0242](../adr/0242-route-unpinned-writable-named-specialists.md)).**
A delegation to a named `agent` that declared **no `model:`** (expressed no model intent) is
ROUTABLE in read-only or `mode:"read-write"`: the router classifies it and rebuilds the def's
SCOPED engine (its catalog/prompt/hooks) on the picked model. The writable path retains its
mutating specialist scope and MAIN runner, so it remains direct-write against the parent
workspace. ANY non-empty `def.Model` — `inherit`, a built-in alias, a concrete id — PINS the
def (routing skips it); **explicit `model: inherit` is how you opt a def OUT of routing**.
Composition excludes a def that switches provider or declares inline MCP from the routable
set, so the classifier is never spent for it. An unavailable routed target fails soft to the
ordinary specialist engine and reports `route-target-unavailable`; per-definition limits are
unchanged. Team members and Parallel branches are out of scope (unchanged).

**Writable delegations too (issue #285).** A `mode:"read-write"` delegation honours the
same axes: a per-call `model` (or the router pick) rebuilds the WRITABLE explorer on that
model via the writable engine factory (direct-write against the parent tree — no fork). A
writable call with an explicit `model` but no writable factory wired is a LOUD error (never
a silent inherit); a plain writable delegation whose routed pick would be discarded (factory
unwired) does not spend the classifier at all. A writable `resume` keeps its own engine.
An unpinned writable specialist (`agent`) is routed through its own scoped writable factory;
explicit `read-write`+`agent`+`model` remains invalid because an explicit override is a
different call shape, not a router decision.

**Fail-soft + breaker.** The router is **never load-bearing**. Any classifier failure,
cancellation, unparseable verdict, or unknown category keeps the engine that delegation would
otherwise use. An unresolvable routed target does the same and reports
`route-target-unavailable`; for a named writable delegation that is its ordinary writable
specialist, not the default explorer. A per-run circuit breaker (default 3
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
the gRPC + HTTP relays and rendered by mecatui (inline card + f6 fleet roster).

The structured miss/gate half of this observability surface is described below under the
per-delegation routing-reason surface ([ADR 0083](../adr/0083-routing-reason-on-delegation-start.md)).

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
and rendered by mecatui (the f6 Teams roster row and the Parallel group-focus branch
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

**The routing-reason surface (ADR 0083, issue #397).** The `model` field answers "what did
it run on" but not "why didn't the router fire". A bounded `routing_reason` now rides all
three delegation-start events — `Subagent` (field 18), `TeamMemberSpec` (field 8),
`Parallel` (field 25) — EMPTY on a routed hit, otherwise a bare-metadata gate/miss constant
(`session.RoutingReason*`: `router-disabled` / `pinned-model` / `agent-def-pinned-model` /
`resume` / `fork` / `route-target-unavailable` / `breaker-open` / `aborted`) or a static
classifier/composition miss code (including `empty-model`).
The three `maybeRoute*` gates attribute their own gate (an explicit
`model`/`fork`/`resume`/agent-def pin is named BEFORE the router-absent gate, so a pinned
delegation is never mislabeled `router-disabled`). Subagent composition supplies an explicit
pinned-name set: provider-switched and inline-MCP defs are ineligible but are not falsely
called model-pinned. If a routed factory declines its selected model, the routed fields are
cleared and `route-target-unavailable` records the fallback; `model` still names the engine
that actually ran. The dispatch closure synthesizes `breaker-open`/`aborted`/`empty-model`.
One chokepoint `routingReasonPayload` (whitespace-collapse +
200-rune cap + an event-safe allowlist) projects it at every emit site — the allowlist
confines the wire to static harness/classifier/composition codes, reducing known detailed
composition reasons to their code and substituting a generic
`routing-miss` for any non-allowlisted string an external `Deps.SubagentModelRouter`
composition returns (gauntlet #7), the verbatim text kept in the operator-diagnostics
channel. mecatui threads it through the client view-model and renders it as
` · not routed: <reason>` on the delegation's model line. `Supervisor.MemberRouting` widens
2→3 returns so the Team tool reads the reason back for the roster.

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
