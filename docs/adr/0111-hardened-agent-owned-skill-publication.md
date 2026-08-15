# ADR 0111 — Hardened publication and recovery for agent-owned skills

- Status: Accepted
- Date: 2026-08-15
- Scope: learned-skill publication, recovery, ownership, evaluation, and API correlation
- Supersedes: [ADR 0110](0110-evaluated-agent-owned-skills.md)
- Superseded by: none

## Context

The first implementation of ADR 0110 predated the path-free `SkillSource` bundle contract and left several unsafe transition and recovery edges. In particular, an arbitrary archived state could become a rollback target, an activation could commit without a reachable publication target, publication inherited a cancelled request, and model drafts were bound to process-global ownerless identity. The review API also lacked enough partition and generation correlation to reject stale responses.

## Decision

The live catalog remains path-free. `skillfs.AtomicCatalog` retains an immutable external `SkillSource` plus independent immutable generations keyed by principal/project partition; refreshing Alice, Bob, global, or project state replaces only that partition and can neither evict nor expose another partition. Caller-bound `LiveTool` instances select the authenticated principal's global partition and, when admitted, its exact project partition. `Spec`, inventory, and `Execute` read that same selected generation, so metadata cannot disclose a foreign skill before execution and a concurrent refresh yields wholly old or wholly new content. External filesystem/driver entries are deployment-owned, immutable, highest precedence, and preserve the ordinary `{name, asset}` schema, logical-name validation, source bounds, and asset behavior. Learned skills return instructions only and reject every asset request; they expose no directory, materialized payload, workspace root, script, or executable capability.

Repository archive accepts only `Active`. Rollback accepts an archived target only when its bounded durable history proves a prior `activate` or `rollback_to` transition and its last evaluation is PASS. Draft, FAIL, rejected, merely evaluated, staged, abstaining, and malformed historical archived records are never rollback candidates.

Activation is a publication-capability operation. The synchronous pipeline activates only when a real `Publisher` is bound; otherwise PASS remains staged. The service has a skill-specific authorization seam independent of memory promotion. It verifies the exact caller/project partition, publication capability, external-name precedence, and agent ownership before mutation. Shared catalogs publish only their bound partition; unrelated callers and projects cannot create active-but-unreachable state.

The pipeline is state-aware and idempotently resumes a content-addressed version after proposal linkage, evaluation, staging, activation, or publication. Validation disposition is stored with the immutable version/provenance before evaluation, and exact convergence preserves the stricter similarity hint, so a crash/retry cannot turn a review-only candidate into an automatic activation. The repository's partition mutation serialization retains the one-Active-version invariant for a skill name. Its inventory contains immutable external names and every learned version in the candidate partition.

`Config.SkillEvaluator` is trusted admission-control supplied by the host. It must use host-issued immutable fixture IDs, keep baseline and treatment independent, expose no tools, shell, or network, fence candidate content, and enforce time/token/output limits. A nil evaluator records ABSTAIN. The project ships no production keyword evaluator or model judge.

After a committed activate, archive, or rollback, publication runs with a bounded `context.WithoutCancel` context. One process-owned publication gate covers the external-name check, durable transition, authoritative active-state reread, generation validation, and partition swap; the repository retains its cross-process flock/CAS. Thus an old pre-archive or pre-rollback snapshot cannot install after a newer commit. Failure and uncertainty clear only the affected partition under the same gate and generation ordering, so an older failed publication cannot revoke a newer success. Every read/startup path lazily reconciles the authenticated principal's global and admitted project partitions from durable state; this caller-scoped hydration makes active skills available after restart without a mutation or unsafe partition-directory discovery. The result returns committed state plus `published` or `pending_reconciliation`; publication failure never masquerades as a stale CAS conflict.

Model-facing `SkillDraft` derives its principal from the verified request context, its project from the exact live workspace root, and its owner from the actual main-agent identity. Missing identity or workspace refuses the draft. There is no ownerless shared draft write. Per-session catalog assembly captures that same authenticated global/project view; no shared request-clobbered learned singleton exists. The v1 output remains body-only and inactive.

Learned-skill API responses carry the project partition, live generation, and selected skill/version correlation. Mutation responses also carry publication status. The durable store maintains a bounded receipt index independent of each version's bounded detail receipts; it preserves historical-version transitions and pages directly in stable `(time, skill, version, receipt-id)` order. Cursors are opaque, exact-boundary, and invalid or expired cursors fail rather than silently restarting. The TUI exhausts global and project skill/receipt pages independently. Its model-lifetime request epoch is monotonic across overlay close/reopen, and response generation comes from the response envelope even for an empty page, so delayed empty, non-empty, list, and detail responses cannot replace newer state. Rendering remains a fixed-size scroll window.

The durable local store opens manifest and body files with no-follow semantics, verifies regular-file type from the opened descriptor, and performs bounded reads before allocation, closing the final-component symlink swap window while retaining portability consistent with the existing platform helper discipline. It retains flock/CAS serialization and the version-before-manifest fsync/rename/directory-sync ordering. HTTP mutation JSON is size-bounded, rejects unknown/trailing/duplicate input, and bounds every string.

## Consequences

A durable lifecycle commit is unambiguous even when publication is temporarily unavailable, while conservative revocation prevents stale learned instructions from remaining callable. Restart and refresh converge from the repository. External, bundled, manual, pinned, project, user, and driver skills remain immutable and retain logical asset parity. No learned path can create assets/scripts, widen workspace roots, or modify core prompts/code.

## See also

- [ADR 0110](0110-evaluated-agent-owned-skills.md)
- [Cloud-native resource inventory](0027-cloud-native.md)
- [Architecture guide](../architecture.md#evidence-backed-reflection)
