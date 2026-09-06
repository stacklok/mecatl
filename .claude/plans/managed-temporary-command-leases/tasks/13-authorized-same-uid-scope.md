---
id: 13-authorized-same-uid-scope
title: Clarify same-UID cleanup scope and platform tests
blocked_by: [12-macos-authorized-final-repairs]
status: done
attempt: 1
branch: "plan-managed-temporary-command-leases/13-authorized-same-uid-scope-attempt-1"
worktree: ".scratch/worker-managed-temporary-command-leases-13-authorized-same-uid-scope-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Clarify same-UID cleanup scope and platform tests

The operator explicitly accepts same-UID command processes as cooperating local principals for managed-temp cleanup. Remove only tests or logic that falsely promise protection from an adversarial same-UID process replacing a name after the final handle-rooted validation; retain all malformed, foreign-owned, symlinked, owner/mode, and ordinary containment checks. Restrict managed-mode test files to `linux || darwin`, and make macOS CI execute the complete relevant reaper suite. Update documentation only as needed to state the chosen scope accurately.

## Acceptance criteria

- AC1.4: Linux and macOS admit `mode: managed`; Windows and other unsupported platforms fail configuration validation before serving, while `mode: system` remains available and preserves existing behavior.
  - verify: `TestADR_0281_ManagedModeUnixAdmission`
- AC4.2: An unlocked, validated command/job lease becomes reclaimable after its applicable TTL from terminal state or last recorded lifecycle transition; malformed, foreign, symlinked, replaced, unrecognized, or lock-contended candidates are retained without deletion.
  - verify: `TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease`
