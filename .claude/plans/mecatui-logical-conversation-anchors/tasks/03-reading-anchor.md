---
id: 03-reading-anchor
title: Conversation-view reading anchor controller
blocked_by: [02-rendered-frame]
status: in-progress
attempt: 1
branch: "plan-mecatui-logical-conversation-anchors/03-reading-anchor-attempt-1"
worktree: ".scratch/worker-mecatui-logical-conversation-anchors-03-reading-anchor-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Introduce the package-private concrete conversation-view owner. Route every
viewport replacement, keyboard/mouse scroll, resize, expanded/collapsed render,
and follow state through it. `Model` keeps event/input/layout routing. Remove
independent `stuck` truth; retain only a derived compatibility projection where
chrome needs it.

## Acceptance criteria

- AC1.4: Anchors restore exact region/location then deterministic same-region, same-block, bias-directed adjacent-block fallbacks.
  - verify: `TestADR_0301_AnchorFallbackIsDeterministic`
- AC1.5: Card and changed-files appendix collapse retain/fall back within the intended document neighborhood.
  - verify: `TestADR_0301_CardAndChangedFilesAppendixFallback`
- AC1.6: Bottom-aligned restoration enters tail-follow and scrolling above bottom anchors reading.
  - verify: `TestADR_0301_BottomAlignedAnchorPromotesTailFollow`
