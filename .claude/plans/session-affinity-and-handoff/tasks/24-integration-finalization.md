---
id: 24-integration-finalization
title: Final integration fixes for provisional admission and optional child leasing
blocked_by: [22-repair-lease-races-final, 23-repair-transport-oracles-final]
status: in-progress
branch: ""
worktree: ".scratch/task-session-affinity-24"
issue: ""
retries: 0
last_error: "final review: run launch used uncancelled context, provisional approval nil dereference, child ErrLeaseUnsupported compatibility"
accumulator: acc/session-affinity-and-handoff
---

# Integration fix brief

- Ensure Run, RetryFailedStep, and ResumeApproval launch with the lease/admission-cancelled context, not the original request context; block/lose lease between final check and launch must prevent provider/tool start.
- Approval paths must treat provisional `runState{run:nil}` as no active run and never dereference it.
- `sessionLiveness` child leasing must mirror the optional lease contract: `ErrLeaseUnsupported` stickily disables child leasing and continues with no capability gating/renew/release, rather than failing all delegation. Preserve other acquire errors.
- Add focused race/compatibility tests.

Do not change CloseSession: surface EndSession already rejects any registered active/provisional run before calling internal CloseSession.
