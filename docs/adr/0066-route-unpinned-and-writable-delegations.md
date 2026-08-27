# ADR 0066 — Route unpinned agent-defs and writable explorers; explicit `inherit` is the pin

- Status: Accepted
- Date: 2026-07-21
- Scope: `engine/agent` (the `Subagent` tool's engine-selection + router gate) + `internal/app` (the composition factories + the routable-def set). No `port.LLMRequest`, proto, or wire-contract change; the classifier/breaker/observability of [ADR 0031](./0031-subagent-model-router.md) are REUSED unchanged.
- Supersedes: NARROWLY — (a) the "the per-call `model` arg does not re-engine a writable explorer, an accepted v1 residual" sentence of [ADR 0077](./0077-direct-write-subagent.md) ONLY (everything else in 0077 — direct-write, no-fork/no-merge, the `parentMutatingCaller` dispatch-serial seam, the writable specialist of [ADR 0058](./0058-writable-named-specialist-subagent.md) — stands); (b) the agent-gating rule of [ADR 0031](./0031-subagent-model-router.md) that a named `agent` is NEVER routed (0031's engine half, breaker, precedence spine, fail-soft posture, and observability are otherwise unchanged; this AMENDS the gate, mirroring how [ADR 0042](./0042-taxonomy-gated-model-router.md) narrowly superseded 0031's enable-model).
- Superseded by: [ADR 0242](./0242-route-unpinned-writable-named-specialists.md) — ONLY the exclusion of writable named specialists from semantic routing; the absent-versus-explicit model-intent rule, writable explorer routing, precedence, and fail-soft posture remain authoritative

## Context

The semantic Subagent model router ([ADR 0031](./0031-subagent-model-router.md), enabled by
taxonomy presence per [ADR 0042](./0042-taxonomy-gated-model-router.md)) classifies a
delegation's task and mints the child on the picked model. As shipped it fired for exactly
ONE shape: a PLAIN read-only default delegation (no `model`, no `agent`, no `fork`, no
`resume`). Two adjacent shapes were left on the operator's default model even when the
operator had opted into routing — the gaps this ADR closes:

1. **Writable explorers (issue #285).** A `mode:"read-write"` delegation with no `agent`
   ran the WRITABLE explorer on its default model regardless of a per-call `model` OR the
   router pick. [ADR 0077](./0077-direct-write-subagent.md) recorded this as an "accepted v1
   residual": the writable child engine had no per-model factory, and an unconditional
   clobber in `resolveEngineAndLimits` discarded whatever engine selection had chosen. So
   `Subagent{mode:"read-write", model:"X"}` silently ignored `X`, and a routed writable
   delegation silently ignored the pick.

2. **Unpinned agent-defs (issue #286).** [ADR 0031](./0031-subagent-model-router.md) gated
   routing on `args.Agent == ""` — ANY named `agent` pinned its own engine, so a specialist
   that declared NO `model:` (expressing no model intent) was nonetheless never routed. An
   operator with a router taxonomy and a stable of unpinned specialists got routing for
   anonymous delegations but not for named ones — an inconsistency with no principled basis.

Both are the same class of bug: an expressed-no-intent delegation not benefiting from the
routing the operator turned on. The fix must preserve every PINNED intent verbatim (a
per-call `model`, a def's own `model:`, `fork`, `resume`), stay FAIL-SOFT (the router is
never load-bearing), and never spend the classifier when the pick could not be consumed.

## Decision

**One precedence ladder governs every `Subagent` engine selection:**

    per-call `model`  >  def `model:` (incl. explicit `inherit`)  >  router pick  >  SubagentModel default  >  session model

Applied to the two closed gaps:

### (a) Writable explorers honour the per-call model + the router pick (issue #285)

- New engine seam `agent.WithWritableEngineFactory(func(model string) (*Engine, bool))` →
  `SubagentTool.writableEngineFactory`, minted in composition by
  `buildWritableSubagentEngineFactory`, which shares the `writableExplorerDeps` recipe with
  `buildWritableSubagentChildEngine` (read-only explorer catalog + Edit + Write, the MAIN
  command runner, direct-write parity) so the default and per-model writable engines cannot
  drift. The routed/override id is used VERBATIM (never re-run through
  `resolveDefaultChildModel`).
- `selectChildEngine`'s writable-explorer arm (`selectWritableExplorerEngine`) resolves a
  per-call `model` (a LOUD error if the factory is unwired or the model is unroutable —
  never a silent inherit), else the router pick (FAIL-SOFT to `writableChildEngine`), else
  the default writable explorer — and RETURNS before the read-only arms, so a writable call
  never runs a read-only engine.
- The unconditional writable clobber in `resolveEngineAndLimits` is DELETED; only a writable
  `resume` still swaps to `writableChildEngine` (its own engine, unchanged).
- `validateMode` rejects `read-write`+`model` (no `agent`) when the writable factory is
  unwired (the no-FS gate + the honest-error posture).

### (b) Unpinned agent-defs are routable; explicit `model: inherit` is the pin (issue #286)

- ROUTABLE ⇔ `TrimSpace(def.Model) == ""`. ANY non-empty `def.Model` — `inherit`, a built-in
  alias (`sonnet`/`opus`/`haiku`), an unknown alias, or a concrete id — is EXPRESSED INTENT
  and PINNED; its resolution is untouched. Explicit `inherit` is therefore the way an
  operator OPTS a def OUT of routing.
- New engine seam `agent.WithRoutableAgents([]string)` → `SubagentTool.routableAgents`, the
  composition-computed set (`routableAgentNames`). `selectReadOnlyAgentEngine` applies the
  routed pick by rebuilding the def's SCOPED engine on it via the existing agent+model
  factory (`WithAgentModelEngineFactory`), FAIL-SOFT to the pre-built def engine on a
  decline; per-def limits are untouched (only the engine swaps).
- The router gate (`(*SubagentTool).maybeRouteModel` → `routeGateOpen`) is the classifier-
  spend guard: it consults the classifier ONLY when the pick could be consumed — a plain
  writable delegation needs the writable factory; a named-agent delegation needs a routable,
  read-only def with the agent+model factory wired. A pinned def, a writable specialist, or
  an unwired factory never spends the classifier.

**Composition excludes from the routable set** (avoiding wasted classifier spend on a pick
that could never mint): a def whose `provider:` resolves to a KNOWN provider ≠ the parent's
(routed ids are parent-provider ids) and a def with INLINE MCP servers (the agent+model
factory declines those — a v1 scope limit). An UNKNOWN `provider:` falls back to the parent
and stays routable. `routableAgentNames` is side-effect-free (the per-def provider/MCP WARNs
are emitted once at the real engine build, not re-logged here).

**Out of scope (unchanged):** team members and Parallel branches do not consult this
agent-def routing path (they have their own model resolution); `read-write`+`agent`+`model`
stays REJECTED (a writable specialist runs on its own resolved model); the writable
specialist (`read-write`+`agent`) is never re-routed.

## Consequences

- **Easier:** an operator who turns on the router now gets consistent behaviour across
  anonymous, named-unpinned, and writable delegations — "route my subagents by task" means
  all of them, without per-shape surprises. A writable subagent can be pinned to a cheaper
  model for a wide edit sweep. Pinning a def to its own model is still one line (`model:`),
  and opting a def OUT of routing is the explicit, discoverable `model: inherit`.
- **Harder / committed:** `def.Model: inherit` now carries SEMANTIC weight (pin-and-inherit)
  distinct from absent-`model:` (routable) — a subtlety operators must learn (documented in
  usage/model-routing.md). The routable-set computation is a new composition input that must
  stay in lockstep with the agent+model factory's decline conditions (provider switch,
  inline MCP) — a drift would spend the classifier for a pick that can't mint (fail-soft, so
  a correctness-safe waste, guarded by the `routableAgentNames` matrix test). Two new engine
  option constructors widen the importable surface (both Added/minor).
- **Fail-soft preserved end to end:** every new path degrades to the pre-existing engine (the
  default writable explorer, or the pre-built def engine) on any miss/decline, and every miss
  logs the per-miss reason INFO (issue #287) — the router remains never load-bearing.

## See also

- Living docs: [providers.md](../architecture/providers.md) (the router precedence ladder +
  def/writable routing), [IMPLEMENTATION-NOTES.md](../design/IMPLEMENTATION-NOTES.md) (the
  PRECEDENCE-by-gating mechanics), [usage/model-routing.md](../usage/model-routing.md) (what
  routes + how to pin a def).
- Related ADRs: [0031](./0031-subagent-model-router.md) (the router), [0042](./0042-taxonomy-gated-model-router.md) (taxonomy-gated enable), [0077](./0077-direct-write-subagent.md) (direct-write writable Subagent), [0058](./0058-writable-named-specialist-subagent.md) (writable named specialist), [0030](./0030-model-selection-heuristics.md) (the model-selection scheme), and the lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
