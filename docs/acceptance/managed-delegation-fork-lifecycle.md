# Managed delegation-fork lifecycle — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — changes the exported environment-fork lifecycle seam, durable managed-allocation protocol, operator retention policy, and cross-process cleanup authority.
**Decision record:** [ADR 0283](../adr/0283-managed-delegation-fork-lifecycle.md)
**Phase:** managed temporary-storage fork family
**Status:** draft, 2026-09-15. ADR 0283 corrected for the existing path-free artifact contract; material lifecycle choices await human review.
**Delivery:** Split. The engine API, durable cleanup protocol, configuration, and retention authority require a Plan / Interface review before implementation.
**Expected tasks:** deferred to orchestration after the human decisions below resolve.

This plan makes locally created delegation environments attributable and recoverable after an abnormal process exit without weakening child isolation or exposing filesystem paths. It extends ADR 0281's private managed namespace with a fork allocation family while preserving immediate cleanup for ordinary children and bounded in-process retention for selected Parallel winners.

## Human decisions

- [ ] Approve the replacement of the process-wide eight-entry preserved-fork LRU with independently capped, workspace-key-scoped LRUs (default four); accept that aggregate retained disk is unbounded across workspace keys. — Decision: pending
- [ ] Approve an exported opaque `engine/tool` fork-lifecycle capability returned by `EnvironmentForker`, with cancellation-aware idempotent cleanup and preserved-winner promotion; approve removal/replacement of the current `agent.PreservedForkStore` ownership path after API-compatibility review. — Decision: pending
- [ ] Approve promotion failure behavior: Parallel cleans the selected winner and returns a tool error without publishing an artifact handle; registry shutdown rejects a late promotion and leaves cleanup with the caller. — Decision: pending
- [ ] Approve ADR 0281's workspace key as the sole partition identity for this feature (canonical local path-derived key today, no same-filesystem-move retention promise); correct ADR 0282's incompatible move claim separately before scratch-cache implementation. — Decision: pending
- [ ] Approve durable owner-only worktree cleanup identity as ADR 0281's narrow additional raw-identity metadata exception, and approve crash-only TTL reaping: a live preserved winner is cleaned only by LRU eviction or `Built.Close`; the reaper acts only after its allocation lock is released by process death. — Decision: pending

## Interface contract

- **gRPC / protobuf:** None — fork lifecycle, allocation records, and artifact handles remain local implementation details; no wire message carries a path, allocation ID, workspace key, or cleanup identity.
- **Exported Go APIs / interfaces:** Proposed exact replacement, subject to the second Human decision: `engine/tool.EnvironmentForker.Fork` returns a non-nil opaque `ForkLifecycle` instead of `func() error`; `ForkLifecycle` exposes `Cleanup(context.Context) error` and `Preserve() error`. `engine/agent` consumes only that capability for ordinary cleanup and Parallel promotion. A failed `Preserve` produces a Parallel tool error and invokes `Cleanup` without publishing an artifact. Remove `agent.PreservedForkStore` and `agent.LRUForkReaper` after composition moves per-workspace retention behind the lifecycle capability. Update `engine/api/*.txt` and classify the intentional change in `engine/CHANGELOG.md`.
- **Tool schemas:** `Parallel` parameters and result schema remain unchanged. Its result retains only the opaque `ArtifactHandle`; no child root, placement selector, exact `EnvironmentRef`, allocation ID, or cleanup locator is added.
- **CLI / config:** Add strict operator-global `temporary_storage.fork_reap_after` (default `1h`) and `preserved_fork_reap_after` (default `10d`), each constrained to `1m`–`30d` with preserved retention no shorter than ordinary retention, plus positive `preserved_fork_cap` (default `4`). Project-tier values warn-ignore. Only an explicitly supplied `--fork-preserved-cap` wins over the setting; an omitted flag does not overwrite YAML with the historical default. `temporary_storage.mode: system` disables fork registration and fork sweeping but leaves in-process LRU capping active.
- **Events / persistence:** Add a versioned, owner-only managedtemp fork manifest under `workspaces/${workspace-key}/forks/fork-${allocation-id}/`, an allocation lock, and owner-only retry/quarantine breadcrumb. States are `allocating`, `active`, `preserved`, `cleaning`, and retryable/quarantined terminal failure; `allocating` has no `workspace/` child and any unexpected contents quarantine, while every materialized/retryable state carries a closed `copy` or `worktree` strategy before materialization begins. No session snapshot, event, transcript, command, environment, credential, or model content changes. Update ADR 0027 List 1 with the workspace-key → LRU registry, fork allocations/locks, and worker extension; explicitly record that none changes session rehydration fidelity.
- **Security / authority:** Only managed local `internal/adapter/forker.Forker` allocations participate. Registration occurs before materialization under private no-link containment. Fixed cleanup dispatches from validated closed metadata, never child `.git`, artifact handles, agent input, or manifest shell text. A failed/moved/replaced/missing worktree parent is retained with a stable quarantine reason; cleanup never deletes the child workspace alone. Admission rejects managed roots equal to or beneath the source tree.
- **Compatibility / migration:** Existing `mode: system` forks continue using ordinary system temporary directories with current immediate cleanup and LRU behavior. Managed allocations are new, disposable state; unknown/future/malformed records fail closed and remain for inspection. Existing process-wide retained winners are not adopted or migrated.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — Registered local fork survives creation and normal cleanup safely

The local forker allocates each managed copy or worktree under ADR 0281's private workspace-key namespace before materializing it. Its opaque lifecycle capability owns cleanup across Subagent, Team, and Parallel normal paths; a worktree fallback remains within the same allocation. This preserves `EnvironmentForker`'s child-environment isolation contract while making cleanup reconstructible from validated adapter data ([ADR 0283](../adr/0283-managed-delegation-fork-lifecycle.md), [parallelism](../architecture/parallelism.md), [AGENTS.md](../../AGENTS.md)).

**Acceptance:**
- AC1.1: A managed local copy or worktree fork is registered below its parent workspace key before child materialization and receives an owner-only versioned record and exclusive lifecycle lock; an `allocating` record has no materialized workspace and quarantines unexpected contents.
  - verify: `TestManagedDelegationForkLifecycle_Scenario1_RegisterBeforeMaterialization`
- AC1.2: A strategy is recorded before its child materializes, a failed worktree attempt changes the same allocation to copy, and no unregistered fallback directory remains.
  - verify: `TestManagedDelegationForkLifecycle_Scenario1_WorktreeFallbackKeepsAllocation`
- AC1.3: Ordinary Subagent, Team, Parallel-loser, and `join=all` cleanup removes the child and allocation record idempotently while holding the allocation lock; partial `cleaning` and cleanup-failure records retain their strategy and are retryable rather than guessed or deleted.
  - verify: `TestManagedDelegationForkLifecycle_Scenario1_NormalCleanupSettlement`
- AC1.4: Managed allocation rejects a root at or beneath the source workspace and rejects malformed, symlinked, foreign-owned, or identity-mismatched control-plane entries without recursive deletion.
  - verify: `TestInvariant_managed_fork_containment`

### Scenario 2 — Selected winners retain opaque artifacts within their workspace bound

A selected Parallel winner transfers its exact opaque lifecycle capability to the composition-owned LRU before its artifact is published. The registry partitions by ADR 0281 workspace key, not a filesystem path or session `EnvironmentRef`; normal system mode retains this memory-only cap. The model-facing result remains path-free and accurately describes artifact ephemerality ([ADR 0283](../adr/0283-managed-delegation-fork-lifecycle.md), [ADR 0291](../adr/0291-server-owned-session-placement.md), [AGENTS.md](../../AGENTS.md)).

**Acceptance:**
- AC2.1: `join=first` and `join=judge` promote the selected winner before publishing its opaque artifact handle; a pre-promotion crash leaves an ordinary allocation and a post-promotion crash leaves a preserved allocation. A failed or shutdown-raced promotion cleans the selected fork, returns a tool error, and publishes no handle.
  - verify: `TestManagedDelegationForkLifecycle_Scenario2_PromotionCrashWindows`
- AC2.2: Four retained winners sharing one workspace key evict the oldest on the fifth, while winners on a different key do not evict them; an empty workspace LRU is removed and `Built.Close` attempts each retained cleanup with a shared deadline, returning without claiming completion for an unfinished allocation.
  - verify: `TestManagedDelegationForkLifecycle_Scenario2_WorkspaceScopedLRU`
- AC2.3: `mode: system` creates no managed fork allocation or sweep target but preserves the configured in-process LRU cap.
  - verify: `TestManagedDelegationForkLifecycle_Scenario2_SystemModeKeepsLRU`
- AC2.4: Parallel result text and its tool specification expose an opaque artifact handle only and state its ephemeral retention; neither exposes a filesystem path or placement authority.
  - verify: `TestManagedDelegationForkLifecycle_Scenario2_OpaqueArtifactGuidance`

### Scenario 3 — A later process reaps only validated crash residue

The shared ADR-0281 worker scans fork allocations under its existing root-GC coordination. It reclaims only expired, lock-claimable, validated records using a closed strategy; ordinary and preserved TTLs apply only after a former owner process has died. Ambiguous worktrees are quarantined intact for retry ([ADR 0281](../adr/0281-managed-temporary-command-leases.md), [ADR 0283](../adr/0283-managed-delegation-fork-lifecycle.md), [ADR 0027](../adr/0027-cloud-native.md)).

**Acceptance:**
- AC3.1: A reaper skips a lock-held active or preserved allocation regardless of age, and reclaims validated crash-orphaned copy/worktree allocations only after the applicable ordinary or preserved retention interval.
  - verify: `TestManagedDelegationForkLifecycle_Scenario3_CrashOnlyRetention`
- AC3.2: Reaper worktree cleanup revalidates adapter-recorded parent and Git-administration identities, never trusts the child `.git` pointer, and uses the fixed remove-worktree → remove-directory → prune sequence.
  - verify: `TestManagedDelegationForkLifecycle_Scenario3_WorktreeRevalidation`
- AC3.3: A moved, replaced, missing, inaccessible, malformed, or identity-mismatched parent retains the complete allocation and writes an owner-only stable `cleanup-quarantined.json` breadcrumb; a later valid retry can clean it.
  - verify: `TestManagedDelegationForkLifecycle_Scenario3_QuarantineAndRetry`
- AC3.4: Project-tier retention settings warn-ignore; valid operator durations are each `1m`–`30d` with preserved retention no shorter than ordinary retention; only an explicit CLI cap overrides YAML. Concurrent servers perform at most one interval-eligible root-GC sweep, and a sweep timeout records no successful completion.
  - verify: `TestManagedDelegationForkLifecycle_Scenario3_OperatorConfiguration`
- AC3.5: The living parallelism architecture describes opaque artifact handles and no longer promises winner or merge-conflict filesystem paths.
  - verify: inspection — `docs/architecture/parallelism.md` is the living implementation guide and must match the path-free result contract

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| User-addressable cross-command artifact storage | ADR 0282 | Preserved fork artifacts remain opaque and ephemeral; scratch-cache API is separate |
| Explicit artifact inspect/release API | future ADR | No path or placement authority is introduced by this lifecycle work |
| Aggregate cap across all workspace keys | future capacity ADR | ADR 0283 chooses a per-workspace cap only |
| Managed lifecycle for custom, remote, or third-party forkers | future adapter contract | This plan covers only the in-tree local forker |
| Same-filesystem workspace move retention | ADR 0282 correction | ADR 0281's current path-derived workspace key is used as-is here |

## Definition of done

1. `task lint`, `task test`, `task api:check`, and `task docs` pass.
2. `task api:update` updates the intentional engine API snapshot and `engine/CHANGELOG.md` records its compatibility classification.
3. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green.
5. ADR 0027 resource and fidelity inventory is updated, and `/panel-review` has no unwaived ship blocker.

## Deferred decisions and known risks

- The five unchecked Human decisions are material. This plan cannot become `proposed` or be dispatched until they are recorded as resolved decisions.
- A workspace-key partition is not a global disk quota. The managed root requires operator capacity planning across distinct workspace keys.
- Fork manifests and locks are durable cleanup control-plane state, not session state; corruption fails closed and may require operator inspection.
