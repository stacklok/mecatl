# ADR 0295 — One scaled acceptance-plan spine for substantive work

- Status: Accepted
- Date: 2026-09-03
- Scope: agent-driven development workflow, worker isolation, and final review gates
- Supersedes: ADR 0072

## Context

ADR 0072 introduced the acceptance-plan spine but retained a separate issue-scale
workflow. The split made routing depend on subjective size labels, duplicated the
implementation path, and encouraged focused issues to bypass the same traceable
acceptance and human-merge controls used by larger capabilities. Conversely, forcing
a large plan shape onto every bug would add ceremony without improving confidence.

The orchestrator also assumed parallel writable workers even though harnesses differ:
some provide concurrent isolated workers, while others serialize writable delegation.
The workflow must preserve isolation and dependency ordering without claiming a
capability the active harness does not have.

## Decision

Use one acceptance-plan spine for all substantive issue and capability work:
`/to-acceptance-plan` → `/plan-orchestrate` → aggregate gates → strict acceptance
trace → panel review → one accumulator PR → human merge.

Scale the plan and task graph to the work. A focused issue may use one compact
scenario, a small set of acceptance criteria, and one task. Larger capabilities split
only on coherent scope and real dependency boundaries. Trivial or purely mechanical
edits may remain direct.

Every writable worker uses exactly one dedicated Git worktree, operates and
commits only there on its attempt-specific task branch, and fails rather than
falling back to the parent checkout. Harness-native writable worktree isolation
is preferred and used as supplied; only a worker without native isolation creates
an explicit attempt-specific worktree under `.scratch/`.

Preserve the task dependency graph and accumulator model across harnesses. Dispatch
independent ready workers concurrently only when the harness supports concurrent
writable workers; otherwise consume the ready set serially in deterministic order.
Both modes produce task branches merged into one accumulator.

Use one PR and one human merge gate for substantive work. Before that PR, use one
integration checkout that sees accumulator state, retains and commits generated docs,
marks the plan landed, regenerates docs, and runs strict acceptance tracing. The final
panel independently reviews Spec, Standards, Test adequacy, and Domain, and ends with
the stable machine record
`PANEL: ship_blockers=<n> important=<n> advisory=<n> reviewer_failures=<n>`.
Automation consumes that record rather than prose.

## Consequences

- Contributors no longer choose between overlapping substantive-work workflows.
- Focused fixes retain traceability without artificial decomposition or parallelism.
- Worker isolation is mandatory while scheduling adapts honestly to harness
  capability.
- Generated documentation and landed plan state are visible to strict tracing in the
  same checkout and are committed rather than discarded.
- The single-PR human merge remains the trust boundary; substantive work pays the
  small cost of an acceptance plan even when it has one task.

## See also

- [ADR 0072](./0072-acceptance-plan-spine.md) — the superseded two-track decision.
- [Development process](../development-process.md) — the living workflow.
- [Acceptance plans](../acceptance/README.md) — the verification contract.
- [ADR 0002](./0002-documentation-lifecycle.md) — frozen decision lifecycle.
