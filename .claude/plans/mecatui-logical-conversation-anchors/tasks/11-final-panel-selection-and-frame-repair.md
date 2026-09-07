---
id: 11-final-panel-selection-and-frame-repair
title: Repair dirty-frame and canonical selection integration
blocked_by: [10-panel-simplify-artifacts-follow-state-and-docs]
status: in-progress
attempt: 1
branch: "plan-mecatui-logical-conversation-anchors/11-final-panel-selection-and-frame-repair-attempt-1"
worktree: ".scratch/worker-mecatui-logical-conversation-anchors-11-final-panel-selection-and-frame-repair-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Repair the final panel ship blockers. During a dirty pending-delta selection,
rebuild the complete frame including the expanded changed-files appendix and
install that exact frame as the conversation-view frame before replacing the
viewport projection. Ensure logical selection points use the same canonical
ANSI-free, grapheme-safe row coordinate transformation as frame provenance;
tool-card borders/padding and leading semantic whitespace must not shift it.
Add regressions for dirty appendix selection and wrapped tool selection across
reflow.

## Acceptance criteria

- AC1.7: Logical selection survives reflow/live appends only when context and copied text remain valid, including pending-delta selection/copy before flush.
  - verify: `TestADR_0301_SelectionPreservesLiveStableText`
- AC1.9: A selection in a live subagent card remains correctly projected and copyable through unrelated updates.
  - verify: `TestLogicalConversationAnchors_Scenario1_ChangingFrame`
