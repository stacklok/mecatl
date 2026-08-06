# ADR 0101 — Surface the routing-miss reason on the delegation wire

- Status: Accepted
- Date: 2026-08-06
- Scope: the delegation observability wire projections (`Subagent`, `TeamMemberSpec`, `Parallel`) and the internal `parentCaps.routeTask` seam
- Supersedes: —
- Superseded by: —

## Context

The opt-in semantic model router ([ADR 0031](./0031-subagent-model-router.md), extended
to team members and Parallel branches by [ADR 0034](./0034-team-parallel-model-routing.md))
puts its *hit* half on the wire: `routed_category` / `routed_model` on `subagent.start`,
the `team.start` roster, and `parallel.branch_start`. Every *miss* or *gate* case
collapses to those two fields being empty — yet the *why* already exists, and is
already vetted safe:

- the closed `RouterMiss*` set (`degenerate-input` / `classifier-error` / `cancelled` /
  `bad-verdict` / `unknown-category`) naming a classifier miss;
- the composition-authored mapping-miss strings (`category-selector-empty` /
  `category-target-unresolvable`);
- the engine-owned gates that keep the classifier from firing at all (an explicit
  per-call `model`, an agent-def pin, a `resume`, a `fork`, a writable delegation with
  no writable factory, the per-run circuit breaker open, no router wired).

Today the only sink for any of that is an operator diagnostics INFO line. A UI reading
the client event stream cannot distinguish "router disabled / explicitly pinned /
def-pinned / inherited default" from "classifier failed" or "breaker-open fallback" —
they all read identically as `routed_*=""` plus a `model` id. ADR 0031 deliberately
scoped the wire as a follow-up; ADR 0034 landed the hit fields; ADR 0035 added the
unconditional `model`. The miss-reason half is the remaining gap (issue #367).

The reason strings are bare metadata by construction — "never the task prompt or the
classifier's output" — so putting them on the wire crosses no new trust boundary
(gauntlet #7): a child's content still never reaches the parent conversation.

## Decision

Expose a single additive `routing_reason` string on the three delegation wire
projections, populated only on a non-routed outcome and empty on a routed hit (where
`routed_category` / `routed_model` carry the classification instead):

- `Subagent.routing_reason` = field 18, `TeamMemberSpec.routing_reason` = field 8,
  `Parallel.routing_reason` = field 25 — additive proto fields, next free numbers,
  generated (never hand-edited).
- Mirrored as `RoutingReason` on `session.SubagentPayload` / `session.TeamMemberSpec` /
  `session.ParallelPayload` (Added/minor core API), threaded through the server mapper
  and the mecatui client view-model.

The value is one of:

- a **closed harness label** for the gates the engine owns — `pinned-model`,
  `agent-def-pinned-model`, `resume`, `fork`, `writable-unroutable`, `breaker-open`,
  `router-disabled`, `not-routed` (the `RoutingReason*` constants);
- a **`RouterMiss*` value** for a classifier miss; or
- a **clamped composition-authored mapping-miss string** — whitespace-collapsed and
  rune-clamped at the emit site (the `subagentCausePayload` discipline), because it
  embeds an operator-controlled category/selector.

Mechanically: widen the *internal* `parentCaps.routeTask` to also return the reason
(the exported `Deps.SubagentModelRouter` already returned it, so no exported signature
changes); synthesize `breaker-open` at the breaker-skip early return and
`router-disabled` at the `routeTask == nil` gate; let each `maybeRoute*` gate name its
own closed label. The three-families-shared dispatch chokepoint already logged the
reason — this is additive plumbing at an existing seam, not new collection. The
"loop emits exactly three diagnostics lines" invariant is untouched: the reason rides
events, not the diag log.

The investigation's optional `WithRouterConfigured(bool)` composition→engine bit is
**not** taken: `router-disabled` synthesized at the `routeTask == nil` gate already
covers every disabled case (no taxonomy, kill-switch, child run, zero-caps) uniformly,
so no new composition bit is needed.

## Consequences

- A UI can now tell the five cases apart from the wire alone; the operator diagnostics
  line and the wire reason agree byte-for-byte on a classifier/mapping miss.
- Consumers must branch only on the **closed** `RoutingReason*` / `RouterMiss*` labels;
  the composition-authored mapping-miss strings are free-form and not a stable API.
- The structural leak guards (`TestTeamMemberSpecHasNoContentFields`,
  `TestParallelPayloadHasNoContentFields`, the subagent payload allowlist) and the
  behavioral gauntlet test are extended to cover `RoutingReason`, so a future content
  field riding this surface fails CI.
- Cost: one more metadata field per delegation event, and a widened internal closure
  signature. No new trust boundary, no new diagnostics line, no proto enum.

## See also

- [ADR 0031](./0031-subagent-model-router.md) — the router (wire scoped as a follow-up).
- [ADR 0034](./0034-team-parallel-model-routing.md) — the hit fields on the wire.
- [ADR 0035](./0035-per-delegation-model-surface.md) — the unconditional `model` field.
- The lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
