# ADR 0224 — Validated automatic learned-skill activation

- Status: Accepted
- Date: 2026-08-17
- Scope: completed-trajectory procedure learning and learned-skill publication
- Supersedes: ADR 0111 (PASS-only automatic activation decision)
- Superseded by:

## Context

ADR 0111 required an evaluator PASS before automatic publication. That is a useful
high-assurance policy, but a standard Auto deployment without a separately configured
evaluator could only stage skills and therefore never made a learned procedure usable.
The draft had already passed body-only structural validation and carried verified
completed-trajectory evidence, but ABSTAIN was treated as a permanent publication
barrier.

The policy must not turn model-authored text into a new capability. Learned skills are
body-only instructions loaded through the existing `Skill` tool: they add no tools,
assets, executable files, paths, permissions, or workspace roots. Direct `SkillDraft`
also lacks completed-trajectory evidence and must remain inactive.

## Decision

Add a closed `validated | evaluated` learned-skill activation policy. The importable
pipeline keeps `evaluated` as its zero value. In the standard application, an omitted
policy resolves to `validated` only when Auto mode is explicitly selected; learning
itself remains Off by default. A trusted project may tighten `validated` to
`evaluated`, but cannot loosen it.

Both policies activate evaluator PASS through the existing high-assurance operation.
Evaluator FAIL is deny-dominant and rejects the version. Evaluator infrastructure
errors durably record the distinct, generic ERROR verdict, reject the candidate, return
the original error only to the caller, and never publish. The raw error is never persisted
or logged. A retry observes the rejected marker rather than reinterpreting it as ABSTAIN.
Under `evaluated`, deliberate ABSTAIN stages. Under Auto `validated`, an
ABSTAIN candidate may use the optional validated-activation repository capability only
when it is non-legacy, evidence-backed, staged, and has an accepted or exact validation
disposition. Similar candidates, collisions, unpublishable partitions, missing
publishers, and repositories without that capability remain staged.

The repository performs validated activation atomically under partition, owner,
revision-CAS, and one-active-version invariants. It records `activate_validated`, while
the PASS path records `activate`. Archive and rollback rely on durable prior-active
transition proof rather than accepting arbitrary ABSTAIN drafts.

## Consequences

A stock Auto deployment can publish genuinely reflected procedures without requiring a
second model evaluator. Existing Auto operators who require the old assurance level can
set `learning.skills.activation: evaluated`. Review and Off behavior do not change.

The security boundary remains capability-neutral: publication changes which bounded
instruction body the existing `Skill` tool can return, not what the harness can do.
The residual risk is quality: structural validation and trajectory evidence do not
prove that an ABSTAIN procedure is useful. Similarity staging, exact partitioning,
external-collision protection, reversible archive/rollback, and the evaluated opt-back
bound that risk.

## See also

- [Architecture](../architecture.md)
- [Learning and skills usage](../usage/skills-soul-usermodel.md)
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md)
- [ADR 0111](./0111-hardened-agent-owned-skill-publication.md)
