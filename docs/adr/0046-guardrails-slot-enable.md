# ADR 0046 — Guardrails slot enables (configure = enable)

- Status: Accepted
- Date: 2026-06-23
- Scope: composition (`internal/app`) + the cmd flag layer (`cmd/mecated`, `cmd/mecatequi`) + the operator-tier `models.slots:` / `guardrails:` config subtrees. NO engine (`engine/agent`) change — the modelhook Runner, the recursion guard, the fail-open/closed posture, the verdict parse, and the rule compilation from [ADR 0021](./0021-guardrails.md) are REUSED unchanged. No `port.LLMRequest`, proto, or wire-contract change.
- Supersedes: the ENABLE-MODEL decision in [ADR 0021](./0021-guardrails.md) ONLY (the "`guardrails.model:` is the on/off gate; a `guardrail` model slot routes an already-enabled checker but does not enable it" clause — the Phase-2 slot wiring added under ADR 0030). 0021's engine half, the threat model, the rule/mode semantics, the operator-tier-only enforcement, the cost caps, and the sanitize-laundering defense are NOT superseded — they carry over verbatim.
- Superseded by: none

## Context

[ADR 0021](./0021-guardrails.md) shipped the LLM-backed guardrails as a **configure = enable**
feature: configuring a checker model (`--guardrails-model` / `guardrails.model` YAML) turns
the checker ON, with `--guardrails=off` as the kill-switch. [ADR 0030](./0030-model-selection-heuristics.md)
Phase 2 then added the `guardrail` **model slot** (`--model-slot guardrail=…` /
`models.slots.guardrail`) so the checker could run on a different (e.g. cheaper) model than
the gate value — but the slot was wired as **routing-only**: it superseded the checker model
ONLY when `--guardrails-model` was already set (the slot could not enable the checker on its
own). The slot was a second-class citizen of the enable model: a `guardrail` slot bound with
no `--guardrails-model` left guardrails silently OFF.

That created two UX warts, both reported in issue #159:

1. **The slot inverted the guardrails-parity enable model the router had just adopted.**
   [ADR 0042](./0042-taxonomy-gated-model-router.md) aligned the subagent model router to
   the guardrails precedent: configuring a `models.router:` taxonomy ENABLES the router
   (configure = enable). The guardrail slot — the guardrails feature's OWN slot — did the
   opposite, requiring a separate flag on top of an already-explicit, already-tier-restricted
   binding. An operator who bound `models.slots.guardrail: cheap` and forgot
   `--guardrails-model` got a silently-inert checker, the exact "configured but inert"
   footgun ADR 0042 removed for the router.
2. **The build-time `guardrails ACTIVE` line reported the WRONG model.** `normalizeGuardrailsModel`
   (`internal/app/build.go`) validated only the GATE value (`cfg.GuardrailsModel`) and logged
   THAT as `"model"`, never running the slot resolution. So under a `guardrail` slot
   superseding the gate value, the startup line named the inert gate model, not the resolved
   checker model a live session actually ran. An operator reading diagnostics to confirm
   which model was inspecting their tool content saw a lie.

The ask-reviewer slot is the documented sibling trap: it is a bare model *id* (a value, not
a structured enable), and its flag stays the on/off gate. It is explicitly OUT OF SCOPE for
#159 (left for a follow-up); this ADR touches the guardrail slot only.

## Decision

**A bound `guardrail` model slot ENABLES guardrails (configure = enable, the ADR-0042 parity).
The `--guardrails-model` flag becomes one of TWO enable paths, and the build-time posture line
reports the RESOLVED checker model + its provenance.** Concretely:

- The enable gate widens. `guardrailsConfigured` (`internal/app/guardrails.go`) changes from
  `!GuardrailsDisabled && GuardrailsModel != ""` to
  `!GuardrailsDisabled && (GuardrailsModel != "" || selectorForSlot(cfg, slotGuardrail) != "")`.
  A bound `guardrail` slot — explicit OR via the `cheap`-tier fall-through
  (`slotDefaultTier[slotGuardrail] == slotCheap`, so binding only `cheap=…` also enables) —
  turns the checker ON. The kill-switch (`--guardrails=off` / `GuardrailsDisabled`) still wins
  absolutely. Resolution precedence is UNCHANGED: a bound slot SUPERSEDES the gate value's
  model when both are set.
- A SINGLE pure resolver is the source of truth for the resolved checker model + provenance:
  `resolveGuardrailsCheckerModel(cfg) (model string, src guardrailSource, configured bool)`
  (`internal/app/guardrails.go`), where `guardrailSource ∈ {srcNone, srcGate, srcSlot,
  srcSlotSupersedingGate}`. Both `buildGuardrailsChecker` (the live checker builder) and the
  new posture line call it, so the reported posture and the live checker can never disagree.
  The precedence reproduces the pre-#46 `buildGuardrailsChecker` byte-for-byte (slot → gate →
  nothing; UseMock gate-only passes the literal through verbatim).
- The build-time `guardrails ACTIVE` emit is REMOVED from `normalizeGuardrailsModel` (it
  becomes validate-only, returning the gate value). It is REPLACED by `logGuardrailsPosture`
  (`internal/app/build.go`), called from `Build` right after `logModelRouterFacts` (alongside
  the other build-once fact emitters). It emits EXACTLY ONE `cfg.diag().Log(LevelInfo, …)`
  line per Build, every state:
  - kill-switch active → `guardrails: OFF (kill-switch active via --guardrails=off)…`
  - nothing configured → `guardrails: OFF (no checker model configured; bind the \`guardrail\` model slot or set --guardrails-model to enable)`
  - configured → `guardrails: ON, checker=<resolved> (via <provenance>), mode=<advisory|block|sanitize>, rules=N[ (default set: WebSearch, WebFetch, mcp__*)]`
  - provenance: `srcSlot` → `via slot \`guardrail\``; `srcSlotSupersedingGate` → `via slot \`guardrail\`, supersedes gate value \`<gateval>\``; `srcGate` → `via --guardrails-model`.
- `--help` / the `--guardrails=on` startup error name BOTH enable paths
  (`--guardrails-model` AND the `guardrail` slot). The flag stays the kill-switch only.

### The byte-identical OFF-by-default guarantee

With NOTHING configured, `guardrailsConfigured` is false ⇒ `buildGuardrailsHooks` returns
`inner` UNCHANGED (same pointer, pinned by `TestGuardrailsOffReturnsInnerUnchanged`). The
ONLY behavioural delta for a configure-nothing operator is ONE explicit `guardrails: OFF`
INFO line (the desired fix — OFF is stated, not inferred from silence). The hook wiring, the
recursion guard, the fail-open/closed posture, the operator-tier-only RULES enforcement, and
the cost caps are all untouched.

### Project-tier slot binding

Under [ADR 0030](./0030-model-selection-heuristics.md) Phase 4, a TRUSTED project's
`models.slots.guardrail=` binding CAN be honoured within the operator allowlist. Under the
widened gate, such an honoured project slot binding WOULD enable guardrails — consistent with
the router (a project `models.router:` taxonomy within the allowlist enables the router). The
operator-tier RULES gate is untouched (a project `guardrails:` RULES block is still ignored
with a WARN — a project cannot configure or weaken a checker). This is documented in
[`docs/usage.md`](../usage.md); it is not a blocker.

## Consequences

**Easier / better:**

- The "bound a guardrail slot but forgot the flag" silent-failure mode is GONE: a bound slot
  enables the checker.
- The startup posture line is HONEST — it names the RESOLVED checker model (slot or gate),
  not the inert gate value a superseding slot overrode. An operator can trust the one line to
  confirm what is inspecting their tool content.
- Full enable-model parity with the router ([ADR 0042](./0042-taxonomy-gated-model-router.md))
  and with the guardrails feature's own original ADR-0021 posture (configure = enable).

**Behaviour change (the honest cost):** under the prior design a `models.slots.guardrail:` /
`--model-slot guardrail=…` binding was inert unless `--guardrails-model` was also set; now the
slot alone enables the checker. An operator who wants a slot bound-but-inert must say so
explicitly (`--guardrails=off` / `guardrails: { disabled: true }`). The feature is
PRE-ADOPTION (no installed base relying on slot-only-inert), so there is no
backward-compat / deprecation concern. The startup line changes from `guardrails ACTIVE`
(argless form) to `guardrails: ON, checker=<model> …` (richer form) — an operator grepping
diagnostics for `guardrails ACTIVE` must update their grep to `guardrails: ON`.

**Unchanged:** the engine-layer routing (the modelhook Runner, the PreToolUse/PostToolUse
wiring, the verdict parse), the recursion guard (checker engine via child deps, inert hooks,
no nested reviewer), the fail-open/closed posture, the sanitize-laundering defense, the
operator-tier-only RULES enforcement, the default advisory rule set, the cost caps, and the
kill-switch absoluteness — all carry over from [ADR 0021](./0021-guardrails.md) verbatim. The
ask-reviewer slot's enable model is UNCHANGED (sibling trap, out of scope).

## See also

- [ADR 0021](./0021-guardrails.md) — the guardrails feature this supersedes the ENABLE-MODEL
  clause of (everything else carries over).
- [ADR 0042](./0042-taxonomy-gated-model-router.md) — the router enable precedent this aligns
  the guardrail slot with (configure = enable).
- [ADR 0030](./0030-model-selection-heuristics.md) — the model-slots layer (Phase 2 wired the
  `guardrail` slot routing-only; this ADR widens it to enable).
- The documentation lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
- Living docs: [`docs/usage.md`](../usage.md) (the guardrails section + the `--guardrails-model`
  / `--guardrails` flag rows + the per-slot models section),
  [Historical implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) (guardrails enable
  mechanics).
