# Development process

mecatl separates human approval of behavior and interfaces from autonomous implementation.
The durable contract is `docs/acceptance/<slug>.md`; implementation follows that approved
contract with isolated TDD workers and ends at a second human merge gate.

```text
design → plan/interface PR → human contract merge → autonomous TDD implementation → gates + panel → implementation PR → human code merge
```

Substantive interface-bearing work uses two PRs by default. Combined delivery is a narrow
exception for a compact one-task plan with exactly one `### Scenario`, where gRPC/protobuf,
exported Go APIs/interfaces, tool schemas, CLI/config, events/persistence, and
security/authority each begin `None — <rationale>`. Compatibility/migration may describe
workflow migration, and splitting must add no review value. A workflow-only meta-change may
treat process documents and skills as the interface reviewed in the same PR. Trivial or mechanical changes may bypass the spine while still
running applicable gates.

## Durable plan contract

A plan moves `draft → proposed → approved → in-progress → landed`:

- `draft`: material decisions may still be open.
- `proposed`: validated and ready for human plan/interface review.
- `approved`: the plan PR was human-reviewed and merged; this is not shipped status.
- `in-progress`: implementation is underway against the recorded approved commit.
- `landed`: after every gate passes, the implementation/Combined candidate carries this
  proposed transition in its PR diff; it becomes authoritative only when that PR merges.
  Before merge, the target branch remains `approved` or `in-progress`.

Every plan contains numbered behavioral acceptance criteria with non-empty `verify:` lines
and a mandatory `## Interface contract`. The contract gives exact proposed surfaces for
gRPC/protobuf, exported Go APIs, tool schemas, CLI/config, events/persistence,
security/authority boundaries, and compatibility/migration. `None` requires a rationale.
Material public decisions cannot be postponed until code exists.

## Split path: the default

1. **Plan — `/to-acceptance-plan`.** Create a dedicated plan branch/worktree, draft the
   acceptance plan and any decision/living docs, run the bundled checker and `task docs`,
   run advisory design reviews, open a **Plan / Interface** PR, and stop. Its issue text is
   non-closing: `Relates to #N` or `Tracking: #N`.
2. **Contract review — human.** Review behavior and exact interfaces. Mark the plan
   `approved` before merging. The merged plan commit is the implementation baseline.
3. **Implement — `/plan-orchestrate`.** Confirm the approved plan is merged, record its PR
   and commit under `.scratch/orchestrate/<slug>/`, decompose run-locally, and dispatch
   isolated `tdd-worker` attempts. Material contract drift stops the run for a
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
checker and docs verification, human review/merge, and plan status `approved`. If no attempt
has integrated, the accumulator may fast-forward or rebase onto the newly approved amendment
baseline. Once any attempt has integrated, merge the approved amendment commit into the
accumulator; never rebase or rewrite integrated commits. In either case the amendment commit
must be an ancestor afterward. Record the amendment PR and full merged commit in `run.md`,
invalidate and regenerate pending briefs/decomposition, and revalidate integrated work
against every amended AC and interface clause.

## Operational state and cleanup

Durable specs stay in `docs/acceptance/`; new decisions stay in ADRs. Task decomposition,
inlined worker briefs, attempt/worktree/branch/retry state, and repair records are local
under ignored `.scratch/orchestrate/<slug>/`, with git ancestry authoritative for integrated
commits. `run.md` records each integration/plan/attempt worktree's path, owner classification
(`harness-owned-native`, `orchestrator-created-disposable`, or `primary-current` where
applicable), branch, creation baseline, and cleanup eligibility; resume verifies it against
`git worktree list`. Do not create new committed `.claude/plans/<slug>/tasks/*.md` state and
do not delete historical tracked plans.

The implementation owns tracked feature-scoped cleanup. There is no cleanup or status-only
PR. Only explicitly orchestrator-created successful worktrees are eligible for removal;
failed, harness-owned, primary, and ambiguous worktrees are retained.

## Verification gates

| Gate | What it pins |
|---|---|
| bundled acceptance-plan checker | plan shape, interface declaration, AC proofs, citations, scope |
| `task lint` | lint, vet, layering rules |
| `task test` | full offline suite and engine standalone proof |
| `task api:check` | guarded engine API compatibility |
| `task docs` | generated docs and strict links |
| `task ac-trace-strict` | every landed AC proof resolves |
| `/panel-review` | independent Spec / Standards / Test adequacy / Domain review |

See [the acceptance-plan guide](acceptance/README.md) and
[ADR 0302](adr/0302-human-reviewed-development-contracts.md).
