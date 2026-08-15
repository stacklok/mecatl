# ADR 0111 — Hardened publication and recovery for agent-owned skills

- Status: Accepted
- Date: 2026-08-15
- Scope: learned-skill publication, recovery, ownership, evaluation, and API correlation
- Supersedes: [ADR 0110](0110-evaluated-agent-owned-skills.md)
- Superseded by: none

## Context

The first implementation of ADR 0110 predated the path-free `SkillSource` bundle contract and left several unsafe transition and recovery edges. In particular, an arbitrary archived state could become a rollback target, an activation could commit without a reachable publication target, publication inherited a cancelled request, and model drafts were bound to process-global ownerless identity. The review API also lacked enough partition and generation correlation to reject stale responses.

## Decision

The live catalog remains path-free. `skillfs.AtomicCatalog` composes the immutable external `SkillSource` with body-only learned versions. Each model-facing execution captures one immutable generation before metadata/body/asset reads, so a concurrent refresh yields wholly old or wholly new content rather than a mixed activation. The ordinary `{name, asset}` schema, logical-name validation, source bounds, and external filesystem/driver asset behavior remain unchanged. Learned skills return instructions only and reject every asset request; they expose no directory, materialized payload, workspace root, script, or executable capability.

Repository archive accepts only `Active`. Rollback accepts an archived target only when its bounded durable history proves a prior `activate` or `rollback_to` transition and its last evaluation is PASS. Draft, FAIL, rejected, merely evaluated, staged, abstaining, and malformed historical archived records are never rollback candidates.

Activation is a publication-capability operation. The synchronous pipeline activates only when a real `Publisher` is bound; otherwise PASS remains staged. The service has a skill-specific authorization seam independent of memory promotion. It verifies the exact caller/project partition, publication capability, external-name precedence, and agent ownership before mutation. Shared catalogs publish only their bound partition; unrelated callers and projects cannot create active-but-unreachable state.

The pipeline is state-aware and idempotently resumes a content-addressed version after proposal linkage, evaluation, staging, activation, or publication. Validation disposition is stored with the immutable version/provenance before evaluation, and exact convergence preserves the stricter similarity hint, so a crash/retry cannot turn a review-only candidate into an automatic activation. The repository's partition mutation serialization retains the one-Active-version invariant for a skill name. Its inventory contains immutable external names and every learned version in the candidate partition.

`Config.SkillEvaluator` is trusted admission-control supplied by the host. It must use host-issued immutable fixture IDs, keep baseline and treatment independent, expose no tools, shell, or network, fence candidate content, and enforce time/token/output limits. A nil evaluator records ABSTAIN. The project ships no production keyword evaluator or model judge.

After a committed activate, archive, or rollback, publication runs with a bounded `context.WithoutCancel` context. One process-owned publication mutex serializes durable active-state reads and atomic catalog CAS swaps, so an older snapshot cannot land after a newer transition. The result returns committed state plus `published` or `pending_reconciliation`; publication failure never masquerades as a stale CAS conflict. Failure revokes the affected learned name immediately, including the automatic pipeline through its optional quarantine seam. Startup catalog construction and bounded live-inventory refresh reconcile from durable active state.

Model-facing `SkillDraft` derives its principal from the verified request context, its project from the exact live workspace root, and its owner from the actual main-agent identity. Missing identity or workspace refuses the draft. There is no ownerless shared draft write. A live learned entry carries its durable partition, and `LiveTool` rechecks the verified principal plus exact project workspace before returning its body; a shared catalog pointer therefore cannot turn another caller's refresh into execution authority. The v1 output remains body-only and inactive.

Learned-skill API responses carry the project partition, live generation, and selected skill/version correlation. Mutation responses also carry publication status. The TUI keeps each selected row's global/project partition authoritative for detail, diff, mutation, and rollback; every asynchronous response is correlated by request epoch plus skill/version before it can replace state. Inventory and receipt reads use bounded page/window limits instead of eagerly aggregating both partitions' maximum inventories, and rendering remains a fixed-size scroll window.

The durable local store opens manifest and body files with no-follow semantics, verifies regular-file type from the opened descriptor, and performs bounded reads before allocation, closing the final-component symlink swap window while retaining portability consistent with the existing platform helper discipline. It retains flock/CAS serialization and the version-before-manifest fsync/rename/directory-sync ordering. HTTP mutation JSON is size-bounded, rejects unknown/trailing/duplicate input, and bounds every string.

## Consequences

A durable lifecycle commit is unambiguous even when publication is temporarily unavailable, while conservative revocation prevents stale learned instructions from remaining callable. Restart and refresh converge from the repository. External, bundled, manual, pinned, project, user, and driver skills remain immutable and retain logical asset parity. No learned path can create assets/scripts, widen workspace roots, or modify core prompts/code.

## See also

- [ADR 0110](0110-evaluated-agent-owned-skills.md)
- [Cloud-native resource inventory](0027-cloud-native.md)
- [Architecture guide](../architecture.md#evidence-backed-reflection)
