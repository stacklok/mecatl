---
id: 09-panel-selection-identity
title: Fail closed for ambiguous logical selection endpoints
blocked_by: [08-panel-canonical-provenance]
status: in-progress
attempt: 1
branch: "plan-mecatui-logical-conversation-anchors/09-panel-selection-identity-attempt-1"
worktree: ".scratch/worker-mecatui-logical-conversation-anchors-09-panel-selection-identity-attempt-1"
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Repair the panel's important logical-selection finding. Endpoint restoration
must use the recorded canonical source offset to establish identity rather than
accepting the first duplicate bounded context in a block/region. If identity
cannot be proved after frame replacement, clear the selection. Add a duplicate
context regression test.

## Acceptance criteria

- AC1.7: Logical selection survives reflow/live appends only when context and copied text remain valid.
  - verify: `TestADR_0301_SelectionPreservesLiveStableText`
