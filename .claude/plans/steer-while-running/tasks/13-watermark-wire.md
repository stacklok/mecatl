---
id: 13-watermark-wire
title: "Round 3: proto appended outcome + Service watermark message_id (tail-on-drain)"
blocked_by: [12-append-default-engine]
status: done
branch: "plan-steer-while-running/12-append-default-engine"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
round: 3
---

# Task brief

Wire/server rework (round 3) per HANDOFF.md "Round 3". Branch
`plan-steer-while-running/13-watermark-wire` off the CURRENT
`feat/steer-while-running` HEAD (post task 12). Commit only this task's files. Do
NOT push.

1. **Proto** (`contracts/proto/mecatl/v1/harness.proto`): the `SteerOutcome` enum
   drops `STEER_OUTCOME_SUPERSEDED` and `STEER_OUTCOME_SLOT_FULL`, adds
   `STEER_OUTCOME_APPENDED`. `message_id` stays SINGLE on all four messages (no
   repeated ids). `task generate` regenerates `contracts/gen` — commit it.

2. **Service watermark correlation** (`internal/adapter/server/service.go`): the
   `steerMsgIDs` FIFO changes from text-match to an ORDERED id-list per pending
   bundle. On each accepted/appended steer, append the frame's `message_id` to the
   bundle's list. On the `EvSteer` drain echo, `SteerEcho.message_id` = the LATEST
   (tail) contributing send's id — a WATERMARK the client splits its ordered queue
   on. The engine stays text-only; the id lives only at this wire-correlation
   layer. Clear the list on fresh-run `register()` (as today). Update the
   `steer echo uncorrelated` WARN to fire when a drain finds NO tracked ids (the
   genuine miss), not on a text-match fail.

3. **Server mapping** (`grpc.go` `steerOutcomeToProto` + the sendEvent EvSteer
   stamp): map `SteerAppended` → `STEER_OUTCOME_APPENDED`; the echo's message_id
   comes from the watermark lookup (tail id), not the text-match pop.

## Acceptance criteria (offline, `-race`)

- R6-1: proto enum has APPENDED, no SUPERSEDED/SLOT_FULL. verify: `TestSteer_ProtoEnumAppend`
- R6-2: two appends (m1 then m2) into one bundle → the drain echo's message_id = m2 (the tail/watermark). verify: `TestSteer_WatermarkEchoLatestId`
- R6-3: a single-send bundle drains → echo id = that send's id. verify: `TestSteer_WatermarkSingleSend`
- R6-4: after a drain, the id-list is consumed; the next bundle starts a fresh list (no stale watermark). verify: `TestSteer_WatermarkFreshAfterDrain`
- R6-5: a drain with NO tracked id (id-less steer) fires the uncorrelated WARN. verify: `TestSteer_UncorrelatedWarnOnMissingIds`
- R6-6: `task generate` + `task api:update` committed; existing arms unchanged. verify: inspection

Run `task lint` + `task test` + `task generate` + `task api:update`. Report branch,
git log, tests added, any R6-x not satisfied.
