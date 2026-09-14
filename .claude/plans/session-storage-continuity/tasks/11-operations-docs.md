---
id: 11-operations-docs
title: systemd and macOS maintenance runbook
blocked_by: [04-migration-job, 06-retention-config]
status: done
branch: "plan-session-storage-continuity/11-operations-docs"
worktree: ""
issue: "596"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Ship tested systemd/launchd examples and the safe policy, backup, migration, restore, privacy, space, and backend runbook. Do not provide external deletion recipes.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC9.1: Tested systemd user-service and launchd examples parse, resolve the intended executable/config/state paths, preserve each argument exactly, and invoke the daemon-owned retention configuration rather than an external deletion command.
  - verify: `TestSessionStorageContinuity_Scenario9_ServiceExamplesExecuteConfiguredArgs`
- AC9.2: Documentation explicitly rejects cron/find/glob deletion, explains embedded-local versus connected-remote management, and shows effective-policy inspection plus dry-run before apply.
  - verify: none — deletion-safety guidance is reviewed by humans; `task docs` checks links and structure
- AC9.3: The runbook covers plaintext sensitivity/permissions, space forecasting, unsupported backends, and stop → backup → migrate/apply → verify → start with restore-to-new-directory validation.
  - verify: none — backup and migration runbook completeness is reviewed by humans; `task docs` checks links and structure
