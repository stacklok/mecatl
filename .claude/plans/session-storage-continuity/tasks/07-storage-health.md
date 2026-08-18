---
id: 07-storage-health
title: Storage health and maintenance status
blocked_by: [02-indexed-inventory, 02b-bounded-pagination, 02c-inventory-locking]
status: done
branch: "repair/07-storage-health-integration"
worktree: ""
issue: "592"
retries: 0
last_error: ""
accumulator: acc/session-storage-continuity
---

# Task brief

Expose bounded indexed health/status data for storage generations, kinds, bytes, policy, sweeps, jobs, and failures. Keep unsupported distinct from zero and preserve caller/management separation.

Follow ADR-0226, ADR-0217, ADR-0027, ADR-0104, AGENTS.md layering/security invariants, and existing repository conventions. Keep tests offline. Do not absorb later tasks or weaken fail-closed behavior. Update the living docs and user-docs affected by this task.

## Acceptance criteria

- AC6.3: Health reports current/reclaimable bytes, session/file and v1/v2/main/child/scheduled/unknown/corrupt counts, last/next sweep, effective policy, active maintenance job, and last failure from bounded indexed data; unavailable is distinct from zero.
  - verify: `TestSessionStorageContinuity_Scenario6_HealthIsBoundedAndHonest`
