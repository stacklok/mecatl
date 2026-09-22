# ADR 0283 — Managed delegation-fork lifecycle

- Status: Proposed
- Date: 2026-08-30
- Scope: `internal/adapter/forker`; `internal/adapter/managedtemp`; `engine/tool` fork-lifecycle seam; `engine/agent` Parallel winner lifecycle; `internal/app` temporary-storage composition
- Depends on: [ADR 0281](./0281-managed-temporary-command-leases.md)
- Supersedes: none
- Superseded by: none

## Context

`EnvironmentForker` creates temporary filesystem/environment forks for read-only Subagents, Parallel branches, and Team members. It creates either an isolated Git worktree or a force-copy workspace. Ordinary child lifecycle owners already remove their forks; Parallel `join=first` and `join=judge` deliberately retain a winning fork through the in-process preserved-fork LRU.

The existing graceful-shutdown LRU cleanup is necessary but insufficient: a crash, forced termination, or abandoned desktop process bypasses it and leaves a full workspace copy or worktree in system temporary storage. These allocations can consume substantially more disk than ordinary command temporary files.

ADR 0281 establishes the prerequisite managed-allocation roots, manifests, per-allocation locks, interval-gated cross-process reaping, operator-global settings, and system-mode rollback. Forks are not command leases: they are created by `EnvironmentForker` and may outlive an individual Bash invocation. They need their own allocation family and lifecycle ownership.

**Terminology.** A “fork” in this ADR is exclusively a filesystem/environment fork: an isolated Git worktree or force-copy workspace made by `EnvironmentForker`. It is not a session fork, continuation, or inherited conversation state. In particular, this ADR neither changes nor relies on Subagent's separate `fork: true` history-seeding option.

## Decision

### 1. Register every local managed delegation fork as an owned allocation

When `temporary_storage.mode: managed`, the in-tree local `forker.Forker` creates every filesystem/environment fork below the parent workspace's ADR-0281 workspace key:

```text
<system-temp-dir>/mecatl/workspaces/<workspace-key>/forks/fork-<allocation-id>/workspace/
```

The forker registers the allocation before materialization. Registration begins in `allocating` state and records no strategy; its `workspace/` child must not exist, so a reaper removes only that empty validated allocation and quarantines unexpected contents. It then records the closed `copy` or `worktree` strategy under the allocation lock **before** materializing that strategy, and transitions to `active` only after materialization succeeds. A failed worktree attempt changes the same allocation to `copy`; it does not mint an untracked fallback directory. A crash in `active`, `preserved`, `cleaning`, or retryable failure is recovered through the recorded strategy. `cleaning` is a resumable idempotent attempt, not authority to guess a strategy.

The manifest contains only lifecycle and cleanup metadata: allocation ID, timestamps, state, strategy after it is selected, owner process identity, the ADR-0281 workspace key and managed-workspace identity, and the worktree cleanup identity needed to validate the registered parent repository. It never records model content, command arguments, environment values, credentials, or transcript data. The worktree identity is a narrow, owner-only exception to ADR 0281's general no-raw-identity metadata rule: it is written only by the adapter after canonicalization, is never model-visible, and exists solely to revalidate fixed Git cleanup. It must be specified and reviewed with the managedtemp manifest version rather than inferred from the child `.git` file or agent input.

`EnvironmentForker` remains strategy-agnostic to the agent, but its returned cleanup capability must carry opaque adapter-owned lifecycle state. A Parallel winner promotes that exact capability before publishing its artifact handle; normal owners invoke the same capability's idempotent `Cleanup(context.Context) error`. `Preserve() error` fails closed: Parallel cleans the selected fork and returns a tool error without publishing an artifact handle. A registry that has begun shutdown similarly rejects preservation and leaves cleanup with the caller. The core never parses allocation IDs, workspace keys, or child paths. The exact exported `engine/tool` seam is a material interface decision and is recorded in the acceptance plan before implementation.

Custom, remote, or third-party `EnvironmentForker` implementations remain unmanaged unless they explicitly implement the new lifecycle capability. This ADR does not claim to redirect arbitrary external forkers beneath the local managed root.

### 2. Keep opaque artifact handles; do not expose fork paths

A selected winner continues to return an opaque `ArtifactHandle`, never a child filesystem root, `EnvironmentRef`, placement selector, or cleanup locator. The handle is not a placement capability. Model-visible result text describes the winner as ephemeral: the retained artifact can disappear through LRU eviction, graceful shutdown, or post-crash reaping after the preserved-fork retention interval. It does not promise a usable cross-command path.

The existing `PreservedForkStore.Preserve(handle, cleanup)` contract cannot select an LRU by workspace or promote the allocation before publication. The replacement lifecycle seam must transfer preservation through the opaque child allocation capability, allowing composition to select the workspace-specific LRU without making paths or allocation metadata visible to `engine/agent`. The obsolete root-based description is not retained.

### 3. Use per-workspace retained-winner LRUs and crash-only TTL reaping

The operator-global settings add:

```yaml
temporary_storage:
  managed_root: "mecatl"        # shared layout root from ADR 0281
  fork_reap_after: 1h
  preserved_fork_reap_after: 10d
  preserved_fork_cap: 4
```

These follow ADR 0281's shared managed-root layout and strict operator-tier rules. Both retention durations must be between one minute and thirty days, and `preserved_fork_reap_after` must not be less than `fork_reap_after`. Project settings cannot redirect the managed root or alter retention. `preserved_fork_cap` is a positive per-workspace-key bound, defaulting to four; an explicitly supplied `--fork-preserved-cap` is an operator-tier CLI override and wins over that setting. An omitted flag does not override YAML with its historical default.

Composition owns a process-scoped workspace-key → LRU registry. It creates an LRU lazily, removes an empty one, and drains every remaining LRU on `Built.Close`. Parallel winners from different sessions on the same ADR-0281 workspace key share one LRU; distinct keys do not evict one another. This intentionally replaces the former process-wide cap with a per-workspace cap. `mode: system` disables managed allocation registration and reaping, while the in-process per-workspace LRU cap remains in force.

Active allocations retain their allocation lock through normal cleanup, exactly as ADR 0281 requires. Therefore fork TTLs are **crash-only reaping fallbacks**: a live process's retained winner is removed by LRU eviction or graceful `Built.Close`, not by a concurrent reaper. After a process dies and the operating system releases the lock, an ordinary fork is eligible after `fork_reap_after`; a promoted preserved winner is eligible after `preserved_fork_reap_after`.

### 4. Preserve existing normal owners and make failure retryable

Normal cleanup remains with the existing lifecycle owners:

- ordinary Subagent forks remain owned by the child-drive lifecycle;
- Team forks remain owned by the supervisor's final cleanup;
- Parallel losers and `join=all` branches remain owned by the Parallel call; and
- a selected winner is owned by the target workspace's LRU until eviction or `Built.Close`.

Each normal cleanup path retains the allocation lock while it records terminal state, runs idempotent cleanup, and removes the allocation record; it releases the lock only as its final action. Copy forks use confined recursive removal. Worktree forks use a fixed, adapter-owned sequence: revalidate the recorded parent-repository and Git-administration identities, remove the Git worktree, remove the managed directory, then prune worktree administration. No cleanup command is built from model-controlled input.

A cleanup failure never removes the allocation record. It records a stable retryable failure state and leaves the validated allocation for a later sweep. A validation failure, including a moved, replaced, missing, inaccessible, or identity-mismatched parent repository, writes an owner-only `cleanup-quarantined.json` breadcrumb containing only a stable failure reason, timestamp, and retry count, then retains the complete allocation. The reaper never removes a worktree directory alone when parent validation fails.

`Built.Close` applies the existing ADR-0281 shutdown budget to LRU cleanup through cancellation-aware lifecycle cleanup. It attempts every retained entry before the deadline; an unfinished cleanup retains its allocation record for a later reaper rather than blocking shutdown indefinitely or claiming removal.

### 5. Reap only validated fork allocations

The shared managed-temp worker scans the `forks/` family under ADR-0281 root-GC coordination. A reaper may claim an allocation only after acquiring its non-blocking exclusive allocation lock and validating root containment, ownership, manifest version/schema, state, workspace identity, and strategy. It dispatches only a fixed cleanup strategy selected from the closed manifest strategy; it neither executes manifest content nor derives a shell command from an agent-controlled path.

For worktrees, cleanup revalidates the adapter-recorded parent repository locator and Git-administration identity. It never reads a cleanup locator from the child `.git` pointer. A missing, malformed, future-version, foreign-owned, symlinked, unrecognised, or identity-mismatched allocation is retained, never deleted. Managed-mode admission must also reject an allocation root equal to or below the source tree, preventing recursive self-copy when an operator configures a managed root inside a workspace.

## Consequences

**Benefits:**

- Every locally managed fork is attributable, independently lockable, and reaped after process death rather than accumulating under generic system temp.
- Existing immediate cleanup and preserved-winner workflows remain intact.
- Retention is calibrated to intent: one hour for ordinary crash residue, ten days for intentionally retained winners after process death, and four retained winners per workspace key.
- Opaque artifact handles preserve the path-free delegation and placement boundary.

**Costs and limits:**

- The lifecycle capability is an engine exported-API change and requires API-compatibility and changelog review.
- Worktree cleanup needs constrained reconstruction; ambiguous parent identity is quarantined and retried rather than guessed.
- A per-workspace cap intentionally does not impose a process-wide aggregate disk bound; deployments serving many workspaces must size the managed root accordingly.
- `mode: system` restores crash residue behavior by design because no managed ownership record exists.

## Implementation and acceptance outline

After ADR 0281 lands:

1. Record the exact lifecycle-capability and preserved-store replacement in the accepted interface contract, then update the engine API snapshot and changelog with implementation.
2. Add fork retention settings to the strict operator-global `temporary_storage` schema, reference documentation, and CLI-precedence tests.
3. Extend managedtemp with the versioned fork allocation state machine, manifest, lock, validated identities, promotion, normal cleanup settlement, and retry/quarantine records.
4. Route local `forker.Forker` allocation through the managed root before copy/worktree materialization, preserving fallback and system-mode behavior.
5. Add the composition-owned workspace-key LRU registry and transfer preserved-winner ownership through the opaque lifecycle capability; remove empty entries and drain all LRUs on `Built.Close` within its cleanup budget.
6. Extend the shared worker with fixed, hardened copy/worktree reaping and quarantine diagnostics.
7. Update living architecture and operator documentation, including ADR 0027's resource inventory. Test managed/system modes, configuration precedence, per-workspace isolation, promotion crash windows, normal-cleanup versus sweep locking, copy/worktree cleanup, retry/quarantine, containment, invalid manifests/identities, reaper concurrency, and model-visible ephemeral-handle guidance.

## See also

- [ADR 0281 — Managed temporary command leases and deterministic reaping](./0281-managed-temporary-command-leases.md)
- [ADR 0282 — Managed workspace scratch cache](./0282-managed-workspace-scratch-cache.md)
- [ADR 0201 — Background Bash](./0201-background-bash.md)
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md)
- [Architecture: parallelism](../architecture/parallelism.md)
- [Architecture: subagents and teams](../architecture/subagents-and-teams.md)
- [Architecture: extensibility](../architecture/extensibility.md)
- [Production readiness](../design/PRODUCTION-READINESS.md)
