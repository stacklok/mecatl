# ADR 0064 — Auto-detect the ToolHive LLM gateway proxy as a native provider

- Status: Accepted
- Date: 2026-07-14
- Scope: `internal/adapter/openaicompat` (new leaf), `internal/adapter/toolhivellm` (new leaf), `internal/app` (`registry.go`, `modellister.go`, `build.go`, `reasoning_effort.go`), `internal/adapter/server` (`service.go`, `grpc.go`, `http.go`), `internal/cliconfig`, all four `cmd/` mains, `cmd/mecatui/{client,ui}`, `contracts/proto/mecatl/v1/harness.proto`
- Supersedes: none
- Superseded by: none

## Context

ToolHive ships an LLM gateway proxy (`thv llm proxy`, distinct from ToolHive's MCP-workload
discovery that mecatl already integrates via `--toolhive`) that fronts an operator's coding-agent
credential — an OpenAI-shaped `GET /v1/models` + `/v1/chat/completions` surface over an
organization's gateway (see stacklok-enterprise-platform#2270 for the concrete wire shape this ADR
targets). An operator running that proxy locally should get mecatl talking to it with **zero
configuration**: no API key to set, no base-URL flag to remember, just `thv llm proxy start` and
`mecatl` picks it up.

Two names collide on the surface: `--toolhive` (already shipped) discovers MCP **tool workloads**
via the embedded ToolHive library; this feature discovers an LLM **gateway proxy** via ToolHive's
own config file. They share a vendor name and nothing else. Every flag, diagnostic, and doc surface
this ADR touches says so explicitly — the #1 confusion risk the internal review flagged.

**The naming pass.** `newOpenAIEntry` (the shared construction path for openai/openrouter) is
renamed `newOpenAICompatEntry` — a "gateway" registry entry is the SAME Responses-API wire adapter
with a different base URL and a placeholder credential, so it must be built by the exact same
construction path or the two would silently drift on resilience wrapping. The `internal/adapter/openai`
package doc-comment is upgraded to say so: it implements a *protocol*, not a single vendor.

**The two-layer split.** `internal/adapter/openaicompat` is a stdlib-only, protocol-generic
`GET /v1/models` lister — it would serve ANY OpenAI-compatible gateway, vendor-blind.
`internal/adapter/toolhivellm` is the ONLY ToolHive-aware code in the tree: it reads ToolHive's own
config file to discover the proxy's loopback port and feeds `openaicompat` a hardcoded
`http://127.0.0.1:<port>/v1`. Neither package imports a ToolHive Go module — there is no dependency
on ToolHive's own dependency graph, only on the shape of its config file and its documented `/v1/models`
convention. If a second gateway-shaped provider ever needs this pattern, extract a `ProviderProbe`
abstraction over the two-layer split at THAT point — building it now, for one consumer, would be
premature (D8; see Scope cuts below).

**Register-on-intent vs. probe.** The obvious design — "probe the proxy at Build, only register on
success" — creates a restart trap: a session that persisted `provider_id: "toolhive"` before a
restart would fail to rehydrate if the proxy happened to be down at that moment (`build.go`'s
`sessionEngineFactory` rejects an unknown provider id with `ErrInvalidArgument`, the exact
"unknown or unavailable provider" class of error this feature must never produce for a legitimately-
registered provider). So registration is **intent-only**: detecting an `llm:` block in ToolHive's
config file (or an explicit `--toolhive-llm-base-url`) is sufficient to register the "toolhive"
entry, unconditionally of reachability. A bounded (≤1.5s) Build-time probe runs AFTER registration
and drives only the startup diagnostic, the initial model list, and default-model eligibility — it
never gates whether the entry exists.

### The accepted deviation from the letter of the default-model rule

The rule as specified: toolhive becomes the DEFAULT provider only when (a) it is the sole provider
AND (b) the probe succeeded. Applied literally, sole-provider + probe-down means NO default provider
resolves at all — and `Build` cannot construct the shared engine without one, so mecated fails to
boot. That is precisely the restart-brick scenario register-on-intent exists to prevent, just moved
one line earlier.

**Resolution:** sole ⇒ toolhive IS the default provider unconditionally (boot must always succeed
for a legitimately-registered sole provider); the probe-succeeded gate governs only the auto-picked
default MODEL and the celebratory startup INFO. Sole + probe-down boots with `defaultModel = ""` and
a WARN naming the remediation (`thv llm proxy start`); the model **heals** the moment a later live
refresh succeeds (the swap path calls `healDefaultModel`), and a session created before that heal —
with no client-side selector — fails at request time exactly like any other mid-session provider
outage. The empty-list case (probe succeeds, credential lists zero models) is NOT covered by this
deviation and still fails Build loudly (`errToolhiveNoModels`) — there is genuinely nothing useful to
default to there, and the operator needs to know NOW, not after a confusing first request.

## Decision

**D1 — register on intent.** `resolveToolhiveIntent` (`internal/app/registry.go`) decides
registration from config alone: an explicit `--toolhive-llm-base-url` (pre-validated loopback-only)
wins outright and skips the config-file read entirely; otherwise, when `--toolhive-llm` is set (the
default), `toolhivellm.DetectConfig` reads `$XDG_CONFIG_HOME/toolhive/config.yaml`. A detection miss
is a single DEBUG diagnostic, never a WARN — most operators simply do not run ToolHive. `--toolhive-
llm=false` skips detection entirely (zero file stats) for a shared host.

**D2 — defaults.** `preferredDefaultProvider` tiers `toolhive` STRICTLY below every key-driven
provider (openai/openrouter/anthropic): any resolved API key always wins the default, alphabetics be
damned (pinned by a test where `anthropic` — which sorts before `toolhive` — still wins). Sole +
probe-ok picks the first-listed model as the default (fed through the same T7 capability-intersection
fixup every other provider gets). `clampEffortForProvider` joins toolhive to the openai/openrouter
low/medium/high clamp — it fronts mixed upstreams over the OpenAI protocol, so the conservative clamp
applies uniformly regardless of which model actually answers.

**D3 — model-list resilience.** The per-provider live-outcome store (`liveOutcomeStore`,
`internal/app/modellister.go`) now retains a **last-known-good** snapshot alongside the merge logic
`resolveProviderModels` already had. For a provider with a NON-EMPTY embedded catalog
(openrouter/anthropic) the behaviour is byte-identical to before: a live failure or an honest empty
response falls back to the embedded floor. For a provider with an EMPTY embedded catalog (toolhive —
there is no fixed per-credential catalog to embed), a live failure falls back to the last
successfully-listed snapshot instead of going blank, and an honest empty response (200, zero models)
REPLACES to empty (it is itself a genuine, actionable answer — "your gateway credential lists no
models" — not a transient blip to paper over).

**D4 — flags.** `--toolhive-llm` (default `true`) and `--toolhive-llm-base-url` (default `""`, NO
environment-variable twin — a base-URL override this security-sensitive stays an explicit, visible
flag) are registered by the shared `cliconfig.RegisterToolhiveLLMFlags` on all four mains. `mecak8s`/
`mecatequi` are naturally inert (a pod/runner has no ToolHive config file); their help + this doc
recommend `--toolhive-llm=false` on a genuinely shared host regardless.

**D5 — security (v1-mandatory).** The probed/routed base URL is ALWAYS the hardcoded
`http://127.0.0.1:<port>/v1` — the host NEVER derives from the config file's `gateway_url` (captured
only for diagnostic display) or from anything else remotely supplied. An explicit
`--toolhive-llm-base-url` is rejected unless its `Hostname()` is a literal loopback IP or exactly
`localhost` (checked via `net.ParseIP`, never a DNS lookup — a resolver-based check is a TOCTOU: the
name could resolve differently between the check and the request). `tls_skip_verify` is never
decoded (the wire struct has no field for it — there is nowhere for the value to land) and
`InsecureSkipVerify` appears nowhere in this feature. The config-file read is hardened: a 1 MiB
`LimitReader`, a regular-file check, and a same-uid-as-the-calling-process ownership check before a
single byte is trusted. The live model list is bounded and C0/DEL-stripped at the `openaicompat` leaf
— a hostile or buggy gateway response can neither OOM the process nor smuggle a terminal escape
sequence into a picker row.

**D6 — surfacing.** An additive proto message, `ProviderStatus` (`provider_id`, `state` — a STRING
passthrough, no enum, matching the `EvNoProgress`/`StopBudget` precedent — `hint`), rides on
`ListModelsResponse.provider_status`, scoped to INTENT-DRIVEN providers only (v1 deliberately never
surfaces an ordinary openrouter/anthropic live-listing blip through this channel). mecatui's
`/models` picker renders one muted remediation line per non-ok status under the list, and replaces
the generic "No selectable models advertised." with a gateway-specific note when the state is
`empty`. The header carries a persistent, muted "via ToolHive gateway" segment whenever the active
session's provider is `toolhive` — disclosure-only, no acknowledgment gate, riding the same
segment-shedding machinery every other header segment already uses.

**D7 — testing.** Every composition test is offline/hermetic via the existing `toolhiveConfigPath` +
`liveModelHTTPClient` + `envDetector` seams (mirroring the OpenRouter/Anthropic live-lister
precedent) — never the real home directory, never a real port 14000. The rehydration-with-proxy-down
gate is a two-`Build` e2e (the `TestApproveAfterRestartE2E` pattern): create a session on `toolhive`
while the proxy answers, restart with the probe transport refusing, and assert `StartRunContent`
never returns `ErrInvalidArgument`/"unknown or unavailable provider" — the failure, when it comes,
must be a genuine request-time connection error.

**D8 — scope cuts (deliberate, for v1).** No direct/off-host mode (the loopback-only invariant is
v1-permanent; a future `--toolhive-llm-allow-remote` is the sanctioned escape hatch, NOT built here).
No Anthropic-protocol gateway path (ToolHive's LLM gateway speaks the OpenAI protocol only, today).
No ToolHive Go import, anywhere, ever. No pricing/cost metadata. No model-fingerprint pinning (a
gateway model list is per-credential and can legitimately change). **No `ProviderProbe`
abstraction** — the two-layer split (`openaicompat` + `toolhivellm`) is deliberately NOT generalized
behind an interface with a single consumer; extract one at the SECOND gateway-shaped provider, not
before (a documented trip-wire, not a deferred TODO to forget).

## Consequences

**Easier:** a ToolHive user gets a working coding-agent session with zero configuration — no key, no
flag, just `thv llm setup && thv llm proxy start`. The zero-keys `errNoProvider` path stops firing for
this operator (an intent-driven registration is itself a form of "a provider is available"). A future
OpenAI-compatible gateway (not ToolHive-specific) can reuse `openaicompat` directly.

**Harder / costs paid honestly:** every mecated/mecatui/mecatequi/mecak8s Build now pays ONE extra
file `Stat` (the config-detection probe) even for an operator who has never heard of ToolHive —
accepted as negligible (a single syscall) against the zero-config payoff. The registry now carries a
"tier" concept (`intentDriven`) that every future provider-ordering decision must remember to respect
— `preferredDefaultProvider` and `providerStatusProto` are the two places this is pinned by test.
The sole+probe-down boot-with-empty-model path is a genuine (if narrow) UX rough edge: a zero-selector
session created in that exact window gets the server's un-resolved default until the next live
refresh heals it — documented, tested, and judged strictly better than the alternative (Build
refusing to boot at all).

## See also

- [Architecture guide — providers](../architecture/providers.md) — the provider-registry
  section's intent-driven availability paragraph.
- [Usage guide](../usage.md) — the ToolHive LLM gateway walkthrough + troubleshooting table.
- [Cloud-native inventory](0027-cloud-native.md) — List 1/List 2 rows for the `liveOutcomeStore` and
  the refresh-cooldown state.
- [ADR 0016 — multi-provider](0016-multi-provider.md) — the registry/capability-intersection
  machinery this feature extends rather than replaces.
- [ADR 0055 — reasoning effort](0055-reasoning-effort.md) — the clamp table `toolhive` joins.
