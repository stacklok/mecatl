---
name: plan-orchestrate
description: >-
  Drive an existing mecatl acceptance plan through isolated TDD workers, task
  branches, one accumulator, aggregate gates, strict AC tracing, four-axis panel
  review, bounded repairs, and one human-merge PR. Compact issue plans may remain
  one task; independent tasks run concurrently only with real writable isolation,
  otherwise serially. Uses a harness-native worktree when supplied, else creates
  explicit .scratch worktrees. Use when docs/acceptance/<plan>.md is committed on
  a clean accumulator. NOT for drafting plans, direct implementation, merging to
  main, or running beyond the PR.
---

# plan-orchestrate

## Purpose

A plan is a tree of tasks under `.claude/plans/<plan>/tasks/`. Each task is a
markdown file with YAML frontmatter declaring dependencies, status, attempt,
and the local branch/worktree holding that attempt.

This skill scans the task directory, computes the ready set, and dispatches
`tdd-worker` agents (one task per worker in a validated harness-native writable
worktree, or an explicit dedicated worktree under `.scratch/` when none is
supplied). The dependency graph is unchanged by
harness capability: independent ready tasks run in parallel when concurrent
writable workers are supported, or serially in deterministic task-id order when
they are not. Workers do the work locally, run the mecatl gates, and return the
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
- **The plan rides the accumulator.** `/to-acceptance-plan` commits
  `docs/acceptance/<plan>.md` and generated docs on `acc/<plan>`, transfers its
  clean checkout as the integration worktree, and never touches `main`.
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
attempt: 0           # increment before dispatch; names branch/worktree
branch: ""           # local attempt branch, set on dispatch/success
worktree: ""         # validated attempt worktree path
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

`blocked_by`, `status`, `attempt`, `branch`, `worktree`, `retries`, `last_error`,
`accumulator`, and `issue` are **orchestrator-managed**. Workers never edit task
files. The orchestrator explicitly stages and commits these shared-state updates
on the accumulator before dispatch or rebase, and commits the final `done` state
after fast-forwarding; the integration worktree stays clean at ownership
boundaries. `done` is computed from git ancestry: `branch != ""` AND reachable from
the accumulator (`git merge-base --is-ancestor <branch> <accumulator>`).
Ancestry is authoritative; the `status` field is the orchestrator's notepad.

## Status state machine (per-task)

```
pending ─dispatch─▶ in-progress ─worker success─▶ (ff-only merge into accumulator) ─▶ done
                          │                              │
                          │                              └─merge conflict─▶ blocked
                          └─worker failure─▶ pending (retries<3) | blocked (retries==3)
```

Plan-level terminal outcomes live in Step 7.

## Prerequisites

- An acceptance plan committed on `acc/<plan>` by `/to-acceptance-plan`, with
  the clean checkout path and its ownership classification transferred as the
  integration worktree. The classification is exactly one of `primary-current`,
  `harness-owned-native`, or `orchestrator-created-disposable`.
- An `origin` remote — the run ends by pushing the accumulator + opening the PR.
- `git status --short` is empty in that integration worktree. Validate that it
  owns `acc/<plan>`; never check out that branch in another checkout.
- `.claude/plans/<plan>/tasks/*.md` exists, or the skill enters **bootstrap
  mode** (Step 0) and decomposes the plan, then proceeds into the loop.

## Workflow

### Step 0: Bootstrap (only if no task files exist)

1. Read `docs/acceptance/<plan>.md` end to end, plus every doc it cites
   (`docs/architecture.md`, `AGENTS.md`, the cited ADRs,
   `docs/design/IMPLEMENTATION-NOTES.md` when named).
2. Decompose into the **smallest coherent task set**. A focused issue with one
   implementation unit becomes one task; do not target an arbitrary LoC range
   or split work to manufacture concurrency. For larger plans, encode real
   `blocked_by` edges, tag each task with the `AC<n.n>` ids (and their `verify:`
   lines) it satisfies, and keep each AC in exactly one task. Give each task a
   plain-language title. You MAY print the decomposition; do not stop for
   confirmation.
3. Write task files that quote each assigned AC and `verify:` line verbatim and
   map any GitHub issue in `issue:`. Explicitly stage the task files in the
   integration worktree; commit them on the already-checked-out accumulator. Do
   not create or check out the accumulator here: `/to-acceptance-plan`
   transferred ownership of its clean checkout.
4. Proceed straight into Step 1.

### Step 1: Load and validate

- Read every task file; parse frontmatter (reject malformed YAML).
- Build the dependency graph; refuse if cyclic (`TASKS_BLOCKED: cycle …`).
- For each task with `branch != ""`, mark `done` if reachable from the
  accumulator.
- Validate that the transferred integration worktree is clean, owns the
  accumulator, and has the plan commit in its ancestry. Refuse a second checkout
  of the accumulator; never run `git checkout <accumulator>` elsewhere.

### Step 2: Compute the ready set

Ready = `status: pending` AND every `blocked_by` entry is `done`. If empty:
all `done` → run the aggregate gate (Step 6); otherwise → `TASKS_BLOCKED`.

### Step 3: Dispatch the ready set with mandatory isolation

**On the first dispatch, flip the plan to in-progress** — if
`docs/acceptance/<plan>.md` reads `**Status:** draft`, edit it to
`**Status:** in-progress` on the accumulator (never in a worker — shared-file
rule).

Dispatch the ready set in parallel only if the active harness supports
concurrent writable workers. Otherwise dispatch one task at a time in task-id
order, collecting and merging each result before dispatching the next ready
task. This changes scheduling only; never flatten or bypass `blocked_by` edges.

For every dispatched task:

- Increment `attempt`, set `status: in-progress`, and derive unique names:
  branch `plan-<plan>/<id>-attempt-<attempt>` and fallback worktree
  `.scratch/worker-<plan>-<id>-attempt-<attempt>`. Record both before launch.
  Never reuse an earlier attempt's branch or retained failed worktree.
- Spawn one worker per task under the **`tdd-worker` agent contract**
  (`.claude/agents/tdd-worker.md`). If the harness supplies that worker a
  writable isolated worktree, the worker validates and uses it. Only when no
  native isolated worktree exists does the worker create the recorded `.scratch`
  fallback. A nested or redundant worktree is forbidden; failure to establish
  exactly one isolated checkout blocks the task.
- In mecatl, dispatch a writable **`Subagent` with `mode: "read-write"`** and
  pass the fallback worktree path because direct-write supplies no isolation.
  The child creates and stays in that path. In Claude Code,
  `Agent(subagent_type: "tdd-worker", isolation: "worktree")` supplies native
  isolation, so the child uses and reports that path instead of creating another.
  Because writable Subagent calls are mutate-serial, use serial ready-set
  execution unless the host exposes genuine concurrent writable workers.
  `description`: "Task <id> attempt <attempt>: <title>"; `prompt`: the brief
  template below with the task body and its acceptance criteria inlined.
- Validate the returned root, branch, and attempt against task state; write the
  actual worktree path back if the native path differs from the fallback.

**The brief MUST be inlined** so the attempt's scope is immutable and the worker
never reads or edits orchestrator-managed task state. The `tdd-worker` agent
carries the full worker
contract (branch off the accumulator, strict TDD via test-writer, Taskfile
gates, offline mocks/conformance, verify ACs, paste git log, do NOT push).
Do not re-lecture it.

**Worker brief template:**

```
You are implementing task <id> for mecatl, following the `tdd-worker`
agent contract at .claude/agents/tdd-worker.md (read it first — task state at
.claude/plans/<plan>/tasks/<id>.md is orchestrator-managed and must not be
read or edited). The brief is inlined below; that is
your sole source of truth for scope.

Accumulator branch: <accumulator>
Attempt: <attempt>
Task branch: plan-<plan>/<id>-attempt-<attempt>
Fallback worktree (use only if no native isolated worktree was supplied):
.scratch/worker-<plan>-<id>-attempt-<attempt>

Task title: <title>

Brief:
<body>

Acceptance criteria you must satisfy (your named pinning test must assert
these; implement the exact test names their `verify:` lines promise):
<acceptance-criteria-with-verify-lines>

Follow the tdd-worker contract: validate and use a supplied native isolated
worktree, otherwise create the fallback .scratch worktree; stay on the named
attempt branch, use strict TDD and Taskfile gates, run offline tests only,
verify ACs, report attempt + worktree + branch + git log, and do NOT push.
```

### Step 4: Collect results and merge

For each worker that returns:

- **Success** (attempt, branch, worktree, and gates validated):
  1. Write `branch: <name>`, clear `last_error` in task state.
  2. Tasks sorted by id ascending, rebase while the task branch remains checked
     out in its returned worker worktree:
     `git -C <returned-worktree> rebase <accumulator>`. On conflict run
     `git -C <returned-worktree> rebase --abort`, set `status: blocked`, record
     `last_error: "merge conflict: …"`, and surface it.
  3. From the integration worktree that already owns the accumulator, run
     `git merge --ff-only <task-branch>`. Never check out the accumulator in a
     worker or any other checkout. On success the task is `done` by ancestry.
- **Worker failure:** retain that attempt's worktree, increment `retries` (the
  count of failed attempts), and record `last_error`; `retries<3` → `pending`
  for a newly named next attempt, `retries==3` → `blocked`. Thus the initial
  attempt may be followed by at most two retries (three total attempts). Never
  clear or reuse failed attempt paths.
- **Self-reported mis-decomposition:** `status: blocked`, record reason, no
  retry.

### Step 5: Loop

Re-compute the ready set (Step 2). Non-empty → dispatch another wave (Step 3).
Empty with all branches merged → Step 6. The orchestrator never touches `main`.

### Step 6: Aggregate gate + landed + ac-trace + panel

Per-task gates ran in worker worktrees and never saw the assembled accumulator.
Run every terminal mutation and gate in the same integration worktree transferred
by `/to-acceptance-plan`; it already owns the accumulator and repair waves keep
using it. If this skill was resumed without that path, adopt the existing clean
checkout that owns the accumulator and classify it from its provenance. If the
accumulator is unowned, create `.scratch/acc-gate-<plan>` explicitly and record
`orchestrator-created-disposable`. If another checkout owns the accumulator but
cannot be adopted, stop as blocked: never detach or remove a primary/current or
harness-owned checkout to seize the branch. Never create a nested worktree.

```bash
# in the validated integration worktree:
task lint;  LINT_RC=$?
task test;  TEST_RC=$?
task docs;  DOCS_RC=$?      # configuration reference + matlatl --strict
go run ./cmd/mecademo; DEMO_RC=$?  # terminal aggregate smoke; workers do not repeat it
```

Use the `; RC=$?` form, never a pipe through `tail` (it swallows the exit code).
Do not run the terminal gate in the parent checkout or a stale detached checkout.

**If any command fails:** surface the output, leave the accumulator as-is (do
NOT open a PR, do NOT auto-fix), keep the integration worktree for post-mortem,
go to Step 7 and report `blocked-pending-integration-fix`.

**If they pass:**
1. **Retain generated surfaces (single writer).** In the integration worktree,
   review generated changes and commit them on the accumulator. If the engine's
   exported API changed, run `task api:update` and include `engine/api/*.txt`
   plus the `engine/CHANGELOG.md` note. Generated files are deliverables: never
   discard or leave them uncommitted.
2. **Land and regenerate in the same checkout.** Edit
   `docs/acceptance/<plan>.md` from `**Status:** in-progress` to
   `**Status:** landed`, run `task docs` again so generated docs reflect the
   landed plan, and commit the status plus generated changes on the accumulator.
3. **Run strict trace from that checkout:** `task ac-trace-strict`. It now sees
   both the landed status and every retained generated file. If it fails (a
   named test missing, a citation unresolved), surface it, report
   `blocked-pending-integration-fix`, and stop.
4. **Run `/panel-review` inline (orchestrator mode)** from the integration
   worktree. Fixed point = `git merge-base main <accumulator>`. Its **last line
   must exactly match** the stable contract:
   `PANEL: ship_blockers=<n> important=<n> advisory=<n> reviewer_failures=<n>`.
   Reject a missing/malformed result as `blocked-pending-review`; do not infer a
   verdict from prose. Consume `ship_blockers` as follows:
   - **ship_blockers > 0 and repair budget remains (< 2 rounds):** in the
     integration worktree derive and explicitly stage/commit one repair task per
     ship-blocker (citing the finding + protected AC), add it under
     `.claude/plans/<plan>/tasks/`, increment the repair-round counter, and loop
     to Step 2. The integration worktree retains accumulator ownership; repair
     workers use fresh attempt-specific worktrees exactly like all other tasks.
   - **ship_blockers > 0 and budget exhausted:** stop; report
     `blocked-pending-review` with the surviving ship-blockers for a human.
   - **reviewer_failures > 0:** retry the failed reviewers before PR creation. If
     failures remain, stop as `blocked-pending-review` unless a human explicitly
     waives the named reviewer failures; record that waiver in the report.
   - **ship_blockers == 0 and reviewer_failures == 0 (or explicitly waived):**
     proceed to the PR. `important` and `advisory` remain visible but do not
     silently become ship-blockers.
5. **Push + open the single PR (then STOP).** From the integration worktree,
   `git push -u origin <accumulator>`; guard PR creation so a re-run updates
   rather than errors:
   `gh pr view <accumulator> >/dev/null 2>&1 || gh pr create --base main …`.
   The PR body: a summary of the tasks + ACs covered, the **inline panel-review
   report**, and `Closes #<n>` for the epic and **every task's mapped issue**
   (read `issue:` from each task file). Verify the closes survived any body
   edit (`gh pr view <pr> --json closingIssuesReferences`). Remove the
   integration worktree **only** when its recorded ownership is
   `orchestrator-created-disposable`. Never remove a `primary-current` or
   `harness-owned-native` checkout; their owner controls their lifecycle.
   **Do NOT merge to `main`. STOP** — the human merges.

### Step 7: Terminal report

- `ALL_TASKS_COMPLETE` — every task merged, aggregate gate + ac-trace green,
  panel has 0 ship-blockers and 0 unwaived reviewer failures; accumulator pushed,
  PR open. Include the PR URL.
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

Agent worktrees are attempt-scoped temp resources. After a task branch merges,
remove its successful checkout with `git worktree remove <path>` only when the
orchestrator explicitly created that attempt's disposable fallback. A
harness-owned native checkout remains harness-owned and must not be removed by
this skill. Failed attempt worktrees are retained for post-mortem. Every retry
increments `attempt` and uses a new branch/path, so retained failures cannot
collide. Scratch dirs live under `.scratch/` (gitignored), never `/tmp`. The
integration checkout follows its separately recorded ownership classification:
only `orchestrator-created-disposable` is removed; `primary-current` and
`harness-owned-native` are never removed here.

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
  Generated API surfaces (`engine/api/*.txt`) + the plan are reconciled once by
  the orchestrator on the assembled accumulator (Step 6). A worker that finds the
  design falsified reports a mis-decomposition
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
- **"Commit to `main` from this workflow."** Never — the orchestrator always
  ends at a PR. The repository's direct trivial/mechanical exception remains
  valid only outside this substantive acceptance-plan workflow.

## See also

- `.claude/agents/tdd-worker.md` — the per-task implementor contract.
- `.claude/skills/panel-review/SKILL.md` — the final gate (orchestrator mode).
- `.claude/skills/to-acceptance-plan/SKILL.md` — writes the plan this consumes.
- `.claude/skills/test-writer/SKILL.md` — workers invoke it for the failing
  test.
- Upstream inspiration: the internal sibling repos `plan-orchestrate`.
