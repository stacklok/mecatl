---
id: 10-macos-panel-repairs
title: Repair macOS managed-storage panel findings
blocked_by: [09-macos-managed-temp]
status: done
attempt: 1
branch: "plan-managed-temporary-command-leases/10-macos-panel-repairs-attempt-1"
worktree: ".scratch/worker-managed-temporary-command-leases-10-macos-panel-repairs-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Repair macOS managed-storage panel findings

Repair the panel's concrete safety and validation findings without broadening the feature: prove candidate lease entries cannot be swapped for symlinks before reaper deletion; remove the foreground manifest-start race in the privacy test; add native-Darwin identity and valid-candidate reaper regression coverage; make the root module's direct `x/sys` use explicit; and correct stale Linux-only wording in the acceptance plan. Keep the no-link, owner-mode, exact-target deletion guarantees and no PID-only fallback.

## Acceptance criteria

- AC3.1: A managed foreground Bash call receives a distinct owner-only `cmd-<allocation-id>/tmp` directory in both `TMPDIR` and `GOTMPDIR`; shell text is byte-for-byte free of injected temporary paths, and manifest metadata contains no command, output, environment, credential, or transcript content. The fixed internal overlay is applied after the common secret scrub and cannot restore, override, audit, or log a scrubbed credential-shaped variable.
  - verify: `TestADR_0281_ForegroundLeaseOverlayAndMetadataPrivacy`
- AC4.2: An unlocked, validated command/job lease becomes reclaimable after its applicable TTL from terminal state or last recorded lifecycle transition; malformed, foreign, symlinked, replaced, unrecognized, or lock-contended candidates are retained without deletion.
  - verify: `TestADR_0281_ReaperDeletesOnlyValidatedEligibleLease`
