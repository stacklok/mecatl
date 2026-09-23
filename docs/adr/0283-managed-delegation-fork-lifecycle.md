# ADR 0283 — Managed delegation-fork lifecycle

- Status: Proposed
- Date: 2026-08-30
- Scope: `internal/adapter/forker`; `engine/agent` Subagent, Parallel, Team, and preserved-winner lifecycle; `internal/app` temporary-storage composition
- Depends on: [ADR 0281](./0281-managed-temporary-command-leases.md)
- Supersedes: none
- Superseded by: none

## Context

`EnvironmentForker` creates `mecatlfork-*` directories for read-only Subagents, Parallel branches, and Team members. It creates either an isolated Git worktree or a force-copy workspace. Ordinary child lifecycle owners already remove their forks; Parallel `join=first` and `join=judge` deliberately retain a winning fork for inspection and manual recovery, subject to the in-process preserved-fork LRU.

The existing graceful-shutdown LRU cleanup is necessary but insufficient: a crash, forced termination, or abandoned desktop process bypasses it and leaves a fork under the system temporary directory. These forks are full workspace copies or worktrees and can consume substantially more disk than ordinary command temporary files.

ADR 0281 establishes the prerequisite managed-allocation roots, manifests, per-allocation locks, interval-gated cross-process reaping, operator-global settings, and system-mode rollback. Forks are not command leases, however: they are created by `EnvironmentForker` and may outlive an individual Bash invocation. They require their own lifecycle adapter and retention classes.

**Terminology.** In this ADR, a “fork” is exclusively a filesystem/environment fork: an isolated Git worktree or force-copy workspace made by `EnvironmentForker`. It does not mean a session fork, continuation, or inherited conversation state. In particular, this ADR neither changes nor relies on Subagent's separate `fork: true` history-seeding option; it manages only the child environment's temporary filesystem allocation.

## Decision

### 1. Register every managed delegation fork as an owned allocation

When `temporary_storage.mode: managed`, `EnvironmentForker` creates every filesystem/environment fork below the parent workspace's managed directory:

```text
<system-temp-dir>/mecatl/workspaces/<parent-workspace-instance-id>/forks/fork-<allocation-id>/workspace/
```

It registers that allocation with the ADR-0281 managed-allocation protocol as an **ordinary** fork before materializing a Git worktree or force-copy. The manifest records only lifecycle and cleanup data: allocation ID, creation time, fork strategy (`worktree` or `copy`), owner process identity, the canonical adapter-derived parent-repository locator for a worktree, and physical identities for that parent repository, its Git administration directory, and the managed workspace. It never records model content, command arguments, environment values, credentials, or transcript data.

The operator-global settings add:

```yaml
temporary_storage:
  managed_root: "mecatl"        # shared layout root from ADR 0281
  fork_reap_after: 1h
  preserved_fork_reap_after: 10d
  preserved_fork_cap: 4
```

These follow ADR 0281's shared managed-root layout and strict operator-tier rules. Project settings cannot redirect the managed root or alter retention. `preserved_fork_cap` is a positive **per-workspace-instance** bound, defaulting to four; the existing `--fork-preserved-cap` is an operator-tier CLI override and wins over that setting. Every Parallel tool targeting the same stable workspace identity shares that workspace's LRU, including separate sessions on the same `app.Build`; distinct workspaces do not evict one another's winners. Composition owns a process-scoped workspace-identity → LRU registry, creates an LRU lazily for a workspace, removes an empty one, and drains every remaining LRU on `Built.Close`. This removes the former process-wide total disk bound; total retained winners can reach four times the number of active workspace identities, while the per-workspace cap and ten-day TTL bound each workspace independently. `mode: system` is a lifecycle rollback only: it disables managed allocation registration and TTL reaping, while the per-workspace LRU and its cap remain in force.

### 2. Preserve current normal cleanup owners and strategies

This ADR does not move normal cleanup responsibility into the generic reaper:

- ordinary Subagent forks remain owned by the child-drive lifecycle;
- Team forks remain owned by the supervisor's final cleanup;
- Parallel losers and `join=all` branches remain owned by the Parallel call;
- a preserved winner remains owned by the existing LRU and graceful `Built.Close` cleanup.

Each normal cleanup path retains its allocation lock while it records terminal state, runs its existing idempotent cleanup protocol, and removes the allocation record; it releases that lock only as the final action. Copy forks use recursive removal. Worktree forks use the existing bounded sequence: remove the Git worktree, remove the directory, then prune Git worktree administration. No new cleanup command is built from a model-controlled path.

A fork is necessarily ordinary when `EnvironmentForker` creates it: winner selection happens later in the Parallel tool. The composition-owned per-workspace LRU registry wraps `PreservedForkStore` with an adapter-owned opaque allocation handle for the target workspace identity. `Preserve(root, cleanup)` first calls the handle's idempotent `PromotePreserved`, which updates the validated manifest under the allocation lock from ordinary to preserved-winner, then hands the existing cleanup callback to that workspace's LRU. Promotion completes before the winner is published in a Parallel result. A crash before promotion leaves an ordinary one-hour orphan; a crash after promotion leaves a ten-day preserved-winner orphan. The core agent continues to see only its existing root-and-cleanup store contract and never imports the allocation adapter.

The generic reaper is the crash/forced-termination fallback, not a second active lifecycle owner.

A selected winner has no dependable "main agent is done inspecting it" signal. The parent may inspect the path in its next turn, a later user turn, or never; parent-run/session completion is therefore not a safe cleanup boundary. In normal operation, a winner is retained until the first of: (1) a fifth winner for the same workspace instance causes that workspace's four-entry LRU to evict the oldest; (2) graceful `Built.Close` drains the workspace LRU; or (3) the managed allocation reaper reaches its ten-day preserved-winner TTL. The path is deliberately an ephemeral inspection opportunity, not a lease the model owns. A future explicit operator release action may shorten that lifetime, but is not needed for automatic cleanup.

**Future capacity direction.** The per-workspace cap deliberately has no aggregate cap across many workspace identities. A future operator-level global disk or preserved-winner budget may evict the oldest eligible winner across workspace LRUs, but it is not part of this decision; deployments serving many workspaces must size the managed root accordingly.

### 3. Use two retention classes

| Class | Members | Default retention |
|---|---|---:|
| ordinary fork | Subagent, Team, Parallel loser, `join=all`, and a failed/abandoned ordinary branch | `fork_reap_after`: 1 hour |
| preserved winner | selected `join=first` / `join=judge` Parallel winner | `preserved_fork_reap_after`: 10 days |

The per-workspace in-process LRU may clean a winner before its ten-day fallback TTL, and graceful `Built.Close` drains every workspace LRU. The longer TTL only governs a preserved-winner allocation left behind when its owning process dies.

As with ADR 0281 command leases, TTL is deliberate cleanup authority for validated managed allocations. The reaper may remove an expired managed fork even if a detached process still uses it; long-lived work must not depend on a disposable fork path.

### 4. Reconstruct cleanup only from validated allocation metadata

A reaper may claim a fork allocation only after acquiring its non-blocking exclusive allocation lock and validating root containment, ownership, manifest schema, strategy, and parent/workspace identity. It invokes a fixed cleanup strategy selected from the manifest's closed strategy value; it never executes arbitrary manifest content or derives a shell command from a path supplied by an agent.

For a worktree allocation, cleanup revalidates the manifest's canonical adapter-derived parent-repository locator and its recorded physical Git-administration identity under the allocation lock. It verifies that the managed workspace is that repository's expected registered worktree before applying the existing Git-environment hardening and fixed worktree-remove → directory-remove → prune sequence. It never reads a cleanup locator from the child `.git` pointer or from agent input. If the parent repository was moved, replaced, missing, inaccessible, or otherwise fails validation, the reaper writes an owner-only `cleanup-quarantined.json` breadcrumb in the allocation directory with a stable failure reason, timestamp, and retry count, then retains the complete allocation for operator inspection and later retry; it does not remove the workspace alone. It never widens into a `/tmp/mecatlfork-*` name scan. TTL is therefore a deterministic reclaim target only for allocations whose cleanup identity remains validated, not authority to delete an ambiguous worktree.

### 5. Keep model-visible paths honest

Parallel result text continues to expose a selected winner path for inspection, but identifies it as ephemeral. It states that the path can disappear through LRU eviction, graceful shutdown, or the configured preserved-winner TTL. Fork paths remain implementation artifacts, not stable user storage. Cross-command disposable output belongs in the ADR-0282 workspace scratch cache; durable project output belongs in the workspace proper.

## Consequences

**Benefits:**

- Every mecatl-owned fork is attributable, independently lockable, and reaped after process death rather than accumulating under generic system temp.
- Existing immediate cleanup and preserved-winner workflows remain intact.
- Retention is calibrated to intent: one hour for ordinary abandoned forks, ten days for intentionally inspectable winners, and four retained winners per workspace instance.
- Operators can relocate the shared managed root, tune both retention classes and the per-workspace cap, or disable managed fork allocation/reaping with the same rollback switch as command temporary storage.

**Costs and limits:**

- Worktree cleanup needs a carefully constrained reconstruction path after process death; failures must remain retriable rather than deleting an ambiguous directory.
- An expired managed fork is disposable even if a detached process still references it; users must not use fork paths as long-lived storage.
- `mode: system` restores the existing crash-residue behavior by design, because the harness has no managed ownership record for those forks.
- A per-workspace cap intentionally does not impose a process-wide winner count; deployments that serve many distinct workspaces must size disk capacity against the configured root and the ten-day fallback TTL.
- The managed fork registry extends the outlives-a-call resource inventory and needs conformance tests across copy, worktree, cancellation, LRU, graceful close, crash, and concurrent-server paths.

## Implementation and acceptance outline

After ADR 0281 lands:

1. Add `fork_reap_after`, `preserved_fork_reap_after`, and per-workspace `preserved_fork_cap` to the same strict operator-global `temporary_storage` settings schema and reference documentation; use ADR 0281's shared managed-root layout.
2. Extend the ADR-0281 managed-allocation protocol with a closed `fork` family, `copy`/`worktree` strategies, and validated identity metadata.
3. Route `EnvironmentForker` allocation through the managed root and register each fork as ordinary before copy/worktree materialization; persist the validated parent-repository locator and Git-administration identity for worktrees while preserving current fallback and rollback behavior.
4. Add the composition-owned workspace-identity → LRU registry and adapter-owned ordinary→preserved promotion wrapper around `PreservedForkStore`; promote under the allocation lock before the target workspace's LRU retention/result publication, remove empty registries, drain all on `Built.Close`, and pin both crash windows.
5. Implement the fixed, hardened reaper cleanup strategies and retry diagnostics for validated orphaned copy/worktree allocations; a moved, replaced, missing, or invalid parent repository retains the complete allocation for retry.
6. Update model-visible winner-path wording and architecture/operator documentation.
7. Test managed/system modes (including the per-workspace LRU remaining active in system mode), configured TTLs and CLI cap precedence, four-per-workspace isolation across sessions, LRU eviction, empty-registry removal, Build-close draining, copy/worktree cleanup, normal-cleanup versus sweep locking, ordinary/preserved promotion and crash windows, crash-orphan recovery, moved/replaced/missing parent repositories, `cleanup-quarantined.json` breadcrumbs, modified child `.git` pointers, invalid manifests/identity mismatches, reaper concurrency, and retry-on-cleanup-failure behavior.

## See also

- [ADR 0281 — Managed temporary command leases and deterministic reaping](./0281-managed-temporary-command-leases.md) — prerequisite allocation, reaper, configuration, and rollback protocol.
- [ADR 0282 — Managed workspace scratch cache](./0282-managed-workspace-scratch-cache.md) — cross-command artifact storage distinct from forks.
- [ADR 0201 — Background Bash](./0201-background-bash.md) — background command lifecycle.
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — resource inventory convention.
- [Architecture: parallelism](../architecture/parallelism.md) — current Parallel fork and preserved-winner behavior.
- [Architecture: subagents and teams](../architecture/subagents-and-teams.md) — current child and team fork ownership.
- [Architecture: extensibility](../architecture/extensibility.md) — command-runner and environment seams.
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md) — shipped/deferred status tracking.
