---
name: to-acceptance-plan
disable-model-invocation: true
description: >-
  Turn Bounded or Architectural work into a concise scenario-first acceptance plan with
  exact interfaces and a decision-record outcome, then hand off at the selected Split or
  Combined checkpoint. Stops before implementation or orchestration. NOT for Spike,
  Routine, Cleanup, or task decomposition.
---

# to-acceptance-plan

Create the durable behavioral and interface contract at
`docs/acceptance/<slug>.md`, then stop at the delivery-specific handoff: a Split plan PR or
a prepared Combined branch.

## Contract

Claude Code's `disable-model-invocation: true` prevents model-initiated invocation in that
host. It does not protect mecatl or any other harness. Another harness must require an
explicit user request before creating a worktree or branch, committing, pushing, or opening
a PR. Without that request, draft and report only; do not perform those side effects.

- Before classification or drafting, load
  [`references/WORK-CLASSIFICATION.md`](references/WORK-CLASSIFICATION.md). Spike, Routine, and
  Cleanup bypass this skill. Continue only for Bounded or Architectural work; if evidence is
  insufficient, escalate or stop for human-authorized Spike work rather than silently
  downgrading.
- Use the eventual delivery branch in exactly one validated writable worktree. Split uses a
  dedicated `plan/<slug>` branch; Combined uses the eventual combined implementation branch.
  Never write in the primary checkout when operating from an isolated worktree.
- Start at `draft`; new and materially amended plans use exact
  `**Contract:** human-reviewed/v2` metadata plus a concise
  `**Work classification:** Bounded|Architectural — <rationale>`. Existing v1 plans remain
  valid until materially amended. A Bounded plan records
  `**Decision record:** None — <substantive rationale>`; an Architectural plan links the new
  or superseding ADR for its genuinely durable decision. Existing ADRs may still be cited.
  Never create an ADR merely to narrate Routine or Bounded work. Unresolved human judgments
  about material behavior or interfaces are unchecked items in `## Human decisions` and keep
  the plan draft.
- Set `proposed` only when `## Human decisions` declares `None — <rationale>` or every
  decision is checked and records `— Decision: ...`; record whether the path is `Split` or
  `Combined`.
- Default to `Split`. `Combined` is a narrow exception for a compact one-task, exactly
  one-`### Scenario` change only when it declares exact `**Expected tasks:** 1` metadata,
  a non-placeholder `**Combined rationale:**` explaining why separate plan review adds no
  value, and gRPC/protobuf, exported Go APIs/interfaces, tool schemas, CLI/config,
  events/persistence, and security/authority each begin `None — <rationale>`;
  compatibility/migration may describe workflow migration. A workflow-only meta-change may
  instead treat repository process documents and skills as the interface reviewed in that
  same PR.
- Split opens a dedicated Plan / Interface PR and stops. Combined opens no plan PR:
  `/plan-orchestrate` must be explicitly invoked to add implementation on the same branch
  and open the sole Combined PR.
- Never implement code, merge, or use a closing issue keyword.

## Authoring

1. Read `AGENTS.md`, `docs/acceptance/README.md`, relevant ADRs, and the owning
   architecture topic from `docs/READING.md`. Follow the documentation change
   review in `docs/development-process.md`: name the owning page, not a list of
   duplicate update targets. Planned behavior stays in the plan until implemented.
2. Draft from
   [`references/ACCEPTANCE-PLAN-TEMPLATE.md`](references/ACCEPTANCE-PLAN-TEMPLATE.md).
   Keep focused work compact. Every scenario has numbered `AC<n>.<m>:` assertions,
   non-empty `verify:` lines, and at least one repository citation.
3. Complete mandatory `## Human decisions` with exactly one machine-readable shape:
   `None — <rationale>`, or checklist items where open decisions are `- [ ] ...` and resolved
   decisions are `- [x] ... — Decision: ...`. Place every material behavior/interface
   judgment there; do not hide one as a deferred decision. Any unchecked item keeps the plan
   `draft`.
4. Complete `## Interface contract` using all seven exact canonical labels from the
   template: gRPC/protobuf, exported Go APIs, tool schemas, CLI/config,
   events/persistence, security/authority, and compatibility/migration. Every category
   needs non-placeholder content; use `None — <rationale>` only when genuinely absent.
   Public or material decisions may not be deferred to implementation.
5. Apply the declared decision-record outcome. Create a new ADR only for the
   Architectural decision named by the plan; update living docs where behavior changes. Add
   the plan to `docs/acceptance/README.md`.
6. Run:

   ```sh
   bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan.sh docs/acceptance/<slug>.md
   bash .claude/skills/to-acceptance-plan/scripts/check-acceptance-plan-test.sh
   task docs
   ```

   The regression fixture script is also wired through `task docs:check` and therefore
   `task docs`; the explicit command makes its authoring-time coverage visible.

7. Run one advisory `devils-advocate` pass and at most two relevant specialist spot-checks.
   Fold clear corrections in; batch material open decisions for the human. Re-run checks.

## Amendment mode

Use amendment mode only after a `blocked-contract-drift` handoff and an explicit
authorization from the directing human to invoke this skill. This skill owns the
recommended Split Plan / Interface PR route: amend the durable plan and related
decision/task docs, run the checker and docs gates, open the amendment PR, then stop
for human review. Its merge is the approval event. The authorization does not grant
branch, commit, push, or PR authority; the normal side-effect authority rules still
apply. For an ADR decision or rationale change, recommend a new or superseding ADR.

The directing human may authorize a different route. A direct amendment belongs to the
blocked `/plan-orchestrate` run, which records the authorization and applies it; return
control to that orchestrator. Do not mutate its accumulator, `run.md`, briefs, or task
dispatch from this skill.

## Worktree ownership record

Before any permitted branch/worktree side effect, create
`.scratch/orchestrate/<slug>/run.md` and record the integration/plan worktree path, owner
classification (`harness-owned-native`, `orchestrator-created-disposable`, or
`primary-current` where applicable), branch, creation baseline, and cleanup eligibility.
On resume, verify the record against `git worktree list`. Only an explicitly
`orchestrator-created-disposable` worktree that completed successfully may be eligible for
removal; never infer ownership from its path.

## Delivery handoff

For **Split**, explicitly stage only the plan/interface docs and generated docs, commit on
`plan/<slug>`, push that branch, and open a PR with stage **Plan / Interface**. Use
`Relates to #N` or `Tracking: #N` as ordinary text. GitHub has no `Related-to` keyword: do
not use closing keywords or sidebar-link this PR as the issue-closing PR. Report the PR
URL, branch, worktree, checker result, and docs result, then **STOP**. Merging the PR is the
approval event — no separate status-line edit is required before merge;
`/plan-orchestrate` proves approval by git ancestry and corrects the label if it lags.

For **Combined**, prepare the proposed plan on the eventual combined implementation branch
and **STOP without pushing or opening a separate plan PR**. If the explicit user request
authorizes a local commit, commit the plan there; otherwise leave it as the recorded handoff
and let the explicitly invoked orchestrator make the first combined candidate commit before
dispatch. The user must explicitly invoke `/plan-orchestrate` to add the one-task
implementation, verify the combined candidate, and open the sole **Combined** PR.
