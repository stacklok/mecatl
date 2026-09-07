---
id: 09-macos-managed-temp
title: macOS managed temporary storage support
blocked_by: [08-reaper-worker-and-docs]
status: done
attempt: 1
branch: "plan-managed-temporary-command-leases/09-macos-managed-temp-attempt-1"
worktree: ".scratch/worker-managed-temporary-command-leases-09-macos-managed-temp-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# macOS managed temporary storage support

Move platform-neutral managed namespace, manifest, locking, allocation, and reaping behavior into shared Unix code; implement Darwin-specific process-start identity without PID-only fallback; keep Windows and other platforms fail-closed. Do not weaken Linux behavior or introduce a new public API.

## Acceptance criteria

- AC1.4: Linux and macOS admit `mode: managed`; Windows and other unsupported platforms fail configuration validation before serving, while `mode: system` remains available and preserves existing behavior.
  - verify: `TestADR_0281_ManagedModeUnixAdmission`
- AC3.1: A managed foreground Bash call receives a distinct owner-only `cmd-<allocation-id>/tmp` directory in both `TMPDIR` and `GOTMPDIR`; shell text is byte-for-byte free of injected temporary paths, and manifest metadata contains no command, output, environment, credential, or transcript content. The fixed internal overlay is applied after the common secret scrub and cannot restore, override, audit, or log a scrubbed credential-shaped variable.
  - verify: `TestADR_0281_ForegroundLeaseOverlayAndMetadataPrivacy`
- AC4.2: An unlocked, validated command/job lease becomes reclaimable after its applicable TTL from terminal state or last recorded lifecycle transition; malformed, foreign, symlinked, replaced, unrecognized, or lock-contended candidates are retained without deletion.
  - verify: `TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease`
