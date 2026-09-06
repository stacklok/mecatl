---
id: 07-background-job-leases
title: Background Bash job lease lifecycle
blocked_by: [05-foreground-managed-leases]
status: done
branch: "plan-managed-temporary-command-leases/07-background-job-leases"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Background Bash job lease lifecycle

Apply the foreground lease protocol to ADR 0201 background Bash jobs, retaining one lock for the complete job lifetime and preserving registry/process-group behavior.

## Acceptance criteria

- AC3.2: A managed `background: true` Bash call receives one distinct `job-<96-bit-random-id>/tmp` lease that remains held for its complete job lifetime and is cleaned under the same terminal rules as a foreground command.
  - verify: `TestADR_0281_BackgroundJobLeaseLifecycle`
- AC3.7: Concurrent managed runners receive distinct leases, and a reaper cannot remove either live allocation while its per-lease lock is held, including for a command with no timeout.
  - verify: `TestADR_0281_ActiveLeaseLockDefeatsReaper`
