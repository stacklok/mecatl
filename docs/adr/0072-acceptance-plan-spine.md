# ADR 0072 — The acceptance-plan spine (to-acceptance-plan / plan-orchestrate / ac-trace)

- Status: Accepted
- Date: 2026-07-24
- Scope: the agent-driven development process (`.claude/` skills + agents,
  `docs/acceptance/`, the ac-trace gate)

## Context

mecatl's contributor base is growing, and the ad-hoc "architect plans →
implement → panel-review" loop (`/dev-pipeline`) covers issue-scale work but
not capability-scale work: a multi-week feature with several parallel work
streams needs a written verification contract, a decomposition step, parallel
TDD workers, and a machine-checkable gate tying each acceptance criterion to
its proof.

The sibling repos (the downstream consumer, a sibling repo, a sibling repo, a sibling repo) converged on a spine
that provides exactly this: `/to-acceptance-plan` writes
`docs/acceptance/<plan>.md` (numbered ACs, each with a `verify:` line),
`/plan-orchestrate` decomposes it into tasks and drives parallel `tdd-worker`
agents onto one accumulator branch, `ac-trace --strict` gates that every
named proof resolves, and `/panel-review` runs inline as the final gate
before one PR. a sibling repo and a sibling repo run the leanest variant; the downstream consumer adds a
two-PR model and repo-specific specialists that don't apply here.

## Decision

Adopt the sibling-repo spine variant, adapted to mecatl's anchors:

- **`docs/acceptance/<plan>.md`** is the design + verification contract for a
  capability: scenario-first, numbered `AC<n.n>` criteria, each with a
  `verify:` line naming its proof (a test name, or `none` / `inspection` /
  `demonstration` with a reason). Status lifecycle `draft → in-progress →
  landed`; only `landed` plans gate.
- **`ac-trace`** (the shared Stacklok tool, wired as pinned `go run` Taskfile
  tasks — not a go.mod `tool` directive, since the module is INTERNAL and a
  directive would force every root-module build to resolve it) checks every
  named proof resolves; `--strict` gates a landed plan.
- **The plan rides the accumulator** — no separate plan PR; orchestrate opens
  one PR (plan + code) and never merges to `main`.
- **Agents**: `tdd-worker` (the per-task TDD implementor) and
  `devils-advocate` (the pre-dispatch design critic, bookend to
  panel-review).
- **mecatl's `panel-review` is the converged review** — its default-on reuse
  pair (`code-duplication-reviewer` + `library-reuse-reviewer`) is kept; the
  sibling two-axis variants are not imported.
- **`/dev-pipeline` stays** as the issue-scale track; the spine is the
  capability-scale track.
- **`docs/design/principles.md`** is added as the ac-trace grounding list
  (the `Principle N` citation surface), a convenience index whose canonical
  text stays in the ADRs / `AGENTS.md`.
- ac-trace is report-only in CI until at least one landed plan exists.

## Consequences

- Capability work gets a written, machine-gated contract; a contributor (or
  agent) can pick up a plan and know exactly what "done" means.
- The per-plan `**Status:**` lifecycle is a *workflow* state, distinct from
  the shipped/deferred subsystem status ADR 0002 reserves to
  [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md) — the two trackers do not overlap.
- `principles.md` is a second prose copy of the AGENTS.md invariants — a
  deliberate, disclaimed index; the source wins on any disagreement, and a
  change to an invariant must update the summary in the same change.
- The orchestrator's PR-only model overrides AGENTS.md's "commit directly to
  `main`" line for capability-scale agent work (noted in AGENTS.md's
  Workflow section); the operator's durable preference is PR-only for all
  work in this repo.

## See also

- [Development process](../development-process.md) — the spine end to end.
- [Acceptance plans](../acceptance/README.md) — the `verify:` contract.
- [Platform principles](../design/principles.md) — the grounding list.
- [ADR 0002](./0002-documentation-lifecycle.md) — the doc-lifecycle rules
  this decision operates within.
