# ADR 0110 — Evaluated, versioned agent-owned skills

- Status: Superseded
- Date: 2026-08-14
- Scope: learned-skill domain lifecycle, proposal linkage, validation, and reference contracts
- Supersedes: none
- Superseded by: [ADR 0111](0111-hardened-agent-owned-skill-publication.md)

## Context

ADR 0109 deliberately deferred procedure materialization. Treating a reflected procedure as live instructions would let model-authored text cross from evidence extraction into a trusted catalog without evaluation, ownership, review, or retry-safe state. Reusing the memory promotion receipt would also conflate mutable fact convergence with an immutable instruction revision. A generic artifact framework would hide the skill-specific collision, activation, and security rules rather than simplify them.

The importable engine needs contracts that hosts can replace before a durable store or runtime pipeline is selected. Historical procedure proposals contain only title and body and must remain readable. New records need a stable skill name and explicit linkage, but there must be no retrospective sweep that silently materializes old deferred proposals.

## Decision

Define a concrete learned-skill vocabulary in `engine/learning`. A skill belongs to an explicit principal/project partition and exactly one owner agent. Its body-only bundle contains name, one-line description, and instruction body; generated assets and scripts are rejected in v1. Immutable content-addressed versions carry bounded proposal, evidence, and signal provenance, a supersedes edge, evaluation records, transition receipts, and created/updated timestamps. Lifecycle state is closed over draft, evaluated, staged, active, archived, and rejected. Evaluation verdict is closed over pass, fail, and abstain.

Expose a replaceable `SkillRepository` with domain-specific compare-and-swap operations: create draft, get, list, record evaluation, stage, activate, reject, archive, and rollback. There is no unconditional save. Mutations require the exact partition, owner agent, skill/version identity, and expected revision. A partition has bounded skills and history; one name has at most one active version. Exact content retries converge and merge bounded provenance without minting another version. Add narrow `SkillValidator` and `SkillEvaluator` interfaces rather than a generic artifact framework.

Ship `engine/adapter/memskill` as the reference repository and `engine/adapter/skillconformance` as the shared contract proof. Ship `internal/adapter/skillstore` as the durable single-host implementation: a stable flock protects a bounded manifest, while immutable content-addressed `versions/<version>/SKILL.md` files are committed before the manifest with temp-file fsync, rename, and directory fsync. A crash can leave only an unreferenced immutable version; reopen ignores it, and exact retries reuse it. Multi-instance CAS serializes through the flock. The store is lazy on empty startup and rejects symlink/path traversal at its fixed layout boundaries. Ship `engine/adapter/skillvalidation` as the logical validator. It reuses the existing skill-name and prompt-injection gates plus canonical memory secret/directive helpers, rejects malformed UTF-8, controls, machine-specific absolute paths, permission-grant claims, generated assets, and non-agent or other-agent exact-name collisions. An exact same-agent duplicate converges provenance; a merely similar item is only a staging hint. The legacy root `DirDrafter` delegates logical admission to this validator by default while retaining its quarantine constructor/tool contract and atomic filesystem write; hosts may explicitly disable the neutral validator during compatibility migration.

Extend procedure candidates with an optional name. Existing title/body-only records remain valid and readable, while `ValidateSkillCandidate` requires a valid name for new materialization. Add a distinct explicit-learn-procedure signal without widening explicit fact remember. Extend proposals with a skill id, the terminal `skill_materialized` status, and CAS `LinkSkillDraft`. The importable `engine/adapter/skillmaterialize` adapter validates an evidence-backed procedure into `SkillDraftInput`, persists its content-addressed draft, then CAS-links the proposal; a crash in between retries by proposal provenance and SkillID without minting another version. Only an explicit operation may link a historical `deferred_unsupported` procedure; it does not use `PromotionReceipt` and no startup sweep is added.

The pre-lifecycle `SkillDraft` quarantine remains compatible but is legacy. Its `mecated skills promote` command moves only an `origin:model` quarantine directory into an operator-selected filesystem catalog; it neither reads nor activates lifecycle repository records. `skills.ImportLegacyDraft` is the explicit bridge: it rejects assets and inventory collisions and imports an `origin:model` candidate as an agent-owned, unevidenced `Draft`, never as evaluated, staged, or active. When Build owns a lifecycle repository, the model-facing `SkillDraft` instead writes a validated versioned draft directly; it still cannot activate it. Operator/manual, bundled, pinned, project, user, external, and driver inventory remains immutable to this repository path.

Ship the importable synchronous `engine/adapter/skilllifecycle.Pipeline`. It processes one logical candidate and returns one bounded receipt; it owns no goroutine and accepts no filesystem path. `off` declines automatic procedures but permits explicit inactive drafts. `review` materializes and evaluates evidence-backed procedures, rejects FAIL, and stages PASS or ABSTAIN. `auto` additionally activates only PASS. A missing host evaluator conservatively ABSTAINS. The existing bounded reflection coordinator remains the sole automatic-work owner, so no second queue is introduced. Procedure proposals are first durably deferred, then materialized and linked; a crash/retry converges through proposal provenance and content identity. Historical deferred proposals move only through an explicit review action—there is no sweep.

Ship `engine/adapter/skillfs.AtomicCatalog` and `LiveTool` beside the unchanged `SkillSource`/`FSSource` snapshot contracts. The catalog retains deployment-owned external entries plus independent immutable learned generations per principal/project partition. Caller-bound views merge only the authenticated principal's global and admitted project generations; metadata, body lookup, and execution use the same view. Refreshing one partition retains every other partition. External/operator/project/user/driver skills merge first and always win collisions. Body-only v1 adds no dynamic read root.

Expose additive gRPC and HTTP learned-skill methods for bounded list/get/diff/change receipts and revision-CAS activate/reject/archive/rollback. All reads and mutations derive the caller/project partition; project mutations retain the exact-root trust gate. Bodies, diffs, evaluation summaries, evidence handles, and receipts are bounded and UTF-8 repaired. The existing `/skills` inventory marks active agent-owned entries, and the existing Reflections review links materialized procedures to their skill; neither surface auto-opens.

## Consequences

Hosts can implement and test safe skill lifecycle storage without depending on mecatl's composition layer. Content and lifecycle races converge under explicit CAS, ownership and partition checks are structural, and historical proposal files continue to decode. The extra domain types and repository methods enlarge the public engine API; the additive candidate/proposal fields are classified per the engine compatibility policy.

An active learned version becomes visible without restart. Authenticated sessions receive caller-bound global/project catalog views, and caller-scoped lazy hydration reconstructs those views from durable active state after restart; no caller refresh can evict or disclose another partition. Durable transition, authoritative reread, publication, and failure quarantine are serialized as specified by ADR 0111. The conservative default evaluator means automatic activation requires an explicitly wired evaluator that returns PASS.

## See also

- [Evidence-backed reflection and durable staged learning](0109-staged-learning-proposals.md)
- [Architecture guide](../architecture.md#evidence-backed-reflection)
- [Engine compatibility contract](../../engine/COMPATIBILITY.md)
