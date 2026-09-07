---
id: 14-darwin-background-lease-cleanup
title: Repair Darwin background lease cleanup
blocked_by: [13-authorized-same-uid-scope]
status: in-progress
attempt: 1
branch: "plan-managed-temporary-command-leases/14-darwin-background-lease-cleanup-attempt-1"
worktree: ".scratch/worker-managed-temporary-command-leases-14-darwin-background-lease-cleanup-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/managed-temporary-command-leases
---

# Repair Darwin background lease cleanup

On Darwin, treat a managed process group containing only zombie processes as gone after command cancellation so normal terminal handling removes its background-job lease rather than retaining it until TTL. A process-table lookup failure must fail closed as live. Preserve Linux and other Unix signal-0 behavior, process-group cancellation, and all existing lease lifecycle semantics. Add Darwin-specific unit coverage plus the existing real background-job end-to-end pin.

## Acceptance criteria

- AC3.2: A managed `background: true` Bash call receives one distinct private job lease that remains held for its complete job lifetime and is cleaned under the same terminal rules as a foreground command.
  - verify: `TestADR_0281_BackgroundJobLeaseLifecycle`
- AC3.3: Successful completion, cancellation, and timeout retain their existing exit/cancellation/deadline outcomes while removing the exact lease after the managed process group has terminated.
  - verify: `TestADR_0281_LeaseCleanupPreservesCommandOutcome`
