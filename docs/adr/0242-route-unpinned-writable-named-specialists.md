# ADR 0242 — Route unpinned writable named specialists

- Status: Accepted
- Date: 2026-08-27
- Scope: `engine/agent` writable named-specialist model selection and `internal/app` composition factories for semantic routing
- Supersedes: [ADR 0058](./0058-writable-named-specialist-subagent.md) — ONLY its fixed-model writable-specialist selection; direct-write execution, scoped writable catalog, and mutate-serial posture remain authoritative; AND [ADR 0066](./0066-route-unpinned-and-writable-delegations.md) — ONLY its exclusion of writable named specialists from semantic routing; its absent-versus-explicit model-intent rule, writable explorer routing, precedence, and fail-soft posture remain authoritative
- Superseded by: none

## Context

The semantic model router already covered anonymous read-only and writable explorers and
unpinned read-only named specialists. Writable named specialists were the remaining cell:
`mode:"read-write"` plus `agent` used the definition's resolved model even when the definition
declared no `model:` and the operator had enabled routing.

Treating every writable specialist as pinned contradicted the existing distinction between an
absent definition `model:` (no expressed intent) and explicit `model: inherit` (pin to the
session model). Reusing the generic writable-model factory would be incorrect: it would lose
the named specialist's prompt, scoped tools, skills, hooks, and limits. Reusing the read-only
agent-model factory would remove direct-write authority. The selected model must also remain
on the definition's resolved provider, because a bare router model is not a provider selector.

## Decision

Route an unpinned named `mode:"read-write"` Subagent through the semantic router when
composition can supply both its ordinary writable-specialist factory and a routed writable
specialist factory.

Add `agent.WithAgentWritableModelEngineFactory` in `engine/agent/subagent.go`
(`WithAgentWritableModelEngineFactory`). The closure accepts the agent name and router-selected
model and returns a fresh engine that keeps the definition's specialist scope with
`allowMutating=true`, the MAIN command runner, and the same resolved provider. Composition
builds it in `internal/app/build.go` (`buildAgentWritableEngineFactories`) through the same
agent-definition engine path as the ordinary writable specialist.

The ordinary `WithAgentWritableEngineFactory` remains required and is the truthful fail-soft
fallback. If the classifier misses, the delegation uses that ordinary writable specialist. If
the classifier selects a model but the routed factory declines it, the delegation also uses
the ordinary writable specialist, clears routed-hit metadata, and reports
`route-target-unavailable`. Per-definition limits and authority ceilings bind both engines.

Preserve the existing precedence and exclusions:

- an explicit call `model`, a pinned definition `model:` (including `inherit`), `fork`, or
  `resume` bypasses the router;
- explicit `read-write` + `agent` + `model` remains invalid—the router's internal selection is
  not an explicit all-three call;
- provider-switched definitions remain unroutable so a bare router target cannot cross
  providers;
- inline MCP remains unsupported for this per-call rebuilt engine; reference-only MCP remains
  supported;
- the child remains direct-write and mutate-serial against the parent environment.

The event and client contracts do not change. Existing `model`, `routed_category`,
`routed_model`, and `routing_reason` fields describe the selected engine and fallback.

## Consequences

An operator's taxonomy now applies consistently to all four Subagent combinations of
anonymous/named and read-only/read-write when no model intent is pinned. Writable named
specialists retain their actual role and authority rather than becoming generic explorers.

The engine public API gains one additive `SubagentOption`, and composition owns another engine
factory. The two writable named factories must stay aligned: one builds the definition's
ordinary model and one substitutes only the routed model. A missing factory disables routing
for this shape rather than spending a classifier call whose result cannot be consumed.

The explicit all-three call remains unavailable, and inline-MCP definitions still cannot use
this per-call rebuild. Those constraints avoid introducing provider selection or a new
long-lived MCP manager into the engine layer.

## See also

- [ADR 0058](./0058-writable-named-specialist-subagent.md) — writable named specialists and their direct-write scope.
- [ADR 0066](./0066-route-unpinned-and-writable-delegations.md) — absent versus explicit definition model intent and the preceding routing cells.
- [ADR 0031](./0031-subagent-model-router.md) — semantic routing and fail-soft classification.
- [ADR 0077](./0077-direct-write-subagent.md) — direct-write execution and mutate-serial dispatch.
- [Subagents and teams](../architecture/subagents-and-teams.md) and [providers](../architecture/providers.md) — living behavior.
- [Model routing](../usage/model-routing.md) — operator configuration and precedence.
