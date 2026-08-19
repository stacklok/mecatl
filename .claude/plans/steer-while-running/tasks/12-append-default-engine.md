---
id: 12-append-default-engine
title: "Round 3: engine append-default enqueue (accepted/appended), drop SUPERSEDED+slot_full"
blocked_by: []
status: done
branch: ""
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
round: 3
---

# Task brief

Engine rework (round 3) per HANDOFF.md "Round 3" section. Branch
`plan-steer-while-running/12-append-default-engine` off the CURRENT
`feat/steer-while-running` HEAD. Commit only this task's files. Do NOT push.

Change `engine/agent/steer.go` `enqueueSteer` from reject-on-full to
append-default:

- `closed → SteerTooLate`
- `has → APPEND: pending = pending + "\n\n" + text, report NEW outcome SteerAppended`
- `else → SteerAccepted` (park as the pending bundle)

DELETE `SteerSlotFull` and `SteerSuperseded` from the `SteerOutcome` enum (append
covers both). Add `SteerAppended`. The single-slot invariant is preserved (one
pending bundle); the slot just grows by append. The mutex discipline is unchanged
(each transition one critical section).

Regenerate `engine/api/agent.txt` via `task api:update` + `engine/CHANGELOG.md`
note (enum change: SUPERSEDED/SLOT_FULL removed, APPENDED added — Changed,
pre-v1).

Downstream-compat: the wire mapping (`internal/adapter/server/grpc.go`
`steerOutcomeToProto`) and the proto enum reference `slot_full`/`superseded` —
update those references so `task test` stays green repo-wide (the full proto
rework is task 13; here keep the tree compiling: map `SteerAppended` to a sensible
proto value and drop the removed engine enum references). The TUI's
`client.SteerSlotFull` may need a temporary mapping too.

## Acceptance criteria (offline mockllm/memfs, `-race`)

- R5-1: enqueue on an empty slot → `accepted`, parks the text. verify: `TestSteer_AcceptedOnEmpty`
- R5-2: enqueue on an occupied slot → `appended`, the pending text is `old + "\n\n" + new` (one bundle, merged). verify: `TestSteer_AppendedMerges`
- R5-3: `SteerSlotFull` and `SteerSuperseded` are gone from the engine enum. verify: `TestSteer_NoSlotFullOrSuperseded`
- R5-4: append preserves the single-slot invariant and the mutex linearizability (concurrent append/drain/close, `-race`). verify: `TestSteer_AppendLinearizable`
- R5-5: an appended bundle drains as ONE merged user continuation at the boundary (recordContinuation + EvSteer carry the merged text). verify: `TestSteer_AppendedDrainMerged`

Run `cd engine && go test ./agent/ -run TestSteer -race`, then `task lint` +
`task test` + `task api:update`. Report branch, git log, tests added, any R5-x
not satisfied.
