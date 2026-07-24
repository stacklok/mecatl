# Development process

mecatl's larger work is built by a mostly-autonomous spine encoded as agent
skills under `.claude/`. The human settles the design; agents write the
contract, decompose it, implement it test-first in parallel, gate it, review
it, and open one PR. The single human checkpoint is merging that PR.

```
design → /to-acceptance-plan → /plan-orchestrate → (waves of tdd-workers) → aggregate gate → ac-trace → panel-review → PR ─▶ human merge
```

For issue-scale work (a bug, a focused feature with an issue as the spec),
the lightweight [`/dev-pipeline`](../.claude/skills/dev-pipeline/SKILL.md)
loop is the right track — the spine earns its keep on capability-scale work
that wants a design contract.

## The steps

1. **Design — `/to-acceptance-plan`.**
   Synthesises a settled design into `docs/acceptance/<plan>.md`: scenario-first,
   numbered acceptance criteria, each with a `verify:` line naming its proof.
   An advisory `devils-advocate` pass and a light specialist spot-check surface
   material gaps; low-severity findings are folded in automatically. No separate
   PR — the plan rides the accumulator.

2. **Orchestrate — `/plan-orchestrate`.**
   Decomposes the plan into tasks under `.claude/plans/<plan>/tasks/`, then
   dispatches **waves** of parallel `tdd-worker` agents — one task per worker,
   each in an isolated worktree, each doing strict red-green TDD (via the
   `/test-writer` skill) against the hexagonal ports and the AGENTS.md
   invariants. Successful branches funnel into one **accumulator** branch.

3. **Gate + review (automatic).** On the assembled accumulator the orchestrator
   runs the aggregate gate (`task lint && task test && task docs`), flips the
   plan to `landed`, runs `ac-trace --strict` (every `verify:` proof must
   resolve), then runs `/panel-review` as the final gate. Panel ship-blockers
   spawn an automatic repair wave (budget 2). When the assembled branch is
   clean it opens **one PR** (plan + code, panel report inline, `Closes` the
   epic + task issues).

4. **Merge (the one human gate).** A human reviews and merges the PR. The
   orchestrator never merges to `main` and never runs beyond the PR. (mecatl
   convention is **PR-only** — never commit to `main` directly.)

## Verification, tracked

Acceptance criteria are the contract *and* the tracker. Each carries a
`verify:` line; [`ac-trace`](https://github.com/stacklok/ac-trace) checks
every named proof resolves (`TestInvariant_<id>` for an invariant,
`TestADR_NNNN_*` for an ADR rule, `Test<Plan>_Scenario<N>_*` for a scenario,
or a descriptive test name). `task ac-trace` reports coverage;
`task ac-trace-strict` gates a `landed` plan. See
[the acceptance README](acceptance/README.md).

## The quality gates a plan must clear

| Gate | What it pins |
|---|---|
| `task lint` | golangci-lint v2 + go vet, both modules — incl. the depguard allowlist (layering) |
| `task test` | full offline suite, `-race`, both modules + the engine-standalone proof |
| `task api:check` | the engine's exported surface vs `engine/api/*.txt` |
| `task docs` | `llms.txt` regen + the matlatl strict link gate |
| `task ac-trace-strict` | every landed AC's `verify:` proof resolves |
| `/panel-review` | Spec / Standards / Domain (incl. the default-on duplication + library-reuse pair) |

## See also

- [Architecture guide](architecture.md) · [Usage & operator guide](usage.md)
  · [ADRs](adr/) · [Implementation notes](design/IMPLEMENTATION-NOTES.md)
  · [Acceptance plans](acceptance/README.md)
