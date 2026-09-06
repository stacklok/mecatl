# Mecatui live-feed reconnect — acceptance plan

**Issue:** [stacklok/mecatl#779](https://github.com/stacklok/mecatl/issues/779)  
**Status:** in-progress
**Scope:** regression closure for the already-landed live-feed reconnect behavior on
`acc/mecatui-live-reconnect`; no production implementation or test changes in this plan.  
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

A bearer-backed live stream whose first `Recv`, reconnect probe `Open`, or reconnect
probe `Recv` returns gRPC `Unauthenticated` is classified as `AuthRejected`. The UI
consumes that closed reason through the existing `/connect` recovery path, preserves the
failed target/session handoff, tears down the affected readers and reconnect loop, and
does not retry. Credential-free `Unauthenticated` and replay errors keep their existing
classifications. This preserves the authentication and generation decisions in
[ADR-0096](../adr/0096-live-feed-reconnect.md).

**Acceptance:**

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

### Scenario 2 — clean close rearms with cross-loop continuity and backoff

A live stream that opens and immediately closes is an ordinary continuity failure. The UI
starts exactly one reconnect loop, carries continuity/backoff state across the initial arm
and reconnect probe, and reopens the live feed after bounded backoff. A second immediate
close is observable as attempt 2 and waits for the next backoff delay rather than resetting
at probe open. Reconnect success clears degraded state and re-arms a fresh live reader.
The single-loop and cancellation invariants remain those documented in
[AGENTS.md](../../AGENTS.md).

**Acceptance:**

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

### Scenario 3 — a real live event restores continuity

After a clean close and rearm, an actual event received from the current live reader
proves the feed is healthy and resets continuity/backoff. The next outage therefore starts
at attempt 1 again. Probe success and replay catch-up alone do not reset it. Catch-up
projection semantics remain unchanged and are not acceptance evidence for #779. The
current-generation and real-event rule follows [ADR-0096](../adr/0096-live-feed-reconnect.md).

**Acceptance:**

- AC3.1: A real event from the current live generation resets the cross-loop continuity
  attempt; the next immediate-close outage starts at attempt 1. A probe/reconnected marker
  or catch-up event alone does not reset it.
  - verify: `TestADR_0096_RealLiveEventResetsContinuity`
- AC3.2: A stale live or reconnect generation is dropped without rearming the old session;
  session switch and TUI cancellation preserve the existing teardown behavior.
  - verify: `TestReconnectUI_StopsOnSessionSwitch`, `TestStaleStreamGenerationDropped`

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
- `task ac-trace-strict` is run when this plan is promoted to `landed`; while draft,
  `task ac-trace` reports missing future proofs without making the plan a gate.
- No ADR, API baseline, generated contract, or unrelated documentation change is
  introduced.
