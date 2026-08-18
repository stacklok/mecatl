---
id: 06-retention-config
title: Configurable automatic retention
blocked_by: [05-cleanup-planner]
status: done
branch: "plan-session-storage-continuity/06-retention-config"
worktree: ""
issue: "591"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Expose versioned operator retention configuration and effective-policy inspection for mecated and embedded mecatui, with explicit acknowledgement for destructive tightening and honest remote capability behavior.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC6.1: Versioned operator configuration exposes main/child/scheduled age and count limits plus sweep cadence; `0` consistently disables, invalid/negative/unknown values fail, and explicit CLI values outrank configuration.
  - verify: `TestSessionStorageContinuity_Scenario6_RetentionConfigPrecedence`
- AC6.2: Embedded mecatui exposes its local effective policy instead of relying on hidden hard-coded destructive behavior; connected mecatui cannot claim to configure a remote server without an advertised management capability.
  - verify: `TestSessionStorageContinuity_Scenario6_EmbeddedAndRemotePolicyTruth`
- AC6.4: Enabling or tightening destructive main retention presents/logs a plan summary and requires explicit acknowledgement; unknown remains protected.
  - verify: `TestSessionStorageContinuity_Scenario6_DestructivePolicyAcknowledgement`
