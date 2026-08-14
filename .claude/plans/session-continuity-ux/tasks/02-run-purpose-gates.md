---
id: 02-run-purpose-gates
title: Kind-aware public chat and scheduler run-entry gates
blocked_by: [01-session-taxonomy]
status: done
branch: "plan-session-continuity-ux/02-run-purpose-gates"
worktree: ".scratch/worktrees/session-continuity-ux-task-02"
issue: "471"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Task brief

Split the trusted scheduler-purpose entry from public chat continuation while converging on the existing locked/leased run-entry implementation after the kind gate. Preserve the historical prefix guard as legacy defense in depth, caller-separation non-enumeration, and all existing recovery seams. Public request data must not select the scheduler purpose.

## Acceptance criteria

- AC2.1: Public `StartRunContent` rejects every explicitly-stamped Subagent, Parallel-branch, team-member, and scheduled session regardless of ID spelling.
  - verify: `TestInvariant_non_main_sessions_cannot_start_as_chat`
- AC2.2: The trusted scheduler-purpose entry drives an explicitly-stamped scheduled session through the same downstream run loop, while a public caller cannot claim that purpose.
  - verify: `TestADR_0108_SchedulerPurposeOnlyDrivesScheduled`
- AC2.3: For legacy snapshots, any historical child or scheduled prefix denies public chat continuation even when kind is missing or conflicting; missing/invalid/conflicting metadata never grants a capability.
  - verify: `TestADR_0108_LegacySafetyGate`
- AC2.4: A normal explicitly-stamped main session with an ordinary opaque ID remains continuable; no client-side prefix parser participates in the decision.
  - verify: `TestInvariant_session_ids_are_not_client_classifiers`
- AC2.5: Unknown/pruned, ownerless-under-enforcement, and foreign-owned exact IDs remain one indistinguishable NotFound-class result across get, list, transcript, and startup-resume surfaces.
  - verify: `TestSessionContinuityUX_Scenario2_OwnershipOracleClosed`
