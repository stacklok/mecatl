---
id: 07-final-panel-repairs
title: Final debugger grammar and ADR supersession repairs
blocked_by: [05-resolver-exact-semantics-api-cleanup, 06-standards-docs-integration-relocation]
status: done
branch: "plan-predictable-session-handles/07-final-panel-repairs"
worktree: ".scratch/worktrees/issue-922-task07"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Close the final panel findings without widening scope. Keep exact IDs and terminal-safe displayed
handles accepted by the positional debugger target. Record ADR-0284 as superseding ADR-0254's
`DEBUG target #<digest>` presentation clause only and retain the scoped backlink already added to
ADR-0254; do not alter debugger authority/evidence/incarnation decisions.

The operator's final UX repair is task 08: one `TARGET` grammar, exact-ID precedence, and unchanged
server exact-ID fallthrough on inventory failure or zero projected matches.

## Acceptance criteria

- AC2.1: leading-hyphen exact IDs remain accepted as positional targets while their displayed
  handles encode the leading hyphen as `%2D`.
  - verify: `TestPredictableSessionHandles_Scenario2_UnifiedTargetGrammar`
- AC3.1: command help leads with the one `TARGET` flow and contains no alternate-mode guidance.
  - verify: `TestPredictableSessionHandles_Scenario3_CommandHelpUsesOneTargetFlow`
- AC3.5: ADR-0284 explicitly supersedes only ADR-0217's ordinary display-handle decision and
  ADR-0254's `DEBUG target #<digest>` presentation clause; both older ADRs carry scoped backlinks,
  while every debugger authority, evidence, and incarnation decision remains in force.
  - verify: inspection — `task docs` validates ADR metadata and links
