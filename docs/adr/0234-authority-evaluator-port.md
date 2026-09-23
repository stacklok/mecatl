# ADR 0234 — Derived delegation authority behind an evaluator port

- Status: Accepted
- Date: 2026-08-19
- Scope: Delegated tool authority, evaluator selection, and authority-policy adapters
- Supersedes: None
- Superseded by: None

## Context

A delegated agent must not gain a tool capability its parent did not hold. Permission
policy and caller ownership answer different questions: permissions decide whether a
call needs approval, while ownership identifies who may access a session. Neither is a
complete representation of the capabilities carried into a child run.

The earlier approach coupled capability representation, serialization, and local
checking. That made it difficult to prove attenuation at every delegation seam or to
use a deployment-specific policy engine without widening the importable engine module.
A policy engine is useful for operator constraints such as a workspace path boundary,
but it must only tighten a carried capability set; it must never become a second source
of grants.

## Decision

Carry one plain derived capability set on a bound session. Derive a child set by
intersection with its parent, an eligible managed definition ceiling, and any
per-call tightening, then consume one delegation hop before allocating child runtime
resources. Persist the set and provenance with the session. Resume checks the
persisted child set against the caller's current set without consuming another hop.

Authorize each execution through `port.AuthorityEvaluator` at the engine execution
chokepoint. The request contains the carried set, the capability-selected tool name,
the distinct operation action, delegation depth, principal, and only a normalized
non-secret resource descriptor where one is known. It never contains raw tool
arguments or credentials. A denial is distinct from an evaluator failure; failures
fail closed and are diagnosable.

Select an evaluator explicitly in composition. `local` is the default in-process
exact-name evaluator. `noop` is an explicit no-enforcement deployment mode. `cedar`
is opt-in, loads one static operator-owned policy at startup, and fails startup rather
than falling back if that policy cannot load. Cedar evaluates only after the carried
set allows the tool, so Cedar rules can add denials but cannot grant omitted tools.
The Cedar dependency remains outside the engine module.

For `CallMcpWithQuery`, reconstruct the addressed `mcp__<server>__<tool>` name at the
dispatch boundary and authorize that target rather than authorizing the meta-tool as a
whole. MCP resources use a reserved opaque per-server derived capability minted from
the resolved resource snapshot; it is not a catalog tool or independently authored
grant. Resource operations require a named server, select that capability, and retain
`ListMcpResources` or `ReadMcpResource` as their evaluator action.

## Consequences

Delegation is monotone: no operation widens a capability set, and an older child cannot
resume under a narrower parent. The default local evaluator keeps the common path
small and offline. Deployments that need resource-aware restrictions can select Cedar
without importing Cedar into engine consumers.

This adds an operator startup choice and a static policy file to manage. Cedar policy
syntax and its availability are adapter concerns; malformed or missing policy is a
startup error. This decision does not mint delegated credentials or authorize calls
outside the process; those are separate outbound-boundary work.

## See also

- [Architecture — Authority evaluation](../architecture/agent-loop.md#authority-evaluation-at-execution)
- [Implementation notes — Delegated authority](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md#delegated-authority)
- [ADR 0014 — Agent teams](./0014-agent-teams.md)
- [ADR 0036 — Engine module boundary](./0036-engine-module.md)
- [ADR 0038 — Event-sourced rehydration](./0038-event-sourced-rehydration.md)
- [ADR 0204 — Caller identity](./0204-caller-identity-threading.md)
- [ADR 0214 — Environment persistence](./0214-environment-persistence.md)
