---
id: 01-v2-snapshots
title: Versioned atomic current snapshots
blocked_by: []
status: pending
branch: ""
worktree: ""
issue: "586"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Implement ADR-0225's bounded v2 current-snapshot representation, v1 compatibility/lazy promotion, crash recovery, and adapter-private atomic replacement. Preserve SessionStore neutrality and append-only tool/event sidecars.

Follow ADR-0225, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC1.1: Saving a same-sized session 1,000 times leaves steady-state snapshot storage bounded to one current snapshot plus bounded metadata and one in-progress temporary replacement.
  - verify: `TestSessionStorageContinuity_Scenario1_BoundedRepeatedSaves`
- AC1.2: On filesystems supporting same-directory atomic rename plus file and directory sync, every injected process/OS-crash point reopens either the prior committed snapshot or the new committed snapshot—never absence, a torn aggregate, or a silently older v1 record; unsupported durability is detected and reported as a weaker explicit capability rather than overclaimed. Once verified v2 is committed it is authoritative over coexisting v1.
  - verify: `TestSessionStorageContinuity_Scenario1_AtomicCrashRecovery`, `TestSessionStorageContinuity_Scenario1_DurabilityCapabilityTruth`
- AC1.3: V1 snapshots remain loadable and a successful lazy promotion preserves conversation, usage, counters, state, profile, selector, environment reference, owner, kind, relationship, title/provenance, logical modification time, and sidecars; `unknown` remains `unknown`.
  - verify: `TestSessionStorageContinuity_Scenario1_V1PromotionFidelity`
- AC1.4: Disk-full or replacement failure leaves the prior committed family readable and produces a loud storage error.
  - verify: `TestSessionStorageContinuity_Scenario1_DiskFullPreservesCommittedSnapshot`
- AC1.5: Startup and the next successful save identify and safely remove abandoned same-family temporary replacements only while holding the cross-process family mutation lock and proving the temp naming/ownership generation is inactive; repeated crashes cannot grow orphan temps without bound, and one process never removes another process's in-progress replacement or a committed snapshot.
  - verify: `TestSessionStorageContinuity_Scenario1_OrphanTemporaryRecovery`, `TestSessionStorageContinuity_Scenario1_ActiveTemporaryNotReaped`
