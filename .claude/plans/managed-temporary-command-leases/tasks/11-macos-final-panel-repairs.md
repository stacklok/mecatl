---
id: 11-macos-final-panel-repairs
title: Repair final managed-storage validation findings
blocked_by: [10-macos-panel-repairs]
status: done
attempt: 1
branch: "plan-managed-temporary-command-leases/11-macos-final-panel-repairs-attempt-1"
worktree: ".scratch/worker-managed-temporary-command-leases-11-macos-final-panel-repairs-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Repair final managed-storage validation findings

Make reaper manifest validation fail closed for incomplete or invalid lifecycle metadata before deletion, and strengthen Darwin process identity testing by independently verifying the persisted kernel start timestamp. Keep scope narrow; do not modify ADR 0281 or orchestrator-managed plan/task state.

## Acceptance criteria

- AC4.2: An unlocked, validated command/job lease becomes reclaimable after its applicable TTL from terminal state or last recorded lifecycle transition; malformed, foreign, symlinked, replaced, unrecognized, or lock-contended candidates are retained without deletion.
  - verify: `TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease`
- AC3.1: A managed foreground Bash call receives a distinct owner-only `cmd-<allocation-id>/tmp` directory in both `TMPDIR` and `GOTMPDIR`; shell text is byte-for-byte free of injected temporary paths, and manifest metadata contains no command, output, environment, credential, or transcript content.
  - verify: `TestADR_0281_ForegroundLeaseOverlayAndMetadataPrivacy`
