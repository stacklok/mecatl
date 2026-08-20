---
id: 11-vertical-repair
title: Complete the feasible authority vertical proof
blocked_by: [09-mcp-resource-authority, 10-authority-disclosure]
status: done
branch: "plan-authority-evaluator-port/11-vertical-repair"
worktree: ""
issue: "371"
retries: 0
last_error: "task test baseline macOS /var symlink failures; task docs requires GOPRIVATE"
accumulator: acc/authority-evaluator-port
---

# Repair brief

Extend the real app.Build vertical slice to prove denied/allowed dispatch,
stale disclosure refusal, meta-tool target denial, durable restart, and narrowed
resume. Keep runtime evaluator-outage proof at the engine/adapter layer unless
a narrow existing composition test seam is available; amend the vertical-test
wording to state that boundary explicitly rather than adding production
injection solely for a test.

## Acceptance criteria

- The final vertical proof exercises local composition through managed-specialist derivation, denied and allowed dispatch, stale disclosure, meta-tool target authorization, restart/resume narrowing, and evaluator failure at the appropriate layer.
  - verify: `TestADR_0233_AuthorityEvaluator_VerticalSlice`
