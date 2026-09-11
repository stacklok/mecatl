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
organization's gateway. An operator running that proxy locally should get mecatl talking to it with **zero
configuration**: no API key to set, no base-URL flag to remember, just `thv llm proxy start` and
`mecatl` picks it up.

Two names collide on the surface: `--toolhive` (already shipped) discovers MCP **tool workloads**
via the embedded ToolHive library; this feature discovers an LLM **gateway proxy** via ToolHive's
own config file. They share a vendor name and nothing else. Every flag, diagnostic, and doc surface
this ADR touches says so explicitly — the #1 confusion risk the internal review flagged.

**The naming pass.** `newOpenAIEntry` (the shared construction path for openai/openrouter) is
renamed `newOpenAICompatEntry` — a "gateway" registry entry is the SAME Responses-API wire adapter
with a different base URL and a placeholder credential, so it must be built by the exact same
construction path or the two would silently drift on resilience wrapping. The `provider/openai`
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
refresh succeeds (the swap path calls `healDefaultModel`), and — per the R2 fix below — that heal now
reaches a **zero-selector session created at ANY time after the heal**, not only the picker/accessor.
The empty-list case (probe succeeds, credential lists zero models) is NOT covered by this
deviation and still fails Build loudly (`errToolhiveNoModels`) — there is genuinely nothing useful to
default to there, and the operator needs to know NOW, not after a confusing first request.

**R2 addendum — the heal now reaches zero-selector sessions (post-review fix).** The v1 heal-path
review found the healed default reached `reg.ResolvedDefaultModel()` and the `/models` picker but
NOT actual session creation: a zero-selector session's engine was built once, at Build time, from the
then-frozen `cfg.Model` (`""`), and nothing re-read the registry afterward. The fix is a **pending-
default posture**: `Config.defaultModelPending` (set once, at Build, from the exact condition
`healDefaultModel` itself guards on — an intent-driven default provider with no resolved model) routes
EVERY zero-selector session through the per-session engine factory (`sessionNeedsPerFactory`) instead
of the shared-engine fast path, and through rehydration (`needsRehydration`) for a session persisted
before a restart into a still-down proxy. The factory resolves the CURRENT
`reg.ResolvedDefaultModel()` at session-build time (resolve-at-use, mirroring the `Deps.ContextWindow`
precedent, but at session granularity — "Provider is FIXED per session" still holds: resolved once at
engine build, then fixed for that session's lifetime). The NARROWED residual: a session built BEFORE
the heal lands still carries the frozen `""` model and fails at request time — only a NEW session (or
a restart-then-run, via the `needsRehydration` arm) picks up the heal. This residual is now scoped to
"before this specific heal", not "for the life of the process".

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
single byte is trusted. The live model list is bounded and C0/C1/DEL/bidi-control/line-separator-
stripped at the `openaicompat` leaf — a hostile or buggy gateway response can neither OOM the process
nor smuggle a terminal escape sequence or a bidi-override spoof into a picker row.

**D5 addendum — redirect refusal now covers the INFERENCE path too (post-review fix).** The v1
listing probe (`openaicompat.NewLister`) already refused redirects (`CheckRedirect` ⇒
`http.ErrUseLastResponse`), but the INFERENCE request — the one carrying the actual conversation body
+ `Authorization` header — rode the `openai` adapter's own client, which by SDK default follows up to
10 redirects. A hostile/misconfigured process squatting the loopback port could answer the inference
request with a redirect and bounce that body off-loopback (CWE-918). The fix shares ONE policy,
`openaicompat.RefuseRedirects`, between both surfaces: the listing lister's default client (unchanged)
and a NEW `openai.WithHTTPClient` option wired ONLY onto the toolhive gateway registry entry
(`newGatewayEntry`) via a redirect-refusing `*http.Client`. Neither openai nor openrouter get this
option — they talk to a real, TLS-terminated, non-loopback endpoint where following a redirect is
ordinary and expected.

**D6 — surfacing.** An additive proto message, `ProviderStatus` (`provider_id`, `state` — a STRING
passthrough, no enum, matching the `EvNoProgress`/`StopBudget` precedent — `hint`, and a fourth
additive field `default_model_auto_selected`), rides on `ListModelsResponse.provider_status`, scoped
to INTENT-DRIVEN providers only (v1 deliberately never surfaces an ordinary openrouter/anthropic
live-listing blip through this channel). mecatui's `/models` picker renders one muted remediation
line per non-ok status under the list, and replaces the generic "No selectable models advertised."
with a gateway-specific note when the state is `empty`. The header carries a persistent, muted "via
ToolHive gateway" segment whenever the active session's provider is `toolhive` — disclosure-only, no
acknowledgment gate, riding the same segment-shedding machinery every other header segment already
uses. **`default_model_auto_selected` (post-review fix)** is true ONLY when `provider_id` is the
DEFAULT provider AND the server AUTO-selected its default model (a first-listed heal/probe pick) —
never when an operator configured `--model`/`--default-model`. It replaces a client-side
`ProviderID == "toolhive"` vendor-name check the picker's "(auto-selected)" provenance label used
(which wrongly labeled an operator-configured toolhive default as auto-selected, and could never
label a future non-ToolHive intent-driven provider correctly): the label is now vendor-neutral and
honest for any provider that carries the bit.

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

**D8 addendum — the favored direction for the deferred direct/off-host mode (review feedback).**
Rather than extending always-on proxy-config auto-detection off-host, the favored future design is a
**`thv llm token` credential-helper model** — mecatl execs ToolHive for a short-lived token on demand
(the git-credential-helper / kubectl exec-auth pattern), combined with **explicit gateway endpoint
config** (an operator-supplied remote base URL, not auto-detected). This keeps the loopback-only
auto-detection invariant intact for v1 while recording the shape a future off-host mode should take;
`--toolhive-llm-allow-remote` remains NOT built here.

**D9 — discoverability (the disclosure surface).** The common operator posture is "I have an API key
set, and the gateway is detected but not my default" — without surfacing that the gateway is *available*
the detection is invisible. D9 adds six disclosure affordances across the wire + the TUI + settings,
NONE of which change the precedence ladder (an explicit credential still wins; this is disclosure, not
routing):

1. **Two additive `ProviderStatus` proto fields** (`contracts/proto/mecatl/v1/harness.proto`,
   `ProviderStatus`), scoped to INTENT-DRIVEN providers only (the v1 invariant: a key-driven
   openrouter/anthropic live-listing blip never grows the `provider_status` list):
   - `model_count` (field 5): the count of models this intent-driven provider's last successful live
     listing returned, derived from the outcome store's last-known-good snapshot
     (`internal/app/modellister.go`, `liveOutcomeStore.getModelCount`). 0 on empty/unreachable/
     unrecorded — a client cannot distinguish "0 models" from "never probed" by this field alone
     (`state` disambiguates: `state=="empty"` with `model_count==0` is a genuine empty list;
     `state=="unreachable"` with `model_count==0` is a probe failure).
   - `available_not_default` (field 6): true ONLY when this intent-driven provider is registered,
     reachable (`state == "ok"`), AND is NOT the active default provider (a key-driven provider
     outranks it on the precedence ladder). Vendor-neutral (no `toolhive`-name check), matching the
     `default_model_auto_selected` discipline — named for the CONDITION so a client can surface
     "the gateway is available" for ANY intent-driven provider, present or future. False whenever the
     provider IS the default (so the sole-provider case does not advertise itself as "available but not
     default").
   Both are additive (fields 5/6) and carry 0/false for every deployment without an intent-driven
   provider — byte-identical to before this wave. Composition projects both from the registry's outcome
   store + the lock-free `Default()` in the SAME `providerStatusProto` pass that already projected
   the first four fields, so the count and the availability bit can never drift from the status the
   picker already renders. The client mirror lives at `cmd/mecatui/client/models.go`
   (`ProviderStatus.ModelCount`/`ProviderStatus.AvailableNotDefault`).

2. **The idle footer notice** (`cmd/mecatui/ui/model.go`, `gatewayNotice`/`gatewayNoticeShown`):
   when an intent-driven provider is detected-and-reachable but NOT the active default, mecatui fires
   a dismissable footer-left notice ONCE per process:
   `<provider-id> gateway available (N models, no API key needed) — /models to use it, or --default-provider <provider-id>`
   It fires only at idle (the `connecting`/`running`/`awaiting-approval` arms own the footer-left in
   their phases), is dismissed by ANY keypress at idle OR by opening `/models`, and a latch
   (`gatewayNoticeShown`) prevents re-firing across repeated `ModelsMsg` landings (a re-open, a live
   refresh). Rendered via the `muted` theme slot so it reads as a notice, not an error. The model count
   comes from the `available_not_default` row's `ModelCount` (`cmd/mecatui/ui/models.go`,
   `availableNotDefaultStatus`).

3. **The picker "org" tag** (`cmd/mecatui/ui/models.go`, `modelRowText`/`intentProviderSet`): model
   rows served by an intent-driven provider carry a `org` ASCII segment in the `/models` picker
   (matching the existing `img`/`reason` token style, fixed-width after ANSI strip for golden
   stability). Derived from the `provider_status` set — every row in `provider_status` is
   intent-driven by the server-side filter, so membership ⇒ the "org" tier. A nil map (no statuses)
   produces no tag, so the no-gateway render path stays byte-identical.

4. **The provenance hint** (`cmd/mecatui/ui/models.go`, `modelProvenanceLine`): when the session's
   default provider is key-driven AND an intent-driven alternative is `available_not_default`, the
   picker's provenance line appends:
   ` · <gateway-id> gateway also available — outranked by your <default-id> key`
   Vendor-neutral (the gateway name is the status row's `ProviderID`, never a hardcoded vendor
   string); suppressed when the gateway IS the default (no outranking) or no `available_not_default`
   row exists.

5. **The header "available" segment (N1)** (`cmd/mecatui/ui/view.go`, `headerIdentityParts`): a muted
   `<provider-id> gateway available` segment renders in the header as a sibling of the existing
   `via ToolHive gateway` segment (item D6 above, the active-case segment). It shows when an
   intent-driven provider is detected-and-reachable but NOT the active default
   (`availableNotDefaultStatus` over `m.models.statuses`). Mutually exclusive with the active-case
   branch by construction: `availableNotDefaultStatus` is false when the gateway IS the default, so
   the two segments never both render. Vendor-neutral — the provider id comes from the status row,
   not a hardcoded `toolhive`, so a future non-ToolHive intent-driven provider reads naturally. A nil
   `statuses` (no gateway) renders neither segment, keeping the no-gateway path byte-identical.

6. **The welcome splash gateway line (N2)** (`cmd/mecatui/ui/help.go` `zeroStateGatewayNote` +
   `cmd/mecatui/ui/welcome/welcome.go` `GatewayNote`): the first-run splash renders a muted
   `<provider-id> gateway detected (no API key needed) — /models` line when an intent-driven
   provider is available-but-not-default, surfacing a no-API-key alternative at the moment a new
   operator is most attentive. The splash renders at `phaseIdle` (post-connect), by which point the
   first `ModelsMsg` has landed and `m.models.statuses` is populated — so the line catches the
   operator after the gateway is known-reachable but before they've typed a prompt. Vendor-neutral
   (the provider id comes from the status row); suppressed when the gateway IS the default or when
   no `available_not_default` row exists (the no-gateway render path stays byte-identical). The
   pre-rendered line rides the splash's `optional` section list and is shed like any other
   low-priority section on short terminals.

The six surfaces are **disclosure-only**: they tell an operator with a key set that the org gateway
is available and how to make it the default, but they never reroute a session. Making toolhive the
default persistently is an explicit operator choice via `models.default_provider` (see below) or
`--default-provider`, both of which feed the UNCHANGED `preferredDefaultProvider` ladder.

**`models.default_provider` (settings.yaml).** A SEVENTH, operator-config surface: the
`models.default_provider` YAML key (the `--default-provider` flag twin) lets an operator declare
"toolhive is my default despite my API key" persistently in `settings.yaml` without unsetting the key.
It is folded by `foldOperatorDefaultProvider` (`internal/app/slots.go`) in `Build` BEFORE
`buildProviderRegistry`/`validateDefaultModel` so the registry sees the YAML value and the fail-fast
gate catches an unknown provider; a CLI `--default-provider` OUT-RANKS the YAML value (the CLI-wins
fold, `Config.DefaultProviderFlagSet`). Operator-tier only (a project-tier `default_provider:` is
ignored with a WARN, the existing `captureModels` discipline); the name pair
(`default = model`, `default_provider = provider`) mirrors the wire grammar's `provider_id`/`model_id`
split exactly. It feeds the UNCHANGED ladder as an explicit override — it does NOT lower the
precedence of key-driven providers.

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
The sole+probe-down boot-with-empty-model path is a genuine (if narrow) UX rough edge, NARROWED by the
R2 pending-default posture: a zero-selector session created BEFORE the heal lands gets the server's
un-resolved default until the next live refresh heals it (a session created AFTER the heal — or a
restart-then-run — now picks it up automatically, per the D1/R2 addendum above) — documented, tested,
and judged strictly better than the alternative (Build refusing to boot at all).

## See also

- [Architecture guide — providers](../architecture/providers.md) — the provider-registry
  section's intent-driven availability paragraph.
- [Usage guide](../usage.md) — the ToolHive LLM gateway walkthrough + troubleshooting table.
- [Cloud-native inventory](0027-cloud-native.md) — List 1/List 2 rows for the `liveOutcomeStore` and
  the refresh-cooldown state.
- [ADR 0016 — multi-provider](0016-multi-provider.md) — the registry/capability-intersection
  machinery this feature extends rather than replaces.
- [ADR 0055 — reasoning effort](0055-reasoning-effort.md) — the clamp table `toolhive` joins.
