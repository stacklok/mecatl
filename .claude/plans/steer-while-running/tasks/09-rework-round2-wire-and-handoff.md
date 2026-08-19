---
id: 09-rework-round2-wire-and-handoff
title: "Rework: proto message_id + slot_full + sequential active-run handoff"
blocked_by: [08-rework-round2-engine-inbox]
status: done
branch: "plan-steer-while-running/09-wire-handoff-rework"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
round: 2
---

# Task brief

Wire + server rework per the Ozz-review decisions (HANDOFF.md). Branch
`plan-steer-while-running/09-wire-handoff-rework` off the CURRENT
`feat/steer-while-running` HEAD (post task 08). Commit only this task's files. Do
NOT push.

Three changes:

1. **Proto** (`contracts/proto/mecatl/v1/harness.proto`): add `string message_id`
   to `Steer`, `SteerCancel`, `SteerAck`, `SteerEcho` (client-minted,
   server-echoed). Replace `STEER_OUTCOME_SUPERSEDED` with `STEER_OUTCOME_SLOT_FULL`
   in the `SteerOutcome` enum. NO `expected_run_id` (the wire has no run-id
   concept). `task generate` regenerates `contracts/gen` — commit it.

2. **Sequential active-run handoff** (`internal/adapter/server/grpc.go` +
   `service.go`). Replace `go h.drivePromotedSteer(...)` (an unjoined second relay
   that orphans the promoted run when `Converse` returns and races `runRelay.sendErr`)
   with a SEQUENTIAL handoff: after the original run's relay drains, if a steer was
   promoted, `Converse` relays the promoted run on the SAME stream (reusing the
   single-writer `streamSender`) before returning, and `readControl`'s control
   target (`run`) swaps ATOMICALLY to the promoted run at the same moment (so
   ResumeApproval/Cancel/CancelChild hit the active run — fixing finding #4 too).
   `runRelay.sendErr` gets a single owner (no cross-goroutine access). The promoted
   run's terminal EvResult + its `steer.outcome{promoted:true}` ack must be relayed
   on the stream BEFORE the RPC returns (no ack-after-close).

3. **Service updates** (`service.go`): `Service.Steer`/`CancelSteer` carry/echo
   `message_id` end-to-end; the `slot_full` outcome maps to the proto enum; the
   authorize-first (GetSession) ordering is preserved. Remove any supersede
   handling in the wire path.

## Acceptance criteria (named tests, offline, `-race`)

- R3-1: `message_id` round-trips: a steer sent with `message_id` echoes it back on the ack AND the EvSteer echo; a client can correlate. verify: `TestSteer_MessageIdRoundTrip`
- R3-2: `slot_full` maps to the proto enum on a second enqueue while one's pending. verify: `TestSteer_SlotFullOnWire`
- R3-3: a promoted steer relays its full event stream + terminal ack on the SAME stream before Converse returns (no orphan, no ack-after-close). verify: `TestSteer_PromotedRelaySequential`
- R3-4: after promote, ResumeApproval/Cancel target the PROMOTED run, not the terminal original. verify: `TestSteer_ControlTargetsPromotedRun`
- R3-5: `runRelay.sendErr` has a single owner — no data race between original + promoted relays. verify: `TestSteer_RelaySendErrSingleOwner` (`-race`)
- R3-6: `task generate` + `task api:update` committed; existing Converse arms unchanged. verify: inspection

Run `task lint` + `task test` + `task generate` + `task api:update`. Report branch,
git log, tests added, any R3-x not satisfied.
