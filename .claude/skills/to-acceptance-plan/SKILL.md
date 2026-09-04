---
name: to-acceptance-plan
description: >-
  Turn settled design for substantive implementation, a bug fix, picked-up issue,
  or feature work into a scenario-first acceptance plan at
  docs/acceptance/<plan>.md. Numbered ACs each carry a non-empty verify: proof and
  cite an ADR, architecture section, or invariant. Runs advisory adversarial and
  specialist checks, commits the plan and generated docs on a clean accumulator,
  then hands it to /plan-orchestrate. Use for implement/fix/pick up issue/feature
  work once design is settled. NOT for task decomposition or implementation.
---

# to-acceptance-plan

The **design** step: synthesise a settled design into `docs/acceptance/<plan>.md`
— the substantive issue or capability's design document **and** its verification contract — then
hand it to `/plan-orchestrate`.

**Do NOT interview the user** — the design discussion already happened.
Synthesise what is known into the plan shape.

**No separate PR.** The plan is committed directly to the accumulator, never to
`main`, then handed to `/plan-orchestrate`. Orchestration carries it from `draft`
to `landed` and opens **one** PR (plan + code) for the human to merge. This is the
single human gate.

## Inputs

- the **design discussion** — decisions, scope cuts, deferrals;
- **`docs/architecture.md`** — the layers, the ports, the layering rule;
- **`AGENTS.md`** — the canonical agent contract: the invariants under
  "Things That Will Bite You" are the equivalent of a domain model's
  invariant list. Respect them; cite them;
- the **ADRs** under `docs/adr/` — frozen decision records. Cite them,
  never contradict them silently;
- **`docs/design/IMPLEMENTATION-NOTES.md`** — the dense per-subsystem
  reference, when the plan touches an existing subsystem.

## Process

1. **Re-read the anchors.** `docs/architecture.md`, `AGENTS.md`, the ADRs
   the design touched, and `docs/acceptance/README.md` for the house shape
   and the `verify:` contract.

2. **Pick the capability + plan slug.** The slug names the file
   (`docs/acceptance/<plan>.md`) and the accumulator branch
   (`acc/<plan>`).

3. **Draft from** [`ACCEPTANCE-PLAN-TEMPLATE.md`](references/ACCEPTANCE-PLAN-TEMPLATE.md).
   Load-bearing rules:
   - **Scenario-first** — organise by what the running harness demonstrates
     (a `mecademo` flow, a gRPC call sequence, an engine behaviour), in
     implementation order.
   - **Numbered acceptance criteria** (`AC<scenario>.<n>:`) — present-tense
     assertions of observable behaviour, not "implement X". These ARE the
     contract: they flow into orchestrate's per-task briefs and are graded by
     `/panel-review`.
   - **A `verify:` sub-line on every AC** — Go test names
     (`TestADR_NNNN_*`, `TestInvariant_<id>`, `Test<Plan>_Scenario<N>_*`,
     or a descriptive unit/e2e test name), or one non-test method
     (`none` / `inspection` / `demonstration`) with a reason. Set
     `**Status:** draft`.
   - **≥1 citation per scenario** — a link to `../adr/**`,
     `../architecture.md`, `../design/IMPLEMENTATION-NOTES.md`, or
     `../../AGENTS.md`.
   - In/out of scope, deferred decisions, **Definition of done** anchored on
     the Taskfile gates (incl. `task ac-trace-strict`).
   - A focused issue is allowed to stay compact: one scenario, a small AC set,
     and one eventual orchestration task. Do not add scenarios or split tasks
     solely to make the artifact look capability-sized.
   - **New decisions land as new ADRs.** If the plan makes a
     costly-to-reverse decision not yet captured, add the ADR stub
     (copy `docs/adr/template.md`) in the same change — ADRs are frozen,
     never edited in place. A new documented invariant lands with its
     pinning test named in the relevant AC's `verify:`.

4. **Offer stub ADRs sparingly.** Capture only *remaining* costly-to-reverse
   decisions the design left uncaptured. Don't duplicate decisions already
   in an ADR or `AGENTS.md`.

5. **Run the bundled check** and fix until it passes:
   ```bash
   bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan.sh docs/acceptance/<plan>.md
   ```
   Add the plan to `docs/acceptance/README.md` (the matlatl gate fails on an
   unreachable doc), then run `task docs` (configuration-reference regeneration
   + link gate).

6. **Devils-advocate pass (advisory, non-blocking).** Dispatch the
   `devils-advocate` subagent against the draft plus the ADRs /
   architecture / AGENTS.md invariants it cites. It returns an advisory gap
   report (missing scenarios, task-shaped or untestable ACs, contradictions
   with an ADR or invariant, uncited scenarios, missing `verify:` lines).
   - **Fold in the clear ones automatically** — a missing `verify:` line, an
     obvious edge scenario, a task-shaped AC reworded to behaviour, a missing
     citation. Re-run the check if you changed ACs/citations.
   - **Surface only material open decisions** — a genuine scope question or a
     contradiction that needs a human call — as a short, batched list. Do not
     run a one-gap-at-a-time human Q&A loop; auto-accept low-severity findings
     and record them under *Deferred decisions*.

7. **Light specialist spot-check (advisory, non-blocking).** For the one or
   two domains the plan most touches (e.g. `secure-code-reviewer` for a
   permission/trust boundary, `software-architect` for a new port/layer),
   run a single scoped pass asking only "are these ACs correct and complete
   for your domain?" — not a re-design. Fold clear corrections into the ACs;
   batch any real open question with Step 6's. Skip for a trivial plan.

8. **Commit and hand off.** Re-run the bundled check and `task docs` after the
   reviews. Create and check out `acc/<plan>` from the current `main` commit. If
   the current checkout is the repository's primary/current checkout, classify it
   as `primary-current`. If the harness supplied a writable isolated worktree,
   validate its repository root and classify it as `harness-owned-native`. Only
   when neither checkout is usable may this skill explicitly create a disposable
   integration worktree under `.scratch/`; classify that as
   `orchestrator-created-disposable`. Never create a redundant nested worktree. Stage only
   the plan, its README/index or new ADRs, and generated documentation paths
   explicitly; commit them on `acc/<plan>`, never on `main`. Require
   `git status --short` to be empty. This checkout is the integration worktree
   transferred to `/plan-orchestrate`. Record and report the checkout path **and
   ownership classification** plus the plan path, then invoke (or tell the user to
   invoke) `/plan-orchestrate <plan>`. **Do not decompose into tasks here**, and
   **do not open a PR** — orchestrate owns both.

## What this skill does NOT do

- **No task decomposition** — that is `/plan-orchestrate` Step 0.
- **No PR** — the plan lands via orchestrate's single PR.
- **No interview** — synthesise, don't ask; surface only material open
  decisions, batched.
