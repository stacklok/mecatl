---
id: 03-panel-repair-2
title: Final live reconnect panel repair round 2
blocked_by: [02-panel-repair]
status: done
attempt: 1
branch: plan-mecatui-live-reconnect/03-panel-repair-2-attempt-1
worktree: .scratch/worker-mecatui-live-reconnect-03-panel-repair-2-attempt-1
issue: "779"
retries: 0
last_error: ""
accumulator: acc/mecatui-live-reconnect
---

# Final live reconnect panel repair round 2

Strengthen only the impacted offline acceptance proofs. Production changes are allowed only
if a named strengthened test first proves a production defect. Do not add API, protocol, or
catch-up scope.

## Impacted acceptance criteria

> AC1.3: That connected live-reader path preserves the failed target and session handoff,
> tears down the affected readers and reconnect loop, and performs no retry for rejection
> returned from initial `Recv`, reconnect probe `Open`, or freshly rearmed-reader `Recv`.
> The named test drives reconnect-probe `Open` rejection through the connected UI reducer,
> proving the `/connect` recovery handoff and teardown rather than only the client loop.
>
> - verify: `TestADR_0096_BearerLiveReaderAuthRejectedPreservesHandoffAndStopsRetry`

> AC2.1: The actual first-close → reconnect-probe-success → freshly rearmed reader
> immediate-close cycle advances continuity from attempt 1 to attempt 2 and
> deterministically requests the exact attempt-2 backoff before its next probe. It must not
> preload continuity or merely inspect delay helper math: probe open alone does not reset
> the attempt; attempts are bounded, increasing, and capped. A deterministic client-loop
> proof sets jitter to zero, injects a private controlled waiter into the real reconnect loop,
> asserts the exact attempt-2 delay, and proves the probe opens only after release.
>
> - verify: `TestADR_0096_ImmediateRearmedCloseUsesAttemptTwoBackoff`, `TestADR_0096_AttemptTwoWaitsDeterministicBackoff`, `TestLiveReconnectDelay_BoundedAndIncreasing`

> AC3.1: A real event from the current live generation positively resets the cross-loop
> continuity attempt, so the next current-reader immediate-close outage emits reconnect
> marker attempt 1. A reconnect marker, probe success, and catch-up event alone each leave
> continuity unchanged.
>
> - verify: `TestADR_0096_OnlyCurrentLiveEventResetsContinuity`

## Verification

- Run the quoted named tests offline with the race detector; no test uses a live provider,
  network, or bearer secret.
- Run `task lint`, `task test`, `task docs`, `task ac-trace-strict`, and
  `go run ./cmd/mecademo` before returning the acceptance plan to `landed`.
