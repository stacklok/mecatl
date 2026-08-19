---
id: 14-tui-watermark-queue
title: "Round 3: TUI ordered queue + watermark split on echo (sendSteer appends, holds one wire frame)"
blocked_by: [13-watermark-wire]
status: done
branch: "plan-steer-while-running/14-tui-watermark-queue"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
round: 3
---

# Task brief

TUI rework (round 3) per HANDOFF.md "Round 3". Branch
`plan-steer-while-running/14-tui-watermark-queue` off the CURRENT
`feat/steer-while-running` HEAD (post task 13). Commit only this task's files. Do
NOT push. This fixes the live-tested stuck-queue bug (session 00a10fc8): typing N
messages while a bundle was outstanding only dispatched the first.

The client model:

1. **Ordered queue of sends.** The TUI keeps an ordered list of pending sends
   `{message_id, text}` (each `enter` mints a FRESH `message_id`). A bundle is the
   collapsed join of the queue's texts.

2. **`sendSteer` while a bundle is outstanding → SEND AN APPEND, do not
   hold-and-pray.** When `m.caps.Steer && m.stream != nil`, `enter` sends the new
   line as a steer frame with its fresh id. The ENGINE appends it to the pending
   bundle (task 12/13). At most ONE bundle is outstanding; multiple sends are
   appended into it server-side. (The wire never carries two bundles — the
   single-slot inbox is the bound.)

3. **Watermark split on the echo.** The drain echo carries `message_id` = the
   LATEST contributing send (the watermark). The TUI walks its ordered queue:
   entries UP TO AND INCLUDING the watermark id are drained (rendered in context);
   entries AFTER it stay pending. This replaces the burned-id set with a clean
   watermark cursor. A bundle drains in one echo; the queue's tail (un-drained
   sends) is re-offered as the next bundle.

4. **Queued-until-landed rendering unchanged in spirit:** the card shows the
   pending (un-drained) tail of the queue; the echo lands the drained prefix in
   context at its true stream position.

5. **The `slot_full` path is gone** (append is default); the TUI handles
   `accepted` (new bundle) vs `appended` (merged into pending) acks to render
   honestly, and `too_late` (promote / not-sent) as today.

## Acceptance criteria

- R7-1: typing 3 messages while a bundle is outstanding sends 3 appends; the drain echo (watermark = the 3rd id) drains all 3 (rendered in context), none stuck. verify: `TestSteer_TUIAppendWatermarkDrainsAll`
- R7-2: the echo's watermark id splits the ordered queue — prefix rendered, suffix stays pending. verify: `TestSteer_TUIWatermarkSplitsQueue`
- R7-3: after a full drain, the next send starts a fresh bundle (no stale watermark carryover). verify: `TestSteer_TUIFreshBundleAfterDrain`
- R7-4: the "m1 drained before m2 appended" case renders m1 landed + m2 as the new pending bundle (accepted ack), no id conflation. verify: `TestSteer_TUIDrainBeforeAppend`
- R7-5: capability-absent → the #228 local queue is byte-identical. verify: `TestSteer_DisabledFallsBackToLocalQueue` (existing, must stay green)
- R7-6: card shows the merged pending parts on separate lines (the symptom-1 fix holds). verify: `task test:golden`

Run `task lint` + `task test` + `task test:golden`. Report branch, git log, tests
added, any R7-x not satisfied.
