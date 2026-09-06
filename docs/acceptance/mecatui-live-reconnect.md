# Mecatui live-feed reconnect — acceptance plan

**Issue:** [stacklok/mecatl#779](https://github.com/stacklok/mecatl/issues/779)  
**Status:** in-progress
**Scope:** regression closure plus the narrow live-reader bearer-provenance correction
exposed by the named end-to-end acceptance test on `acc/mecatui-live-reconnect`; final
panel repair round 2 strengthens the offline acceptance proofs without widening scope.
**ADR:** [ADR-0096](../adr/0096-live-feed-reconnect.md) — client-owned reconnect,
bounded backoff, full-log delivery catch-up, FireID deduplication, and generation guards.  
**References:** [`docs/tui.md`](../tui.md), [`AGENTS.md`](../../AGENTS.md) (single-loop,
stale-generation, cancellation, and catch-up invariants).

## Goal and scope cut

Close the regression surface around the existing `/connect` recovery and live-feed
rearm paths. The proof is deliberately client-local and offline: bearer provenance is
preserved when authentication fails during first receive or reconnect probe/open,
authentication rejection goes to the existing connect recovery surface rather than the
reconnect loop, while ordinary clean closes continue through reconnect/backoff. A real
live event proves continuity has recovered. Catch-up projection semantics are unchanged
and out of scope for #779.

Out of scope: a new cursor or server protocol, a new reconnect UI phase, token refresh,
changes to the embedded/external transport boundary, catch-up projection semantics, or
a new ADR. ADR-0096 already makes the required decisions.

### Scenario 1 — bearer rejection is authentication recovery, not reconnect

A bearer-backed live stream whose first `Recv`, reconnect probe `Open`, or first
`Recv` on the freshly rearmed live reader returns gRPC `Unauthenticated` is classified as
`AuthRejected`. The UI consumes that closed reason through the existing `/connect`
recovery path, preserves the failed target/session handoff, tears down the affected
readers and reconnect loop, and does not retry. Credential-free `Unauthenticated` and
replay errors keep their existing classifications. This preserves the authentication and
generation decisions in [ADR-0096](../adr/0096-live-feed-reconnect.md).

**Acceptance:**

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
  The named test drives reconnect-probe `Open` rejection through the connected UI reducer,
  proving the `/connect` recovery handoff and teardown rather than only the client loop.
  - verify: `TestADR_0096_BearerLiveReaderAuthRejectedPreservesHandoffAndStopsRetry`
- AC1.4: A non-bearer first-`Recv` authentication failure remains `AuthNotEnrolled`,
  and replay transport errors remain unclassified; neither case widens bearer-only
  recovery.
  - verify: `TestEventStreamAuthClassificationRespectsBearerProvenance`, `TestAuthFailureUsesTypedLocalCausesAndBearerProvenance`

### Scenario 2 — clean close rearms with cross-loop continuity and backoff

A live stream that opens and immediately closes is an ordinary continuity failure. The UI
starts exactly one reconnect loop, carries continuity/backoff state across the initial arm
and reconnect probe, and reopens the live feed after bounded backoff. A second immediate
close is observable as attempt 2 and waits for the next backoff delay rather than resetting
at probe open. Reconnect success clears degraded state and re-arms a fresh live reader.
The single-loop and cancellation invariants remain those documented in
[AGENTS.md](../../AGENTS.md).

**Acceptance:**

- AC2.1: The actual first-close → reconnect-probe-success → freshly rearmed reader
  immediate-close cycle advances continuity from attempt 1 to attempt 2 and
  deterministically waits for the attempt-2 backoff before its next probe. It must not
  preload continuity or merely inspect delay helper math: probe open alone does not reset
  the attempt; attempts are bounded, increasing, and capped. A deterministic client-loop
  proof sets jitter to zero and shows `ReconnectLiveCmdFromAttempt(prior=1)` does not open
  before the attempt-2 delay and does open afterward, using wide timing bounds.
  - verify: `TestADR_0096_ImmediateRearmedCloseUsesAttemptTwoBackoff`, `TestADR_0096_AttemptTwoWaitsDeterministicBackoff`, `TestLiveReconnectDelay_BoundedAndIncreasing`
- AC2.2: The same session has at most one reconnect loop; reconnect success clears
  degraded state, tears down the completed loop, and re-arms one fresh live reader.
  - verify: `TestReconnectUI_NoDuplicateConcurrentReconnect`, `TestReconnectUI_TriggerOnStreamCloseAndError`
- AC2.3: Cancellation stops and joins the reconnect work without a retry or goroutine
  leak, including while an open/probe is blocked.
  - verify: `TestReconnectLiveCmd_StopsOnCtxCancel`, `TestReconnectLiveCmd_RetriesWedgedOpen`

### Scenario 3 — a real live event restores continuity

After a clean close and rearm, an actual event received from the current live reader
proves the feed is healthy and resets continuity/backoff. The next outage therefore starts
at attempt 1 again. Probe success and replay catch-up alone do not reset it. Catch-up
projection semantics remain unchanged and are not acceptance evidence for #779. The
current-generation and real-event rule follows [ADR-0096](../adr/0096-live-feed-reconnect.md).

**Acceptance:**

- AC3.1: A real event from the current live generation positively resets the cross-loop
  continuity attempt, so the next current-reader immediate-close outage emits reconnect
  marker attempt 1. A reconnect marker, probe success, and catch-up event alone each leave
  continuity unchanged.
  - verify: `TestADR_0096_OnlyCurrentLiveEventResetsContinuity`
- AC3.2: After a real session switch, a stale reconnect generation cannot mutate or rearm
  the old session. Stale live generations and TUI cancellation retain their existing
  teardown behavior.
  - verify: `TestADR_0096_StaleReconnectAfterSessionSwitchCannotRearmOldSession`, `TestStaleStreamGenerationDropped`

## Out of scope

| Item | Defer-to | Reason |
|---|---|---|
| Server cursors, sequence numbers, or proto/port changes | future ADR-0096 upgrade path | ADR-0096 explicitly chooses full replay |
| Token refresh or login changes | existing auth/connect work | this plan only classifies the transport failure and routes recovery |
| New reconnect UI phases or alternate transport behavior | future UX/transport plan | the degraded footer and `/connect` surface already exist |
| Catch-up projection semantics | existing reconnect/catch-up behavior | unchanged by #779 and not acceptance evidence |

## Definition of done

- The named scenario tests above pass offline with the race detector; no test uses a
  live provider, network, or bearer secret.
- `task lint`, `task test`, `task docs`, `task ac-trace-strict`, and `go run ./cmd/mecademo`
  pass; `go run ./cmd/mecademo` still demonstrates a complete offline turn/tool/
  permission/approval/result session.
- `task ac-trace-strict` is a landed requirement: every named proof resolves before this
  plan returns to `landed`.
- No ADR, API baseline, generated contract, or unrelated documentation change is
  introduced.
