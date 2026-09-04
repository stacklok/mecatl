# Development process

mecatl's substantive work is built by a mostly-autonomous spine encoded as agent
skills under `.claude/`. The human settles the design; agents write the
contract, decompose it only as far as needed, implement it test-first in isolated
workers, gate it, review it, and open one PR. The single human checkpoint is
merging that PR.

```
design → /to-acceptance-plan → /plan-orchestrate → (capability-dependent tdd-worker ready sets) → aggregate gate → ac-trace → panel-review → PR ─▶ human merge
```

Substantive issue and capability work use this one spine. Scale the artifacts
to the work: a focused bug or feature may have one compact scenario, a small AC
set, and one task. Do not split work merely to create a graph or worker swarm.
Only trivial or mechanical edits may bypass the spine and land directly.

## The steps

1. **Design — `/to-acceptance-plan`.**
   Synthesises a settled design into `docs/acceptance/<plan>.md`: scenario-first,
   numbered acceptance criteria, each with a `verify:` line naming its proof.
   An advisory `devils-advocate` pass and a light specialist spot-check surface
   material gaps; low-severity findings are folded in automatically. No separate
   PR — the plan rides the accumulator.

2. **Orchestrate — `/plan-orchestrate`.**
   Decomposes the plan into the smallest coherent task set under
   `.claude/plans/<plan>/tasks/`, then dispatches isolated `tdd-worker` agents —
   one task per worker, each doing strict red-green TDD (via the
   `/test-writer` skill) against the hexagonal ports and the AGENTS.md
   invariants. A focused plan should remain one task. For independent tasks,
   execution is parallel when the harness supports concurrent writable workers
   and serial by ready set otherwise. Successful branches funnel into one
   **accumulator** branch.

3. **Gate + review (automatic).** On the assembled accumulator the orchestrator
   runs the aggregate gate (`task lint`, `task test`, `task docs`, and the terminal
   `go run ./cmd/mecademo` offline smoke) in the integration worktree. Workers do
   not repeat the demo; terminal orchestration owns that integration proof. It
   retains and commits generated surfaces, flips the plan to `landed`, regenerates
   docs, then runs `ac-trace --strict` from that same checkout (so it sees landed
   status and generated files). It then runs `/panel-review` in non-pausing
   orchestrator mode as the final Spec / Standards / Test adequacy / Domain gate.
   The panel's final `PANEL:` record is the machine-readable decision input;
   ship-blockers spawn an automatic repair wave (budget 2), while reviewer
   failures must be retried or explicitly waived by a human. When the assembled
   branch is clean it opens **one PR** (plan + code, panel report inline, `Closes`
   the epic + task issues).

4. **Merge (the one human gate).** A human reviews and merges the PR. The
   orchestrator never merges to `main` and never runs beyond the PR. Substantive
   work is PR-only; only the trivial/mechanical exception above may land
   directly.

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
| `task docs` | configuration-reference regeneration + the matlatl strict link gate |
| `task ac-trace-strict` | every landed AC's `verify:` proof resolves |
| `/panel-review` | independent Spec / Standards / Test adequacy / Domain review; final `PANEL:` result drives the gate |

## See also

- [Architecture guide](architecture.md) · [Usage & operator guide](usage.md)
  · [ADRs](adr/) · [Implementation notes](design/IMPLEMENTATION-NOTES.md)
  · [Acceptance plans](acceptance/README.md)
