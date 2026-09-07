# ADR 0281 — Managed temporary command leases and deterministic reaping

- Status: Proposed
- Date: 2026-08-30
- Scope: `internal/adapter/permconfig` operator-global settings; `internal/adapter/osfs` command runner; `internal/adapter/procgroup`; `internal/app` command-runner composition; `internal/testutil/testhome`; managed command/job temporary storage
- Supersedes: none
- Superseded by: none

## Context

Desktop sessions are intentionally long-lived and do not have a dependable terminal lifecycle. Cleanup tied to a session end is therefore neither prompt nor reliable.

The local Bash runner kills the complete command process group when an agent command reaches its context deadline. That prevents a timed-out `go test` from leaving compilers or test subprocesses alive, but it can prevent Go test cleanup callbacks and `TestMain` defers from running. The repository's isolated test-home helper consequently leaves `mecatui-test-home-*` roots under the system temporary directory when its test process is forcibly terminated. Test-created `t.TempDir` roots have the same failure mode.

The current system temporary directory is shared with unrelated applications and may be RAM-backed. Name-prefix cleanup in `/tmp` is neither a sound ownership proof nor safe in the presence of concurrent harnesses. A desktop process can also be killed before its normal cleanup path runs, so immediate cleanup alone cannot guarantee recovery.

The harness needs an automatic lifecycle that does not require an agent to remember a testing convention, preserves a visible escape for host-shared temporary state, and allows multiple local servers to run without deleting one another's resources.

## Decision

### 1. Expose managed and system temporary-storage scopes on Bash

Bash gets a `temp_scope` enum:

- `managed` is the default. The harness supplies private command/job temporary storage and owns its lifecycle.
- `system` preserves the inherited operating-system temporary-directory behavior. The harness makes no deletion claim for paths created there.

`temp_scope` selects only the harness-provided `TMPDIR` and `GOTMPDIR` defaults. It is not an OS-enforced filesystem boundary: Bash can still set `TMPDIR`, invoke `mktemp -p`, or write an absolute host-temporary path under its ordinary Bash authority. The harness therefore treats `system` as an explicit, model-visible request for supported host-shared-temp behavior, not as evidence that a command did not use system temporary storage.

`system` is permission-visible and operator policy defaults that declared escape to human approval. A `temp_scope: system` call requires BOTH the ordinary Bash-command decision and a distinct synthetic `BashSystemTemp` capability decision; an Allow for `Bash(go test:*)` alone never permits system scope. `BashSystemTemp` defaults to Ask, is deny-dominant, and the approval/audit record names the requested system temporary-storage scope without recording a raw temporary path. Deployments may deny it or deliberately allow it at a looser posture. In this first version, `BashSystemTemp` is a tool-wide capability: an Allow permits the system-scope environment overlay for every Bash command that is independently allowed. It neither grants Bash nor authorizes any filesystem path.

For example, an operator who allows tests and accepts that any otherwise-permitted Bash command may select the system temporary-directory overlay configures both rules:

```yaml
permissions:
  allow:
    - "Bash(go test:*)"
    - "BashSystemTemp"
```

An operator can retain the default approval for system scope while allowing the test command, or deny the escape entirely:

```yaml
permissions:
  allow:
    - "Bash(go test:*)"
  deny:
    - "BashSystemTemp"
```

A future decision may introduce a compound or parameterized permission grammar that binds a Bash command pattern to temporary-storage scope (and potentially other Bash options). It is explicitly deferred: the v1 action is intentionally global, and no new mini-language is introduced by this ADR.

No raw agent-supplied temporary path is accepted as a harness cleanup target.

All managed-temp behavior is configured exclusively in the operator's user-global `mecatl/settings.yaml`; project settings cannot select paths, lower retention, or disable cleanup. The proposed surface is:

```yaml
temporary_storage:
  mode: managed                 # managed (default) | system
  managed_root: "mecatl"        # relative to inherited system temp dir
  system_temp_dir: ""           # empty: inherit TMPDIR/TMP/TEMP from the server
  command_reap_after: 1h
  reap_interval: 1h
  reap_timeout: 5m             # maximum duration of one startup/periodic sweep
  shutdown_reap_timeout: 1m    # maximum worker-join grace during shutdown
```

Configured managed roots may be absolute or relative. A relative root is cleaned lexically, must not escape with `..`, and is resolved beneath the inherited system temporary directory (`TMPDIR`, then `TMP`, then `TEMP`, then the platform default). An absolute root must be owner-controlled. An empty `system_temp_dir` inherits that same system temporary directory; a non-empty value follows the same absolute/relative validation. `command_reap_after` must be between one minute and thirty days; `reap_interval` must be between one minute and twenty-four hours; `reap_timeout` must be between one second and one hour; and `shutdown_reap_timeout` must be between one second and five minutes.

A previously absent managed root is created private by the harness. A later process may adopt an existing root only when it is a non-symlinked directory owned by the current user with private permissions, and every managed child it opens satisfies the same owner/type/no-link checks. Allocation and cleanup use handle-rooted traversal that refuses link traversal, then revalidate the opened target immediately before mutation or deletion; they never validate one string path and recursively delete another. Managed mode is supported on Linux and macOS in this first version. Windows and other platforms fail configuration validation loudly when selecting `mode: managed` and must use `mode: system`.

`mode: system` is the rollback switch: the harness stops creating managed command leases and supplies the configured/inherited system temporary directory to Bash. It also stops managed-root reaping and leaves existing managed data untouched for explicit operator inspection or removal. The agent's per-call `temp_scope` cannot re-enable management under this operator-selected system mode.

### 2. Allocate one private lease per managed command or background job

For a foreground managed Bash call, the local command runner creates a random, owner-only lease directory before starting the shell. It passes the lease's `tmp` child to the command through `TMPDIR` and `GOTMPDIR`, and supplies a reserved internal lease marker for harness-aware test helpers. These values are supplied through a new per-invocation command-environment overlay capability; they are never rendered into or prepended to shell text. A runner without that capability cannot serve managed Bash. The overlay is an intentional exported engine API addition and must follow the engine API-compatibility and changelog protocol.

The repository's test-home helper recognizes the marker, validates that it names the runner-owned lease, and creates its isolated `HOME` and `XDG_CONFIG_HOME` below it. This brings test homes and Go temporary directories under the same exact cleanup target.

A foreground lease is removed after ordinary command completion when its managed process group is gone. On cancellation or timeout, the runner terminates and joins that group, then attempts the same cleanup. The runner does not and cannot reliably discover descendants that called `setsid` or otherwise left the original group; if the group is gone, it treats the managed command as complete and deletes the lease. If the group remains, it defers immediate cleanup to the deterministic reaper policy in decision 6. The managed-scope contract explicitly requires processes that need longer-lived temporary storage to request the declared `system` scope. Cleanup never alters the command's original exit, cancellation, or deadline result.

A background Bash job owns one lease for its complete job lifetime. It is removed when the job completes or is cancelled when its managed process group is gone. A background process that needs temporary state beyond the managed lease's one-hour grace period must obtain the declared `system` scope.

### 3. Keep managed leases beneath a private, durable parent

`temporary_storage.managed_root` names one private managed namespace beneath the inherited system temporary directory. It is organized first by an opaque workspace key, giving an operator one place to find every resource associated with that workspace. An osfs workspace derives its key transiently from its canonical physical path; a remote, virtual, or on-demand-sandbox workspace provider supplies its own opaque ID. A worktree needs no special treatment: its distinct physical root naturally derives a distinct key. The key is distinct from the materialization path and `session.EnvironmentRef`.

No raw backend identity becomes a path component or durable metadata. The only owner-only diagnostic exception is the canonical current workspace path in `workspace.manifest`; it is not a workspace identity/index and is refreshed when the matching key is opened from a new path. The directory key is the first 96 bits of a domain-separated SHA-256 digest of backend kind and the transient identity, encoded with canonical unpadded URL-safe Base64 (exactly 16 characters). This makes the key compact and filesystem-safe without introducing a custom encoding. The per-workspace manifest repeats the key, its format version, and the canonical current workspace path for owner-only operator diagnosis; it is refreshed when that key is opened from a new path. An absent, malformed, or non-canonical key fails closed. A 96-bit collision is practically negligible for this disposable local namespace, so no global workspace index is maintained; an allocation-name collision fails closed rather than reusing a lease. This is the common key for all workspace-scoped resources; if a future session-recovery or audit use case requires persistence, it needs a separate durable field and reattachment contract rather than overloading `EnvironmentRef`.

```text
<system-temp-dir>/mecatl/
  gc.lock
  last-successful-sweep.json
  workspaces/
    <workspace-key>/
      workspace.manifest
      workspace.lock
      commands/                         # ADR 0281
        cmd-<allocation-id>/
          manifest.json
          lease.lock
          tmp/
        job-<allocation-id>/
          manifest.json
          lease.lock
          tmp/
      scratchpad/                       # ADR 0282
      scratchpad.manifest               # ADR 0282
      scratchpad.lock                   # ADR 0282
      forks/                            # reserved for ADR 0283
```

ADR 0281 owns the root GC coordination files and the command/job lease directories. The scratchpad and filesystem/environment-fork children are reserved for the follow-on allocation protocols. Every allocation remains in its own directory with its own manifest and lock; grouping them under a workspace reduces discovery sprawl without widening any allocation's deletion authority. The managed root, workspace roots, and every lease are created owner-only. They are managed namespaces, not general replacements for `/tmp`; user-selected system temporary storage remains unmanaged.

Each command/job lease directory is named `<kind>-<allocation-id>`, where `kind` is `cmd` or `job` and `allocation-id` is an opaque, cryptographically random 96-bit identifier encoded with canonical unpadded URL-safe Base64 (exactly 16 characters). The allocation ID is never derived from command text, a PID, or a timestamp. A collision fails closed rather than reusing an allocation. The manifest repeats the directory's ID and kind; a reaper rejects a mismatch.

A command receives only its lease's `tmp/` child as `TMPDIR` and `GOTMPDIR`. `manifest.json` and `lease.lock` remain direct children of that lease directory.

Each command/job lease contains an atomically written, versioned manifest with only lifecycle metadata:

- allocation ID and resource kind;
- creation and lifecycle-transition timestamps, including terminal state when it is observed; `last_activity` is not a command heartbeat;
- creating UID;
- owner PID plus process-start identity to reject PID reuse;
- process-group identity where the platform supports it; and
- lifecycle state.

It never stores command text, command output, environment values, credentials, or transcript content. Every manifest and the sweep-completion record carries an explicit format version. A missing, malformed, or newer-than-supported version fails closed: the process reports the condition and retains the record/allocation without rewriting or deleting it. Compatibility or migration policy for future supported versions is deliberately deferred until a version increment is proposed.

### 4. Coordinate concurrent harnesses with root and per-lease locks

Each active lease holds a per-lease advisory lock continuously for its normal lifecycle. The runner creates and locks it before publishing the manifest or starting a command, and retains it until normal cleanup has completed. Normal cleanup and a reaper both require that lease lock. A normal owner records terminal state, performs its idempotent cleanup, removes the allocation record, and releases the lock only as its final action; “release from the registry” never means an early unlock.

Bash has no default absolute command timeout: `timeout_ms` is optional. That does not make an active lease eligible for reaping. A reaper attempts this lock non-blockingly and skips a lease it cannot acquire, regardless of its creation or last-activity timestamp; it must never use age alone while the runner holds the lock. Lifecycle timestamps therefore need no command heartbeat. If the harness crashes, the operating system releases its lock; a later reaper can then validate the inactive lease and apply the TTL. A command that escaped its owner after that crash can lose managed temporary data at the TTL, by design; it must use the declared `system` scope when it needs longer-lived storage.

The managed-temp root has a separate non-blocking GC coordination lock plus an atomically written last-successful-sweep record. On startup or periodic maintenance, a process:

1. attempts the root GC lock;
2. rereads the completion record after acquiring the lock;
3. skips scanning when a completed sweep is newer than the configured interval;
4. scans only if the interval has elapsed; and
5. records completion only after the full sweep finishes.

The root lock does not gate command execution or allocation. It prevents repeated scans when many desktop servers start and stop concurrently. If a sweep dies before completion, it records no completion timestamp; a later process may retry.

### 5. Own and bound periodic maintenance explicitly

`app.Build` owns the managed-temp maintenance worker. It performs one interval-gated best-effort startup attempt, then attempts the same sweep on the configured cadence while the Build remains live. Each startup or periodic sweep runs with `temporary_storage.reap_timeout` (five minutes by default) and contends only on the root GC lock; it never delays command execution.

`Built.Close` cancels the maintenance worker and waits for it to join for at most `temporary_storage.shutdown_reap_timeout` (one minute by default) before cache/store teardown. The worker must observe cancellation during every scan and metadata operation; a shutdown deadline expiry is reported as a bounded shutdown failure, never recorded as a successful sweep. A disabled or unsupported managed-temp backend returns a no-op cleanup. The sweep's completion state is durable coordination data, but the worker's in-memory ticker and active state reset on restart.

The worker, root lock, and completion record are new outlives-a-call resources. Their owner, scope, cleanup, and restart disposition must be recorded in the cloud-native resource inventory with the implementation.

### 6. Reap validated managed allocations deterministically after their TTL

The reaper considers a command/job lease for deletion only when all of the following hold:

1. it acquired the lease's non-blocking exclusive lock;
2. the directory, lock, and manifest pass ownership, mode, containment, schema, and no-symlink validation; and
3. the allocation has exceeded its configured TTL since its recorded owner terminal state, or since creation or its most-recent recorded lifecycle transition when a harness crash prevented recording that state.

The command/job recovery TTL defaults to one hour (`temporary_storage.command_reap_after`). With the default hourly sweep cadence (`temporary_storage.reap_interval`), a lease that was not cleaned immediately is targeted for removal roughly one to two hours after it became eligible. Process or process-group liveness can accelerate immediate cleanup and is recorded for diagnostics, but is not a prerequisite for this bounded reaper. This deliberately gives deterministic reclaim priority over preserving a detached child that kept using managed temporary storage. Managed temporary storage supports Linux and macOS; Windows and other platforms retain system-temp behavior until equivalent ownership, locking, process-identity, and no-link traversal guarantees are designed and tested.

Validation failures retain the lease: a malformed, foreign-owned, symlinked, or unrecognised allocation is never deleted. TTL is the explicit deletion authority only for an otherwise validated allocation inside the harness-owned managed root.

### 7. Keep scope intentionally narrow

This decision does not introduce a global `/tmp` sweeper, a raw agent-selected temporary path, a container/VM sandbox, or a session-lifetime temporary directory. It does not delete user-configured temporary roots. It intentionally treats a missing managed process group as command completion even if a descendant escaped it and continues using temporary storage.

## Consequences

**Benefits:**

- Agent-launched tests receive automatic, exact-target cleanup after ordinary completion and after timeout when the managed process group is gone; every other validated managed lease is reclaimed by the bounded TTL policy without relying on Go defers.
- A process crash leaves attributable managed residue rather than anonymous shared `/tmp` directories.
- Startup and periodic recovery are bounded and coordinated across concurrent desktop servers.
- The agent can declare that it needs the supported host-shared temporary default, while that request is visible to policy and the human operator.
- The reaper has no authority over arbitrary `/tmp` content or user-configured directories.

**Costs and limits:**

- Managed temporary storage is intentionally disposable: a process that escapes its command group can lose temporary paths as soon as the original group exits, and a process that remains in the group loses them after the one-hour TTL. Long-lived processes must request the declared `system` scope.
- The declared system scope is a lifecycle affordance, not a security control over arbitrary shell paths; OS sandboxing would be a separate capability.
- The runner, test helper, process-group adapter, composition, and maintenance lifecycle need coordinated changes and cross-platform tests.
- A manifest, locks, and sweep state form a durable cleanup protocol. Their ownership and validation rules require an implementation-specific conformance suite.
- A dead harness cannot clean immediately; residue remains until the next interval-gated sweep reclaims the validated managed allocation.

## Implementation and acceptance outline

Implementation should proceed as separately reviewable work:

1. Add the operator-global `temporary_storage` settings schema, strict validation, shared managed-root adoption, and the `mode: system` rollback behavior; project-tier settings are warn-ignored.
2. Add canonical transient workspace identity derivation, compact URL-safe Base64 digest keys, per-workspace manifests, and handle-rooted no-link filesystem operations shared by every allocation family.
3. Add the per-invocation command-environment overlay capability, then add `temp_scope`, the independent `BashSystemTemp` permission gate, and model-visible guidance.
4. Add managed per-command/job leases to local Linux and macOS command runners and direct test-home temporary roots into a validated lease.
5. Preserve current process-group cancellation semantics; add immediate cleanup when the managed group is gone and record terminal state for deterministic TTL cleanup when it is not.
6. Add manifest, per-lease locking, interval-gated root GC coordination, and deterministic command/job orphan reaping.
7. Add the `app.Build`-owned startup/periodic maintenance worker and shutdown join; inventory the long-lived GC state and maintenance resource.
8. Test default settings, root adoption, compact workspace-key encoding, per-workspace manifest validation, lease-name and manifest-ID agreement, per-lease layout, invalid/project-tier/rollback settings, both `BashSystemTemp` approval examples, normal completion, timeout, cancellation, a long-running command with no timeout that a reaper skips while its lease lock is held, process-group-still-live deferral, escaped-child immediate deletion, concurrent runners/reapers, malformed/symlinked/replaced path components, crash recovery, worker shutdown, Linux/macOS managed-mode admission, and declared `system`-scope policy behavior.

## See also

- [ADR 0027 — Cloud-native arc](./0027-cloud-native.md) — resource inventory and process-lifetime cleanup convention.
- [ADR 0201 — Background Bash](./0201-background-bash.md) — background-command lifecycle and process-group cancellation.
- [ADR 0211 — Execution-environment runtime seam](./0211-execution-environment-runtime-seam.md) — bound command runners and environment ownership.
- [ADR 0282 — Managed workspace scratch cache](./0282-managed-workspace-scratch-cache.md) — depends on this reaper and adds retained cross-command artifacts.
- [Architecture: extensibility](../architecture/extensibility.md) — the `tool.CommandRunner` execution seam.
