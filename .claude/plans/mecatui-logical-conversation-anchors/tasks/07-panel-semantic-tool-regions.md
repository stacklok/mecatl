---
id: 07-panel-semantic-tool-regions
title: Derive tool-card provenance from semantic sections
blocked_by: [06-panel-anchor-lifetime-and-appendix-order]
status: pending
attempt: 0
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Repair the panel-review ship-blocker in rendered-frame provenance. Remove the
`len(rows)/2` heuristic that labels tool-card argument and result rows. Derive
provenance from the actual rendered semantic sections so tool argument/result
anchors retain their region across different lengths, wrapping, and collapsed
state. Keep canonical visible ANSI-free grapheme offsets correct and add a
focused regression test. Do not introduce an abstraction outside the UI scope.

## Acceptance criteria

- AC1.4: Anchors restore exact region/location then deterministic same-region, same-block, bias-directed adjacent-block fallbacks.
  - verify: `TestADR_0301_AnchorFallbackIsDeterministic`
- AC1.5: Card and changed-files appendix collapse retain/fall back within the intended document neighborhood.
  - verify: `TestADR_0301_CardAndChangedFilesAppendixFallback`
