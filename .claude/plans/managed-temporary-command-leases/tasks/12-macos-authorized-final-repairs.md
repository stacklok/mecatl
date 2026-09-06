---
id: 12-macos-authorized-final-repairs
title: Apply authorized managed-storage validation repairs
blocked_by: [11-macos-final-panel-repairs]
status: in-progress
attempt: 1
branch: "plan-managed-temporary-command-leases/12-macos-authorized-final-repairs-attempt-1"
worktree: ".scratch/worker-managed-temporary-command-leases-12-macos-authorized-final-repairs-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Apply authorized managed-storage validation repairs

The operator authorized an exceptional third repair wave. Restrict managed storage to Linux and macOS build targets. A terminal manifest must contain `terminal_at` and a complete prior start-identity bundle (`started_at`, owner PID, process-start identity, process group), but do not impose timestamp ordering because local wall-clock adjustments are legitimate. Preserve reaping of the exact untouched pre-start allocation shape by `created_at`; reject incomplete or partially-started active manifests. Add regression tests proving those distinctions and the non-Linux/non-Darwin build split. Do not edit the ADR or orchestrator-managed plan/task state.

## Acceptance criteria

- AC4.2: An unlocked, validated command/job lease becomes reclaimable after its applicable TTL from terminal state or last recorded lifecycle transition; malformed, foreign, symlinked, replaced, unrecognized, or lock-contended candidates are retained without deletion.
  - verify: `TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease`
- AC1.4: Linux and macOS admit `mode: managed`; Windows and other unsupported platforms fail configuration validation before serving, while `mode: system` remains available and preserves existing behavior.
  - verify: `TestADR_0281_ManagedModeUnixAdmission`
