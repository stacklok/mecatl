---
id: 02-session-persistence
title: Persist and restore the carried authority set
blocked_by: [01-authority-domain]
status: done
branch: "plan-authority-evaluator-port/02-session-persistence"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline blockers: macOS /var symlink checks and skills lifecycle workspace test; task docs cannot fetch matlatl"
accumulator: acc/authority-evaluator-port
---

# Task brief

Carry the derived authority value on the `session.Session` aggregate and persist
it through the snapshot and event-source fold using one plain serialized payload.
Implement fail-closed handling for malformed claimed authority and documented
legacy handling for records predating the feature. Preserve it through terminal
recovery transitions. Do not wire evaluator execution or child derivation.

## Acceptance criteria

- AC2.1: A bound session persists its capability set, its provenance, and any resolved definition identity before its first runnable state, and restores them byte-equivalently through the snapshot and the event fold.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_SetRoundTripsThroughSnapshotAndFold`
- AC2.2: The persisted payload contains no path, credential, token, header, catalog pointer, runner, or raw identity claim.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_PayloadExcludesSensitiveRuntimeData`
- AC2.3: A record that claims a capability set but cannot be decoded fails closed before a run starts, with a diagnostic naming the failure; a genuinely pre-feature record with no set is classified legacy and behaves as documented.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_UndecodableSetFailsClosedLoudly`
- AC2.4: A decode failure is never silently swallowed into a bound-but-empty state; no code path sets "this run is bound" while discarding the error that produced an empty set.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_NoSilentBoundButEmptyState`
- AC2.5: Completed, cancelled, and failed bound sessions retain the same persisted set through Reopen, Interrupt, Recover, and Abandon.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_TerminalRecoveryPreservesSet`
