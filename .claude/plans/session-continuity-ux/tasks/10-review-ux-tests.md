---
id: 10-review-ux-tests
title: Repair UX and test-adequacy findings from panel review
blocked_by: [09-review-server-contract]
status: done
branch: "plan-session-continuity-ux/10-review-ux-tests"
worktree: ".scratch/worktrees/session-continuity-ux-repair-10"
issue: "525"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Repair brief

Replace vacuous rebind-matrix tests that merely call `bindSessionID` with real reducer/command journeys for stored continuation, model carryover, effort fork, and worktree switch, proving `/session` copy and exit handoff observe the final adopted ID. Add coverage for exact resume when listing is unavailable. Verify schedule-fire inspection returns to the prior active main chat and labels unknown rows honestly instead of treating them as child runs. Preserve the existing behavior only when it matches ADR-0217; otherwise make the smallest correction and refresh only affected goldens.

## Protected acceptance criteria

- AC4.1 unknown is not mislabeled as a child.
- AC4.5 inspection preserves active chat.
- AC5.3 and AC7.2 real rebind paths drive final identity.
- AC6.2 exact resume is independent of inventory paging.

Run targeted/golden/full gates and docs if user-visible wording changes.
