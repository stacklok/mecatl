---
name: plan-orchestrate
description: >-
  Drives a multi-task mecatl acceptance plan to completion using parallel
  tdd-worker agents — one task per worker, one local branch per task, all
  funneled into a single accumulator branch. Per-task state is local files
  under `.claude/plans/<plan>/` (git ancestry is authoritative). After the
  merge loop drains, it runs the aggregate gate, flips the plan to `landed`,
  runs `ac-trace --strict`, then runs `/panel-review` inline as the final
  gate — spawning an auto repair-wave for any ship-blockers (budget 2). When
  the assembled branch is clean it pushes the accumulator once and opens ONE
  PR (plan + code) for the human to merge; it never merges to `main` and
  never auto-runs beyond the PR. Use when an acceptance plan exists at
  docs/acceptance/<plan>.md (draft is fine — it rides the accumulator). NOT
  for drafting the plan — that is /to-acceptance-plan.
---

# plan-orchestrate

## Purpose

A plan is a tree of tasks under `.claude/plans/<plan>/tasks/`. Each task is a
markdown file with YAML frontmatter declaring its dependencies, status, and
(after work lands) the local branch holding its commits.

This skill scans the task directory, computes the ready set, and dispatches
parallel `tdd-worker` agents (one task per worker, each in an isolated git
worktree). Workers do the work locally, run the mecatl gates, and return the
branch they created. The orchestrator **auto-merges that branch into the
accumulator** when gates passed. After the merge loop drains it runs the
**aggregate gate**, flips the plan to `landed`, runs **`ac-trace --strict`**,
then runs **`/panel-review` inline** and spawns an **auto repair-wave** for any
ship-blockers (budget 2). When the assembled branch is clean it **pushes the
accumulator once and opens one PR** for human review and merge. It does **not**
merge to `main` and does **not** run beyond the PR.

Autonomy: this skill **may auto-fire** (after `/to-acceptance-plan` hands off,
or when re-invoked to resume) and may be wrapped in `/loop` for continuous
drive. The single human gate is the final PR merge.

## State + the single-PR model

- **State is local files + git ancestry.** The authoritative record is the
  file tree under `.claude/plans/<plan>/` and the accumulator's ancestry.
- **The plan rides the accumulator.** `docs/acceptance/<plan>.md` need not be
  merged to `main` first. The accumulator is branched off `main`; the plan doc
  travels on it (draft → landed) and lands in the single PR with the code.
- **One PR** funnels every successful task branch + the plan doc — not N
  per-task PRs, and not a separate plan PR.
- **Issues close via `Closes #N`** in that PR body (the epic + each task's
  mapped issue). No live status-label mirror.

## Layout

```
.claude/plans/<plan>/
  README.md                 # human-readable index
  tasks/
    01-domain-core.md        # one file per task
    02-store-adapter.md
    03-composition-wiring.md
    ...
```

**Accumulator branch** (`acc/<plan>`): created off `main` before the first
dispatch. Every successful task branch merges into it; the plan doc lives on
it. Pushed once at the end as the single PR.

Task file format:

```markdown
---
id: 02-store-adapter
title: Redis SessionStore adapter + conformance
blocked_by: [01-domain-core]
status: pending      # pending | in-progress | done | blocked
branch: ""           # local branch, set on worker success
worktree: ""         # worktree path, set at dispatch
issue: ""            # mapped GitHub issue number (for Closes #N; optional)
retries: 0
last_error: ""
accumulator: acc/<plan>
---

# Task brief

(Self-contained worker brief — scope + branch instructions. The TDD/gates
discipline lives in the tdd-worker agent; do not re-lecture it here.)

## Acceptance criteria

(The `AC<n.n>` items this task satisfies, quoted verbatim from
docs/acceptance/<plan>.md, WITH their `verify:` sub-lines. The worker's named
pinning test asserts these; ac-trace gates the verify: names.)
```

`blocked_by`, `status`, `branch`, `worktree`, `retries`, `last_error`,
`accumulator`, `issue` are **orchestrator-managed**. Workers never edit task
files. `done` is computed from git ancestry: `branch != ""` AND reachable from
the accumulator (`git merge-base --is-ancestor <branch> <accumulator>`).
Ancestry is authoritative; the `status` field is the orchestrator's notepad.

## Status state machine (per-task)

```
pending ─dispatch─▶ in-progress ─worker success─▶ (ff-only merge into accumulator) ─▶ done
                          │                              │
                          │                              └─merge conflict─▶ blocked
                          └─worker failure─▶ pending (retries<2) | blocked (retries==2)
```

Plan-level terminal outcomes live in Step 7.

## Prerequisites

- An acceptance plan at `docs/acceptance/<plan>.md` (draft is fine).
- An `origin` remote — the run ends by pushing the accumulator + opening the PR.
- Working tree clean. The skill creates worktrees + the accumulator off `main`.
- `.claude/plans/<plan>/tasks/*.md` exists, or the skill enters **bootstrap
  mode** (Step 0) and decomposes the plan, then proceeds into the loop.

## Workflow

### Step 0: Bootstrap (only if no task files exist)

1. Read `docs/acceptance/<plan>.md` end to end, plus every doc it cites
   (`docs/architecture.md`, `AGENTS.md`, the cited ADRs,
   `docs/design/IMPLEMENTATION-NOTES.md` when named).
2. Decompose: one task per ~200–400 LoC of expected change, `blocked_by` edges
   encoded, each task tagged with the `AC<n.n>` ids (and their `verify:`
   lines) it satisfies. Every numbered AC lands in exactly one task; none
   orphaned. Give each task a plain-language title. You MAY print the
   decomposition; do not stop for confirmation.
3. Write the task files — each quoting its ACs **and their `verify:`
   sub-lines** verbatim. Map each to its GitHub issue number in `issue:` where
   one exists. Create the accumulator off `main`:
   `git branch acc/<plan> main`.
4. Proceed straight into Step 1.

### Step 1: Load and validate

- Read every task file; parse frontmatter (reject malformed YAML).
- Build the dependency graph; refuse if cyclic (`TASKS_BLOCKED: cycle …`).
- For each task with `branch != ""`, mark `done` if reachable from the
  accumulator.
- Ensure the accumulator exists (create off `main` if not).

### Step 2: Compute the ready set

Ready = `status: pending` AND every `blocked_by` entry is `done`. If empty:
all `done` → run the aggregate gate (Step 6); otherwise → `TASKS_BLOCKED`.

### Step 3: Dispatch a wave of parallel workers

**On the first dispatch, flip the plan to in-progress** — if
`docs/acceptance/<plan>.md` reads `**Status:** draft`, edit it to
`**Status:** in-progress` on the accumulator (never in a worker — shared-file
rule).

For each ready task, in a single message (parallel):

- Edit the task file: `status: in-progress`.
- Spawn one worker per task — in mecatl the writable-worker verb is a
  **`Subagent` with `mode: "read-write"`** (direct-write against the real
  workspace; the orchestrator serialises merges itself), driven by the
  **`tdd-worker` agent contract** (`.claude/agents/tdd-worker.md`). When the
  Claude-Code harness is available, `Agent(subagent_type: "tdd-worker",
  isolation: "worktree")` is the equivalent dispatch. `description`:
  "Task <id>: <title>"; `prompt`: the brief template below with the task body
  **and its `## Acceptance criteria` subsection (with `verify:` lines)
  substituted verbatim inline**.
- Write the returned worktree path back to the task's `worktree` field.

**The brief MUST be inlined** — the worker starts from `main`, where the
task file does not exist. The `tdd-worker` agent carries the full worker
contract (branch off the accumulator, strict TDD via test-writer, Taskfile
gates, offline mocks/conformance, verify ACs, paste git log, do NOT push).
Do not re-lecture it.

**Worker brief template:**

```
You are implementing task <id> for mecatl, following the `tdd-worker`
agent contract at .claude/agents/tdd-worker.md (read it first — the task
file lives at .claude/plans/<plan>/tasks/<id>.md on the orchestrator's
branch and does NOT exist on yours). The brief is inlined below; that is
your sole source of truth for scope.

Accumulator branch: <accumulator>
Task branch to create: plan-<plan>/<id>

Task title: <title>

Brief:
<body>

Acceptance criteria you must satisfy (your named pinning test must assert
these; implement the exact test names their `verify:` lines promise):
<acceptance-criteria-with-verify-lines>

Follow the tdd-worker contract: branch off <accumulator>, strict TDD,
Taskfile gates, offline tests only, verify ACs, paste git log, do NOT push.
```

### Step 4: Collect results and merge

For each worker that returns:

- **Success** (branch reported, gates green per the worker's report):
  1. Write `branch: <name>`, clear `last_error`.
  2. Merge into the accumulator, tasks sorted by id ascending:
     `git checkout <accumulator>`; try `git merge --ff-only <task-branch>`; if
     it fails (accumulator advanced), `git rebase <accumulator> <task-branch>`
     then `git merge --ff-only`. On rebase conflict: `git rebase --abort`, set
     `status: blocked`, `last_error: "merge conflict: …"`, surface.
  3. On success the task is `done` (ancestry).
- **Worker failure:** increment `retries`; `retries<2` → `pending` + record
  `last_error`; `retries==2` → `blocked`.
- **Self-reported mis-decomposition:** `status: blocked`, record reason, no
  retry.

### Step 5: Loop

Re-compute the ready set (Step 2). Non-empty → dispatch another wave (Step 3).
Empty with all branches merged → Step 6. The orchestrator never touches `main`.

### Step 6: Aggregate gate + landed + ac-trace + panel

Per-task gates ran in each worker's worktree against that task's branch — they
never saw the **assembled** accumulator. This terminal gate runs the full
suite once on the assembled branch, from a **fresh temp worktree** (the main
checkout shares `.git` with agent worktrees and would scan them):

```bash
git worktree add .scratch/acc-gate-<plan> <accumulator>
# in .scratch/acc-gate-<plan>:
task lint;  LINT_RC=$?
task test;  TEST_RC=$?
task docs;  DOCS_RC=$?      # llms.txt regen + matlatl --strict
```

Use the `; RC=$?` form, never a pipe through `tail` (it swallows the exit code).

**If any of those fail:** surface the output, leave the accumulator as-is (do
NOT open a PR, do NOT auto-fix), keep the temp worktree for post-mortem, go to
Step 7 and report `blocked-pending-integration-fix`.

**If they pass:**
1. **Reconcile generated surfaces (single writer).** On the assembled
   accumulator: if the engine's exported API changed, `task api:update` +
   the `engine/CHANGELOG.md` note (or confirm `task api:check` is green);
   if any markdown changed, `task docs` was already run above. No manual
   prose reconciliation.
2. **Flip the plan to `landed`.** Edit `docs/acceptance/<plan>.md` from
   `**Status:** in-progress` to `**Status:** landed` and commit on the
   accumulator (never in a worker).
3. **Run `ac-trace --strict`:** `task ac-trace-strict` from the temp worktree.
   A landed plan's every `verify:` proof must resolve. **If it fails** (a named
   test missing, a citation unresolved), treat it like a failed gate: surface,
   report `blocked-pending-integration-fix`, stop.
4. **Run `/panel-review` inline (orchestrator mode).** Fixed point =
   `git merge-base main <accumulator>`. Parse its final line:
   `PANEL: ship_blockers=<n> …`.
   - **ship_blockers > 0 and repair budget remains (< 2 rounds):** derive a
     repair task per ship-blocker (a small task file citing the finding + the
     AC it protects), add them to `.claude/plans/<plan>/tasks/`, and **loop
     back to Step 2**. Increment the repair-round counter.
   - **ship_blockers > 0 and budget exhausted:** stop; report
     `blocked-pending-review` with the surviving ship-blockers for a human.
   - **ship_blockers == 0:** proceed to the PR.
5. **Push + open the single PR (then STOP).**
   `git push -u origin <accumulator>`; guard PR creation so a re-run updates
   rather than errors:
   `gh pr view <accumulator> >/dev/null 2>&1 || gh pr create --base main …`.
   The PR body: a summary of the tasks + ACs covered, the **inline panel-review
   report**, and `Closes #<n>` for the epic and **every task's mapped issue**
   (read `issue:` from each task file). Verify the closes survived any body
   edit (`gh pr view <pr> --json closingIssuesReferences`). Remove the temp
   worktree. **Do NOT merge to `main`. STOP** — the human merges.

### Step 7: Terminal report

- `ALL_TASKS_COMPLETE` — every task merged, aggregate gate + ac-trace + panel
  (0 ship-blockers) all green; accumulator pushed, PR open. Include the PR URL.
- `blocked-pending-integration-fix` — tasks merged but the aggregate gate or
  ac-trace failed on the assembled branch. No PR.
- `blocked-pending-review` — panel ship-blockers survived the repair budget. No
  PR; list them.
- `TASKS_BLOCKED: <list>` — open tasks remain, ready set empty (cycle, retries
  exhausted, merge conflict).

Print: counts (`done`/`in-progress`/`pending`/`blocked`), the accumulator's
commit count ahead of `main`, the PR URL on success, any blocked tasks' errors,
and the repair-round count.

## Worktree lifecycle

Agent worktrees are temp. After a task's branch merges, clean up with
`git worktree remove <path>` (failed worktrees are kept for post-mortem —
the skill does not auto-remove). Scratch dirs live under `.scratch/`
(gitignored), never `/tmp` (repo convention).

## Resuming

Re-invocation reads the task files + git ancestry and recomputes everything.
No in-memory state. A task stuck `in-progress` after a crash needs a manual
reset (edit back to `pending`, clear `worktree`/`branch`). The accumulator is
the single source of truth for what landed.

## Anti-patterns

- **"Auto-merge into `main`."** Forbidden — the human merges the PR. The
  accumulator exists so `main` stays clean.
- **"Run beyond the PR."** The PR is the terminus. Do not merge, tag, or
  deploy.
- **"Edit the plan doc from a worker."** Forbidden — shared-file merge trap.
  Generated surfaces (`engine/api/*.txt`, `llms.txt`) + the plan are
  reconciled once by the orchestrator on the assembled accumulator (Step 6).
  A worker that finds the design falsified reports a mis-decomposition
  (`status: blocked`).
- **"Skip the failing-test-first step."** TDD is the discipline; the worker is
  not done until its named test pins each AC and can fail when planted.
- **"Mock the LLM over the network."** Banned — tests are offline:
  `engine/adapter/mockllm` + `engine/adapter/memfs` (see tdd-worker).
- **"Land a new ADR or invariant without its enforcing test."** Same task
  branch or it doesn't land.
- **"Retry a failing worker indefinitely."** Two retries, then `blocked`.
- **"Force-resolve a merge conflict."** Mark `blocked` and surface — the
  conflict signals overlap the decomposition missed.
- **"Push a per-task branch."** Only the orchestrator pushes, once, the
  assembled accumulator, at the end.
- **"Commit to `main`."** Never — mecatl convention is PR-only (this
  overrides the AGENTS.md commit-directly note).

## See also

- `.claude/agents/tdd-worker.md` — the per-task implementor contract.
- `.claude/skills/panel-review/SKILL.md` — the final gate (orchestrator mode).
- `.claude/skills/to-acceptance-plan/SKILL.md` — writes the plan this consumes.
- `.claude/skills/test-writer/SKILL.md` — workers invoke it for the failing
  test.
- Upstream inspiration: atrium/titlani/tequitl `plan-orchestrate`.
