# Spine convergence — acceptance plan

**Phase:** agentic development spine (process infrastructure)
**Status:** draft, 2026-07-24. Converges the the internal sibling repos spine into mecatl.
**Delivery:** Split. Historical plan that predates the Combined exception.
**Accumulator branch:** `acc/spine-convergence` (off `main`).

The smallest set of work that brings the spine (acceptance-plan →
orchestrate → TDD workers → ac-trace gate → panel-review) into mecatl so
multi-contributor, agent-driven work has the same trust loop the sibling
repos already run.

The doc is organized scenario-first because acceptance is about what a
contributor (human or agent) can do in the repo, not which files exist.

## Human decisions

- [ ] Decide when `ac-trace-strict` becomes a mandatory CI gate.

## Interface contract

- **gRPC / protobuf:** None — historical repository process work; no wire contract changed.
- **Exported Go APIs / interfaces:** None — no exported Go surface changed.
- **Tool schemas:** None — no model-facing runtime tool schema changed.
- **CLI / config:** None — no binary or operator configuration changed.
- **Events / persistence:** None — no runtime event or persistence shape changed.
- **Security / authority:** None — the historical plan introduced repository workflow checks, not a runtime authority boundary.
- **Compatibility / migration:** The repository workflow contract is superseded where necessary by [ADR 0304](../adr/0304-human-reviewed-development-contracts.md); no runtime migration applies.

## Why these scope cuts

- **mecatl's panel review was the convergence base, not an unchanged artifact.**
  It already carried the default-on reuse pair (`code-duplication-reviewer` +
  `library-reuse-reviewer`) and its references. Convergence preserved those
  strengths and added independent Test adequacy as the fourth axis and stable
  `PANEL:` automation contract.
- **One spine serves every substantive change.** Focused issues use a compact
  one-scenario, one-task plan; larger capabilities decompose only along real
  dependency boundaries. Trivial or mechanical edits may remain direct.
- **ac-trace is report-only in CI until this plan lands.** `task
  ac-trace-strict` only bites on a `landed` plan, so wiring it in can't
  fail the build on an empty `docs/acceptance/` tree.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — The spine skills + agents exist and route

A contributor opening the repo finds the same spine the sibling repos run:
`/to-acceptance-plan` writes the contract, `/plan-orchestrate` drives it,
`/test-writer` disciplines the tests, `tdd-worker` + `devils-advocate` are
the two agents, and `/onboarding` is the router that names all of it. The
shape follows the sibling-repo originals, adapted to mecatl's anchors
([`AGENTS.md`](../../AGENTS.md) — the layering rule and invariants;
[`architecture.md`](../architecture.md); the [ADRs](../adr/)).

**Work:**
- `.claude/skills/`: `to-acceptance-plan` (+ template + check script),
  `plan-orchestrate`, `test-writer`, `onboarding`.
- `.claude/agents/`: `tdd-worker.md`, `devils-advocate.md`.
- `docs/development-process.md` — the spine end to end.
- `docs/acceptance/README.md` — the `verify:` contract + plan index.

**Acceptance:**
- AC1.1: every spine skill carries a frontmatter `description` that names
  its trigger and its boundary (what it does NOT do), and routes to the
  next step rather than doing the work itself.
  - verify: inspection — skill frontmatter + the router discipline is a
    doc property, reviewable in the diff.
- AC1.2: the onboarding router names each spine step and each specialist
    skill, and defers to them (no duplicated logic).
  - verify: inspection.
- AC1.3: the tdd-worker and devils-advocate agents reference mecatl's
  anchors (AGENTS.md, architecture.md, the ADRs) and not a sibling repo's
  modelith model.
  - verify: inspection.
- AC1.4: `docs/development-process.md` and `docs/acceptance/README.md`
  describe the same spine and `verify:` contract the skills implement.
  - verify: inspection.

### Scenario 2 — ac-trace is wired and gated

The [`ac-trace`](https://github.com/stacklok/ac-trace) tool runs in the
repo as a Go tool, reports coverage, and gates a `landed` plan — the same
mechanism the sibling repos run, per the `verify:` contract in
[`docs/acceptance/README.md`](README.md) and the doc-gate workflow in
[`AGENTS.md`](../../AGENTS.md).

**Work:**
- `Taskfile.yml`: `ac-trace`, `ac-trace-strict`, `ac-trace-matrix` tasks,
  invoking the tool via `go run …@v0.0.3` (pinned) rather than a go.mod
  `tool` directive — ac-trace is an INTERNAL module, and a tool directive
  would force every build of the root module (incl. CI without internal-org
  access) to resolve it.

**Acceptance:**
- AC2.1: `task ac-trace` runs and reports coverage for `docs/acceptance/`.
  - verify: demonstration — the task runs green on this branch.
- AC2.2: `task ac-trace-strict` exits non-zero when a `landed` plan's
  `verify:` proof doesn't resolve.
  - verify: demonstration — the gate is the tool's own `--strict`
    behaviour; exercised when this plan flips to landed.
- AC2.3: the check script flags a plan with no numbered ACs / no
  citations / no out-of-scope section.
  - verify: demonstration — `check-acceptance-plan.sh` exits 1 on a
    stripped fixture.

### Scenario 3 — This plan is the dogfood

This change is itself driven by an acceptance plan (this file), so the
spine is exercised on its own landing: the plan carries numbered ACs with
`verify:` lines, passes the bundled check, and is graded by
`/panel-review` on the PR — the same review the spine's Step 6 runs inline, per
[`docs/development-process.md`](../development-process.md) and the review
workflow in [`AGENTS.md`](../../AGENTS.md).

**Acceptance:**
- AC3.1: `bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan.sh docs/acceptance/spine-convergence.md` passes.
  - verify: demonstration — the check runs green on this file.
- AC3.2: the PR carries a `/panel-review` report grading this diff against
  this plan (the Spec axis).
  - verify: inspection — the panel report is on the PR.
- AC3.3: `task lint`, `task test`, and `task docs` are green on the branch.
  - verify: demonstration — CI parity tasks run green.

## Out of scope

| Item | Defer-to | ADR / decision |
|---|---|---|
| Wiring `ac-trace-strict` into a mandatory CI job | a later PR, once ≥1 landed plan exists | the tool only gates `landed` plans |
| Converging the host-specific specialists (arch-*, kind-*, ui-*) | never — not mecatl's domain | host-specific |
| Extending `panel-review` beyond the then-current three axes | ADR 0295 convergence | the common final gate now includes independent test adequacy |
| A first *feature* acceptance plan driven through orchestrate | the next capability | this PR lands the machinery + one dogfood plan |

## Definition of done

1. `task lint` and `task test` pass (both modules, `-race`).
2. `task docs` — configuration reference regenerated and the matlatl strict link gate green.
3. `task ac-trace` runs and reports this plan.
4. The bundled `check-acceptance-plan.sh` passes on this file.
5. `go run ./cmd/mecademo` still prints a full offline session.
6. The PR carries a `/panel-review` report.

## Deferred decisions and known risks

- **The orchestrate skill's worker-dispatch verb** is written for the
  mecatl harness (`Subagent mode:"read-write"`) with the Claude-Code
  `Agent`/`tdd-worker` dispatch noted as the equivalent — the contract is
  the same, the verb differs by harness.

## Exit criteria

When every point under *Definition of done* holds on the branch, this plan
is satisfied.
