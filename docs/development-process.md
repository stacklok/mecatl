# Development process

mecatl classifies work before choosing a workflow. Classification follows the decision and
blast radius, not diff size:

| Class | Observable criterion | Route |
|---|---|---|
| **Spike** | Evidence gathering with explicit questions and stop conditions; its output is not shipped as-is. | Bypass acceptance planning only when a human explicitly requests or authorizes the spike. Shipping the result requires reclassification. |
| **Routine** | Introduces no new durable decision and applies an established or mechanical, reversible transformation without changing a durable contract. | Implement directly with applicable tests and review. |
| **Cleanup** | Removes obsolete aliases, shims, duplicate representations, or legacy readers in favor of established canonical behavior after the operator has chosen the breaking-state treatment; it introduces no new product behavior or design decision. | Implement directly. Record the scope, current contract, and approved breaking-state treatment in the implementation PR. |
| **Bounded** | Substantive work that needs an acceptance contract but introduces no durable architecture decision. | Use the acceptance-plan spine; record local rationale in the issue, PR, or plan, not a new ADR. |
| **Architectural** | Changes a durable public/API compatibility, persistence/data-ownership, security/trust, deployment/operator, module/system-boundary, or cross-subsystem-invariant decision. | Use the acceptance-plan spine and create a new or superseding ADR for the genuinely durable decision. |

Routine is not bounded by locality or file count: a repository-wide mechanical rename remains
Routine when it is reversible and changes no durable contract. Cleanup is likewise not promoted
solely because it removes interfaces or spans many files. The operator's explicit choice to remove
compatibility for the named cleanup is sufficient; it does not require a formulaic spine waiver,
acceptance plan, ADR, or separate approval PR. Safety invariants, applicable tests and review, and
human merge authority still apply. New product behavior, a new design choice, or unresolved data
destruction is not Cleanup and must be classified on the decision still required.

When evidence does not support the lower class, escalate or ask the human to authorize a
Spike; workers never silently downgrade. Split versus Combined delivery is a separate choice
made after classification.

For Bounded and Architectural work, mecatl separates human approval of behavior and
interfaces from autonomous implementation. The durable contract is
`docs/acceptance/<slug>.md`; implementation follows that approved contract with isolated TDD
workers and ends at a second human merge gate.

```text
design → plan/interface PR → human contract merge → autonomous TDD implementation → gates + panel → implementation PR → human code merge
```

Split delivery is the default. Combined delivery is a narrow exception for a compact one-task
plan with exactly one `### Scenario`, where gRPC/protobuf, exported Go APIs/interfaces, tool
schemas, CLI/config, events/persistence, and security/authority each begin
`None — <rationale>`. Compatibility/migration may describe workflow migration, and splitting
must add no review value. A workflow-only meta-change may treat process documents and skills
as the interface reviewed in the same PR.

The human carve-outs from the spine remain explicit. Exploratory/Spike work bypasses planning
only when the directing human says so, is never merged to `main` as-is, and must be
reclassified before shipping. The directing human may also explicitly waive the spine for a
named piece of work ("skip the spine for this"). A waiver lifts only plan/interface ceremony,
never layering rules, AGENTS.md invariants, applicable verification, or human merge authority.
Agents infer neither carve-out.

## Durable plan contract

A plan moves `draft → proposed → approved → in-progress → landed`:

- `draft`: human judgments about material behavior or interfaces may still be open and are
  listed as unchecked items under `## Human decisions`.
- `proposed`: every human decision needed to implement the contract is resolved and recorded;
  the plan is validated and ready for human plan/interface review.
- `approved`: the plan PR merged into the target branch — merging is the approval event,
  proved by git ancestry, not a required status-line edit; this is not shipped status.
- `in-progress`: implementation is underway against the recorded approved commit.
- `landed`: after every gate passes, the implementation/Combined candidate carries this
  proposed transition in its PR diff; it becomes authoritative only when that PR merges.
  Before merge, the target branch remains `approved` or `in-progress`.

Every new plan and every materially amended legacy plan uses
`**Contract:** human-reviewed/v2`. It adds exactly one
`**Work classification:** Bounded|Architectural — <rationale>` and one matching
`**Decision record:**` outcome. A Bounded plan says exactly `None — <substantive rationale>`;
an Architectural plan uses exactly `[ADR NNNN](../adr/NNNN-*.md)` for an existing ADR file.
The checker validates only that form and target: contract review judges whether the ADR is new,
superseding, and relevant to the durable decision. Spike, Routine, and Cleanup work do not create
acceptance plans. Existing `human-reviewed/v1` plans remain valid and are not bulk-migrated.

Both versions contain numbered behavioral acceptance criteria with non-empty `verify:` lines,
a mandatory `## Human decisions` section, and a mandatory `## Interface contract`. Human
decisions are either `None — <rationale>` or checklist items; unchecked items require
`draft`, while checked items record `— Decision: ...`. The interface contract gives exact
proposed surfaces for gRPC/protobuf, exported Go APIs, tool schemas, CLI/config,
events/persistence, security/authority boundaries, and compatibility/migration. `None`
requires a rationale. Material public decisions cannot be postponed until code exists.

## Split path: the default

1. **Plan — `/to-acceptance-plan`.** Create a dedicated plan branch/worktree, draft the
   acceptance plan and any decision/living docs, run the bundled checker and `task docs`,
   run advisory design reviews, open a **Plan / Interface** PR, and stop. Its issue text is
   non-closing: `Relates to #N` or `Tracking: #N`.
2. **Contract review — human.** Review behavior and exact interfaces, then merge the PR.
   Merging is the approval event; no separate status edit is required. The merged
   plan commit is the implementation baseline.
3. **Implement — `/plan-orchestrate`.** Confirm the approved plan is merged (by git ancestry;
   correct a lagging `proposed` label to `approved` on entry), record its PR and commit under
   `.scratch/orchestrate/<slug>/`, decompose run-locally, and dispatch
   isolated `tdd-worker` attempts. A worker that discovers a missing human decision reports
   contract drift instead of making it; any material drift stops the run for a
   human-reviewed plan amendment.
4. **Gate and review — automatic.** On the assembled implementation branch run `task lint`,
   `task test`, `task docs`, the offline demo, `task ac-trace-strict`, and `/panel-review`.
   The implementation PR links the approved baseline and reports interface conformance or
   an approved amendment.
5. **Code review — human.** Review and merge the implementation PR. Only a PR that fully
   completes an issue uses `Closes #N`/`Fixes #N`; partial work remains non-closing.

## Combined path: narrow exception

A compact one-task plan may choose `Combined` only under the eligibility rule above. It
must carry exact `**Expected tasks:** 1` metadata and a non-placeholder
`**Combined rationale:**` explaining why separate plan review adds no value.
`/to-acceptance-plan` prepares the proposed plan on the eventual combined implementation
branch and stops without opening a separate plan PR. A separately and explicitly invoked
`/plan-orchestrate` adds implementation on that branch, runs all gates, and opens the sole
Combined PR. Human merge remains mandatory.

## Contract amendments

Material drift stops dispatch with `blocked-contract-drift`; the orchestrator cannot draft,
commit, push, or open an amendment. A separately and explicitly authorized
`/to-acceptance-plan` amendment mode uses the Split Plan / Interface PR flow, including
checker and docs verification, human review/merge. Merging is the approval event; no separate
status edit is required. Orchestration proves approval by git ancestry and may correct a lagging
`proposed` label to `approved` on entry. If no attempt has integrated, the accumulator may
fast-forward or rebase onto the newly merged amendment baseline. Once any attempt has integrated,
merge the amendment commit into the accumulator; never rebase or rewrite integrated commits. In either case the amendment commit
must be an ancestor afterward. Record the amendment PR and full merged commit in `run.md`,
invalidate and regenerate pending briefs/decomposition, and revalidate integrated work
against every amended AC and interface clause.

## Operational state and cleanup

Durable specs stay in `docs/acceptance/`. New or superseded durable architecture decisions
stay in ADRs; local rationale stays in the issue, PR, or acceptance plan; current behavior
stays in living docs; repeatable procedure stays in skills; temporary execution state stays
under ignored `.scratch/`. Rules state current invariants and may link to ADR rationale, but an
ADR does not automatically become a rule. Task decomposition, inlined worker briefs, attempt/worktree/branch/retry state, and repair records are local
under ignored `.scratch/orchestrate/<slug>/`, with git ancestry authoritative for integrated
commits. `run.md` records each integration/plan/attempt worktree's path, owner classification
(`harness-owned-native`, `orchestrator-created-disposable`, or `primary-current` where
applicable), branch, creation baseline, and cleanup eligibility; resume verifies it against
`git worktree list`. Do not create new committed `.claude/plans/<slug>/tasks/*.md` state and
do not delete historical tracked plans.

The implementation owns tracked feature-scoped cleanup; do not defer it to a follow-up cleanup
or status-only PR. Only explicitly orchestrator-created successful worktrees are eligible for removal;
failed, harness-owned, primary, and ambiguous worktrees are retained.

## Verification gates

| Gate | What it pins |
|---|---|
| bundled acceptance-plan checker | plan shape, human-decision/status consistency, interface declaration, AC proofs, citations, scope |
| `task lint` | golangci-lint (including govet), layering rules |
| `task test` | full offline suite and engine standalone proof |
| `task api:check` | guarded engine API compatibility |
| `task docs` | generated docs and strict links |
| `task ac-trace-strict` | every landed AC proof resolves |
| `/panel-review` | independent Spec / Standards / Test adequacy / Domain review |

See [the acceptance-plan guide](acceptance/README.md) and
[ADR 0306](adr/0306-human-reviewed-development-contracts.md).
