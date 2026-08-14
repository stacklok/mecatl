# ADR 0111 — Hardened publication and recovery for agent-owned skills

- Status: Accepted
- Date: 2026-08-15
- Scope: learned-skill publication, recovery, ownership, evaluation, and API correlation
- Supersedes: [ADR 0110](0110-evaluated-agent-owned-skills.md)
- Superseded by: none

## Context

The first implementation of ADR 0110 predated the path-free `SkillSource` bundle contract and left several unsafe transition and recovery edges. In particular, an arbitrary archived state could become a rollback target, an activation could commit without a reachable publication target, publication inherited a cancelled request, and model drafts were bound to process-global ownerless identity. The review API also lacked enough partition and generation correlation to reject stale responses.

## Decision

The live catalog remains path-free. `skillfs.AtomicCatalog` composes the immutable external `SkillSource` with body-only learned versions. The ordinary `{name, asset}` schema, logical-name validation, source bounds, and external filesystem/driver asset behavior remain unchanged. Learned skills return instructions only and reject every asset request; they expose no directory, materialized payload, workspace root, script, or executable capability.

Repository archive accepts only `Active`. Rollback accepts an archived target only when its bounded durable history proves a prior `activate` or `rollback_to` transition and its last evaluation is PASS. Draft, FAIL, rejected, merely evaluated, staged, abstaining, and malformed historical archived records are never rollback candidates.

Activation is a publication-capability operation. The synchronous pipeline activates only when a real `Publisher` is bound; otherwise PASS remains staged. The service has a skill-specific authorization seam independent of memory promotion. It verifies the exact caller/project partition, publication capability, external-name precedence, and agent ownership before mutation. Shared catalogs publish only their bound partition; unrelated callers and projects cannot create active-but-unreachable state.

The pipeline is state-aware and idempotently resumes a content-addressed version after proposal linkage, evaluation, staging, activation, or publication. Similarity disposition forces review/staged even in auto; exact same-agent content converges provenance without a new version. Its inventory contains immutable external names and every learned version in the candidate partition.

`Config.SkillEvaluator` is trusted admission-control supplied by the host. It must use host-issued immutable fixture IDs, keep baseline and treatment independent, expose no tools, shell, or network, fence candidate content, and enforce time/token/output limits. A nil evaluator records ABSTAIN. The project ships no production keyword evaluator or model judge.

After a committed activate, archive, or rollback, publication runs with a bounded `context.WithoutCancel` context. The result returns committed state plus `published` or `pending_reconciliation`; publication failure never masquerades as a stale CAS conflict. Failure revokes the learned live entry fail-safe. Startup catalog construction and every live inventory refresh reconcile from durable active state.

Model-facing `SkillDraft` derives its principal from the verified request context, its project from the exact live workspace root, and its owner from the actual main-agent identity. Missing identity or workspace refuses the draft. There is no ownerless shared draft write. The v1 output remains body-only and inactive.

Learned-skill API responses carry the project partition, live generation, and selected skill/version correlation. Mutation responses also carry publication status. The TUI drains every skill and receipt page, preserves project on list/detail/diff/mutation/rollback, rejects stale generation or selection responses, and renders errors. Change pagination is bounded by receipt count rather than by the number of containing skills.

The durable local store reads only regular files through bounded readers before allocation, retains flock/CAS serialization, and keeps the version-before-manifest fsync/rename/directory-sync ordering. HTTP mutation JSON is size-bounded, rejects unknown/trailing/duplicate input, and bounds every string.

## Consequences

A durable lifecycle commit is unambiguous even when publication is temporarily unavailable, while conservative revocation prevents stale learned instructions from remaining callable. Restart and refresh converge from the repository. External, bundled, manual, pinned, project, user, and driver skills remain immutable and retain logical asset parity. No learned path can create assets/scripts, widen workspace roots, or modify core prompts/code.

## See also

- [ADR 0110](0110-evaluated-agent-owned-skills.md)
- [Cloud-native resource inventory](0027-cloud-native.md)
- [Architecture guide](../architecture.md#evidence-backed-reflection)
