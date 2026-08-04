# ADR 0016 — Multi-provider and multi-model

- Status: Accepted (P0/P1 shipped; P2/P3 deferred)
- Date: 2026
- Scope: the provider registry, per-session engine binding, DTO neutrality at the LLM port, capability single-source intersection, live model listing, and per-sub-agent provider selection
- Superseded by: [ADR 0071](./0071-seamless-model-switch.md) (in part — the "Mid-session model switch" confirm-overlay UX and the "Same-provider history carryover" gate; the rest of this ADR stands)

## Context

mecatl started with a single hardwired OpenAI provider. Adding Anthropic's native Messages API — a genuinely different wire shape — would contaminate the domain and agent loop if provider-private concerns leaked past the port. Capability truth (which models accept images, audio) also needed a single authoritative source to avoid the ACP gate and the picker diverging. Operators also needed to route specific agent definitions to cheaper or specialist models on different providers.

## Decision

All multi-provider wiring lives in the composition layer only; the domain, agent loop, and server adapters receive only a bare `port.LLMProvider` or a neutral value. Provider-private knobs (thinking budget, reasoning effort, store/include flags) are adapter-construction options, never `port.LLMRequest` fields — a reflection guard enforces this. Capability truth is a single composition-computed intersection of per-model catalog modalities and adapter transmit ability, feeding `ListModels`, the session echo, and the ACP gate from one value. P2 (disk cache, periodic refresh, Chat-Completions adapter) and P3 (secrets store, OAuth, per-client key custody) remain deferred.

## Consequences

Adding Anthropic required zero domain or port changes — only composition and cmd wiring — validating the abstraction. Per-session context-window resolution is a live-first closure, not a frozen scalar, so a post-build live-catalog swap self-corrects without an engine rebuild. Disclosure hardening ensures no API key or credentialed URL reaches any wire, log, or proto surface. Current behaviour is described in `docs/architecture.md`; shipped/deferred state is tracked in `docs/design/PRODUCTION-READINESS.md`.

---

mecatl serves more than one LLM provider in a single process and binds a **provider +
model per session**, speaking a provider-agnostic `port.LLMProvider` behind a
server-side registry. All of the multi-provider wiring lives in the composition layer
(`internal/app`); the domain, the agent loop, and the server/acp adapters never see a
registry or a catalog — they receive a bare `port.LLMProvider` or a neutral value.

This document is the design rationale. The runtime overview is `docs/architecture.md`
§18 (which cross-links here).

---

## 1. Purpose & phasing

- **P0 (shipped).** Registry + env-credential detection + embedded model catalog +
  availability-gated picker + a two-field provider/model selector + a per-session
  engine + a DTO-neutrality audit + capability single-source + disclosure hardening.
  Providers: **OpenAI** and **OpenRouter**, both on the SAME stateless Responses
  adapter (OpenRouter = the openai adapter with the OpenRouter base URL substituted).
- **P1 (SHIPPED).** A native **Anthropic Messages** adapter (`provider/anthropic`,
  on the official MIT `anthropic-sdk-go`) — the first provider with a genuinely different
  wire shape, which **VALIDATES the abstraction**. Per-model modality divergence (e.g. some
  Anthropic models take image, some do not) is a pure **data** change: a new registry entry
  whose adapter `Capabilities()` reports its real transmit ability (Image:true, Audio:false,
  EmbeddedContext:true), plus catalog rows whose `inputModalities` differ per model. The
  intersection formula (§7) is unchanged; `engineDepsForProvider`, per-session routing, the
  capability intersection, and per-sub-agent-provider switching ALL work for anthropic with
  **zero new code** (they treat it as data). **No domain/agent/server/acp/proto edit** — the
  only edits are composition (registry + Config + cmd key plumbing). The abstraction held;
  details in §4. Extended thinking is **ON and model-aware**; `max_tokens` is an adapter-
  construction default (catalog-derived per model); `cache_control` is a single ephemeral
  breakpoint at the `StablePrefix` boundary.
- **Live model listing (SHIPPED).** A one-shot **per-provider** live catalog fetch via an
  optional, composition-local `modelLister` capability (§11). OpenRouter is the first
  implementer: its public, **unauthenticated** `/models` endpoint enumerates the real
  catalog (336 models) and **replaces** the curated embedded subset for that provider on
  success; the embedded catalog is the **fallback floor** on any error/empty/timeout. The
  embedded snapshot is seeded synchronously at `Build` (the `ModelSelection` cap is honest
  from t=0) and the live set is swapped in by a background goroutine — `Build` never
  touches the network.
- **Live metadata → resolvers (SHIPPED).** The live model record now feeds the
  request-path RESOLVERS, not just the picker: a composition-owned `liveMetaStore`
  (seeded from the catalog at t=0, swapped by the SAME one-shot refresh) backs
  live-first-with-catalog-floor lookups for the output ceiling (`max_tokens`), the
  context window, and the Anthropic thinking matrix. The **Anthropic keyed lister**
  (`client.Models.List`) supplies all of these incl. the live thinking descriptor;
  the **OpenRouter** lister now also captures `top_provider.max_completion_tokens`.
  See §12.
- **P2.** Disk cache + **periodic/interval** live refresh; OpenAI lister (sparse —
  catalog-only, see §12); the per-session live-capability closer (so a session bound
  to a *live-only/uncatalogued* model resolves image from live modalities, not the
  adapter-only passthrough fallback). Plus a Chat-Completions adapter for the long
  tail (Gemini-native / Together / …).
- **P3.** Secrets store + OAuth + per-client/profile key custody (see §8).

---

## 2. The provider registry (`internal/app/registry.go`)

`buildProviderRegistry` constructs, ONCE at `Build`, the set of **available** providers.
A provider is available iff one of its credential env vars resolves; the var NAMES come
from the embedded models.dev catalog (`providercatalog`), with one composition-layer
augmentation: OpenRouter also accepts `OPENAI_API_KEY` by mecatl convention (it rides
the same Responses adapter). Only available providers are constructed and held — there
is no point holding an unkeyed provider, and its very availability is sensitive (§8,
CWE-200). The registry is composition-only; it is **not** a `port` (a single consumer),
mirroring the `envDetector` seam. `UseMock` short-circuits to a single synthetic `mock`
entry (offline); the zero-keys case is the named, actionable `errNoProvider`.

`buildProvider` returns the registry **and** its default provider, so the shared engine
and every child/fork/team/dream/reviewer engine keep receiving the single default
provider exactly as before — the default path is byte-identical to pre-multi-provider.

A composition-only `providerConstructor` seam (mirroring `envDetector`) lets the offline
e2e back two real provider ids with distinct mocks; production leaves it nil and uses
the resilience-wrapped openai adapter.

Because OpenRouter rides the SAME openai adapter, its function tools are sent
**non-strict** (`FunctionToolParam.Strict` left unset). This matters on
strict-enforcing OpenAI-compatible upstreams (e.g. Azure reached via OpenRouter): strict
mode would reject any tool schema whose `required` omits an optional property, and several
built-in tools have optional params. Non-strict avoids the upstream `400`; argument
validation happens at the execution edge (`session.ParseArgs` / `NewToolError`), so strict's
guarantee is not needed. See `IMPLEMENTATION-NOTES.md`.

---

## 3. Catalog-as-data (`internal/adapter/providercatalog`)

The model catalog is an embedded, pinned, hand-curated SUBSET of the community
[models.dev](https://models.dev/api.json) snapshot (MIT-licensed; the full license +
attribution is vendored in `MODELS_DEV_LICENSE`). It is a stdlib-only LEAF data adapter:
`embed` + `encoding/json` only, no domain/port/app/other-adapter imports. Its
`Catalog`/`Provider`/`Model` are package-own value types and must never leak into the
domain or port — only the composition layer reads them.

Curation is explicit and reviewable (no silent caps, no regex sweep): openai (all 50),
anthropic (all 25, made ready for P1), openrouter (a 27-route flagship allowlist). An
unknown provider/model id is an honest `(_, false)` lookup miss, never a substitution.
Regeneration is one deterministic `jq -S` filter (recorded in the package doc-comment)
so a re-pin diff shows only real model changes.

The embedded catalog is now the **FALLBACK FLOOR**, not the only source: a provider with
a live `modelLister` (OpenRouter — §11) has its real catalog fetched and **replaces** the
curated subset for that provider; on any fetch error/empty the embedded subset is shown
unchanged. So a keyed OpenRouter picker shows 336 live models, but offline (or on an
upstream blip) it still shows the curated 27. SSRF hardening for the live fetch shipped
with the lister (fixed-host const URL, keyless, size cap); the SSRF/async-refresh items
formerly parked at P2 are partly delivered (one-shot OpenRouter refresh) — periodic/disk
refresh remains P2.

---

## 4. DTO-neutrality principle

The uniform internal format is `port.LLMRequest` at the `port.LLMProvider` seam. It is
deliberately provider-NEUTRAL and guarded:

- `Model` is a **bare opaque string** — no provider/endpoint/key rides the request;
  those are server-side registry concerns.
- Provider-PRIVATE knobs (OpenAI store/include flags, an Anthropic thinking budget, a
  reasoning effort) are an **adapter-construction** concern — a `WithThinkingBudget`-style
  Option like the existing `openai.WithBaseURL`, NOT a new `LLMRequest` field. The
  domain/agent loop never branches on provider.
- Reasoning replay is uniform in STRUCTURE (one opaque blob per message via
  `ChunkReasoningItem`), provider-private in CONTENTS (OpenAI `encrypted_content`,
  Anthropic `(thinking,signature)`). The display summary (`ChunkReasoning`) vs replay
  blob split is the neutral seam P1 validates — do not collapse it.
- A reflection guard (`engine/port/llm_neutral_test.go`) tripwires any silent
  `LLMRequest` field addition.

### P1 verdict: the abstraction HELD (`Message.Reasoning` stayed a `string`)

The native Anthropic adapter shipped with **no domain/port/agent change**. The two genuine
wire-divergences P1 surfaced were both absorbed at adapter-construction, not in the DTO:

- **`max_tokens` (required by Anthropic, absent everywhere in the harness)** — resolved
  **per request model**, not baked in: composition injects a `WithMaxTokensResolver(func(model) int)`
  built from the catalog's per-model output limit (`anthropicOutputLimit`), and `buildParams`
  computes `max_tokens` from `req.Model` via it, with a conservative flat fallback
  (`defaultMaxTokens`=4096, the lowest common Claude ceiling) for an uncatalogued model so it
  never 400s. CRITICAL for per-session/sub-agent routing: a route to a smaller-ceiling model
  (e.g. `claude-3-5-haiku`=8192) must not send the default model's larger ceiling. NOT an
  `LLMRequest` field; the adapter stays catalog-free. (The resolver is now LIVE-FIRST
  with the catalog as the floor — §12.)
- **Extended thinking is model-class-dependent — THREE outcomes** (`thinkingConfigFor`):
  `{type:"adaptive"}` for Opus 4.8/4.7/4.6 + Sonnet 4.6 + Mythos (a manual
  `{type:"enabled",budget_tokens}` **400s** on Opus 4.8/4.7); `{type:"enabled",budget_tokens:N}`
  for older thinking-CAPABLE families (Claude 4: Sonnet 4.5/4, Opus 4.5/4.1/4, Haiku 4.5; and
  Claude 3.7 Sonnet); and **NONE — omit `thinking` entirely** for thinking-INCAPABLE models
  (Claude 3.5 and earlier — sending `{type:"enabled"}` there 400s). `display:"summarized"` is
  set explicitly so display deltas stream. The budget is the `WithThinkingBudget` Option
  (clamped ≥1024 & <max_tokens). Thinking is **ON**. The mode is now LIVE-FIRST via
  `WithThinkingResolver` (Anthropic's `Capabilities.Thinking.Types`), with these
  prefix lists kept as the OFFLINE FLOOR — §12.
- **Ambient-env custody** — `New` passes `option.WithoutEnvironmentDefaults()` FIRST, so the
  SDK does NOT autoload `ANTHROPIC_BASE_URL`/`ANTHROPIC_AUTH_TOKEN`/WIF profiles; the adapter
  contributes only the harness-resolved key + optional `--anthropic-base-url`, preserving the
  single-knob credential/base-URL custody and the availability gate.
- **Tool `input_schema` fidelity** — the WHOLE tool schema passes through
  (properties/required on their typed fields, every other top-level key via
  `ToolInputSchemaParam.ExtraFields`), so `$defs`/`additionalProperties`/nested enums/top-level
  constraints survive on Anthropic exactly as on OpenAI.
- **Stream-accumulation cap** — the per-block tool-args / thinking-text / signature buffers
  are bounded (8 MiB) and fail the stream when exceeded (MITM/DoS hardening, matching the
  openrouter lister's caps).

- **Reasoning replay packed into the opaque string.** Anthropic's replay unit is a *list*
  of `thinking` blocks `{thinking,signature}` plus possibly `redacted_thinking` `{data}`
  (interleaved thinking can produce several per turn, and redacted blocks must round-trip
  too). A single bare string cannot hold a list directly, but the `Message.Reasoning`
  contract is explicitly "opaque blob, adapter packs/unpacks its own wire shape", so the
  adapter packs the ordered list **into** the string as a versioned JSON envelope and
  unpacks it to reconstruct the thinking/redacted blocks **before** the `tool_use` blocks
  on the next turn (omitting/misordering them on a tool-bearing assistant turn 400s — the
  load-bearing correctness item). The envelope:

  ```json
  {"v":1,"blocks":[
     {"t":"thinking","x":"<thinking text>","s":"<signature>"},
     {"t":"redacted","d":"<opaque redacted data>"}
  ]}
  ```

  Order in `blocks[]` = the SSE `index` order = the model's original emission order, so the
  thinking-sequence rule is preserved by construction. An empty list packs to `""` (replay
  no-op, exactly like the openai empty-blob case). The adapter emits ONE `ChunkReasoningItem`
  carrying the packed envelope at `message_stop` (honoring "one opaque blob per message"
  even with multiple blocks); the display deltas stream separately as `ChunkReasoning`.
  **`session.Message.Reasoning` stayed a bare `string`** — no domain widening, no leak.

---

## 5. Provider/model selection primitive

The wire surface is **two proto fields** — `provider_id` + `model_id` on
`CreateSessionRequest` — NOT a slash-joined string (an opaque pair avoids a parsing/
escaping seam). At the server boundary they map to the neutral
`server.ProviderSelector` value object; the server adapter imports neither the registry
nor the catalog. The composition root resolves the selector. Resolution table:

| `provider_id` | `model_id` | Outcome |
|---|---|---|
| `""` | `""` | **Shared engine** (default provider, no per-session build) — byte-identical to today |
| `""` | set | **InvalidArgument** — a bare model on the env-derived default provider is ambiguous |
| known+available | `""` | per-session engine on that provider's default model |
| known+available | catalogued | per-session engine bound to (provider, model) |
| known+available | NOT catalogued | **passthrough** — the model string reaches the provider verbatim (catalog gates nothing) |
| unknown/unavailable | any | **InvalidArgument** — `"unknown or unavailable provider"`, never a silent fallback |

The provider is **fixed for the session lifetime** (reasoning-replay + the byte-stable
cache prefix are provider-private; "switch provider" = a new session).

**Server-configured deployment default — shipped (issue #21):** `--default-provider` +
`--default-model` (mecated, and mecatui's embedded server) set a deployment-wide default
shared by every client — the SAME two-field `provider_id`+`model_id` grammar as the wire
selector, never a slash-joined string. `--default-provider` overrides the built-in
provider preference (openai-first) when that provider is available; `--default-model` is
the default model for the resolved default provider (set without `--default-provider` it
applies to the preferred provider). Both are validated **FAIL-FAST at Build**
(`validateDefaultModel`, no-op under `--mock`): an unknown/unavailable provider or a
model not catalogued for the default provider refuses startup — deliberately STRICTER
than per-session selectors (which allow passthrough), because a deployment default must
be known-good. The tier slots BELOW client-side defaults (a selector still wins) and
ABOVE the per-provider builtin (`resolveDefaultModel` step 2; see the effective-model
precedence in §9). Still deferred: client last-used persistence (S4, client-side).

---

## 6. Per-session engine + `engineDepsForProvider`

The widened `SessionEngineFactory func(ctx, sel, specs) (SessionEngineResult, error)`
is the ONE seam for a per-session engine — it serves BOTH a non-default provider/model
AND client-provided streaming-HTTP MCP servers (orthogonal inputs → ONE engine over ONE
catalog). The composition factory resolves the selector against the registry and builds
Deps via **`engineDepsForProvider`**, which re-derives EVERY provider/model-closing
field — LLM, Compactor, Model, model-keyed TokenCounter, `PromptConfig.Env.Model`, and
the **`Deps.ContextWindow` resolver** (a `func() int` built by `reg.windowResolver` —
override→live→catalog→128k floor — and read live at the point of use, so the compaction
trigger AGREES with the `ListModels`-advertised `context_limit` and self-corrects after
a live-catalog swap with no rebuild; only a genuinely uncatalogued passthrough model
falls back to the 128k default). The DEFAULT model resolves through the SAME resolver
(`baseEngineDeps`, issue #63) — it is no longer pinned to the 128k floor. This is the cross-provider contamination guard: a shallow clone swapping only the
LLM would compact and count through the wrong model.

**Per-session catalog = the FULL shared-catalog formula, assembled by the SAME
`assembleCatalog`** (`internal/app/catalog.go`, issue #42): core + server-global MCP
(+ the `MCPResourceTools` meta-tools) + client MCP + Subagent/InspectSubagent/
SubagentStatus + Parallel + Team/InspectMember + the six memory/user-model tools +
Skill/SkillDraft. The factory does NOT build its own registration list — it calls
`assembleCatalog` over the process-wide `catalogAssets` Phase A (`buildCatalog`)
produced once: the shared global MCP manager, the flocked memory/user-model stores
(threaded, never re-opened), the resolved skills, and the one process-wide
preserved-fork LRU. The only sanctioned per-session deltas are the client MCP tools
and the unwrapped hooks (`maybeWrapUserModelReview` is main-engine-only); guarded by
`TestPerSessionCatalogMatchesSharedCatalog`. The server-global MCP tools
(`cfg.MCPServers` + ToolHive — the same tools the main engine gets) are mounted by
reusing the **shared** manager Build already connected (`mainMgr.Tools()`, carried on
`catalogAssets.globalMgr`). This closes the selector-strips-MCP bug: before the fix,
selecting any non-default provider/model (what the mecatui `/models` picker always
does) silently dropped every server-global MCP tool (github/slack/fetch/…); issue #42
then closed the SAME drift class for memory/Parallel/skills/resource-tools. Mount
order is **core → global MCP → client MCP → Subagent trio → Parallel → Team → memory →
skills**, which gives **global-wins** collision precedence:
`mcp.Register` is **first-wins + skip-and-continue** — a client tool whose namespaced
name collides with an already-registered global one is SKIPPED (the global tool stays),
and **every other non-colliding client tool is still registered**. This is the
strictly-robust behaviour: a return-on-first-duplicate would have silently dropped every
client tool ordered *after* the collider, and also mis-handled two global servers
clashing. `Register` returns the **skipped tool names** alongside the joined error, so
each mount site emits ONE **provenance-bearing** WARN naming exactly which tools were
dropped and who won — distinct per tier: the per-session client mount says the tool(s)
were *shadowed by an existing server-global tool of the same name (the global tool
wins)* (the line an end-user reads to self-diagnose a vanished tool); the global mounts
(per-session and build-time — the one `assembleCatalog` mount) say a *server advertised a name already
registered* (a defective-server / within-global condition). These are composition-layer
logs, so the loop's "exactly two diagnostics lines" invariant does not apply.
**Lifecycle isolation:** `globalMgr` is owned by `Build`; it is reused, never
reconnected, and its `Close` is **NEVER** folded into the per-session
`SessionEngineResult.Close` — a per-session `CloseSession` tearing down the shared
manager would kill MCP for every other live session. `globalMgr` is also the
`reference:`-resolution mainMgr for per-session Subagent/Team subagent defs (falling back to
the client mgr only when there is no global manager), so a selector session's
`reference: <name>` resolves against the server-global servers — parity with the
build-time path.

`session.Session` is NOT widened — the selector resolves to an ENGINE at create time,
registered in the same `sessionEngines` map (and selected the same way by
`StartRunContent`) the client-MCP path uses. The map is **capped** at
`Config.MaxSessionEngines` (default 1024): the gRPC/HTTP surfaces have no
connection-teardown drain, so without a cap a client creating selector sessions and
never calling `CloseSession`/`EndSession` could grow it unbounded (CWE-770). Past the
cap, `createSession` returns `ErrTooManySessionEngines` (gRPC `ResourceExhausted` /
HTTP 429); `CloseSession`/`EndSession` frees a slot.

`SessionEngineResult` (a struct, not a tuple) carries the built engine, the per-session
resolved **capabilities** (§7), and the MCP close func. The struct keeps the two
interface-typed members readable and leaves room for future per-session metadata
without another signature churn.

---

## 7. Capability single-source / intersection (`internal/app/capability.go`)

**The defect this fixes.** Capability truth previously came from two disconnected reads:
`ModelInfo.image` was the catalog ALONE (the wired adapter's transmit ability was never
consulted), and `Service.capabilities()` / `Service.ProviderCapabilities()` both read
the SHARED engine's provider — so a session bound to a DIFFERENT provider/model via the
per-session factory got capability bits describing the wrong engine, and the ACP gate
had the same blind spot.

**The fix.** `modelCapability(reg, providerID, modelID) port.ProviderCapabilities` is
the SINGLE SOURCE: the INTERSECTION of the catalog's per-model modalities and the wired
adapter's `Capabilities()`. The adapter is the AUTHORITY on what it can actually
TRANSMIT; the catalog is the authority on what the model ACCEPTS; the AND of the two is
the honest truth. It lives in composition (`internal/app`) — the only layer holding both
inputs. The result is a NEUTRAL `port.ProviderCapabilities`; neither the catalog nor the
registry type crosses into the server/acp adapters.

```
                       internal/app  (HAS catalog + registry adapters + live meta store)
  reg.meta.modalitiesFor ─► modelCapability(reg, providerID, modelID)
   (LIVE, openrouter)        = ProviderCapabilities{
  catalog.Model ──────►          Image: adapter.Image AND (LIVE-image ELSE catalog-image),
   .InputModalities (floor)      Audio: adapter.Audio AND (LIVE-audio ELSE catalog-audio), (P0 ⇒ false)
  reg.Lookup(pid)                EmbeddedContext: adapter.EmbeddedContext }
   .provider                  (same live InputModalities the picker reads via projectModelEntry)
   .Capabilities() ───►       
                                       │ NEUTRAL values only — no catalog/registry type crosses
                ┌──────────────────────┼───────────────────────────────┐
                ▼                      ▼                               ▼
   (a) ModelInfo.image       (b) CreateSessionResponse           (c) ACP gate
       (ListModels)              .session_capabilities (echo)        Service.ProviderCapabilities()
                                  for the RESOLVED provider+model     = DefaultCapabilities (default)
```

**Modality input is LIVE-FIRST, not catalog-only.** A provider with a live lister
(OpenRouter) stores authoritative per-model `input_modalities` in the `liveMetaStore`
(`modalitiesFor`), and `modelCapability` reads them FIRST: `Image = adapter.Image AND
hasImageModality(live)` (and audio likewise). This is the SAME live
`modelEntry.InputModalities` the picker reads via `projectModelEntry`, so the session
echo / ACP gate and the picker derive image from ONE source and cannot disagree — the
single-source guarantee now spans the live path, not just the embedded catalog. It fixes
the bug where an OpenRouter TEXT-ONLY model (e.g. `openai/gpt-4`) reported `image:true` in
the session echo: OpenRouter shares the openai adapter (`Capabilities()` Image:true) and
nothing read the model's live `["text"]` modalities, so the permissive passthrough default
leaked image. The effect is OpenRouter-scoped: only providers WITH a lister get honest
live gating; openai-direct/anthropic semantics are unchanged.

Precedence (per modality field): (1) LIVE modalities (when the meta store HAS the model —
PRESENCE-keyed: a present entry wins even with an EMPTY modality list, treated text-only
exactly as the picker does, so present-but-empty cannot diverge into echo=true via a
catalogued image row) → (2) embedded CATALOG floor (catalogued model, no live entry) →
(3) ADAPTER-ONLY passthrough (uncatalogued + no live entry). Fail-safe rules (all toward text-only): an
unknown/unavailable provider (or nil registry) ⇒ zero value (a provider we cannot reach
transmits nothing); an uncatalogued or empty `model_id` with NO live entry ⇒ ADAPTER-ONLY
caps (a passthrough model trusts the adapter when both catalog and live are silent —
zeroing would strip image from every passthrough model, and the live-absent permissive
fallback is deliberately preserved).

**The three sinks share one value:**
- (a) `modelSnapshot` sets `ModelInfo.Image = modelCapability(...).Image`.
- (b) the per-session factory computes `modelCapability` for the resolved
  (provider, model) and returns it on `SessionEngineResult.Capabilities`; the Service
  stores it on `sessionEngine.caps` and echoes it verbatim on the new proto
  `SessionCapabilities { bool image = 1; bool audio = 2; }`
  (`CreateSessionResponse.session_capabilities`). For the zero-selector shared-engine
  path (which never calls the factory) the echo reads `Config.DefaultCapabilities` —
  the composition-computed intersection for the default provider + `cfg.Model`.
- (c) `Service.ProviderCapabilities()` returns the SAME `Config.DefaultCapabilities`,
  so the ACP gate and the wire echo for a default-engine session cannot disagree.

**A dedicated `SessionCapabilities` message** (not a reuse of `ServerCapabilities`, not
two bare bools) keeps the per-session surface MINIMAL: only the model-varying input axis
(image/audio) belongs per session; the other ~10 `ServerCapabilities` bits
(mcp/skills/teams/…) are server-wide and would be misleading or duplicated per session.
The field is additive — an older server leaves it absent and the client falls back to
`capabilities`.

**Reasoning is intentionally EXCLUDED from the echo and the intersection.** It is a
`ModelInfo` field (catalog-sourced), not a `port.ProviderCapabilities` bit, and there is
no adapter "can replay reasoning" authority bit in P0. If P1 wants reasoning intersected,
add the adapter bit then — do not invent it speculatively.

### Effective-model echo (`resolved_model`) — same single-source shape

The client cannot show the model a fresh session resolved to, because the request it
sent carries no usable id: `model_id` is **empty** for a default session and **ambiguous
passthrough** for a non-catalogued selector. The server therefore echoes the EFFECTIVE
(resolved) model — the SAME composition-computed-single-source pattern as
`session_capabilities`, NOT a second mechanism:

- proto: `ResolvedModel { string provider_id = 1; string model_id = 2; int64
  context_window = 3; }`, echoed on `CreateSessionResponse.resolved_model` (field 4) and
  mirrored in the `Session` snapshot (`resolved_model`, field 9). Additive — nil/absent
  from an older server ⇒ the client falls back to today's behavior (no model segment in
  the header).
- composition (`internal/app/build.go`): the per-session factory returns the resolved
  `ProviderID`/`ModelID` IDENTITY on `SessionEngineResult` from the SAME locals that
  built the engine (`resolvedProviderID`/`resolvedModel`) — never recomputed; the
  context WINDOW is no longer a frozen result field. The zero-selector default's
  effective model is `Config.DefaultResolvedModel` (the registry default provider + the
  resolved `cfg.Model`), set once in `Build` next to `DefaultCapabilities`.
- server (`internal/adapter/server`): the IDENTITY is stored on
  `sessionEngine.{providerID,modelID}`; `Service.ResolvedModel(id)` mirrors
  `Service.SessionCapabilities(id)` (per-session identity when registered, else
  `Config.DefaultResolvedModel`). Echoed in `grpc.go` CreateSession + `toProtoSession`
  and in `http.go`'s JSON response — always from `Service.ResolvedModel(id)`, NEVER read
  back off `req.GetModelId()`. The provider/model IDENTITY is verbatim; the
  `ContextWindow` SCALAR is resolved **live-first at call time for BOTH branches** via
  the injected `Config.ResolveContextWindow` (issue #66). That closure is the **ECHO**
  resolver (`reg.echoWindowResolver`), NOT the engine resolver: it shares the
  **override → live → catalog** precedence *core* (`resolveWindowCore`) with the engine's
  `reg.windowResolver` — so any override / catalogued / live window agrees byte-for-byte —
  but differs in **one deliberate branch**. For a model the live listing has but the
  curated catalog lacks (e.g. OpenRouter `openai/gpt-5.5`), while the one-shot live refresh
  is still in flight the echo returns a **deliberate PROVISIONAL 0** (the `liveMetaStore`'s
  `refreshCompleted()` is still false and the model is not known at a real value). The
  echo can honestly say "live window not in yet"; the engine cannot (it would run on a 0
  window), so this is the only place the two resolvers part. The client treats a `0` as
  "refetch on turn-end" (the footer-heal gate — see `cmd/mecatui/ui/update.go`), so a
  session created in the sub-second window before the swap lands self-heals to the real
  live window on the next `GetSession`. The resolver still floors to the catalog (never
  LOWERING a catalogued window) and honours the operator `--context-window-override`
  (which wins for both echo and engine).
  **Provisional 0 vs unwired (nil):** a `0` from the wired echo resolver is the honest
  "not in yet" signal — DISTINCT from `Config.ResolveContextWindow == nil` (the
  memstore/driver paths), which means no window scalar at all (the identity-only
  `ResolvedModel`).
  **No-network boundedness (floors-and-stops):** once the refresh SETTLES —
  `markRefreshCompleted()` fires on every settle path (sync swap, async success, async
  fetch-fail/empty fallback to the embedded floor, AND the no-lister no-op), but NEVER on
  a shutdown-cancel — an uncatalogued model that never got a live entry (e.g. a fully
  offline deployment whose fetch failed) floors to **128k** instead of a perpetual
  provisional 0. The footer-heal gate then closes (the client stops refetching). No
  push/event is added — the next snapshot read is honest.

#### The ENGINE-window half: resolve-at-use (issue #66, unification)

The echo resolver above fixes only what `GetSession` *reports*. The shared (default)
engine is built once at `Build`, PRE-swap, and never rebuilds — so a frozen
construction-time window would keep *compacting* a **live-only** default model (in the
live listing, absent from the curated catalog — e.g. OpenRouter `openai/gpt-5.5`) at
the ~128k floor even after the swap, while the echo reports the real ~1M window. Echo
and engine would **diverge**.

The fix is structural: `Deps.ContextWindow` is a `func() int` resolver, NOT a frozen
scalar, resolved at the **point of use** (every `Engine.maybeCompact` /
`Engine.ContextWindow`). Composition builds it via `reg.windowResolver(cfg, provider,
model)` — the ONE place the **override → live → catalog → 128k-floor** precedence lives
— and threads it onto EVERY engine: the shared engine (`baseEngineDeps`), per-session
selector engines (`sessionEngineFactory`), and child engines
(`childEngineDepsForProvider`). Because the resolver reads `reg.meta.contextWindowFor`
live on each call, a post-`Build` live `Swap` self-corrects the SAME engine on the next
turn — **no rehydration, no engine rebuild**. The echo's `Config.ResolveContextWindow`
is the SIBLING `reg.echoWindowResolver` wrapped to `int64`: it shares the
`resolveWindowCore` precedence with `windowResolver` (so echo and engine are
byte-identical for every override / catalogued / live window) and differs ONLY in the
terminal unknown branch — the echo may return a provisional `0` while the live refresh is
in flight, where the engine always floors to 128k (it can never see 0). The ENGINE never
reads `echoWindowResolver`. (`engine/agent` imports no adapter — the closure is a stdlib
`func() int` built only in `internal/app`; the layering DAG + depguard stay green.)

**No rehydration trigger:** the context window is no longer a reason to rehydrate a
default session (the old `defaultSessionNeedsLiveWindow` predicate is gone). A default
FS session keeps riding the shared engine, which self-corrects at use.

**Pre-swap race (accepted, eventually-consistent — mirrors the echo):** if the first
prompt arrives before the live swap, the resolver returns the 128k floor ⇒ that ONE run
compacts at the floor; the next post-swap turn reads the live window through the same
resolver. No new resource, no rehydration trigger; `decision = derive` (nothing new
persisted) — see `docs/adr/0027-cloud-native.md`.
- client/ui (`cmd/mecatui`): a proto-free `client.ResolvedModel` (sibling of
  `client.Capabilities`) + `resolvedModelFrom` mapper (nil ⇒ zero), threaded out of the
  `CreateSession` wrapper and stored on `Model.effectiveModel`. The header shows the
  effective model id from turn zero (no segment while connecting); it resolves a human
  display name from the already-held `ListModels` inventory by `(provider_id, model_id)`
  — no `display_name` is added to the proto. The header only CHOOSES which KNOWN string
  to display; it never resolves a default itself.

### Right-sized OUT of P0 (deferred to P1)

- **Per-session ACP capability gate.** ACP carries NO per-session provider/model
  selector in P0 (`session/new` passes only `mcpServers`, never a selector), so every
  ACP session rides the DEFAULT engine and the Agent's capture-once
  `a.caps = svc.ProviderCapabilities()` is correct for every ACP session — provided (as
  now) `ProviderCapabilities()` returns the intersected default caps. The gate moves from
  capture-once to per-session lookup only when an ACP selector lands (P1+). Plumbing a
  per-session ACP gate now would be speculative work with no P0 caller.
- **Reasoning-capability intersection** — see above.
- **Audio catalog modality** — the catalog has no explicit audio field today; `Audio`
  derives from the raw input-modality list (false in P0 data) AND-ed with the adapter
  (P0 adapter `Audio:false`), so the result is false regardless. No catalog schema
  change in P0.

---

## 8. Disclosure posture & per-client key custody (CWE-200)

API keys are **operator-supplied, server-side, env-only** for Phase 0. A key is read
once at startup by the composition root (`internal/app`), held only inside the
resilience-wrapped `port.LLMProvider` in the server-side provider registry, and is NEVER
placed on any wire, in any proto message, log line, or client-visible field. A remote
client selects a provider by an **opaque `provider_id`** and a model by an **opaque
`model_id`**; it receives back only public catalog metadata + boolean capabilities. A
provider with no resolved credential is omitted from `ListModels` entirely — its very
availability is concealed (CWE-200). There is no per-client key: all authenticated
clients share the operator's server-side keys; tenant isolation and per-client /
secret-store / OAuth key custody are **P3**.

### Auth-gating guarantee

`ListModels` is behind auth on BOTH surfaces: the gRPC `UnaryInterceptor` wraps ALL
handlers method-agnostically, and the HTTP `auth.Middleware` wraps the whole mux. An
explicit test (`TestListModelsRequiresAuth`, gRPC + HTTP) pins this against a future
handler that might bypass the interceptor — an unauthenticated `ListModels` would
otherwise leak the available-provider set.

### Log-audit checklist (every new/changed slog line on the multi-provider path)

| Line | Logs | Verdict |
|---|---|---|
| `registry.go` mock warn | a fixed string | clean (no key/URL) |
| `registry.go` "LLM provider available" | `provider` (id), `model`, `base_url` | clean — base URL is a plain host (OpenRouter `https://openrouter.ai/api/v1`; OpenAI operator-set), never a userinfo/`?key=` credentialed form |
| `registry.go` "LLM resilience enabled" | knobs (attempts/timeouts) only | clean |
| `build.go` client-MCP warns/info | `server`/`url`/counts/`err` | clean — the URL is the client-supplied MCP server URL, not an LLM-provider credentialed URL |
| `grpc.go` / `http.go` CreateSession echo | nothing logged | clean — `session_capabilities` is bools-only and unlogged |
| `grpc.go` / `http.go` `resolved_model` echo | nothing logged | clean — `provider_id`/`model_id`/`context_window` are all PUBLIC catalog/registry values (the same opaque ids `ListModels` already discloses), no key/URL, and unlogged |

`TestNoKeyInStartupLogs` captures the slog output of a real `buildProviderRegistry` with
a sentinel key and asserts the key appears in no line. `TestModelSnapshotNoSecrets`,
`TestSessionCapabilitiesNoSecrets`, and the multi-provider e2e tripwire the sentinel
across `ListModels` and the `CreateSessionResponse` (incl. the capability echo). Verdict:
**clean** — no key or credentialed URL on any wire, proto, or log surface.

---

## 9. Phasing recap + open/deferred

- **P0 — shipped:** registry, env-detect, embedded catalog, availability-gated picker,
  two-field selector, per-session engine, DTO audit, capability single-source/
  intersection, disclosure hardening. OpenAI + OpenRouter.
- **P1:** native Anthropic Messages adapter (validates the abstraction); per-model
  modality divergence is a data change; per-session ACP gate + (if wanted) reasoning
  intersection land here.
- **Live model listing — shipped:** the optional `modelLister` capability + the OpenRouter
  lister adapter; one-shot background refresh, live-replaces-embedded merge, embedded
  fallback floor, atomic snapshot swap (§11). The SSRF hardening + one-shot refresh moved
  OUT of P2 (they ship here).
- **P2:** disk cache + periodic/interval live refresh; OpenAI/Anthropic listers; the
  per-session live-capability closer; Chat-Completions adapter.
- **P3:** secrets store + OAuth + per-client/profile key custody + tenant isolation.

Model-selection persistence (mecatui client state, `models.yaml`) is a per-workspace
map (realpath-keyed) **plus** a global `default:` block. A normal pick writes only the
per-workspace entry; the global `default` is written ONLY by the explicit **`ctrl+g`
"set as global default"** affordance in the `/models` picker (`SaveGlobalDefault`,
read-modify-write preserving the workspaces map). **Effective-model precedence**
(highest → lowest), as the client resolves what the next `CreateSession` requests and
labels the picker's `current: …(<provenance>)` line: **in-session restart pick →
`--model` flag → per-workspace default → client global default → server-configured
default (`--default-provider`/`--default-model`, issue #21) → server built-in
default.** An unseen repo with no per-workspace entry falls back to the global default
(then the server's configured-or-builtin default), never inheriting another repo's
per-workspace pick. One DELIBERATE edge: the server-configured `--default-model`
applies only to ZERO-selector sessions — a client selector naming a provider (even
the same one) with an empty `model_id` gets that provider's adapter/endpoint default
per the §5 resolution table, NOT the configured default model (a client preference,
even a partial one, wins over the deployment default).

**Mid-session model switch — shipped (client-side restart):** the `/models` picker's
`enter` confirm offers "start a new session now" (a `CloseSession` of the old session
+ a fresh `CreateSession` on the picked model, with a clean transcript) or "switch
next time" (the pending-next selection applied on the next create). Provider stays
FIXED per session (the switch is a NEW session, never a live re-route). The header
`next:` badge previews a pending-next that differs from the live model.

**Same-provider history carryover — shipped (issue #20):** the `/models` picker also
offers `[c]` **carry-over** when the cursor model shares the live session's provider
(`liveProviderID()` in `cmd/mecatui/ui/models.go`).  The client calls
`CreateSessionWithCarryover` (`cmd/mecatui/client/client.go`) which sets
`source_session_id` on the `CreateSessionRequest`.  The server-side
`validateCarryover` (`internal/adapter/server/service.go`) snapshots the source
session's conversation via `session.ForkSnapshot` and seeds it into the new session
with `SeedHistory` before the first save — zero `engine/` or domain change.  The
gate is same-provider only (cross-provider → `InvalidArgument`).  Model is
intentionally NOT compared — v1 carries the conversation onto a different model
within the same provider.  Cross-provider strip (`session.StripReasoning`) is the
deferred v2.

Open / deferred items: cross-provider context carryover (requires replay-blob
stripping — v2), and a zero-keys first-run UX.  (The `small_model` tier — the
sub-agent cheap model — is SHIPPED as `Config.SubagentModel` / `--subagent-model`;
see the "Def-less child default model" subsection under §10.)

## 10. Per-sub-agent provider selection (SHIPPED — both halves)

A Subagent agent definition and a team member may resolve to a DIFFERENT provider (+
model) than the parent, routed through the same `providerRegistry` — without leaking
the registry past the composition layer (`internal/app`). Both halves are SHIPPED:

- **Half A — def-pinned provider.** A new `provider:` frontmatter field on an agent
  def (pure data on `agents.AgentDef.Provider`; the adapter never imports the
  registry) routes that def's child engine to the named provider. It is orthogonal
  to `model:` (two fields, mirroring the wire's `provider_id`/`model_id` — never a
  slash-joined string).
- **Half B — session-provider propagation.** A session that SELECTED provider P over
  the Phase-0 wire now gets a per-session Subagent tool (and, under `--enable-teams`, an
  in-catalog Team tool) wired to P as the inherited parent — so its sub-agents that
  pin NO provider inherit P, not the build-time default. The build-time per-session
  catalog was core-tools-only, so a selected session previously could not spawn
  sub-agents at all; Half B closes that gap by reusing the SAME `buildTaskTool`/
  `buildTeamWiring` builders (no per-session-catalog drift), folding the Subagent tool's
  inline-MCP close into `SessionEngineResult.Close`, bounded by `MaxSessionEngines`.

**Three-level provider precedence:** `def.Provider > session-selected provider >
build-time default provider`, realised by a `parentProviderID` the call site threads
into the one shared resolver `resolveProviderModel(cfg, reg, def, parentProviderID,
parentModel)` (the build-time path passes `reg.Default()`/`cfg.Model`; a selected
session passes its resolved provider/model). When the provider SWITCHES, the model is
rebased off `def.Model` (or the new provider's `builtinDefaultModel`), NEVER the
inherited parent model (a bare `gpt-5` is invalid on openrouter). Same-provider keeps
the existing `resolveModel` chain (full back-compat).

**Contamination fix:** every child engine — def-pinned OR session-inheriting — is
built through `newChildEngineForProvider` → `engineDepsForProvider`, so a child on
provider X compacts/counts/prompts through X with X+model's catalogued context
window. A non-switching (same provider+model) child inherits the parent's RESOLVED
window via `childWindowFor` → `reg.meta.contextWindowFor` (issue #64) — not the old
hardcoded 128k floor; a same-model child of a 1M-context parent now compacts on the
parent's real window. (128k applies only to a genuinely uncatalogued model.)

**Fail-safe:** a def naming an unknown/unavailable provider is a LOUD fallback to the
parent provider + `slog.Warn` (mirroring every other forgiving def-error handler) —
one bad shared-repo def never wedges startup.

**Deferred (NOT built):** (1) the standalone gRPC `CreateTeam` RPC's per-session
provider — `server.Config.MemberEngine` is wired ONCE at build with the default
provider and CreateTeam carries no selector today; the in-catalog Team tool IS
covered. (2) Surfacing the resolved provider in `ListAgents`/`AgentInfo` — that is a
proto change with no consumer yet.

### Def-less child default model (`SubagentModel` — SHIPPED, issue #35)

`Config.SubagentModel` (`--subagent-model` on BOTH `mecated` and `mecatui`'s embedded
server; the analogue of `CLAUDE_CODE_SUBAGENT_MODEL`) is the global DEFAULT model for
every child engine that pins nothing of its own — across ALL the delegation families.
The def-resolved paths (named Subagent specialists, defined team members) honoured it
from day one via `resolveModel`; issue #35 extended it to the DEF-LESS families: the
default Subagent explorer, UNDEFINED team members, and Parallel BRANCH children. ONE
resolver serves all of them — each def-less site calls `resolveDefaultChildModel`,
which delegates to `resolveModelFor(cfg, agents.AgentDef{}, parentModel)` (the SAME
chain the def paths use, with the def tier empty) — so the precedence is uniform
everywhere:

**per-call `model` override > def `model:` > `SubagentModel` > parent (session) model.**

The child's context window is always re-derived live-first (`childWindowFor` over
`provReg.meta.contextWindowFor` — the ONE rule shared by `resolveChildProvider`,
`resolveDefaultChildModel`, and the per-call factory: it resolves the child's own
resolved (provider, model) window, whether or not that differs from the parent's, so
a same-provider def `model:` compacts on ITS window AND a full-inherit same-model
child compacts on the parent's REAL window, never the 128k floor — issue #64) and the
engine is minted through `newChildEngineForProvider` — the same contamination-safe
path as everything else in this section, never a clone-and-swap. Alias resolution happens ONCE at build
(`normalizeSubagentModel` in `app.Build`) and is FAIL-FAST: a non-empty
`--subagent-model` that does not resolve to a usable model id — an unknown bare
alias, or an alias resolving to "inherit" (the built-in `sonnet`/`opus`/`haiku`
aliases unless overridden via `--model-alias`) — **fails startup** with an error
naming the flag, the value, and the reason (warn-and-inert would silently run the
whole child fleet on the expensive parent model). A valid override narrates one
INFO fact naming the active child-default model. The one engine the default does
NOT touch: the user-model REVIEW engine (`buildUserModelReviewEngine`) stays on the
session model — it is a Stop-review hook engine, not a delegation child.

Decisions recorded with the feature:

- **Same-provider only.** The id is resolved on the parent's provider (the registry
  is keyed by provider, not model); a def's `provider:` remains the only
  cross-provider seam.
- **Lead included (v1).** ALL undefined team members adopt the cheap default — the
  LEAD too. A lead-strong/members-cheap split is DEFERRED; a lead that must stay on
  the strong model can pin it today via an agent def (`AgentType` + `model:`).
- **Parallel judge asymmetry (deliberate).** The judge KEEPS the session model and
  never consults `SubagentModel`: winner selection is a judgement call the operator
  implicitly trusts to the model they picked for the session, while branches are the
  bulk-token workers the cheap default exists for. Pinned by
  `TestParallelJudgeStaysOnParentModel`.
- **`--child-model` alias considered and skipped.** The existing
  `SubagentModel`/`--subagent-model` name is KEPT (no `ChildModel` rename): a knob
  rename has migration cost and no behavioural payoff; "subagent" reads as the
  umbrella for every child family here.
- **Image-capability failure mode (documented; no gate in v1).** A cheap child model
  that lacks image input fails on the provider-400 path when a child request carries
  an image — the same fail-safe posture as the OpenRouter passthrough caps (§7): the
  local gate is not authoritative, the provider error is surfaced, and the run is
  recoverable. A capability-aware downgrade gate would be speculative until it bites.

## 11. Live model listing (SHIPPED — OpenRouter)

The picker used to show only the curated embedded subset (`providercatalog`, §3) — for
OpenRouter, a hand-pinned 27 of 336 (now: all openrouter models are vendored, kept
fresh by a weekly CI job). Live model listing makes a provider whose API can
enumerate its real catalog do so, behind a clean **optional capability** so a future
provider opts in trivially.

**The abstraction.** A composition-local **`modelLister`** interface
(`internal/app/modellister.go`) — `ListModels(ctx) ([]modelEntry, error)` — NOT a `port`,
for the same reason `providerRegistry` is composition-only: a SINGLE consumer
(`liveModelSnapshot`), and the domain/agent never enumerate a catalog (the server still
receives only `[]*mecatlv1.ModelInfo`). `modelEntry` is a NEUTRAL, SOURCE-AGNOSTIC composition-local type
(id, displayName, contextLimit, inputModalities, reasoning, toolCall) — never a `port`
type, never `providercatalog.Model`. Both the live listers AND the embedded floor
(`embeddedModels`) produce `modelEntry`, so a SINGLE projection (`projectModelEntry`) +
sort (`sortModelInfos`) serve BOTH the synchronous seed (`modelSnapshot`) and the live
refresh — the floor and the seed cannot hand-sync-drift. The capability rides on an OPTIONAL
`providerEntry.lister` field (NOT a type-assert on `entry.provider`, because OpenRouter
and OpenAI share the SAME `openai.Provider` adapter and only OpenRouter opts in). **The
whole opt-in for a future provider is: implement `ListModels` + set `entry.lister` at
registry build** — zero merge/snapshot/registry-plumbing change.

**The OpenRouter lister adapter** (`internal/adapter/openrouter`) is a stdlib-only LEAF
(no domain/port/app/other-adapter import; returns its OWN `openrouter.Model`, which
composition maps to `modelEntry` — no import cycle). It GETs the FIXED-host const
`https://openrouter.ai/api/v1/models` over an INJECTED `*http.Client`. Hardening: SSRF —
fixed const URL, no caller-supplied host; CWE-200 — KEYLESS, no `Authorization` header
(the endpoint is unauthenticated; the key never reaches the lister); CWE-770 — an
`io.LimitReader` 4 MiB cap rejects an oversized body; a `ctx` deadline + client timeout
bound a hang. Mapping: `id`→id, `name`→displayName, `context_length`→contextLimit,
`architecture.input_modalities`→modalities, `supported_parameters ∋ {reasoning, tools}`→
reasoning/toolCall.

**Merge + fail-safe.** Per available provider: a SUCCESSFUL, non-empty live result
**REPLACES** the embedded subset for that provider (the point: the real catalog, not the
curated 27 union'd with their live duplicates); ANY error/timeout/empty (or no lister)
falls back to the embedded subset, with one `slog.Warn`. Since every in-scope provider
has an embedded subset, an available provider always contributes ≥ its curated subset —
the picker is **never blanked** by an upstream blip. Availability gating is free:
`liveModelSnapshot` iterates `reg.Available()`, so an unkeyed provider is neither fetched
nor shown (the keyless endpoint does not bypass this — no key ⇒ no entry ⇒ no lister).

**Capability single-source.** The image bit a live model advertises is
`adapterCaps.Image && hasImageModality(model.InputModalities)` — the SAME extracted
`hasImageModality` predicate the embedded path uses (both live and embedded carry a
`modelEntry` with authoritative modalities). So a live text-only model advertises
`image=false` even though the (openai-adapter-backed) OpenRouter provider reports
`Image:true`. **Scope: the PICKER only.** A session bound to a *live-only/uncatalogued*
model still resolves caps via the per-session factory's adapter-only passthrough
(`image=true`) — a documented, bounded picker-vs-session divergence; the closer (a shared
live cache threaded into the per-session factory) is **P2**.

**Async swap.** `Build` seeds `Config.Models` with the EMBEDDED snapshot synchronously
(so `ModelSelection` is honest from t=0 and `Build` NEVER touches the network), then kicks
ONE background goroutine that fetches the live catalog and **atomically swaps** the merged
result via `Service.SetModels` (an `atomic.Pointer[[]*ModelInfo]` inside the Service that
`ListModels`/`ModelSelection` read lock-free). The goroutine is cancelled by `Close`
(no leak; verified under `-race`). A composition-only `liveModelRefreshSync` test seam runs
the refresh inline for deterministic offline e2e (no sleeps). When no available provider
has a lister (mock/openai-only), the refresh is a no-op — no goroutine. Consequence for
clients (issue #41): a `ListModels` fired at connect time sees the EMBEDDED floor (the
async swap always loses the boot race), so client-side reconciliation must not treat
snapshot absence as model absence — the TUI reconciles at PROVIDER level only and lets
the server validate the model string verbatim.

---

## 12. Live metadata → the resolvers (SHIPPED)

§11's live record reached ONLY the picker (`Service.SetModels`). The request-path
metadata that actually drives a turn — the `max_tokens` output ceiling, the
compaction context window (the engine's `Deps.ContextWindow` resolver, a `func() int`
resolved at use — see "The ENGINE-window half: resolve-at-use (issue #66)" above), and the Anthropic
extended-thinking mode — read STATIC sources (the catalog, or hardcoded id-prefix
lists) on a SEPARATE path the live data never touched. This slice **broadens the live
record so it also feeds those resolvers**, without leaking the lister/registry/SDK
past composition.

**The enriched neutral record.** `modelEntry` (`modellister.go`) gains `OutputLimit`
(the `max_tokens` ceiling) and a `thinkingDescriptor{Known,Adaptive,Enabled}` — the
NEUTRAL, source-agnostic projection of a model's thinking capability. The zero
descriptor (`Known=false`) means "unknown" ⇒ the adapter falls back to its prefix
matrix. Only the live Anthropic source sets `Known=true`; every other source leaves
it zero (a struct of bools costs nothing). The picker proto slice AND the resolver
store both project from the ONE `modelEntry` list per refresh, so they cannot drift.

**The `liveMetaStore`** (`internal/app/livemeta.go`) is a composition-owned
`map[providerID]map[modelID]modelEntry` behind an `atomic.Pointer` (the same lock-free
swap the picker uses). It is **seeded from the embedded catalog at Build BEFORE any
network call** (`seedFromCatalog` over `reg.Available()`), so every resolver has a
correct-enough value at t=0; the SAME background refresh that calls `SetModels` also
calls `store.Swap` with the merged live list. It rides on `providerRegistry.meta`
(every resolver call site already holds `reg`/`provReg`); nil-tolerant reads keep a
hand-built test registry safe.

**Per-field, live-first-then-catalog-floor helpers** replace the bare catalog reads:

| Resolver call site (before)                 | After                                            |
|---------------------------------------------|--------------------------------------------------|
| `WithMaxTokensResolver(anthropicOutputLimit)` | closure over `meta.outputLimitFor(anthropic, …)` |
| `catalogContextWindow(pid, model)` (build/agentdefs) | `reg.meta.contextWindowFor(pid, model)`  |
| `usesAdaptiveThinking`/`thinkingCapable`    | `meta.thinkingFor(…)` via `WithThinkingResolver` |

Precedence is **PER FIELD**: take the live value only when the store has the model
AND the field is present (non-zero / `Known`); else the catalog floor; else the
conservative default the consumer already applies (`engineDepsForProvider`'s 128k,
the adapter's `defaultMaxTokens`). A live MISS for a model the catalog knows falls
back WHOLESALE to the catalog row — **live absence NEVER erases the catalog**. The
per-session child engines (`engineDepsForProvider`/`resolveChildProvider`) pick up
the store FOR FREE through the registry. With NO lister wired the store is
catalog-seeded ⇒ behaviour is **byte-identical** to before (a test asserts it).

**The Anthropic keyed lister** (`provider/anthropic/lister.go`) is the
reliability headline. It calls `client.Models.ListAutoPaging` and maps the SDK's rich
`ModelInfo` → its OWN neutral `anthropic.Model`: `MaxTokens`→OutputLimit,
`MaxInputTokens`→ContextLimit, `Capabilities.ImageInput`→image, and
`Capabilities.Thinking.Types.{adaptive,enabled}`→`ThinkingDescriptor`. **Auth:** unlike
the keyless OpenRouter lister, this endpoint is AUTHENTICATED — the lister carries the
key for a READ-ONLY metadata GET, used ONLY to read and NEVER logged (CWE-200). It is
availability-gated by construction (it runs only for a keyed = AVAILABLE anthropic
provider). The SDK takes `option.WithHTTPClient` for a mock transport, so the lister
is fully offline-testable (a `testdata/models.json` fixture; no live call ever).

**Thinking-from-live via `WithThinkingResolver`.** A new adapter Option mirrors
`WithMaxTokensResolver`: `thinkingConfigFor` consults the resolver FIRST and, when it
reports `known=true`, TRUSTS the live bits (adaptive ⇒ `{type:"adaptive"}`, else
enabled ⇒ manual `{type:"enabled",budget}`, else NEITHER ⇒ omit thinking = NONE). When
`known=false` (nil resolver, offline, or model absent from the live list) it falls
back to the EXISTING `usesAdaptiveThinking`/`thinkingCapable` **prefix matrix — the
offline floor, which is NOT deleted** (its tests stay green). So a newly-released
Claude model the prefix lists don't know gets its true thinking mode from the API,
while offline/uncatalogued runs keep the deterministic guess.

**OpenRouter live data → resolvers.** The OpenRouter lister now captures the NEW wire
field `top_provider.max_completion_tokens` → `OutputLimit` (a `null` value ⇒ 0 ⇒
catalog floor); its context window + image already arrived but only reached the
picker — they now route through the `liveMetaStore` into the resolvers too. OpenRouter
exposes no thinking-types matrix (only a coarse `reasoning` flag), so a model routed
via OpenRouter leaves the thinking descriptor zero and defers to the adapter floor.

**Per-provider matrix.** Anthropic self-describes EVERY field (output ceiling, context
window, image, thinking) — the authoritative live source. OpenRouter supplies output
ceiling + context + image (no thinking matrix). **OpenAI stays catalog-only**: its
`/v1/models` is sparse (id/created/owned_by only — it cannot self-describe ceilings),
so there is no OpenAI lister and the embedded catalog remains OpenAI's source for
every metadata field.

**Refresh cadence.** ONE-SHOT at Build (reused `startLiveModelRefresh`; no TTL) — a
periodic/disk-cached refresh stays a P2 item. **Not surfaced in the picker:** the
output ceiling / thinking mode remain INTERNAL resolver inputs (no proto/mecatui/caps
change); a TUI badge for them is a deferred follow-up (the picker-proto slice "Slice
D"). The OpenRouter `reasoning_details` replay fix is a SEPARATE request-path bug, not
bundled here.


---

*Part of the [design docs](../design/README.md). Related: [OpenAI Responses API for a Go Agentic Coding Harness — 2026 Implementation Brief](0017-openai-responses-api.md), [mecatl — Architecture](0004-v1-architecture.md).*
