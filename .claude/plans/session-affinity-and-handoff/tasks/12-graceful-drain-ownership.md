---
id: 12-graceful-drain-ownership
title: Graceful drain ownership ordering
blocked_by: [09-lease-loss-capability, 10-awaiting-lease-loss-handoff, 11-close-session-semantics]
status: done
branch: "plan-session-affinity-and-handoff/12-graceful-drain-ownership"
worktree: ".scratch/task-session-affinity-12"
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Replace the current cancel-on-Service-close posture with the accepted mecak8s drain sequence: stop all new prompt/retry/resume/out-of-band mutation admission, preserve unattended durable awaiting state, cancel and join executing runs, persist the recoverable terminal when possible, and release only ownership that has settled.

**Likely scope:** `internal/adapter/server/service.go` drain/run lifecycle APIs, `cmd/mecak8s/serve.go` bounded shutdown ordering, diagnostics, and server/mecak8s shutdown tests. Keep process signal/listener mechanics in the composition root and lease decisions in Service.

**Invariants:** admission closes before lease acquisition; awaiting `PendingAsk` is not cancelled; executing lease releases only after join and persistence attempt; persistence failure is diagnosed without replacing prior durable truth; timeout retains the lease for process death/TTL takeover. Nil lease stays compatible. Use injectable bounds/channels/fake clocks, offline deterministic TDD, and no real Kubernetes cluster.

## Acceptance criteria

- AC6.1: Once drain begins, new prompt, retry, resume, and out-of-band mutation entries
  are refused before lease acquisition while already-owned runs follow the bounded
  shutdown path.
  - verify: `TestADR_0290_DrainStopsAdmissionBeforeOwnershipChange`

- AC6.2: Drain preserves a persisted awaiting session's durable `PendingAsk`; when no
  local run is live, it may close local resources and release the lease without
  destroying the durable resume point.
  - verify: `TestADR_0290_DrainPreservesAwaitingResumePoint`

- AC6.3: Drain cancels and joins an executing run before explicitly releasing its lease.
  A terminal or cancelled recoverable snapshot is persisted when storage is available;
  a persistence failure is diagnosed and the prior durable state remains authoritative.
  - verify: `TestSessionAffinityAndHandoff_Scenario6_DrainCancelsJoinsAndDiagnosesPersistFailure`

- AC6.4: If an executing run cannot join before the shutdown bound, mecak8s does not
  explicitly release its lease; process death and lease TTL govern later takeover.
  - verify: `TestADR_0290_DrainTimeoutRetainsLeaseForTTLTakeover`
