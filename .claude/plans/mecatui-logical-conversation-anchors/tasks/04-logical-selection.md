---
id: 04-logical-selection
title: Logical selection projection
blocked_by: [03-reading-anchor]
status: in-progress
attempt: 1
branch: "plan-mecatui-logical-conversation-anchors/04-logical-selection-attempt-1"
worktree: ".scratch/worker-mecatui-logical-conversation-anchors-04-logical-selection-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Migrate current selection gestures, styling, and copying to logical frame
coordinates. Store exact copied ANSI-free text and independent 16-grapheme
before/after endpoint contexts. Preserve a selection only when its logical
context and copied text resolve after replacement; safely clear otherwise.

## Acceptance criteria

- AC1.7: Logical selection survives reflow/live appends only when context and copied text remain valid, including pending-delta selection/copy before flush.
  - verify: `TestADR_0301_SelectionPreservesLiveStableText`
- AC1.9: A selection in a live subagent card remains correctly projected and copyable through unrelated updates.
  - verify: `TestLogicalConversationAnchors_Scenario1_ChangingFrame`
