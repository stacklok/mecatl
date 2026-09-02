---
id: 08-reaper-worker-and-docs
title: Deterministic reaper, Build worker, and lifecycle docs
blocked_by: [03-lease-protocol, 05-foreground-managed-leases, 07-background-job-leases]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Deterministic reaper, Build worker, and lifecycle docs

Build the bounded interval-gated reaper and Build-owned maintenance lifecycle over validated leases. Inventory every outlives-a-call resource and document the running harness behavior. Do not widen the scratchpad or fork scope.

## Acceptance criteria

- AC4.1: A startup or periodic sweep first obtains the nonblocking root lock, rereads durable completion state, skips a scan completed within the interval, and records completion only after a full successful scan; it never delays allocation or command execution.
  - verify: `TestADR_0281_IntervalGatedSweepCoordination`
- AC4.2: An unlocked, validated command/job lease becomes reclaimable after its applicable TTL from terminal state or last recorded lifecycle transition; malformed, foreign, symlinked, replaced, unrecognized, or lock-contended candidates are retained without deletion.
  - verify: `TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease`
- AC4.3: A crash-like abandoned lease is reclaimed by a later sweep, while concurrent runners and reapers cannot delete the wrong allocation or publish a successful sweep state after an interrupted scan.
  - verify: `TestADR_0281_CrashRecoveryAndConcurrentReaping`
- AC4.4: `app.Build` performs one best-effort interval-gated startup attempt and later sweeps on the configured cadence. A startup/periodic sweep runs under the configured `reap_timeout` (five minutes by default); a blocked scan or metadata operation is cancelled, records no successful completion, and cannot hang shutdown. `Built.Close` cancels the worker and applies the independently configured `shutdown_reap_timeout` (one minute by default), reporting a deadline expiry rather than claiming the worker joined. Disabled, system, and unsupported backends add no worker or mutation.
  - verify: `TestADR_0281_BuildOwnsManagedTempWorkerLifecycle`
- AC4.5: The ADR 0027 resource inventory records the managed-temp worker, root/lease locks, and completion record with owner, scope, cleanup, and restart disposition; architecture and public operator documentation describe the Linux-only managed lifecycle and system-mode rollback.
  - verify: inspection — documentation and inventory are reviewed with the lifecycle implementation; `task docs` enforces links and generated `llms.txt`.
- AC4.6: In an offline end-to-end run, a managed Bash command allocates a private lease, normal terminal handling removes it, and a separately deferred eligible residue is removed only by a deterministic later reaper sweep—without a live model or network.
  - verify: `TestManagedTemporaryCommandLeases_Scenario4_EndToEnd`
