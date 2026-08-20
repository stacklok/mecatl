---
id: 08-vertical-docs
title: Prove the vertical slices and document the authority design
blocked_by: [05-delegation-derivation, 07-cedar-adapter]
status: done
branch: "plan-authority-evaluator-port/08-vertical-docs"
worktree: ""
issue: "371"
retries: 0
last_error: "aggregate blockers: task test fails baseline macOS /var symlink and skills lifecycle tests; task docs cannot fetch matlatl"
accumulator: acc/authority-evaluator-port
---

# Task brief

Add the two offline vertical-slice tests through the real `app.Build` path: one
for scenarios 1–6 with the local evaluator, and one for Cedar scenario 7. The
local slice covers managed-specialist child derivation, denied and allowed
dispatch, stale disclosure, meta-tool target authorization, restart/resume
narrowing, and evaluator failure. The Cedar slice covers the shipped policy and
an operator path rule.

Write accepted ADR-0233 and update the living architecture, implementation
notes, and user-facing documentation for the evaluator selector and operator
policy file. Follow documentation citation rules; do not cite salvage-only
paths. Regenerate documentation through the Taskfile.

## Acceptance criteria

- The final vertical proof exercises the local adapter from ordinary composition through managed-specialist child derivation, denied and allowed dispatch, stale disclosure, meta-tool target authorization, restart/resume narrowing, and evaluator failure.
  - verify: `TestADR_0233_AuthorityEvaluator_VerticalSlice`
- The Cedar vertical proof reruns the shared stack with the shipped policy and an operator path rule.
  - verify: `TestADR_0233_AuthorityEvaluator_VerticalSlice_Cedar`
