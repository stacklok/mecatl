---
id: 10-panel-simplify-artifacts-follow-state-and-docs
title: Simplify artifact provenance, remove follow-state mirror, and reconcile docs
blocked_by: [09-panel-selection-identity]
status: in-progress
attempt: 1
branch: "plan-mecatui-logical-conversation-anchors/10-panel-simplify-artifacts-follow-state-and-docs-attempt-1"
worktree: ".scratch/worker-mecatui-logical-conversation-anchors-10-panel-simplify-artifacts-follow-state-and-docs-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Apply the accepted panel dispositions. Remove substring-based artifact-row
classification: artifacts use their enclosing tool-result provenance in v1,
which eliminates false ownership without a structured rendering metadata
pipeline. Remove `Model.stuck` and every synchronization assignment; derive
chrome follow state directly from the package-private `conversationView` owner.
Update ADR 0301's initial-region decision to remove artifact, change its status
to Accepted, and reconcile the acceptance-plan index status to landed. Add or
adjust tests to pin the simpler ownership/follow-state behavior. Run docs gates.

## Acceptance criteria

- AC1.2: A rendered frame carries exactly one provenance record per rendered line and preserves existing visible output.
  - verify: `TestADR_0301_RenderedFrameProvenanceMatchesLines`
- AC1.6: Bottom-aligned restoration enters tail-follow and scrolling above bottom anchors reading.
  - verify: `TestADR_0301_BottomAlignedAnchorPromotesTailFollow`
