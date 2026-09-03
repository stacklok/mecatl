---
id: 06-mecatui-title-ux
title: Mecatui title command and live reconciliation
blocked_by: [02-title-wire-projection, 05-title-coordinator]
status: in-progress
branch: ""
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Add client-only `/title`. It reuses RenameSession, performs safe optimistic active-session update/reconciliation, and no-argument output is a local nonpersisted notice containing title and provenance. Project `session.title` live events to active UI state and refetch GetSession on reconnect/reopen. Terminal automatic non-success produces one deduplicated muted notice with `/title <text>` guidance and no provider detail. No polling, RPC, prompt message, or engine tool.

## Acceptance criteria
- AC1.1, AC1.2, AC1.3, AC1.4, AC4.6, AC4.7.
- verify: `TestSessionTitleGeneration_Scenario1_TitleCommandPersistsOperatorTitle`
- verify: `TestSessionTitleGeneration_Scenario1_TitleCommandReadAndRejectsBlank`
- verify: `TestSessionTitleGeneration_Scenario1_TitleCommandReconcilesFailure`
- verify: `TestSessionTitleGeneration_Scenario1_TitleCommandIsClientOnly`
- verify: `TestSessionTitleGeneration_Scenario4_ClientReconnectReconcilesTitle`
- verify: `TestSessionTitleGeneration_Scenario4_QuietFailureOffersManualTitle`
