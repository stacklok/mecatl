---
name: dev-pipeline
description: >-
  Drives substantial issue/feature work as a multi-agent pipeline:
  architect plans → separate agent implements → review panel scrutinises →
  findings loop back → commit per iteration. Use when the user says "let's
  work on these issues", "let's go", "let's pick up issue N", "implement this
  feature", or hands over an issue/PRD for end-to-end delivery.
  NOT for trivial edits, one-off questions, or pure-docs/config tweaks
  (just do those directly).
---

# Dev Pipeline

A disciplined approach → implement → review → iterate → commit pipeline for
non-trivial issue and feature work. The value is **separation of concerns**:
the agent that plans is not the agent that implements, and neither judges its
own work — an independent review panel does, adversarially, before anything
lands.

Run the full pipeline **per issue**. Honor the repository's `CLAUDE.md` for
build/test/commit conventions throughout — this skill describes the *process*,
not project-specific commands.

## When to run this

Trigger phrases: "let's work on these issues", "let's go", "let's pick up
issue N", "implement this feature/issue", or when handed an issue number, PRD,
or spec for end-to-end delivery. For several issues, run the pipeline once per
issue in sequence — finish one before starting the next.

**Complexity gate.** This pipeline trades latency for rigor — it earns its
keep on multi-file, non-obvious, or unfamiliar work. If the change is a
one-sentence diff (a typo, a single-line fix, a pure-docs/config tweak), skip
the pipeline and just make the edit. When unsure, ask whether the scope is
worth the ceremony.

## The pipeline

### 1. Approach (plan, no code)
Spawn an **architect** agent (`Plan` or `software-architect`, on a high-reasoning model) to
investigate the issue and produce a concrete, file-level implementation plan.
It reads code and writes a plan — it writes **no production code**. The plan is
a **checkable artifact**: later steps verify the diff *against* it, so capture
the requirements and the intended file-level changes explicitly. **Include the
docs the change makes stale** — design notes that track the issue as
planned/deferred and must flip to *shipped*, READMEs, capability lists, help
text, log strings — as named files in the plan, so they aren't an afterthought.
Surface the plan to the user before implementing if the approach is non-obvious
or has trade-offs worth a decision.

**Size and complexity-rate every implementation task.** The architect must tag
each task with: a **diff-size band** (S = a few lines in one file; M = one or
two files, tens of lines; L = multi-file, hundreds of lines), a **complexity
rating** (mechanical = direct translation of the plan; moderate = some design
choices within the plan's bounds; tricky = load-bearing decisions the plan
under-specifies), **dependencies/ordering** (which tasks must land first), and
**single-pass vs chunked** (can one agent do it in one drive, or must it be
broken across drives/agents). The orchestrator uses this tagging to pick the
implementer path in step 2.

### 2. Implement (build + test, no commit)
Spawn a **separate** expert agent to execute the plan. It is the **executor of
a plan it did not author** — whether that agent is you (the orchestrator) or a
delegated agent is a *tooling* choice, not a rigor choice. The rigor that
matters is **plan ≠ review**: the plan is written by a separate architect, and
the review is run by fresh-context agents that see only the diff + the plan,
never the implementer's reasoning. That independence is what makes the review
meaningful — NOT whether the implementer was you or a delegate.

**Delegate as ONE unit.** Hand the implementer the plan + the whole task; do
NOT relay file contents through the orchestrator, and do NOT spawn one subagent
per file. Chunking by file destroys the plan's coherence and forces the
orchestrator to re-author context the implementer could have read itself.

**Pick the implementer path by what your tools allow:**
- **Write-capable delegation with merge-back** (a delegated agent whose edits
  land in this workspace, or a Parallel single-branch run with
  `--parallel-auto-merge` whose winner's diff is merged back) → **delegate**.
- **Read-only subagent** (a Subagent whose worktree is discarded; no
  Edit/Write) → **implement directly yourself** — the subagent can investigate
  and produce code as text in its final message, but it cannot land edits. You
  write the files from its output.
- **Fork-with-merge** (a single-branch `Parallel` with `join: first`/`judge`,
  whose winner's fork is preserved) → **delegate, then merge** the winner's
  diff back into this workspace (or rely on `--parallel-auto-merge` to do it
  automatically).

The implementer:
- builds and runs the project's **offline** test suite (per `CLAUDE.md`),
- **writes tests at the level the change demands** — unit tests for logic, and
  an **end-to-end test through the project's real harness** (the agent loop,
  server, or CLI entry point) when the change adds a user-reachable capability.
  Match the repo's existing e2e convention; don't stop at unit tests when the
  harness supports driving the feature end-to-end. Assertions must be
  *meaningful* — a test that stays green when the behaviour is broken is worse
  than no test,
- **updates the docs the plan flagged as stale** in the same pass — code and
  its documentation land together, not in a follow-up,
- does **NOT** commit — leaves changes in the working tree for review.

### 3. Review (panel, parallel)
Invoke the **`panel-review`** skill (spec + standards + domain specialists,
fanned out in parallel) over the working-tree diff. The reviewers run in
**fresh contexts that see only the diff + the plan**, never the implementer's
reasoning — that independence is what makes the review worth running. Scope
them to **correctness and stated-requirement gaps**, not style nits or
speculative hardening: a gap-hunting reviewer will always find *something*, and
chasing every finding leads to over-engineering. It returns a tiered findings
report.

**Also review the tests, not just the production code.** The panel above
scrutinises the diff's *logic*; nobody on it is asking "are the tests
adequate?" Run a **QA / test-expert agent** in its own fresh context to
adversarially review the **test suite**: does coverage match what changed, is
there an **end-to-end / wiring gap** (the feature unit-tested but never driven
through the real harness), would any assertion still pass if the behaviour
broke, and are there **determinism / flake risks** (ordering, time, randomised
map iteration)? Have it first learn the repo's test conventions so its
recommendations match them, and return a tiered gap list (must-add /
should-add), not a wishlist. If no QA agent is installed, brief a
general-purpose agent for the role.

### 4. Iterate (fix confirmed findings)
Feed the **cross-confirmed, actionable** findings — from both the code panel
and the QA test-adequacy review (its must-add coverage gaps) — back to the
implementation agent to fix. Hand it the findings + the diff, not the whole
transcript. Re-run build + offline tests as the **executable done-condition** —
don't accept "looks right" without green output. Re-review if the changes were
substantial.

**Cap the loop.** Stop when the panel is clean, when remaining findings are
explicitly accepted with the user, or after ~3 iterations — whichever comes
first. If it hasn't converged by then, surface the state to the user rather
than looping indefinitely.

### 5. Commit (one per iteration)
Commit the verified change with explicit paths and the project's commit
trailer (per `CLAUDE.md`). **One commit per iteration**, not one giant commit
at the end. Commit directly to the working branch the repo conventions specify.

Auto-committing per iteration is the **deliberate, preferred posture** here —
don't stop at a summary and wait to be told to commit. The gate stays on
*outward-facing* actions only (opening PRs, filing/closing issues, posting
comments): those are the user's call. Local commits are not gated.

## Standing rules (apply throughout)

- **Finish, don't defer.** Don't silently drop pieces of an issue. If
  something genuinely must wait, flag it explicitly and get agreement — the
  default is to finish it.
- **Probe assumptions hard.** Investigate orthogonal questions that surface
  mid-stream ("is X actually true?", "should we use a library here?") rather
  than hand-waving them. Bring findings back, don't bury them.
- **Outward-facing GitHub writes are the user's call.** Filing or closing
  issues, opening PRs, posting comments — confirm with the user first. Local
  commits are fine per repo convention.
- **Leave it better than found.** Add nil guards, backfill tests for code you
  touch, tidy adjacent rough edges — kept relevant to the change, no sprawl.
- **Sync the docs as part of the change.** Shipping a feature makes its design
  notes, READMEs, capability lists, and help/log strings stale — flip "planned"
  or "deferred" to *shipped*, and reconcile any design that the as-built code
  diverged from. Stale docs are a finishing gap, not a separate task; the user
  shouldn't have to ask.
- **Tests are a reviewed deliverable.** Cover the change at the right level —
  including an end-to-end test through the real harness for user-reachable
  capabilities, not just unit tests — and have the test suite itself reviewed
  by a QA/test agent, separate from the production-code panel. "The tests pass"
  is not the bar; "the tests would fail if this broke, and nothing reachable is
  left uncovered" is. The user shouldn't have to ask whether e2e tests exist.
- **One purpose per Bash call.** Never bundle a destructive op (`rm`, `mv`)
  with read-only exploration; keep each approval prompt easy to reason about.
- **Stage explicit paths** — never `git add -A`.

## Notes

- Steps 1–4 are agent fan-outs; if multi-agent orchestration is available
  (the Workflow tool), the approach→implement→review→iterate sequence maps
  cleanly onto a pipeline. Otherwise drive it with sequential `Agent` calls.
- The review step is deliberately a *separate* skill (`panel-review`) so it can
  be run standalone too.
