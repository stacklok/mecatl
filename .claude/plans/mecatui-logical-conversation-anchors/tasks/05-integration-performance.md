---
id: 05-integration-performance
title: Logical anchors integration and performance
blocked_by: [04-logical-selection]
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

Exercise the assembled behavior through real Model paths and extend cache and
allocation-scale regression coverage. Run the existing offline scrollback
benchmarks for comparison against the 5% review target; do not introduce a
flaky numeric CI gate. Update only documentation that describes a real observed
change.

## Acceptance criteria

- AC1.8: The assembled normal path preserves cached-prefix and frame-coalescing complexity without a second transcript.
  - verify: `TestADR_0301_ScrollbackFrameRetainsLinearMetadataOnly`
- AC1.9: The integrated live-card selection behavior is proven through Model event/input paths.
  - verify: `TestLogicalConversationAnchors_Scenario1_ChangingFrame`
