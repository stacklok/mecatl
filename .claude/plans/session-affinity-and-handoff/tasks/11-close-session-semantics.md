---
id: 11-close-session-semantics
title: Lease-aware CloseSession preconditions
blocked_by: [08-session-mutation-lease-inventory, 10-awaiting-lease-loss-handoff]
status: pending
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/session-affinity-and-handoff
---

# Task brief

Make the surface-facing close operation distinguish a genuinely local live/awaiting run from a persisted unattended awaiting snapshot. Reject close with `FailedPrecondition` while local work owns the session; otherwise tear down local engine/policy/environment and release ownership without deleting a durable awaiting resume point.

**Likely scope:** `internal/adapter/server/service.go` `EndSession`/`CloseSession` contract, gRPC and HTTP close handlers/status mapping, ACP disconnect handling if needed, and close tests over both transports.

**Invariants:** close is not cancel or delete; rejection leaves lease and all local resources intact; a runless persisted awaiting snapshot and `PendingAsk` remain byte-exact; successful close can release only local resources/lease; gRPC and HTTP semantics match; no-lease behavior remains compatible. Strict TDD with in-process/bufconn/httptest fixtures.

## Acceptance criteria

- AC5.3: `CloseSession` returns `FailedPrecondition` while a local run is active or
  awaiting, and does not release the lease or tear down its engine, policy, or
  environment; the caller must cancel or settle that local run first.
  - verify: `TestADR_0290_CloseGRPCAndHTTPRejectLiveOrAwaitingRun`

- AC5.4: A persisted awaiting session with no live local run retains its durable
  `PendingAsk`; `CloseSession` may release local resources and its lease without
  destroying that resume point.
  - verify: `TestADR_0290_CloseGRPCAndHTTPPreservePersistedAwaitingResumePoint`
