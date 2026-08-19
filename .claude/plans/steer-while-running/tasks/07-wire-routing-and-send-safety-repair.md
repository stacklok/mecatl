---
id: 07-wire-routing-and-send-safety-repair
title: Repair — gRPC steer routing single-owner + concurrent-Send hazard + dead exports
blocked_by: [05-wire-grpc-steer-frame]
status: done
branch: "plan-steer-while-running/07-wire-routing-and-send-safety-repair"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
repair_of: panel-review ship-blockers
---

# Task brief

Panel-review repair wave (round 1). Fix the ship-blockers the panel found in
the steer wire path. The accumulator `feat/steer-while-running` has tasks 01–06
merged. Branch `plan-steer-while-running/07-wire-routing-and-send-safety-repair`
off the CURRENT HEAD. Commit ONLY this task's files. Do NOT push.

## Ship-blockers to fix

1. **Concurrent `stream.Send` (gRPC stream is not goroutine-safe).** In
   `internal/adapter/server/grpc.go`: the original run's `relayRun` (grpc.go:238)
   Sends until the run's event channel CLOSES, but `closeSteer()` runs at the top
   of `terminate` (`engine/agent/loop.go:2264`, `:2317`) BEFORE `drainChildren` and
   the terminal `EvResult` drain. A steer landing in that window → `too_late` →
   `handleSteerFrame` spawns `drivePromotedSteer` on a new goroutine (grpc.go:359)
   whose `relayRun` (grpc.go:388) ALSO `stream.Send`s while the original relay is
   still draining → two goroutines Send on one stream. FIX: serialize all Sends on
   the Converse stream behind a single send mutex (or a single-writer lane) shared
   by the original relay, the promoted relay, the RecoverNotice pre-send, and the
   steer-ack interleave. No two goroutines may Send concurrently. Add a regression
   test that a terminal-race steer during the drain window does not race the
   original relay (`-race`).

2. **Lost-steer in the terminate window.** Same window: `Service.Steer`'s promote
   path calls `StartRunContent` which hits `IsLive(id)` (the original run is still
   registered) → `ErrFailedPrecondition`, so the ack is `too_late`/`promoted=false`
   and the steer is DROPPED — contradicting the "never silently dropped" contract
   (`harness.proto` Steer comment, `SteerAck.promoted`). FIX: make the terminate-
   window steer reliably promote (e.g. `drivePromotedSteer` retries/waits until the
   original run deregisters, bounded) OR hold the steer until the inbox closes AND
   the run is no longer live, then promote. The honest contract: a steer is either
   accepted into a live run OR promoted to a follow-up run — never dropped without
   a loud ack AND a follow-up run.

3. **Single-owner routing (architecture HIGH).** `handleSteerFrame` calls
   `run.EnqueueSteer` directly and only calls `Service.Steer` for the promoted
   half; the `SteerCancel` arm calls `run.CancelSteer` directly with NO Service
   path. The "live-enqueue vs terminal-race promote" decision has ONE owner:
   `Service.Steer`. FIX: move the whole route into `Service.Steer` / a new
   `Service.CancelSteer`; keep the grpc handler a dumb frame→Service mapper
   (mirroring how `Approve`/`Cancel` route through the Service). The deferred
   HTTP/SSE endpoint must be able to reuse this without re-deriving logic.

4. **Dead exported engine vars (api-gate ossification).**
   `ErrSteerDisabled`/`ErrSteerPending` in `engine/agent` are exported, in
   `engine/api/agent.txt`, CHANGELOG'd, but nothing returns them (their own doc
   comments admit it). FIX: delete both vars, regenerate `engine/api/agent.txt` via
   `task api:update`, and update `engine/CHANGELOG.md`. (Note: removing an
   already-added symbol before release is still Added=minor net — it never shipped
   in a release.)

5. **Optional-but-recommended (reuse MEDIUM): `steerInbox` → `chan string` cap 1.**
   Replace the hand-rolled `sync.Mutex` + `pending/has/closed` bools in
   `engine/agent/steer.go` with a `chan string` (cap 1) + a `chan struct{}` close
   signal, using `select` for enqueue/cancel/drain. Shorter, race-free by design,
   expresses the single-slot contract in the type system. Only do this if it stays
   strictly internal (the exported `SteerOutcome` enum + `EnqueueSteer`/`CancelSteer`
   signatures must not change) — otherwise skip and note why.

## Acceptance criteria (map to the plan's contract; the named tests prove them)

- AC-R1: All Converse-stream Sends are serialized; a terminal-race steer during the
  drain window never races the original relay. verify: `TestSteer_NoConcurrentSendOnTerminalRace` (`-race`)
- AC-R2: A steer arriving in the terminate window is promoted to a follow-up run
  (or accepted), never dropped with a bare `too_late` ack. verify: `TestSteer_TerminateWindowPromotes`
- AC-R3: The grpc handler routes steer + steer_cancel through `Service` (no direct
  `run.EnqueueSteer`/`run.CancelSteer` in grpc.go). verify: `TestSteer_ServiceOwnsRouting`
- AC-R4: `ErrSteerDisabled`/`ErrSteerPending` are gone from the exported surface.
  verify: `task api:check` green + `git grep -c ErrSteerDisabled engine/` reports 0
- AC-R5: `task lint`, `task test` (both modules, `-race`), `task api:update` all green.
  verify: gates

Run `cd engine && go test ./agent/ -run TestSteer -race` and
`go test ./internal/adapter/server/ -run TestSteer -race` from root, then the full
gates. Respect the layering rule; the routing decision stays in the Service layer.
Report the branch, `git log --oneline`, tests added, and which of 1–5 you did NOT
complete (with why).
