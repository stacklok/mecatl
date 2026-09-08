---
id: 11-rework-round2-docs
title: "Rework: architecture.md section + ADR-0027 List 1/2 + ADR-0114/plan supersede-drop & message_id"
blocked_by: [10-rework-round2-tui]
status: done
branch: "plan-steer-while-running/11-docs-rework"
worktree: ""
issue: "512"
retries: 0
last_error: ""
accumulator: feat/steer-while-running
round: 2
---

# Task brief

Doc-lifecycle rework per the Ozz-review decisions (HANDOFF.md). Branch
`plan-steer-while-running/11-docs-rework` off the CURRENT `feat/steer-while-running`
HEAD (post task 10). Commit only this task's files. Do NOT push.

1. **`docs/architecture.md`** — add a living "how it works" section for
   steer-while-running: the run-scoped mutex inbox (single-slot, `slot_full` —
   supersede dropped), the Step 2a drain, the sequential active-run handoff, the
   promote-grace, the `message_id` correlation. (AGENTS.md requires the living doc
   move with behavior.)

2. **ADR-0027 (`docs/adr/0027-cloud-native.md`)** — add the resource-inventory
   rows the steer work introduces:
   - **List 1** (outlives one call): the `Run`-scoped `steerInbox` (mutex+state,
     lives as long as the run), the `steerAcks` lane, the `streamSender` gate, the
     promote-path handoff.
   - **List 2** (restart fidelity): the pending (un-drained) steer is **restart-lost
     BY DESIGN** — record the explicit fidelity decision (reset-by-design: in-memory,
     dies with the run; only a recorded steer survives as durable history).

3. **ADR-0114** (`docs/adr/0114-steer-while-running.md`) — update for the decisions
   made after it was written: supersede dropped in favour of `slot_full` (replace =
   cancel-then-resend), `message_id` correlation, the sequential active-run handoff,
   the clean-exit continue-run rule, the queued-until-landed rendering rule. (ADR
   content is frozen-once-accepted; since ADR-0114 is still pre-merge on this
   branch, update it in place to match the shipped design — do NOT contradict
   silently.)

4. **Acceptance plan** (`docs/acceptance/steer-while-running.md`) — update the
   scope-cut bullets that say "a second enqueue supersedes" → "a second enqueue
   while one's pending is rejected (`slot_full`) — cancel first", and the
   correlation/echo descriptions for `message_id`. Keep the 27 AC ids stable; only
   reword the behaviour text where the design changed (supersede → slot_full).

5. **user-docs** (`user-docs/what-you-get/mecatui.md`) — adjust the steer paragraph
   for the queued-until-landed rendering and the cancel-then-recompose edit (`↑`
   pulls the whole not-yet-drained set back as one blob).

Run `task docs` (configuration-reference regeneration + matlatl strict) and `task site:build`. Report
branch, git log, files changed, any item not done.
