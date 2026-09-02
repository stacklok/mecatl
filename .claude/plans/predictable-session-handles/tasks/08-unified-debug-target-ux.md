---
id: 08-unified-debug-target-ux
title: Unified debugger target UX
blocked_by: [07-final-panel-repairs]
status: done
branch: "acc/predictable-session-handles"
worktree: ".scratch/worktrees/issue-922-session-handles"
issue: "922"
retries: 0
last_error: ""
accumulator: acc/predictable-session-handles
---

# Task brief

Apply the operator's final UX decision: users run only `mecatui debug TARGET` or
`mecatui connect ADDRESS debug TARGET`. Remove every alternate parser/config/client/help concept.
For short candidates, consult the complete caller-visible inventory; exact full-ID equality wins,
otherwise resolve one projected handle. Multiple projections require the copied full ID through
the same command. Inventory failure and zero matches pass `TARGET` unchanged to the server's
existing exact-ID authority.

## Acceptance criteria

- The rendered handle and exact final exit ID feed the same `CreateDebugSession` path.
  - verify: `TestPredictableSessionHandles_Scenario2_RenderedHeaderCreatesBoundDebugger`
- Exact precedence, unique/ambiguous/no-match/inventory-failure behavior are explicit.
  - verify: `TestCreateDebugSessionResolution`
- Embedded and connected help lead with one `TARGET` grammar.
  - verify: `TestPredictableSessionHandles_Scenario3_CommandHelpUsesOneTargetFlow`
  - verify: `TestDebugHelpRoutesRenderDedicatedContract`
- Living docs, user docs, acceptance records, and generated corpus contain no alternate-mode
  guidance.
  - verify: `task docs`
  - verify: `task site:build`
