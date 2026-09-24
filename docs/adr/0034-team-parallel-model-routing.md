# ADR 0034 — Extend the model router to team members and Parallel branches

- Status: Accepted
- Date: 2026-06-18
- Scope: composition (`internal/app`) + the engine team-supervisor (`engine/agent/teamsupervisor.go`) and Parallel (`engine/agent/parallel.go`) delegation paths. No new config, no `port.LLMRequest`, no proto/wire change. Reuses the [ADR 0031](./0031-subagent-model-router.md) `models.router:` taxonomy + the `--subagent-model-router` enable flag verbatim.
- Supersedes: none (it EXTENDS [ADR 0031](./0031-subagent-model-router.md) to the other two delegation families)

## Context

[ADR 0031](./0031-subagent-model-router.md) shipped the operator-gated semantic model
router for ONE delegation family: a plain `Subagent` call's task prompt is classified into
an operator-defined category whose model the child is minted on. The other two families —
agent-TEAM members and PARALLEL branches — were left out: a team member and a Parallel
branch still inherited the def-less default child model (`SubagentModel > parent`),
regardless of how heterogeneous their work was. A team whose lead does deep architecture
while a worker renames a variable, or a Parallel fan-out exploring a hard refactor against
a trivial cleanup, paid the same model for both.

The routing PRIMITIVE was already family-agnostic. ADR 0031's `parentCaps.routeTask`
closure — bound once per run by the dispatcher (`Engine.parentCaps`), holding the per-run
breaker mutex across BOTH the classification AND the classifier-usage fold into the parent
session (the #92 CWE-770 fix) — is threaded into the `ParallelTool` and the team
`Supervisor` ALREADY (both hold `parentCaps`). So extending the router was a question of
CONSUMER-SIDE gating + composition factory plumbing, not a new engine primitive: no new
breaker, no new usage-fold path, no change to `RunModelRouter` / `buildModelRouterTask` /
the config.

Two seam questions had to be answered per family, because the two families differ in WHEN
a child's engine is built:

- A team member's engine is built ONCE at `AddMember` and reused across rounds via
  `Reopen`; its `MemberSessionID(teamID, member)` is stable for the member's life. Routing
  per-round would force a forbidden clone-and-swap and break the id↔engine stability.
- A Parallel branch's child engine is the single shared `childEngine`, used by every
  branch; a branch runs once.

## Decision

Extend the router to both families, reusing `caps.routeTask` verbatim, with a per-family
seam that respects each family's engine lifetime. Precedence per path:
**per-call/explicit model > def model > router > inherited default** — the router fills the
gap, never overrides an explicit or def-pinned choice.

**Team members — route ONCE at AddMember, off the member spec.** The supervisor (which owns
`s.caps`) classifies each PLAIN UNDEFINED member (`spec.AgentType == ""`) exactly once in
`AddMember`, off its `InitialPrompt` (falling back to the member name when empty), and
threads the resulting concrete model id into the per-member factory. A DEFINED member is
skipped — its agent def pins its own model. `AddMember` runs SERIALLY on the single
Team-tool dispatch goroutine, OUTSIDE the round `errgroup`, so members classify one at a
time. Decide-once: `Reopen` across rounds never re-routes. The `MemberEngine` /
`TeamMemberEngineFactory` / `server.MemberEngineFactory` signatures gain a `routedModel
string` parameter (the supervisor owns the route decision; composition substitutes it for
the default child model on the undefined branch only). The lead is routed too in v1.

**Parallel branches — route per-branch in runBranch, via an engine factory.** `ParallelTool`
gains an OPTIONAL `engineFactory func(model string)(*Engine,bool)` (the exact
`WithSubagentEngineFactory` shape) injected by composition. In `runBranch` — once per branch,
on the per-branch goroutine — `caps.routeTask` classifies the branch's composed prompt; on
a hit the branch runs on `engineFactory(routedModel)`, on a miss/unwired the shared
`childEngine` (byte-identical). The breaker mutex inside `caps.routeTask` serialises the
concurrent branch classifications + the parent-usage folds, so no new lock is added to
`parallel.go`.

**Observability.** `session.ParallelPayload` (branch_start) and `session.TeamMemberSpec`
(the EvTeamStart roster entry) each gain `RoutedCategory` / `RoutedModel` string fields,
mirroring `SubagentPayload`. They are BARE METADATA — a category label and a concrete model
id — never the member role/branch prompt or the classifier's reasoning (gauntlet #7).

> **Update (proto/client wire landed).** This ADR's original slice scoped the routed fields
> to the engine struct + diagnostics, with the proto/client wire as a deliberate follow-up.
> That follow-up has since landed (mirroring ADR 0031's #110): `routed_category`/
> `routed_model` are now proto fields on the `TeamMemberSpec` (team.start roster, 5/6) and
> the `Parallel` event (branch_start, 19/20), populated by `toProtoTeam`/`toProtoParallel`,
> carried by the mecatui client structs, rendered as a muted `routed: <category> → <model>`
> cue on the ctrl+a Teams roster + Parallel group-focus branch rows, and asserted on the
> wire by the live e2e specs. Still bare metadata only (gauntlet #7).
>
> **Update (generic per-delegation model — ADR 0035, issue #112).** The routed fields answer
> "the router chose this" only; the common cases (router off, inherited/default, agent-def
> pin, per-call override) surfaced no model at all. ADR 0035 widens the wire with a generic
> `model` field on the same three payloads (`Subagent` 13 / `TeamMemberSpec` 7 / `Parallel`
> 21) carrying the concrete model the child ACTUALLY ran on, set unconditionally. When
> routed, `model == routed_model`; the `routed_*` provenance signal this ADR introduced
> stays unchanged.

The category→model→engine mapping stays entirely in composition (`internal/app`): the
member factory and the new `buildParallelEngineFactory` build the routed engine through
`newChildEngineForProvider` / `parallelChildDeps`'s `childEngineDepsForProvider` so the
override child re-derives its Compactor/TokenCounter/Env.Model/ContextWindow on the routed
model — the same contamination-safe per-provider path the Subagent per-call model override
uses, NEVER a clone-and-swap. `engine/agent` stays model-string-only.

## Consequences

- A team/Parallel deployment with `--subagent-model-router` + a `models.router:` taxonomy
  now routes ALL THREE delegation families, with no extra configuration: the operator
  taxonomy is reused.
- The `MemberEngine` factory signature changed (a `routedModel` parameter). This is an
  internal seam (composition + the team supervisor), not a wire contract; the gRPC
  `RunTeam` direct path stays ZERO-CAPS, so `routedModel` is always `""` there and members
  are built byte-identically to before.
- Fail-soft, decide-once, the per-run breaker, the untrusted-fence/whole-output-single-JSON
  verdict, and the classifier-usage fold are all INHERITED from ADR 0031 unchanged — one
  shared `caps.routeTask` governs every family in a turn, so a mixed turn (Subagent +
  Parallel + Team) serialises on ONE breaker/counter/fold.
- NO new outlives-a-call resource: the per-run breaker is the same one ADR 0031 inventoried;
  the routed engines are minted per-call (Parallel branch) / per-AddMember (team member),
  session-scoped, torn down with their child/member. See [ADR 0027](./0027-cloud-native.md)
  List 1.
- Cost: the router now classifies more per turn (one call per undefined member + one per
  branch), all folded into the parent budget and circuit-broken per run — the brake the #92
  fix put in place covers the wider fan-out.

## See also

- [ADR 0031](./0031-subagent-model-router.md) — the Subagent model router this extends.
- [ADR 0030](./0030-model-selection-heuristics.md) — the layered model-selection scheme.
- [ADR 0027](./0027-cloud-native.md) — the resource inventory (List 1 "no new resource" note).
- [docs/architecture/providers.md](../architecture/providers.md) — the living router section.
- [docs/design/IMPLEMENTATION-NOTES.md](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md) — the member/branch routing mechanics.
- The lifecycle convention in [ADR 0002](./0002-documentation-lifecycle.md).
