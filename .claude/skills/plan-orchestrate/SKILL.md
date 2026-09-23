---
name: plan-orchestrate
disable-model-invocation: true
description: >-
  Implement an approved mecatl acceptance plan with run-local decomposition, isolated
  TDD workers, aggregate gates, strict AC tracing, panel review, and an Implementation
  PR. Split work requires a merged approved plan and recorded baseline. Combined work
  requires an explicit no-interface rationale. Never drafts contracts, merges, or runs
  beyond the PR. NOT for direct Routine or Cleanup work.
---

# plan-orchestrate

Autonomously implement the human-approved contract, then open only its implementation
(or explicitly Combined) PR. Never merge to `main`.

Claude Code's `disable-model-invocation: true` prevents model-initiated invocation in that
host. It does not protect mecatl or any other harness. Another harness must require an
explicit user request before creating a worktree or branch, committing, pushing, or opening
a PR. Without that request, stop before the first such side effect.

## Entry gate

Route Routine and approved compatibility Cleanup directly under `docs/development-process.md`;
this skill's plan gate does not apply to them.

Read `docs/acceptance/<slug>.md` and the documents it cites. Run its bundled acceptance-plan
checker before delivery-specific validation; any failure blocks entry. Accept supported
`**Contract:** human-reviewed/v1|v2` metadata. For v2, require Bounded or Architectural work
classification and its matching decision-record outcome; do not downgrade it during
decomposition. Confirm the `## Human decisions` section has no unchecked item:
implementation never begins while human judgment remains.

### Split (default)

Require all of these or stop:

- the Plan / Interface PR is merged into the target base;
- the plan file, unchanged since that PR, is present at a commit reachable from the target
  base (`git merge-base --is-ancestor <plan-commit> <target-base>` — this ancestry is the
  approval proof; the `**Status:**` word is not a precondition); and
- the implementation branch starts from that exact commit (or a descendant that has not
  changed the contract).

If the merged plan still reads `**Status:** proposed`, correct it to `approved` as part of
the first commit on the accumulator (same timing as the `in-progress` bump below); do not
stop for a human to have made this edit.

Record the Plan / Interface PR URL/number and full approved commit in
`.scratch/orchestrate/<slug>/run.md` before decomposition.

### Combined (narrow exception)

Require `**Delivery:** Combined`, exact `**Expected tasks:** 1` metadata, a non-placeholder
`**Combined rationale:**` explaining why separate plan review adds no value, exactly one
`### Scenario`, and an explicit declaration that gRPC/protobuf, exported Go APIs/interfaces,
tool schemas, CLI/config, events/persistence, and security/authority each begin
`None — <rationale>`. Compatibility/migration may describe workflow migration.
A workflow-only meta-change may treat repository process documents and skills as the
interface reviewed in the same PR. The proposed plan must be in the integration worktree on the eventual combined
implementation branch; if the handoff left it uncommitted, explicitly stage and commit it as
the first combined candidate commit before decomposition or dispatch. Do not open a separate
plan PR. If any eligibility claim is false, stop and require the Split path. This explicit
invocation adds implementation and eventually opens the sole Combined PR.

## State model

All new orchestration state is ignored and run-local:

```text
.scratch/orchestrate/<slug>/
  run.md                 # stage, approved PR/commit, integration branch/worktree
  tasks/                 # decomposition and inlined briefs
  attempts/              # attempt, branch, worktree, result, retry and repair records
```

Do not create or modify future task state under `.claude/plans/<slug>/tasks/`; do not
delete historical tracked plans. Git ancestry is authoritative for merged attempts.
Inlined briefs are the worker's scope; local files are only the orchestrator's restart
notebook. Retain failed attempts for diagnosis.

Use one implementation accumulator branch (`impl/<slug>`) and one validated integration
worktree. For Combined delivery, continue on the eventual combined branch that already
contains the proposed plan. Never modify the primary checkout from an isolated workflow.

`run.md` must record the integration/plan worktree path, owner classification
(`harness-owned-native`, `orchestrator-created-disposable`, or `primary-current` where
applicable), branch, creation baseline, and cleanup eligibility, in addition to the stage
and approved baseline metadata. Record every attempt worktree the same way. On initial
entry and every resume, verify these records against `git worktree list`; mismatch blocks.
Only an explicitly `orchestrator-created-disposable` successful worktree may be eligible for
removal. Path location alone never proves ownership.

## Decompose

Create the smallest coherent task set under `.scratch/orchestrate/<slug>/tasks/`. A focused
change may be one task. Each task records real dependencies and quotes its assigned ACs and
`verify:` lines verbatim. Assign every AC exactly once. Do not defer or reinterpret an
approved interface. For Combined delivery, reject the decomposition unless it contains
exactly one task; do not dispatch or reinterpret `**Expected tasks:** 1`.

Compute ready tasks from dependencies. Dispatch independent tasks concurrently only when
the harness provides genuine concurrent writable isolation; otherwise dispatch in stable
task-id order.

## Dispatch isolated TDD workers

Each attempt gets a unique branch and worktree, for example:

- branch: `impl-<slug>/<task>-attempt-<n>`
- fallback worktree: `.scratch/orchestrate/<slug>/worktrees/<task>-attempt-<n>`

Use a harness-supplied writable isolated worktree when present. Otherwise create the
verified-absent fallback from the implementation accumulator. Exactly one worktree belongs
to an attempt; never create a nested worktree or fall back to the parent checkout.

Dispatch `.claude/agents/tdd-worker.md` with a self-contained brief containing:

```text
Plan: docs/acceptance/<slug>.md
Approved baseline: <full commit>              # Split
Plan PR: <URL>                                 # Split
Accumulator: impl/<slug>
Attempt / task branch / fallback worktree: <values>
Task title and scope: <text>
Acceptance criteria and verify lines: <verbatim text>
Exact approved interface clauses this task implements: <verbatim text>
```

The worker uses strict red-green-refactor TDD, applicable Taskfile gates, offline fakes,
and no push. It reports branch, worktree, commits, AC proof, and interface conformance.

If a worker discovers that a human judgment needed to implement the approved contract was
not resolved and recorded, or that the contract is otherwise materially wrong or incomplete,
it must stop as `contract-drift`; it never makes the missing decision or repairs around it.
Stop all dispatch and return
`blocked-contract-drift`. Orchestration must not draft, open, commit, or push an amendment,
and the blocked run cannot authorize one. Resumption requires a separately and explicitly
authorized `/to-acceptance-plan` amendment-mode invocation using the **Split** Plan /
Interface PR flow, including checker and task-doc verification, human review and merge. Merging is
the approval event; no separate status edit is required. Orchestration proves approval by git
ancestry and may correct a lagging `proposed` label to `approved` on entry. Before resuming,
record that amendment PR and its full merged commit
in `run.md`. If no attempt has integrated, the accumulator may fast-forward or rebase onto
the newly merged amendment baseline. Once any attempt has integrated, merge the amendment
commit into the accumulator; never rebase or rewrite integrated commits. In either
case, the amendment commit must be an ancestor afterward. Invalidate and regenerate all
pending briefs and decomposition, and revalidate already integrated work against every
amended acceptance criterion and interface clause before dispatch continues.

## Integrate and retry

Validate every returned branch/worktree/attempt. Rebase a successful task branch onto the
current accumulator in its own worktree, then fast-forward merge from the integration
worktree. A conflict is blocked, not force-resolved.

On worker failure, retain its branch/worktree and diagnostic record. Use a fresh attempt
path and branch for at most two retries (three attempts total). Never overwrite failed
state. A mis-decomposition or contract drift blocks immediately.

At first successful implementation mutation, set the plan to `in-progress` on the
accumulator. Workers never edit the shared plan.

## Aggregate gates and final review

After all tasks integrate, run from the integration worktree and preserve exit codes:

```sh
task lint; LINT_RC=$?
task test:race; TEST_RC=$?
task docs; DOCS_RC=$?
go run ./cmd/mecademo; DEMO_RC=$?
```

Run `task api:update` plus the required changelog update for intentional engine API changes.
Review and commit generated deliverables explicitly. Only after every implementation and
verification gate passes does the implementation/Combined candidate set the plan to
`landed` in its PR diff, regenerate docs, and run `task ac-trace-strict`. That edit is the
candidate branch's proposed state transition, not the target branch's current state:
`landed` becomes authoritative only when the PR merges. Until then, the target branch
remains `approved` or `in-progress`. Do not create a cleanup or status-only follow-up PR.

Run `/panel-review` in orchestrator mode. Its final line must be:

```text
PANEL: ship_blockers=<n> important=<n> advisory=<n> reviewer_failures=<n>
```

Malformed/missing output or reviewer failures block unless a human explicitly waives the
named reviewer failure. Ship blockers may receive at most two repair rounds using fresh
run-local tasks/attempts and the same TDD/isolation rules. A repair that changes the
contract is contract drift and requires a plan amendment.

## Open the PR and stop

Push only the assembled implementation/combined branch and open one PR for this stage.
The body must state:

- stage: **Implementation** or **Combined**;
- Plan / Interface PR and full approved commit baseline (Split), or the Combined rationale;
- ACs completed and gate/panel results;
- **Interfaces match approved contract: Yes**, or link every human-approved amendment;
- any non-material implementation deviations; and
- issue reference semantics.

Use `Closes #N`/`Fixes #N` only when this PR fully completes the issue. Otherwise use
`Relates to #N` or `Tracking: #N`. Closing keywords belong in this final completing PR
body, never commits. The Plan / Interface PR is never the closing PR.

Stop after opening/updating the PR. Do not merge, tag, deploy, or run beyond it.

## Cleanup and terminal states

Tracked feature-scoped cleanup belongs in the implementation PR, not a follow-up cleanup PR.
Ignored scratch/worktrees are removed only by their owning workflow and only when safe.
Remove a successful fallback worktree only if this orchestrator created it; never remove a
harness-owned or primary checkout. Retain failed attempts.

Report one of:

- `IMPLEMENTATION_PR_OPEN` — gates and panel green; include PR URL and approved baseline.
- `blocked-pending-integration-fix` — aggregate gate or strict trace failed.
- `blocked-pending-review` — panel/reviewer blockers remain.
- `blocked-contract-drift` — human-reviewed amendment required.
- `TASKS_BLOCKED` — dependency, retry, isolation, or conflict block.

Always include task counts, attempt/repair counts, branch/worktree, gates, interface
conformance, and unresolved concerns.
