---
id: 02-panel-repair
title: Strengthen live reconnect acceptance proofs
blocked_by: [01-reconnect-regression]
status: pending
attempt: 0
branch: ""
worktree: ""
issue: "779"
retries: 0
last_error: ""
accumulator: acc/mecatui-live-reconnect
---

# Strengthen live reconnect acceptance proofs

Apply the panel-confirmed corrections by strengthening or replacing offline tests only.
Make a production edit only if a named new or strengthened acceptance test fails against
the existing implementation and exposes a production defect. Do not add protocol, API,
ADR, or catch-up scope.

## Corrected impacted acceptance criteria

- AC1.1: On the first live `Recv`, reconnect probe `Open`, or first `Recv` on the
  freshly rearmed live reader, gRPC `Unauthenticated` plus bearer provenance produces
  `StreamErrMsg{AuthReason: AuthRejected}`; it is not treated as transient.
  - verify: `TestEventStreamAuthClassificationRespectsBearerProvenance`, `TestADR_0096_ReconnectProbeOpenAuthRejectedStopsRetry`
- AC1.2: A connected model routes bearer rejection returned by the actual initial or
  freshly rearmed live-reader command/message handoff through the rejected-bearer
  `/connect` recovery overlay. The overlay gives actionable issuer, audience, or CA
  guidance, disables re-login, clears the stale `live feed reconnecting` footer, and
  disarms reconnect/live state; it is not a test that injects a pre-classified
  `StreamErrMsg` directly.
  - verify: `TestADR_0096_BearerLiveReaderRecvAuthRejectedRoutesToConnectRecovery`
- AC1.3: That connected live-reader path preserves the failed target and session handoff,
  tears down the affected readers and reconnect loop, and performs no retry for rejection
  returned from initial `Recv`, reconnect probe `Open`, or freshly rearmed-reader `Recv`.
  - verify: `TestADR_0096_BearerLiveReaderAuthRejectedPreservesHandoffAndStopsRetry`
- AC2.1: The actual first-close → reconnect-probe-success → freshly rearmed reader
  immediate-close cycle advances continuity from attempt 1 to attempt 2 and
  deterministically waits for the attempt-2 backoff before its next probe. It must not
  preload continuity or merely inspect delay helper math: probe open alone does not reset
  the attempt; attempts are bounded, increasing, and capped.
  - verify: `TestADR_0096_ImmediateRearmedCloseUsesAttemptTwoBackoff`, `TestLiveReconnectDelay_BoundedAndIncreasing`
- AC3.1: A real event from the current live generation positively resets the cross-loop
  continuity attempt, so the next immediate-close outage starts at attempt 1. A reconnect
  marker, probe success, and catch-up event alone each leave continuity unchanged.
  - verify: `TestADR_0096_OnlyCurrentLiveEventResetsContinuity`
- AC3.2: After a real session switch, a stale reconnect generation cannot mutate or rearm
  the old session. Stale live generations and TUI cancellation retain their existing
  teardown behavior.
  - verify: `TestADR_0096_StaleReconnectAfterSessionSwitchCannotRearmOldSession`, `TestStaleStreamGenerationDropped`

## Verification

- Run the named tests offline with the race detector; no test uses a live provider,
  network, or bearer secret.
- Run `task lint`, `task test`, `task docs`, `task ac-trace-strict`, and
  `go run ./cmd/mecademo` before returning the plan to `landed`.
