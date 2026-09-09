# ADR 0319 — Merge-into-main is the plan-approval signal

- Status: Accepted
- Date: 2026-09-09
- Scope: acceptance-plan lifecycle status, `/plan-orchestrate` entry gate
- Supersedes: ADR 0306 where it requires a human to edit the plan's `**Status:**` line to
  `approved` before or during merging the Plan / Interface PR

## Context

ADR 0306 requires a human to explicitly mark a plan `approved` before merging its Plan /
Interface PR, treating that edit as a deliberate signal distinct from the ordinary act of
merging. Nothing enforces it: `check-acceptance-plan.sh` validates that some allowed status
word is present, but no CI check verifies that a Plan/Interface PR's diff sets `approved`
specifically, and the repository has no CODEOWNERS or branch-protection rule tying merge to
that edit. A reviewer can approve and merge the PR on GitHub without ever touching the status
line, leaving the plan on `main` still reading `proposed`. `/plan-orchestrate`'s entry gate
then hard-stops on a decision that was, in substance, already made, with no actionable next
step beyond editing the file and re-merging.

The distinction ADR 0306 draws — "someone merged this" versus "a human specifically attests
to this exact contract" — does not hold anywhere else in the spine. The Implementation PR
checkpoint (`docs/development-process.md` step 5) is exactly "review and merge," with no
equivalent field-flip, and ADR 0306 applies the identical rule to its own adoption ("merging
the PR is the acceptance event"). A Plan / Interface PR's diff is scoped to only the plan
document and generated docs, so it does not carry the bundled-unrelated-changes risk that
would make merge alone an unreliable proxy. Holding the plan checkpoint to a stricter standard
than the implementation checkpoint, with no scoped extra risk to justify it, produces friction
without a matching safety gain.

## Decision

Merging the Plan / Interface PR into the target branch is the approval event, full stop.
`/plan-orchestrate`'s Split entry gate no longer reads the plan's `**Status:**` line as a
precondition; it instead confirms, via `git merge-base --is-ancestor`, that the exact plan
file at the recorded commit is reachable from the target branch (`main`), unchanged since the
Plan / Interface PR. That ancestry check is the approval proof.

The `**Status:**` word remains in the template for human-readable tracking (the
`docs/acceptance/README.md` plan list, and distinguishing a still-open plan from a merged one
at a glance), but it is no longer a merge precondition and no longer a `/plan-orchestrate`
gate. If the merged plan still reads `proposed`, orchestration corrects it to `approved` as
part of its first commit on the accumulator, the same way it already sets `in-progress` at
first mutation. A reviewer editing the status line before merge remains allowed but is never
required.

No repository automation is added to detect or block a `proposed` plan merging: this decision
treats that outcome as expected and harmless, not as a defect to catch.

If a future need arises for a deliberate, GitHub-native approval signal distinct from merge
(for example, if plan PRs start bundling unrelated changes, or a reviewer other than the
actual technical approver begins merging routinely), the correct lever is a repository
branch-protection rule requiring an approving review on `plan/*` branches — a platform policy,
not text in the plan file or logic in `/plan-orchestrate`.

## Consequences

- Reviewing and merging a Plan / Interface PR now takes exactly the same action as reviewing
  and merging any other PR in this repository — no separate status-edit step to forget.
- `/plan-orchestrate` cannot be blocked by a forgotten status edit; ancestry is a fact of git
  history, not a manually maintained label.
- The `**Status:** approved` value in a merged plan file is now cosmetic bookkeeping,
  self-corrected by orchestration, not evidence the checker or entry gate depends on.
  Historical plans that read `proposed` on `main` are harmless and need no retroactive fix.
- This gives up the distinct "explicit contract attestation separate from merge" signal ADR
  0306 introduced over ADR 0295. That signal already did not exist in comparable form for the
  Implementation PR checkpoint, so the spine's actual guarantee is unchanged: a human reviewed
  and merged the contract, and a human reviews and merges the implementation.
- A plan could still merge with unresolved `## Human decisions` if a reviewer skims past the
  checker's `draft`-forcing rule; that risk existed before this ADR and is unchanged by it —
  the checker, not the status-transition mechanism, defends that invariant.

## See also

- [ADR 0306](./0306-human-reviewed-development-contracts.md) — the human-reviewed spine this
  ADR narrows.
- [ADR 0295](./0295-unified-development-spine.md) — the single-PR predecessor.
- [Development process](../development-process.md) — the living workflow.
- [Acceptance plans](../acceptance/README.md) — lifecycle and contract format.
