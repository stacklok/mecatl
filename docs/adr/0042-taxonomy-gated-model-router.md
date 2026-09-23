# ADR 0042 — Taxonomy-gated subagent model router (enable by config, not a flag)

- Status: Accepted
- Date: 2026-06-22
- Scope: composition (`internal/app`) + the cmd flag layer (`cmd/mecated`, `cmd/mecatequi`) + the operator-tier `models.router:` config subtree. NO engine (`engine/agent`) change — the classifier, the breaker, the precedence, and the observability from [ADR 0031](./0031-subagent-model-router.md) are REUSED unchanged. No `port.LLMRequest`, proto, or wire-contract change.
- Supersedes: the ENABLE-MODEL decision in [ADR 0031](./0031-subagent-model-router.md) ONLY (the "operator-tier only / the ENABLE gate is a FLAG" paragraph). 0031's engine half (`RunModelRouter`), the composition `buildModelRouterTask` mechanics, the per-run breaker, the precedence gating, the fail-soft posture, the observability, and the cost-amplification analysis are NOT superseded — they carry over verbatim.
- Superseded by: none

## Context

[ADR 0031](./0031-subagent-model-router.md) shipped the semantic Subagent model router
as an **opt-in CLI flag** (`--subagent-model-router`, default `false`), with the category
taxonomy living in the operator-tier `models.router:` subtree of the user-global
`settings.yaml`. The flag was the ENABLE gate; the taxonomy was the configuration. 0031
justified the flag as "deliberately NOT a permission-config key — autonomous
per-delegation model selection is a spend/capability decision the operator owns, the same
posture as `--subagent-ask-reviewer`."

Lived experience showed that rationale does not hold, and the flag creates a
silent-failure UX wart:

1. **The taxonomy is already operator-tier-only.** `foldOperatorModelRouter` reads from
   `OperatorModelPolicy()` (user-global `settings.yaml` + CLI); a project-tier `router:`
   is stripped with a WARN. So "the operator owns the spend" is ALREADY satisfied by the
   tier gate — the flag was a second gate on top of an already-explicit, already-tier-
   restricted configuration.
2. **Authoring `models.router.categories` with per-category model ids IS the opt-in to
   spend.** An operator who writes a taxonomy has already made the spend decision. The
   flag added no consent the taxonomy did not already express — it only created a
   "configured but inert" footgun: write the taxonomy, forget the flag, get a startup
   WARN, no routing, no error.
3. **The guardrails sibling — also an autonomous LLM spend — uses the opposite model.**
   Guardrails ([ADR 0021](./0021-guardrails.md)) is enabled by CONFIGURING a checker
   model (`--guardrails-model` / `guardrails:` YAML), with `--guardrails=off` as a
   kill-switch. A configured model with no rules is ON with a default advisory set. The
   router inverted this — flag-to-enable, taxonomy-to-configure — for no rationale that
   survives. The ask-reviewer is flag-only, but it is a bare model *id* (a value, not a
   bool) with no structured YAML — a different shape.

The one real point in the flag's favour — process-arg visibility for an autonomous-spend
capability in a systemd unit — is weak: the spend comes from the category model ids in
the same YAML file the config lives in, and `logModelRouterFacts` already emits a
build-once "router ACTIVE" INFO naming the category count and classifier model, so the
spend is observable in diagnostics either way.

## Decision

**The TAXONOMY enables the router; the flag is repurposed to a kill-switch.** This is the
guardrails-parity enable model (configure = enable).

Concretely:

- The router is ON **iff** a non-empty operator taxonomy is configured
  (`len(cfg.RouterCategories) > 0`) AND it is not explicitly disabled. The guard in
  `buildModelRouterTask` changes from `!cfg.SubagentModelRouter || len(cats)==0` to
  `cfg.RouterDisabled || len(cats)==0`. An empty taxonomy still returns `nil` — the
  byte-identical OFF-when-unconfigured invariant (the real guarantee, the taxonomy, not
  the flag) is preserved.
- The `app.Config.SubagentModelRouter` enable bool is REMOVED (a now-unused enable gate
  is dead code) and replaced with `app.Config.RouterDisabled` — the kill-switch field,
  documented like `GuardrailsDisabled`.
- A new YAML kill-switch key **`disabled: true`** under `models.router:` (parsed
  strictly, like the rest of the subtree). `foldOperatorModelRouter` ORs it onto
  `cfg.RouterDisabled`, mirroring `foldOperatorGuardrails`' handling of `guardrails:
  disabled`.
- The `--subagent-model-router` CLI flag becomes a KILL-SWITCH (both `mecated` and
  `mecatequi`), detected via `fs.Visit`:
  - **unset** → the router is governed by taxonomy presence (`RouterDisabled` stays
    false unless the YAML disables it).
  - **`--subagent-model-router=false`** → the kill-switch → `RouterDisabled = true`
    (forces the router OFF despite a taxonomy).
  - **bare `--subagent-model-router` / `=true`** → a harmless no-op: it does NOT set
    `RouterDisabled` (the router stays governed by the taxonomy). The feature is
    PRE-ADOPTION — there is no backward-compat / deprecation concern, so the bare form is
    simply inert (it neither enables nor disables), not a deprecated enable.
- `logModelRouterFacts` is rewritten: no taxonomy ⇒ SILENT (byte-identical OFF); taxonomy
  + `RouterDisabled` ⇒ a one-time DISABLED WARN (configured but kill-switched); taxonomy
  + not disabled ⇒ the existing "subagent model router ACTIVE" INFO. The old "flag set
  but no taxonomy → WARN" state is GONE (there is no enable flag to set).

### The `disabled:` vs `enabled:` deviation from the issue

Issue #138 proposed an `enabled: false` override. We chose **`disabled: true`** instead,
to mirror `GuardrailsSection.Disabled` (`guardrails: { disabled: true }`) exactly. The
guardrails precedent is the one this ADR aligns the router with end-to-end (configure =
enable, with a `disabled:` kill-switch), so the on-disk vocabulary should match its
sibling rather than introduce a second, inverted boolean (`enabled: false`) for the same
"temporarily off" intent. A `disabled:` field also reads naturally as the YAML twin of
the CLI kill-switch direction (`--subagent-model-router=false` and
`models.router.disabled: true` are the same decision in two surfaces, OR'd together).

## Consequences

**Easier / better:**

- The "forgot the flag" silent-failure mode is GONE: a configured taxonomy routes.
- The gate is co-located with the config it gates (the operator's own point in the
  conversation that prompted #138).
- A taxonomy in the operator-global `settings.yaml` enables the router for EVERY binary
  — including mecatui's embedded server — with no per-binary flag wiring, which makes the
  #137 mecatui-flag-wiring gap largely moot.
- Full parity with the guardrails enable model.

**Behaviour change (the honest cost):** under the prior design a `models.router:`
taxonomy was inert until `--subagent-model-router` was passed; now the taxonomy alone
enables the router. An operator who wants that taxonomy present-but-inert must say so
explicitly (`disabled: true` or `--subagent-model-router=false`). This is the deliberate
inversion — "configured but inert by default" was the footgun being removed. The feature
is PRE-ADOPTION, so there is no installed base relying on the old flag-to-enable
semantics and therefore no backward-compat / deprecation concern: the bare flag still
parses, but it is simply a harmless no-op (the router stays governed by the taxonomy).

**Unchanged:** the engine-layer routing (`RunModelRouter`, the category→model mapping,
the precedence gating `explicit > agent-def > fork/resume > router > inherited`), the
per-run circuit breaker (`modelRouterBreaker`), the classifier engine construction, the
fail-soft posture, the `RoutedCategory`/`RoutedModel` observability, the no-nesting guard,
and the cost-amplification (CWE-770) bounding by the token budget — all carry over from
ADR 0031 verbatim. The team/parallel routers ([ADR 0034](./0034-team-parallel-model-routing.md),
[ADR 0035](./0035-per-delegation-model-surface.md)) reuse the SAME `routeTask` closure /
engine-factory seam, so they inherit the new enable model with no further change.

## See also

- [ADR 0031](./0031-subagent-model-router.md) — the router this supersedes the ENABLE
  model of (everything else carries over).
- [ADR 0021](./0021-guardrails.md) — the guardrails enable precedent this aligns with
  (configure = enable, `disabled:` kill-switch).
- [ADR 0034](./0034-team-parallel-model-routing.md) and
  [ADR 0035](./0035-per-delegation-model-surface.md) — the team/parallel routers that
  reuse the same closure and inherit this enable model.
- [ADR 0077](./0077-direct-write-subagent.md) — the narrow-supersession wording template
  this ADR follows.
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
- Living docs: [`docs/usage.md`](../usage.md) (the `--subagent-model-router` kill-switch
  + the `models.router:` YAML block with `disabled:`),
  [`docs/architecture/providers.md`](../architecture/providers.md) (the router section),
  [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) (router
  enable mechanics).
