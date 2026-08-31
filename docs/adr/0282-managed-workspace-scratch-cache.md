# ADR 0282 — Managed workspace scratch cache

- Status: Proposed
- Date: 2026-08-30
- Scope: `internal/adapter/permconfig` operator-global settings; `internal/app` workspace-cache composition and maintenance; user-addressable cross-command scratch artifacts
- Depends on: [ADR 0281](./0281-managed-temporary-command-leases.md)
- Supersedes: none
- Superseded by: none

## Context

A command-scoped temporary lease is deliberately disposable. It is the correct location for builds, tests, and other one-command intermediates, but it cannot hold an artifact an agent must use in a later Bash call.

The repository-local `.scratch/` directory is only a convention. It does not provide a user-level ownership boundary, a stable identity across worktree/session lifecycles, retention policy, inspection, or coordinated cleanup. System temp is the wrong substitute for cross-command artifacts: it may be swept by the operating system, often after ten days on Debian hosts, and `temp_scope: system` is a human-approved compatibility escape rather than the default storage model.

ADR 0281 supplies the prerequisite owned-root, manifest, locking, interval-gated reaper, and `mode: system` rollback mechanics for command/job temporary storage. This ADR adds a distinct, retained scratch cache built on that foundation.

## Decision

### 1. Provide one user-addressable scratch cache per workspace instance

When `temporary_storage.mode: managed` and `workspace_scratch_enabled: true` (the default), the harness provides a per-user, per-workspace cache for disposable scratch files that intentionally cross Bash invocations:

```text
<system-temp-dir>/mecatl/workspaces/<workspace-identity>/scratchpad/
```

The complete operator-global settings surface is:

```yaml
temporary_storage:
  mode: managed                 # managed (default) | system
  managed_root: "mecatl"        # relative to inherited system temp dir
  system_temp_dir: ""           # empty: inherit TMPDIR/TMP/TEMP from the server
  command_reap_after: 1h
  reap_interval: 1h
  workspace_scratch_enabled: true
  workspace_scratch_reap_after: 10d
```

The scratchpad is the fixed `scratchpad/` child of the workspace directory defined by ADR 0281. The directory is owner-only. The model receives the scratchpad path as a named workspace-scratch affordance, with a model-visible instruction distinguishing it from command temporary storage:

- command temporary storage is disposable at command completion or the command TTL;
- workspace scratch persists across commands but is automatically reaped after its configured inactivity period; and
- durable project artifacts belong in the workspace proper.

The scratch cache is not a transcript store, a persistent user-memory store, a replacement workspace, or a general file-storage API.

### 2. Use a stable workspace-instance identity

`<workspace-identity>` is the fixed-width, filesystem-safe directory key produced by ADR 0281's canonical workspace-instance resolver. Cache creation, lookup, rebinding, inspection, purge, and reaping all use that one resolver and validate the owner-only workspace manifest/index binding under their lifecycle locks. The resolver's local physical derivation, future provider-supplied identity, collision handling, and separation from `session.EnvironmentRef` are defined in ADR 0281.

A move within the same filesystem retains the scratchpad when the canonical identity revalidates; a checkout replacement, Git-directory replacement, identity mismatch, unsupported resolver, or cross-filesystem move conservatively creates a fresh workspace instance. Distinct worktrees have distinct identities even when they share a Git common directory.

### 3. Coordinate cache access and maintenance

The versioned scratchpad manifest and lifecycle lock are direct entries in the workspace directory as `scratchpad.manifest` and `scratchpad.lock`; they are not exposed through the model-facing `scratchpad/` path. Activity/access records remain internal control-plane data. An eligible managed Bash invocation receives the path in the per-invocation command-environment overlay as `MECATL_SCRATCHPAD`; the built system prompt instructs the model to use `$MECATL_SCRATCHPAD` for disposable cross-command files and to write durable output in the workspace. This instruction is absent when scratchpad support is disabled, system-scoped, or unavailable to the environment. Before the runner invokes the command, it acquires shared scratchpad protection and registers a cache-access lease. It retains that protection for the command/job lifetime; on successful release it atomically advances `last_released_at`, then releases the lease. Inspection does not refresh retention, and arbitrary writes after command release do not extend it.

The inherited maintenance worker and root GC coordination from ADR 0281 service workspace caches as well as command leases. Reaping and explicit purge acquire exclusive cache protection, re-read the manifest under that lock, and prevent new cooperating-harness cache access before deletion.

This protocol coordinates cooperating harness processes and ordinary cache writers; it is not an OS security boundary on a host where Bash has the launching user's full filesystem authority. A hostile same-UID command can seek out, replace, or delete user-owned cache/control-plane paths, just as it can delete the workspace itself. When an OS sandbox is available it must grant commands only the artifact subtree. Without that boundary, unexpected control-plane validation failures stop automatic reaping/purging rather than proceeding, but no tamper-proof integrity guarantee is claimed.

### 4. Reap inactive workspace scratch after ten days

The cache stays while any validated cache-access lease is active. Once inactive, the reaper may remove it after `temporary_storage.workspace_scratch_reap_after` (default **10 days**), measured from the authoritative `last_released_at` timestamp, which is written only after a completed cooperating command/job access. If a harness crashes while an access lease is held, operating-system lock release makes the cache eligible after the last committed `last_released_at`; a held lock always prevents reaping. The same operator-global settings and root-GC cadence from ADR 0281 apply. A malformed, foreign-owned, symlinked, unrecognised, or validation-failed cache is retained rather than deleted.

`temporary_storage.workspace_scratch_enabled: false` disables only workspace scratch: managed command/job leases remain enabled, but the harness neither creates nor exposes a scratchpad and does not reap existing workspace caches. Existing caches remain for explicit operator action. `temporary_storage.mode: system` remains the broader rollback switch: it disables managed command/job leases and workspace scratch together, and likewise leaves existing managed data untouched.

### 5. Provide explicit operator inspection and purge

A local operator inspect/purge command enumerates the exact scratch-cache deletion plan and requires interactive confirmation. It is unavailable to a headless agent as an autonomous destructive operation. After confirmation, purge acquires exclusive lifecycle protection and recomputes the plan. Any plan, identity, or manifest-revision drift rejects deletion and requires a fresh inspection.

### 6. Possible future direction: project-local scratchpad payloads

A future ADR may let a trusted repository declare a workspace-relative scratchpad payload location, such as `.scratch/`, making the existing repository convention an official harness affordance. The default remains the external managed scratchpad in this ADR. Only the payload location could move into the workspace: the lifecycle control plane, ownership record, and operator-controlled retention policy remain external.

This must be a trust-gated, explicit project declaration and must never grant automatic deletion authority over an arbitrary workspace directory. The implementation would require a harness-owned marker and validated binding to the external control-plane record; a missing, foreign, malformed, or drifted marker makes the directory ineligible for automatic reaping or purge. The project declaration cannot loosen retention, disable cleanup, or redirect the command/job managed roots. It must also specify Git-ignore guidance so scratch files do not become accidental project outputs. Until those ownership, trust, and deletion semantics are designed, `.scratch/` remains an ordinary repository convention.

## Consequences

**Benefits:**

- Agents gain an explicit cross-command scratch location without relying on `.scratch/` or host `/tmp` conventions.
- Scratch artifacts are isolated by workspace instance and survive a same-filesystem workspace move.
- The ten-day default aligns with the common Debian `/tmp` sweep horizon while retaining an explicit harness lifecycle.
- Cache retention, location, and the system-mode rollback remain operator-controlled.

**Costs and limits:**

- Scratch contents are intentionally not durable and can be removed after ten days of inactivity; users must store durable outputs in the workspace proper.
- Workspace identity and concurrent cache-access/reaping require a new, carefully tested protocol.
- On an unsandboxed same-UID host, locks coordinate cooperating actors but cannot protect against deliberately hostile Bash.
- The cache is a new outlives-a-call resource and must be added to ADR 0027's resource inventory when implemented.

## Implementation and acceptance outline

After ADR 0281 lands:

1. Add `temporary_storage.workspace_scratch_enabled` and `workspace_scratch_reap_after` to the same strict operator-global settings schema, using ADR 0281's shared managed-root layout and independent workspace-scratch disable semantics; `mode: system` remains the broader rollback from ADR 0281.
2. Add scratchpad manifest/access-lease lifecycle under the shared canonical workspace-instance resolver.
3. Add `MECATL_SCRATCHPAD` to the managed Bash invocation overlay and model-visible workspace-scratch guidance; test through the real factory that the instruction is present only for eligible managed environments.
4. Extend the ADR-0281 maintenance worker to reap only inactive, validated workspace caches after the configured ten-day default, using `last_released_at` and held-access exclusion.
5. Add an interactive operator inspect/purge flow with exclusive lock and plan-drift revalidation.
6. Test cross-command sharing, same-filesystem moves, checkout/worktree replacement, unsupported identity resolution, active-access and crash protection, completed access near the TTL boundary, inspection-not-refreshing retention, concurrent servers, retention expiry, mode-system and independently-disabled scratchpad behavior, malformed/symlinked control-plane rejection, and purge plan drift.

## See also

- [ADR 0281 — Managed temporary command leases and deterministic reaping](./0281-managed-temporary-command-leases.md) — prerequisite command/job lease and reaper protocol.
- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — resource inventory convention.
- [ADR 0201 — Background Bash](./0201-background-bash.md) — background-command lifecycle.
