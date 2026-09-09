# ADR 0306 — Human-reviewed development contracts before implementation

- Status: Accepted
- Date: 2026-09-06
- Scope: acceptance planning, interface review, autonomous implementation, and pull-request boundaries
- Supersedes: ADR 0295 where it requires one plan-and-code PR and one human checkpoint
- Superseded by: ADR 0319 where it requires a manual status-line edit as the mark of approval

## Context

ADR 0295 converged substantive work onto one acceptance-plan spine, but put the plan
and implementation in one accumulator PR. That made the behavioral plan visible only
when the code already existed. Reviewers could reject an interface, persistence shape,
or authority boundary only after autonomous implementation had made that choice costly.
It also committed task and retry bookkeeping even though those details are useful only
while an orchestration run is active.

The durable artifact should describe the behavior and exact interfaces humans approved.
Implementation mechanics should remain autonomous, isolated, test-first, and finally
reviewed, without mistaking approval of a contract for delivery of that contract.

## Decision

### Durable contract and lifecycle

`docs/acceptance/<slug>.md` is the durable behavioral contract, accompanied by a new ADR
and updates to living documentation where the decision requires them. Its status moves:

`draft → proposed → approved → in-progress → landed`

`proposed` means every human decision needed to implement the behavioral/interface contract
is resolved and recorded, so the plan is ready for review. `approved` means humans reviewed
the contract and merged its plan PR; it does **not** mean the behavior shipped. Implementation
sets `in-progress`. After all verification passes, the implementation or Combined candidate
sets `landed` in its PR diff. That is a proposed transition until merge: the target branch
remains `approved` or `in-progress`, and `landed` becomes authoritative only when that diff
merges. No cleanup or status-only PR follows.

ADR 0306 carries `Accepted` as the target state of this proposal PR: merging the PR is the
acceptance event. Until merge, this ADR remains mutable during review; once merged, it is
frozen under ADR 0002.

Every new plan carries exact `**Contract:** human-reviewed/v1` metadata and the current template
shape. Unmarked historical plans are grandfathered and need not be bulk-migrated, but any
material amendment after ADR 0306 adopts the full current template and passes its checker.

Every plan also has a non-empty `## Human decisions` section. It contains either
`None — <rationale>` or checklist items: unresolved judgments use `- [ ] ...`; resolved
judgments use `- [x] ... — Decision: ...`. Any unchecked item requires `draft`; `proposed`,
`approved`, `in-progress`, and `landed` require all decisions resolved and recorded. Material
behavior/interface choices belong here and cannot be hidden among deferred implementation
notes.

Every plan has a non-empty `## Interface contract` section. It enumerates the exact
proposed surfaces, including explicit `None — <rationale>` declarations, for:

- gRPC and protobuf;
- exported Go APIs and interfaces;
- model-facing tool schemas;
- CLI and configuration;
- events and persistence;
- security, trust, and authority boundaries; and
- compatibility and migration.

A public or otherwise material interface decision cannot be deferred to implementation.
An unresolved material decision keeps the plan in `draft`.

### Pull-request choice and human checkpoints

Substantive interface-bearing work uses two PRs by default:

1. `/to-acceptance-plan` creates a dedicated plan branch and worktree, validates and
   reviews the plan, marks it `proposed`, opens a **Plan / Interface** PR, and stops.
   That PR uses ordinary `Relates to #N` or `Tracking: #N` text.
2. A human reviews the behavioral and interface contract. Before merge, the plan is
   marked `approved`. The merged plan commit is the implementation baseline.
3. `/plan-orchestrate` records the plan PR and approved commit, autonomously implements
   with isolated TDD workers, runs aggregate gates and panel review, and opens only the
   **Implementation** PR. Humans review and merge that PR.

Combined is a narrow exception for a compact one-task plan with exactly one `### Scenario`.
It carries exact `**Expected tasks:** 1` metadata and a non-placeholder
`**Combined rationale:**` explaining why separate plan review adds no value. gRPC/protobuf,
exported Go APIs/interfaces, tool schemas, CLI/config, events/persistence, and
security/authority each begin `None — <rationale>`. Compatibility/migration may describe
workflow migration. A workflow-only meta-change may treat repository process documents and
skills as the interface reviewed in the same PR.
`/to-acceptance-plan` prepares the proposed plan on the eventual combined implementation
branch and stops without opening a separate plan PR. Only an explicit
`/plan-orchestrate` invocation adds implementation, runs verification, and opens the sole
**Combined** PR. Trivial or mechanical work remains exempt from the spine.

GitHub has no native “Related-to” closing keyword. Plan PRs use non-closing prose and are
not sidebar-linked as closing PRs. `Closes #N` or `Fixes #N` appears only in the body of
the final implementation PR that fully completes the issue, never in commit messages.
Partial implementation uses `Relates to #N` or `Tracking: #N` instead.

### Contract drift and amendments

The implementation PR links the plan PR and approved commit baseline, and states whether
its interfaces exactly conform. If a worker discovers a material choice that the plan did
not resolve, that is contract drift; the worker does not decide it. If implementation
discovers any material contract drift, orchestration stops all dispatch and returns
`blocked-contract-drift`; it cannot draft,
commit, push, or open an amendment. A separate, explicitly authorized
`/to-acceptance-plan` amendment mode updates the durable plan and related ADR/living/task
docs through the Split Plan / Interface PR flow. Its checker and docs gates pass, a human
reviews and merges it, and the plan returns to `approved` before orchestration may resume.
If no attempt has integrated, the accumulator may fast-forward or rebase onto the newly
approved amendment baseline. Once any attempt has integrated, merge the approved amendment
commit into the accumulator; never rebase or rewrite integrated commits. In either case,
the amendment commit must be an ancestor afterward. Record the amendment PR and full merged
commit in `run.md`; invalidate and regenerate pending briefs and decomposition; and
revalidate already integrated work against every amended acceptance criterion and interface
clause before dispatch continues. A code-review finding cannot silently redefine the
contract.

### Transient orchestration artifacts

Future task decomposition, inlined worker briefs, attempts, branches, worktrees, retries,
and repair-wave state live under ignored `.scratch/orchestrate/<slug>/` plus git ancestry.
They are run-local and never committed under `.claude/plans/<slug>/tasks/`. Existing
historical tracked plans remain untouched. The run record identifies every integration,
plan, and attempt worktree by path, owner classification (`harness-owned-native`,
`orchestrator-created-disposable`, or `primary-current` where applicable), branch, creation
baseline, and cleanup eligibility. Resume verifies those records against
`git worktree list`.

The implementation PR owns tracked feature-scoped cleanup. There is no dedicated cleanup
PR. Only explicitly orchestrator-created successful worktrees are eligible for removal;
harness-owned, primary, ambiguous, and failed worktrees are not.

## Consequences

- Humans can change behavior and interfaces before implementation cost accumulates.
- Substantive interface-bearing work pays for two reviews and two merges; this is the
  deliberate trust boundary.
- `approved` is unambiguous but requires readers and automation not to treat it as shipped.
- Compact no-interface work avoids a low-value extra PR, but must make that claim auditable.
- Implementation remains autonomous after contract approval: strict TDD, worktree
  isolation, aggregate gates, strict acceptance tracing, and panel review are retained.
- Run-local task state no longer pollutes durable history; failed attempts remain locally
  inspectable, while git ancestry records integrated work.

The workflow is influenced by Matt Pocock's
[`to-spec`](https://github.com/mattpocock/skills/tree/main/skills/to-spec),
[`to-tickets`](https://github.com/mattpocock/skills/tree/main/skills/to-tickets),
[`code-review`](https://github.com/mattpocock/skills/tree/main/skills/code-review), and
[`codebase-design`](https://github.com/mattpocock/skills/tree/main/skills/codebase-design)
patterns, and by obra/superpowers'
[brainstorming](https://github.com/obra/superpowers/tree/main/skills/brainstorming) and
[planning](https://github.com/obra/superpowers/tree/main/skills/writing-plans) practices.
These are acknowledged influences, not runtime or tooling dependencies; no external
scripts or hooks are imported.

## See also

- [ADR 0295](./0295-unified-development-spine.md) — the superseded single-PR choice.
- [Development process](../development-process.md) — the living workflow.
- [Acceptance plans](../acceptance/README.md) — lifecycle and contract format.
- [ADR 0002](./0002-documentation-lifecycle.md) — frozen decision lifecycle.
