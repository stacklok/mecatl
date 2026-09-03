## 5. Per-slot models (`models:`, ADR 0030)

#### Quickstart: pick a model per job

Model selection is a stack of independent mechanisms. Pick the one(s) you need:

| You want… | Use |
|---|---|
| Short names for models you reference often | `models.aliases:` |
| A concise automatic session title | explicit `models.slots.title` (no fallback; see [session titles](#session-title-generation)) |
| Cheaper compaction / guardrail / ask-reviewer calls | `models.slots:` (`compaction`/`guardrail`/`ask-reviewer`) |
| A cheaper default for every delegated subagent | `models.subagent:` (the settings.yaml twin of `--subagent-model`) |
| Plan on a strong model, execute on a cheaper one | `models.slots: plan:` (the opusplan pattern) |
| Pick a subagent's model per task automatically | `models.router:` (a taxonomy enables it; `--subagent-model-router=false` is the off-switch) |
| Let a trusted repo re-bind models within your cap | `models.allowlist:` + a project `.mecatl/settings.yaml` |

A complete tiered setup on one provider (here, OpenRouter — model selection only
swaps the model within a session's provider, never the provider itself):

```yaml
# ~/.config/mecatl/settings.yaml
models:
  aliases:                 # short names → concrete ids (the spine everything else references)
    heavy: z-ai/glm-5.2
    coder: deepseek/deepseek-v4-flash
    quick: google/gemini-3.5-flash
  default: z-ai/glm-5.2    # the session model (the orchestrator)
  subagent: coder          # the def-less child default — the fail-soft FLOOR when the router below
                           # is present (router pick > this > session model), and the child default
                           # when no router taxonomy is configured. A def `model:` / per-call override /
                           # CLI --subagent-model still wins. Operator-tier only.
  slots:                   # route housekeeping + plan-mode to a cheaper/stronger model
    compaction: quick
    guardrail: quick
    ask-reviewer: quick
    plan: heavy            # plan-mode turns swap to the heavy model
    router: quick          # the classifier itself
    title: quick           # opt-in asynchronous session-title generator
  context_windows:         # exact final provider/model IDs → total context tokens
    openrouter:
      z-ai/glm-5.2: 200000
  router:                  # pick a subagent's model per task (a taxonomy enables it; ADR 0042)
    # disabled: true        # optional kill-switch: keep the taxonomy but turn routing off
    default-category: medium
    categories:
      - name: large
        description: deep multi-step reasoning, architecture, subtle bugs
        model: heavy
      - name: medium
        description: standard implementation, bug fixes, writing and reviewing tests
        model: coder
      - name: small
        description: trivial mechanical tasks, single-file edits, quick lookups
        model: quick
```

Defining the `models.router:` taxonomy above is all it takes to enable the router
(ADR 0042 — configure = enable, the guardrails-parity model). To keep the taxonomy but
turn routing off, set `models.router.disabled: true` or launch with
`mecated serve --subagent-model-router=false` (the kill-switch). With no `models.router:` block
at all, every call keeps the session model — the default is byte-identical.

`models.context_windows` ([ADR 0207](../adr/0207-context-window-overrides.md)) is
operator-tier only and maps an exact provider ID to an
exact final model/routing ID and its total context-token limit. Alias and slot routing
finish before lookup, so the map never performs fuzzy, reverse, or cross-provider
matching. It takes precedence over live provider metadata and the models.dev catalog;
the global `--context-window-override` remains the highest-priority override. Empty
keys and non-positive or over-2,000,000 values fail parsing. A project-tier map is
removed with an operator warning and cannot influence compaction or displayed limits.

**Verifying it's wired.** On startup mecated logs one build-once fact per active slot
(`model slot ACTIVE`) and, when the router is enabled, `subagent model router ACTIVE`
(or a `DISABLED` WARN if a taxonomy is present but the kill-switch is set).
Check the mecated log (stderr, or `$XDG_STATE_HOME/mecatl/mecatui.log` under mecatui)
for those lines. A slot that failed to resolve WARNs and degrades to the session model,
so a missing `ACTIVE` line is the signal something didn't bind.

#### How it works

The internal **lightweight** LLM calls — the compaction summary, the headless
ask-reviewer, and the guardrail checker — can run on a **cheaper model** than the
session via a **model slot** (the `--model-slot` flag, above, or the user-global
`settings.yaml` `models:` subtree). A slot binds a named call to a model **selector**
(an alias or a concrete id), resolved through the alias map. The byte-identical
default holds: with no slot configured every call keeps the session model.

```yaml
# ~/.config/mecatl/settings.yaml  (user-global only — NOT a checked-in project file)
models:
  aliases:                   # the alias spine (same map as --model-alias; CLI wins per key)
    cheap: gpt-4o-mini
    reasoning: gpt-5
  slots:                     # bind a slot (or a tier) to a selector
    compaction: cheap        # the compaction tier-4 summary call
    ask-reviewer: cheap      # the headless child-ask reviewer
    guardrail: cheap         # the LLM content checker
    plan: reasoning          # plan-mode turns run on the reasoning model (opusplan)
    router: cheap            # the subagent model-router classifier (ADR 0031)
    # cheap: gpt-4o-mini     # a TIER key gives a default a slot falls through to
```

- **Slots** route the internal-call slots `compaction`, `ask-reviewer`, `guardrail`,
  `router`, **plus** `plan` (the mode axis, below). A **tier** key
  (`cheap`/`fast`/`reasoning`) is the default a slot with no explicit binding falls
  through to — the internal-call slots default to `cheap`, while **`plan` defaults
  to `reasoning`** (a plan model is a strong-reasoning model, not a cheap one).

#### Session title generation

- **The `title` slot.** This is the explicit opt-in for automatic session-title
  generation ([ADR 0290](../adr/0290-session-title-generation-and-auxiliary-usage.md)).
  It has **no tier or session-model fallback**: omit it and generation is disabled,
  so no title-model call occurs. On a compatible fixed session provider, the server
  captures up to three early genuine prompts and asynchronously makes a bounded
  tool-less call after a successful exchange. Its usage is durable
  `session_title` auxiliary accounting, not the session/run budget or normal result
  usage. The title model never changes the session model.

#### Other slots

- **The `plan` slot (the opusplan workflow).** Bind `plan` to a strong-reasoning model
  and a session **automatically swaps to it while in plan mode** and back to the session
  model when executing — re-resolved **between turns** at the run-entry seam (never
  mid-turn; the model is fixed per turn), within the **same provider**. E.g.
  `--model-slot plan=reasoning --model-alias reasoning=anthropic/claude-opus-4.5` runs
  planning on Opus and execution on the session model. With no `plan` slot a mode flip
  changes nothing (**byte-identical**). The `resolved_model` echo re-emits the new model
  on the next `GetSession`/turn after the switch.
- **Resolution choke point** is `resolveSlotModel`: explicit slot binding > the slot's
  default tier > the session model. The selector is resolved through the **same alias
  machinery** as `--model-alias` / an agent def's `model:`.
- For the **compaction** slot, ONLY the summary LLM call's model changes — the
  session's own model, token counter, prompt, and context window stay put. For
  **ask-reviewer** the slot **supersedes the model** of `--subagent-ask-reviewer`, but
  that flag stays the **on/off gate** (a slot alone never enables the reviewer — the
  sibling trap, out of scope for #159). For **guardrail** the slot **supersedes the
  model** of `--guardrails-model` AND (per [ADR 0046](../adr/0046-guardrails-slot-enable.md),
  configure = enable) **also enables** guardrails — a bound `guardrail` slot alone turns
  the checker ON; the flag is no longer the sole enable gate.
- **Fail-soft**: a typo'd slot key or an alias that means *inherit* WARNs and degrades
  to the session model — a broken housekeeping slot never wedges the call.
- **Operator-tier by default, project-overridable within an allowlist.** The
  `models:` mapping is parsed **strictly** (an unknown top key like `slotz:` errors).
  `--model-slot`/`--model-alias` out-rank the YAML per key. By default a project-tier
  `models:` block is **ignored with a WARN** — UNLESS the operator opts in with an
  allowlist (next subsection). Team synthesis is a later ADR-0030 layer, not yet wired;
  the subagent **router** is described below.

#### The subagent model router (`models.router:`, ADR 0031, enable model ADR 0042)

The **semantic model router** picks which model a `Subagent` delegation runs on,
**per task**, from a category menu you define. **Defining the `models.router:` taxonomy
enables it** ([ADR 0042](../adr/0042-taxonomy-gated-model-router.md) — configure = enable,
the same model as guardrails); there is no enable flag to forget. To keep the taxonomy but
turn routing off, set `disabled: true` in the subtree (or launch with
`--subagent-model-router=false` — the two combine). Define the taxonomy in the
**operator-tier** `models.router:` subtree:

```yaml
# ~/.config/mecatl/settings.yaml  (operator-tier ONLY — a project-tier router: is stripped with a WARN)
models:
  aliases:
    cheap: gpt-4o-mini
    big: anthropic/claude-opus-4.5
  slots:
    router: cheap            # the CLASSIFIER itself runs on this slot (default: cheap tier)
  router:
    # disabled: true         # optional kill-switch: keep the taxonomy but turn routing off (ADR 0042)
    classifier-slot: cheap   # optional; overrides the `router` slot for the classifier model
    default-category: small  # what the classifier picks when none clearly fits
    categories:
      - name: small
        description: trivial, mechanical, single-file edits; quick lookups; renames
        model: cheap
      - name: large
        description: deep multi-step reasoning, architecture, subtle concurrency bugs
        model: big
```

- **Give categories CLEAR, DISTINCT descriptions** — the description is the classifier's
  ONLY signal. Vague or overlapping descriptions make routing unreliable (and it
  fail-softs to the default model on a miss, so the win is simply lost).
- **The classifier** is a tiny one-turn call on the `router` slot (or `classifier-slot`),
  reusing the hardened single-JSON-verdict parse; the task prompt is fenced as untrusted.
- **Precedence** (the router fills the gap, never overrides): an explicit per-call
  `model`, an agent-def's own `model:`, a `fork`, or a `resume` already pins the engine →
  the router does NOT fire. Otherwise: per-call `model` > agent-def `model:` (incl. explicit
  `inherit`) > fork/resume > **router** > `--subagent-model` default > session model.
- **Per-category `model`** is an alias / slot / concrete id, resolved through the same
  alias map (operator targets are **uncapped** — the operator is authoritative).
- **Unpinned agent-defs route too** (issue #286, writable parity issue #517): a delegation
  to a named `agent` that declared **no `model:`** is classified and its scoped engine rebuilt
  on the routed model in either read-only or `mode:"read-write"`. The writable form remains
  direct-write and preserves the specialist's tools, prompt, provider, and per-def limits.
  To keep a def on a fixed model — i.e. to opt it OUT of routing — set its `model:`
  explicitly; **`model: inherit`** pins it to the session model without routing. (A def that
  switches `provider:` or declares inline MCP servers is never routed.) An unavailable
  writable routed target falls back to the ordinary writable specialist and reports
  `route-target-unavailable`.
- **Writable delegations route too** (issues #285 and #517): a `mode:"read-write"` explorer
  or unpinned named specialist picks its model from the same taxonomy, running the WRITABLE
  engine on the routed model directly against your workspace. An explicit
  `read-write`+`agent`+`model` call remains invalid; that explicit override is not the same as
  the router selecting a model. A pinned specialist or a `resume` keeps its own model.
- **Fail-soft + breaker**: any classifier failure or unknown/hallucinated category keeps the
  engine that the delegation would otherwise use: a named delegation keeps its ordinary
  specialist (read-only or writable), while an anonymous/default delegation keeps its inherited
  explorer engine. An unresolvable routed target falls back on the same shape and reports
  `route-target-unavailable`. A per-run breaker (3 consecutive misses) skips the classifier for
  the rest of the run. **OFF (no taxonomy, or the kill-switch) is byte-identical** to no router.
- It runs in **both** interactive and headless deployments, and the gRPC `RunTeam`-direct
  path is excluded (zero-caps). See [ADR 0031](../adr/0031-subagent-model-router.md) (the
  router) and [ADR 0042](../adr/0042-taxonomy-gated-model-router.md) (the taxonomy-gated
  enable model).
- **Why a delegation was NOT routed rides the wire** (issue #397, ADR 0083): every
  delegation-start event (`subagent.start`, `parallel.branch` `branch_start`, the
  `team.start` roster) carries a bounded `routing_reason` — EMPTY on a routed hit,
  otherwise a bare-metadata gate/miss constant (`router-disabled` / `pinned-model` /
  `agent-def-pinned-model` / `resume` / `fork` / `route-target-unavailable` /
  `breaker-open` / `aborted` / `empty-model`, or a
  static classifier/composition miss code). It lets a UI distinguish router-off from
  pinned-model from agent-def-pinned from classifier-failure from breaker-open, where
  previously every miss collapsed to empty `routed_*`. mecatui renders it as
  ` · not routed: <reason>` on the delegation's model line. Bare metadata only
  (never the task prompt or classifier reasoning — gauntlet #7). Known composition
  detail is reduced to its static code; any other non-allowlisted reason from an external
  engine composition is substituted with a generic `routing-miss` label on the wire,
  with the verbatim text kept in the operator-diagnostics channel.
  `route-target-unavailable` means the classifier picked a model but the relevant engine
  factory declined it; the delegation ran its fallback model, which remains visible in
  the event's ordinary `model` field.
- **Cost note (CWE-770):** an untrusted/peer-injected task prompt can **steer** the
  classifier toward your most-expensive category (the breaker only counts *misses*, not
  steered-but-valid classifications). It is **bounded** — the router can only pick from
  *your* taxonomy, the provider is fixed, and **`--max-run-tokens`** (plus
  `--max-team-tokens` and the per-call `max_run_tokens`) is the actual spend ceiling. The
  budget caps a routed child regardless of the chosen model **and** (since #92) folds each
  classifier call's own token spend into the parent run's cumulative `--max-run-tokens`, so
  repeated classifications cannot run up unbounded classifier cost either. Keep the category
  cost range modest and rely on the token budget as the hard ceiling.

#### Authoring skills & agent definitions for model selection

The router steers a delegation's model from the **operator's** config; a skill or
agent definition does not need to — and should not — name provider-specific model
ids. Two ways a delegation's model is chosen, and how to author for each:

- **Let the router pick (preferred).** Write the skill to describe the *capability*
  needed ("on a high-reasoning model") rather than a slug. A plain `Subagent`
  delegation with no `model`/`agent`/`fork`/`resume` is classified by the router
  into your category taxonomy, so the operator's `models.router:` config — not the
  skill text — decides the model. This keeps the skill provider-agnostic and
  portable across deployments.
- **Pin via an alias.** If a skill needs to force a tier, it can pass
  `Subagent(model="<alias>")`, where `<alias>` is a name the operator bound in
  `models.aliases:`. The alias is the only model vocabulary the model can usefully
  reference; mecatl does **not** inject the alias map into the prompt, so the skill
  must name the alias itself. Note this bypasses the router (an explicit per-call
  `model` wins by precedence), and the alias only resolves to a real model if the
  operator bound it — an unbound alias fail-softs to the inherited default.

The built-in aliases `sonnet`/`opus`/`haiku` default to "inherit the parent model"
until an operator overrides them, so a skill that names them is portable but inert
until configured. For a clean deployment-neutral posture, describe capabilities and
let the router own the mapping.

#### Project-overridable model config, capped by an operator allowlist

A **trusted** project's `.mecatl/settings.yaml` may re-bind `models.default` /
`models.slots` / `models.aliases` — but only to entries the operator **allowlisted**. The
operator declares the cap in the **user-global** `settings.yaml`:

```yaml
# ~/.config/mecatl/settings.yaml  (operator-tier — the cap and the operator's own bindings)
models:
  allowlist:                 # the NON-WIDEABLE cap: alias names and/or concrete ids
    - reasoning
    - anthropic/claude-opus-4.5
  aliases:
    reasoning: gpt-5
  default: gpt-5             # the operator's session default (uncapped — operator is authoritative)
```

```yaml
# <repo>/.mecatl/settings.yaml  (project-tier — honoured ONLY within the allowlist, on a trusted repo)
models:
  default: anthropic/claude-opus-4.5   # accepted (allowlisted)
  slots:
    plan: reasoning                    # accepted (alias resolves to gpt-5, allowlisted)
    compaction: some-unvetted-model    # DROPPED with a WARN (not in the allowlist)
```

- **Opt-in by allowlist.** With **no** operator `models.allowlist`, a project `models:`
  block stays WARN-ignored — **byte-identical** to the default.
- **The allowlist is operator-tier and non-wideable.** A project-tier `models.allowlist:`
  key is always **ignored with a WARN** (a project cannot widen its own cap).
- **Trust-gated.** An **untrusted** workspace's project `models:` block is ignored (the
  same `--trust-project` / `trustedWorkspaces:` gate as a project's allow rules).
- **Resolve-then-check.** Each project binding's value is resolved to a concrete id and
  tested for membership in the (alias-resolved) allowlist set; an allowed binding is
  applied, an out-of-cap one is dropped with a build-once WARN (keeping the
  operator/default value). The cap applies to **every** config-file binding — the session
  `default`, all slots (including `plan`), and aliases.
- **Precedence:** `CLI (--model/--model-slot/--model-alias) > project-YAML (capped) >
  operator-YAML (settings.yaml) > built-in`. A project binding overrides the operator-YAML
  value for the same key, but an explicit operator **CLI flag** for a key still wins (a
  deliberate per-run override). This holds for the session `default` too: an operator
  `models.default:` re-binds the session default over the registry default (the operator's
  own default is **uncapped** — the allowlist caps project bindings only), a capped project
  `default:` can override it, and a CLI `--model` beats both.
- **The allowlist is a flat set, not per-slot.** A model you allowlist may be bound by a
  trusted project to **any** slot — including the `guardrail` and `ask-reviewer` **safety
  checkers**, not just a cheap session default. This stays within the trust you declared
  (the operator approved the model), but it is coarser than "approved models" might
  suggest: **do not allowlist a model you would be unwilling to see used as a safety
  checker.** Per-slot allowlist scoping is a deliberate future follow-up, not a current
  knob.
- **Out of scope (this slice):** the allowlist caps **config-file** bindings only — an
  agent-def `model:` literal and the per-session API `model_id` selector are not capped
  here.

## 5b. OpenRouter downstream-provider routing (`openrouter:`, issue #480)

OpenRouter is a *meta-provider*: a single model id (e.g.
`anthropic/claude-sonnet-4-6`) is served by several **downstream** inference
providers (Anthropic, Amazon Bedrock, Google Vertex, DeepInfra, …). By default
OpenRouter load-balances across them on price. mecatl lets an operator steer which
downstream serves a model **and** see which downstream actually served each turn.
(mecatl's "provider" stays the wire adapter — these are the *downstream* providers
OpenRouter routes to.)

### Steering: per-model preferred downstream order

Set a per-model `order:` in your **operator-tier** `settings.yaml`:

```yaml
# ~/.config/mecatl/settings.yaml  (operator-tier ONLY — NOT a project file)
openrouter:
  models:
    "anthropic/claude-sonnet-4-6":
      order: ["anthropic", "google-vertex"]
      allow_fallbacks: false        # default true when absent
    "openai/gpt-5":
      order: ["deepinfra/turbo"]
```

- `order:` lists downstream provider slugs (lowercase-kebab — e.g. `anthropic`,
  `google-vertex`, `deepinfra/turbo` for an endpoint variant) tried in order.
  **Setting an order disables OpenRouter's default price load-balancing.**
  Base-slug matching applies: `google-vertex` matches all its regions/variants
  (service tiers excepted); use the full slug (`google-vertex/us-east5`,
  `deepinfra/turbo`) to pin one variant.
- `allow_fallbacks:` absent ⇒ OpenRouter's default (`true` — after `order` is
  exhausted, other downstreams are tried). Explicit `false` pins hard to `order`.
  ⚠️ **A hard pin can hard-fail the turn.** With `allow_fallbacks: false`, if
  OpenRouter cannot satisfy *any* downstream in your `order` for that model (it's
  out of policy on your account, unlisted for the model, or transiently
  unavailable), the request fails with a `404 No endpoints found for <model>` and
  the turn errors — there is no silent fallback. (Verified live: a downstream can
  be *listed* as healthy on a model's endpoints and still be unroutable because
  it's out of policy on your OpenRouter account.) List every downstream you'd
  accept, or leave `allow_fallbacks` at its default so an exhausted `order`
  degrades to other downstreams instead of erroring.
- **Operator-tier only.** A project-tier `openrouter:` block is **ignored with a
  WARN** — steering requests to a particular downstream is a spend/compliance/
  capability decision the operator owns (the same gate as `models.default_provider`,
  `models.allowlist`, `models.router`). Invalid slugs / empty orders are dropped
  with a build-once WARN, keeping the rest.

### Observability: which downstream served a turn

For the `openrouter` provider mecatl arms OpenRouter's `X-OpenRouter-Metadata`
opt-in, and the routed downstream echoes back as a `provider.route` event. mecatui
briefly shows `via <display-name>` in the footer and appends
`/<display-name>` to the header's model segment for the current turn (for example,
`Kimi K3/Google`). The next turn clears that suffix before any metadata arrives,
so a cache hit or metadata miss shows no stale route. The event is metadata-only
and degrades to **absent on a cache hit** (OpenRouter strips the metadata from
cached responses): no value is ever fabricated.

**The echo is a display name, NOT your config slug.** You *send* lowercase-kebab
slugs (`google-vertex`); OpenRouter *returns* its own display name for the
downstream (`Google`). These come from two different OpenRouter surfaces (the
request's `provider` object vs. the response's routing metadata) and use different
vocabularies. mecatl relays the display name **verbatim** for the status echo and
deliberately does *not* try to map it back to a config slug (guessing a slug we
didn't receive could be wrong — e.g. `"Google"` → `google` ≠ `google-vertex`). So
don't string-match the echo against your `order:` list; treat it as a human
readout, not a round-trippable identifier.

See the [configuration reference](../configuration-reference.md#openrouter) for the
full key listing and [ADR 0210](../adr/0210-openrouter-downstream-provider-steering.md)
for the design.
