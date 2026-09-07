---
id: 06-panel-anchor-lifetime-and-appendix-order
title: Preserve previous-frame provenance and document-order appendix fallback
blocked_by: [05-integration-performance]
status: pending
attempt: 1
branch: "plan-mecatui-logical-conversation-anchors/06-panel-anchor-lifetime-and-appendix-order-attempt-1"
worktree: ".scratch/worker-mecatui-logical-conversation-anchors-06-panel-anchor-lifetime-and-appendix-order-attempt-1"
issue: ""
retries: 1
last_error: "task test failed: TestUsableAutoSkillsStockBuildPolicyMatrix/untrusted_project_ignored (unrelated learned skill operation failure)"
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Repair the panel-review ship-blockers. A `conversationView` keeps a prior frame
until it captures the anchor during replacement; its provenance must therefore
remain immutable until capture. Do not reuse the next frame's mutable
provenance backing storage for the retained frame. Also make anchor fallback
use physical frame/document order rather than monotonic block-ID allocation
order: the changed-files appendix is allocated on its first membership change,
but is physically rendered after all conversation blocks, including later
blocks. Preserve cached-prefix behavior and add focused regression tests.

## Acceptance criteria

- AC1.4: Anchors restore exact region/location then deterministic same-region, same-block, bias-directed adjacent-block fallbacks.
  - verify: `TestADR_0301_AnchorFallbackIsDeterministic`
- AC1.5: Card and changed-files appendix collapse retain/fall back within the intended document neighborhood.
  - verify: `TestADR_0301_CardAndChangedFilesAppendixFallback`
