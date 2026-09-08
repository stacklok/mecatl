---
id: 10-awaiting-lease-loss-handoff
title: Awaiting-approval lease-loss settlement
blocked_by: [09-lease-loss-capability]
status: done
branch: "plan-session-affinity-and-handoff/10-awaiting-lease-loss-handoff"
worktree: ".scratch/task-session-affinity-10"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Implement the awaiting-specific lease-loss path: invalidate and stop the stale local run, retract only its local ask delivery, and preserve the already-durable `StateAwaiting` snapshot and exact `PendingAsk` for a successor. After expiry/takeover, only the new owner may resume that same ask.

**Likely scope:** `internal/adapter/server/service.go` run-state/lease-loss/approval paths, relay ask retraction, `resume_awaiting` and lease tests, and minimal `engine/agent` run cancellation APIs only if an existing retraction seam cannot be reused. Keep durable state transitions on the Session aggregate.

**Invariants:** stale ownership cannot approve/deny/cancel/overwrite the ask or start its tool; local cancellation must not persist cancelled over awaiting; inability to join must not explicitly release; TTL plus successor acquire is the ownership transition; tool execution remains exactly once through `ResumeApproval`. Use deterministic offline fake-clock tests, no sleeps or network.

## Acceptance criteria

- AC5.6: Lease-renewal loss while a local run is awaiting approval stops and invalidates
  that local run and retracts its local permission-ask delivery. It neither resolves,
  cancels, overwrites, nor destroys the already-durable awaiting snapshot or `PendingAsk`.
  If local cancellation cannot settle the run, it does not explicitly release merely on
  that cancellation; after ownership loss the stale process cannot approve, deny, or
  otherwise resolve the ask.
  - verify: `TestADR_0291_AwaitingLeaseLossRetractsLocalAskPreservesSnapshot`

- AC5.7: After lease expiry and successor takeover, the successor reloads and resumes the
  exact durable `PendingAsk`; no stale local approval can change that ask or start its
  tool call.
  - verify: `TestSessionAffinityAndHandoff_Scenario5_AwaitingLeaseLossSuccessorResumesExactAsk`
