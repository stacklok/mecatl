---
id: 10-rework-round2-tui
title: "Rework: mecatui queue→one bundle, cancel-then-recompose edit, queued-until-landed render"
blocked_by: [09-rework-round2-wire-and-handoff]
status: done
branch: "plan-steer-while-running/10-tui-rework"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
round: 2
---

# Task brief

mecatui rework per the Ozz-review decisions (HANDOFF.md). Branch
`plan-steer-while-running/10-tui-rework` off the CURRENT `feat/steer-while-running`
HEAD (post task 09). Commit only this task's files. Do NOT push.

The client model (resolves finding #6 + the multi-message confusion):

1. **Queue → one bundle.** The client keeps a LIST of staged messages; on send it
   collapses them into ONE steer bundle and mints a fresh `message_id`. Server
   inbox stays single-slot ⇒ at most ONE bundle outstanding (no "cancel-latest vs
   edit-earlier" confusion).

2. **Optimistic `↑` edit = cancel-then-recompose.** On `↑`: issue
   `steer_cancel{message_id}` for the outstanding bundle (do NOT block on the ack),
   immediately pull the whole not-yet-drained set into the textarea as ONE editable
   blob, let the user edit (earlier steers included — they're one un-drained set),
   resend as ONE new bundle with a fresh `message_id`. A drained `message_id`
   (EvSteer echo seen) is BURNED — resend after a drain is a NEW submission, never a
   confused re-edit. Late ack reconciles: `retracted`=clean edit,
   `none_pending`=drained→"shipped" treatment (the text went out; the edit is a
   fresh draft). NO synchronous wait on the cancel ack.

3. **Queued-until-landed rendering.** The steer shows as a PENDING card at the
   bottom until the `EvSteer` echo lands, then appears IN CONTEXT at its true
   position (the echo is stream-positioned: after the settled tool results, before
   the next `EvTurnStart`). The client renders the ECHO — never infers position by
   text-matching history. The pending card clears when the echo lands; the landed
   line appears at the boundary. Fixes the edit-back duplication bug
   (`update.go:2256`) — `↑` no longer leaves the old steer in place to be merged on
   resend.

4. **Fallback unchanged.** When `Capabilities.Steer` is false, the #228 local
   merge-queue stays byte-identical.

Update the card golden (`task test:golden`) and the relevant assertions (the
`Contains` asserts at `steer_test.go` that masked the duplication bug must become
exact-equality).

## Acceptance criteria

- R4-1: a sent steer carries a fresh `message_id`; the TUI renders the ack/echo by id, ignoring stale ones. verify: `TestSteer_TUIIgnoresStaleAck`
- R4-2: `↑` cancels the outstanding bundle and pulls the whole not-yet-drained set into one editable blob; resend collapses to one new bundle with a NEW id. verify: `TestSteer_TUIEditCancelThenRecompose`
- R4-3: resend after `↑` does NOT duplicate the old text (exact-equality assertion). verify: `TestSteer_TUIEditBackNoDuplicate`
- R4-4: a drained `message_id` is burned — editing after the echo opens a fresh draft, and the "shipped" state is shown. verify: `TestSteer_TUIBurnedIdFreshDraft`
- R4-5: the card shows pending until the echo lands, then the landed line appears in context. verify: `TestSteer_TUIQueuedUntilLanded` (+ `task test:golden`)
- R4-6: capability-absent → the #228 local queue is byte-identical. verify: `TestSteer_DisabledFallsBackToLocalQueue` (existing, must stay green)

Run `task lint` + `task test` + `task test:golden`. Report branch, git log, tests
added, any R4-x not satisfied.
