---
id: 01b-v2-durability
title: Atomic replacement durability and failure safety
blocked_by: [01-v2-snapshots]
status: done
branch: "plan-session-storage-continuity/01b-v2-durability"
worktree: ""
issue: "586"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Complete the v2 snapshot core with explicit atomic replacement durability: same-directory temp, file sync, atomic rename, directory-sync capability truth, verified-v2 authority during coexistence, and disk/failure injection preserving the prior committed snapshot. Do not add orphan-temp reaping/cross-process generation ownership—that is task 01c.

Keep `port.SessionStore` unchanged, use stdlib/existing atomic-write seams, preserve v1 compatibility and sidecars from task 01, and update durability docs/tests only.

## Acceptance criteria

- AC1.2: On filesystems supporting same-directory atomic rename plus file and directory sync, every injected process/OS-crash point reopens either the prior committed snapshot or the new committed snapshot—never absence, a torn aggregate, or a silently older v1 record; unsupported durability is detected and reported as a weaker explicit capability rather than overclaimed. Once verified v2 is committed it is authoritative over coexisting v1.
  - verify: `TestSessionStorageContinuity_Scenario1_AtomicCrashRecovery`, `TestSessionStorageContinuity_Scenario1_DurabilityCapabilityTruth`
- AC1.4: Disk-full or replacement failure leaves the prior committed family readable and produces a loud storage error.
  - verify: `TestSessionStorageContinuity_Scenario1_DiskFullPreservesCommittedSnapshot`
