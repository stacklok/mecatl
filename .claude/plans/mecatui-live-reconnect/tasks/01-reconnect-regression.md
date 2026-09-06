---
id: 01-reconnect-regression
title: Offline live-feed reconnect regression coverage
blocked_by: []
status: done
attempt: 1
branch: plan-mecatui-live-reconnect/01-reconnect-regression-attempt-1
worktree: .scratch/worker-mecatui-live-reconnect-01-reconnect-regression-attempt-1
issue: "779"
retries: 0
last_error: ""
accumulator: acc/mecatui-live-reconnect
---

# Offline live-feed reconnect regression coverage

Add only missing offline regression tests for the already-landed live-feed reconnect behavior. Make a production edit only when a named failing test proves the current code does not satisfy the acceptance contract. Do not add protocol/API/ADR/catch-up scope.

## Acceptance criteria

- AC1.1: On the first live `Recv`, and on a reconnect probe `Open` or probe `Recv`, gRPC
  `Unauthenticated` plus bearer provenance produces `StreamErrMsg{AuthReason: AuthRejected}`;
  it is not treated as transient.
  - verify: `TestEventStreamAuthClassificationRespectsBearerProvenance`, `TestADR_0096_ReconnectProbeAuthRejectedStopsRetry`
- AC1.2: The first-`Recv` UI path renders the rejected-bearer recovery overlay with
  actionable guidance: re-login is disabled, and the operator is told to check the
  issuer, audience, or CA. The reconnect/live state is disarmed and the stale `live feed
  reconnecting` footer is cleared.
  - verify: `TestADR_0096_BearerFirstRecvAuthRejectedRoutesToConnectRecovery`
- AC1.3: `AuthRejected` preserves the failed target and session handoff, tears down the
  affected readers and reconnect loop, and performs no retry for rejection returned from
  first `Recv`, reconnect probe `Open`, or reconnect probe `Recv`.
  - verify: `TestADR_0096_BearerAuthRejectedPreservesTargetSessionAndTearsDownRetry`
- AC1.4: A non-bearer first-`Recv` authentication failure remains `AuthNotEnrolled`,
  and replay transport errors remain unclassified; neither case widens bearer-only
  recovery.
  - verify: `TestEventStreamAuthClassificationRespectsBearerProvenance`, `TestAuthFailureUsesTypedLocalCausesAndBearerProvenance`
- AC2.1: Open→immediate-close advances continuity into reconnect attempt 1; a second
  immediate-close starts at attempt 2 and waits for the attempt-2 backoff before probing.
  The reconnect loop does not reset the attempt merely because its probe opened, and
  backoff attempts are bounded, increasing, and capped.
  - verify: `TestADR_0096_CleanCloseAdvancesCrossLoopContinuity`, `TestLiveReconnectDelay_BoundedAndIncreasing`
- AC2.2: The same session has at most one reconnect loop; reconnect success clears
  degraded state, tears down the completed loop, and re-arms one fresh live reader.
  - verify: `TestReconnectUI_NoDuplicateConcurrentReconnect`, `TestReconnectUI_TriggerOnStreamCloseAndError`
- AC2.3: Cancellation stops and joins the reconnect work without a retry or goroutine
  leak, including while an open/probe is blocked.
  - verify: `TestReconnectLiveCmd_StopsOnCtxCancel`, `TestReconnectLiveCmd_RetriesWedgedOpen`
- AC3.1: A real event from the current live generation resets the cross-loop continuity
  attempt; the next immediate-close outage starts at attempt 1. A probe/reconnected marker
  or catch-up event alone does not reset it.
  - verify: `TestADR_0096_RealLiveEventResetsContinuity`
- AC3.2: A stale live or reconnect generation is dropped without rearming the old session;
  session switch and TUI cancellation preserve the existing teardown behavior.
  - verify: `TestReconnectUI_StopsOnSessionSwitch`, `TestStaleStreamGenerationDropped`

## Verification

- The named scenario tests above pass offline with the race detector; no test uses a live provider, network, or bearer secret.
- `task lint`, `task test`, `task docs`, `task ac-trace-strict`, and `go run ./cmd/mecademo` pass; `go run ./cmd/mecademo` still demonstrates a complete offline turn/tool/ permission/approval/result session.
- `task ac-trace-strict` is run when this plan is promoted to `landed`; while draft, `task ac-trace` reports missing future proofs without making the plan a gate.
- No ADR, API baseline, generated contract, or unrelated documentation change is introduced.
