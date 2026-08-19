---
id: 08-rework-round2-engine-inbox
title: "Rework: engine mutex inbox (drop supersede) + clean-exit continue-run"
blocked_by: []
status: done
branch: "plan-steer-while-running/08-engine-inbox-rework"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
round: 2
---

# Task brief

Engine rework per the Ozz-review decisions (HANDOFF.md). Branch
`plan-steer-while-running/08-engine-inbox-rework` off the CURRENT
`feat/steer-while-running` HEAD. Commit only this task's files. Do NOT push.

Two engine changes:

1. **Mutex inbox + DROP SUPERSEDE** (`engine/agent/steer.go`). Replace the
   cap-1-channel `steerInbox` with a `sync.Mutex` + `{closed bool, pending string,
   has bool}` triple. Transitions, each ONE critical section:
   - enqueue: `closed → SteerTooLate`; `has → SteerSlotFull` (NEW outcome — reject,
     "cancel first"); else `pending,has = text,true → SteerAccepted`.
   - cancel: take+clear → `SteerRetracted` / `SteerNonePending`.
   - drain: take+clear → commit.
   - close: `closed = true`.
   DELETE the supersede path entirely (`SteerSuperseded` is removed from the
   `SteerOutcome` enum). Add `session.ToValidUTF8` repair in `enqueueSteer` BEFORE
   the text enters the inbox (history==echo==model-view, two-layer UTF-8 rule).
   Update `EnqueueSteer`/`CancelSteer` exported wrappers accordingly. Deterministic
   liveness tests forcing drain/close between observe & mutate (the finding-#1
   deadlock shape).

2. **Clean-exit continue-run** (`engine/agent/loop.go` `finishTurnNoTools` +
   `runLoop`). Before a clean terminal (`terminateComplete` for meaningful-text /
   benign stop), re-check the inbox: if a steer is PARKED, do NOT terminate —
   continue the loop so the steer drains at the next Step 2a (the run extends until
   the steer is consumed; the never-drop contract holds engine-internally). Mirror
   the `bgPendingNudged` defer pattern but steer blocks clean exit *until drained*
   (not once-only). A parked steer must never be closed-unconsumed by
   `closeSteer()`.

## Acceptance criteria (named tests, offline mockllm/memfs, `-race`)

- R2-1: enqueue on a full slot returns `slot_full`, leaves the pending steer intact; no supersede. verify: `TestSteer_SlotFullRejects`
- R2-2: the supersede path is gone — `SteerSuperseded` is not in the enum and no second-enqueue-replaces path exists. verify: `TestSteer_NoSupersedePath`
- R2-3: enqueue/cancel/drain/close are each one critical section; a forced drain-between-observe-and-mutate cannot deadlock or double-commit. verify: `TestSteer_InboxLinearizable` (`-race`)
- R2-4: invalid-UTF-8 steer text is repaired at ingress; the recorded history, the EvSteer echo, and the model-visible message are all valid UTF-8 and identical. verify: `TestSteer_IngressUTF8Repaired`
- R2-5: a steer enqueued while a meaningful-text no-tool-call answer is streaming is NOT dropped: the clean exit defers, the steer drains at the next boundary, the model acts on it. verify: `TestSteer_CleanExitContinuesRun`
- R2-6: a parked steer is never closed-unconsumed on any terminate path. verify: `TestSteer_NeverClosedParked`

Regenerate `engine/api/agent.txt` via `task api:update` (enum change) +
`engine/CHANGELOG.md` note. Run `cd engine && go test ./agent/ -run TestSteer -race`
then `task lint` + `task test`. Report branch, git log, tests added, any R2-x not
satisfied.
