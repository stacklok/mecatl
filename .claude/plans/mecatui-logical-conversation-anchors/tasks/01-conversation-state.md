---
id: 01-conversation-state
title: Conversation-owned logical block state
blocked_by: []
status: done
attempt: 1
branch: plan-mecatui-logical-conversation-anchors/01-conversation-state-attempt-1
worktree: .scratch/worker-mecatui-logical-conversation-anchors-01-conversation-state-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/mecatui-logical-conversation-anchors
---

# Task brief

Move UI-local monotonically allocated block identities, changed-file membership,
and the synthetic changed-files appendix identity into `conversation`. Preserve
existing visible behavior. Reset this identity/state with reconstructed
conversations. Do not add viewport, renderer, selection, or protocol work.

## Acceptance criteria

- AC1.1: `conversation` owns monotonically allocated UI-local identities for ordinary blocks and changed-file membership plus its synthetic appendix block; reset/reconstruction starts a new document.
  - verify: `TestADR_0301_UIBlockIdentityResetsWithConversation`
