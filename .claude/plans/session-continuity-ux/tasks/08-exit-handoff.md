---
id: 08-exit-handoff
title: Final session ID exit handoff
blocked_by: [06-active-session-details, 07-startup-resume]
status: done
branch: "plan-session-continuity-ux/08-exit-handoff"
worktree: ".scratch/worktrees/session-continuity-ux-task-08"
issue: "473"
retries: 0
last_error: ""
accumulator: acc/session-continuity-ux
---

# Task brief

After all rebind paths exist, emit the documented reversible final-session handoff on stderr after alternate-screen teardown. Preserve existing stdout and forced/startup-failure behavior. Update docs/user-docs and add process-level tests.

## Acceptance criteria

- AC7.1: Clean exit emits one documented quoted/JSON-safe stderr line from which the byte-exact final active session ID can be decoded after alternate-screen teardown.
  - verify: `TestSessionContinuityUX_Scenario7_FinalIDAfterTeardown`
- AC7.2: Stored-session continuation, model carryover, effort fork, and worktree switch each cause exit to emit the final adopted ID rather than the startup ID.
  - verify: `TestSessionContinuityUX_Scenario7_FinalRebindMatrix`
- AC7.3: No-session, startup-failure, and forced-exit paths do not print a misleading successful handoff; stdout remains available for existing output and stderr grammar is stable in embedded/connect modes.
  - verify: `TestSessionContinuityUX_Scenario7_OutputContract`
- AC7.4: Exit output and startup flags are documented together as the normal “copy now or resume later” workflow.
  - verify: inspection — `task docs` and `task site:build` pass with the reviewed workflow
