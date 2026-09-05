---
id: 07-title-docs-and-gates
title: Document title generation and verify aggregate delivery
blocked_by: [01-session-title-domain, 02-title-wire-projection, 03-title-slot-resolution, 04-title-generator, 05-title-coordinator, 06-mecatui-title-ux]
status: done
branch: "plan-session-title-generation/07-title-docs-and-gates"
worktree: ""
issue: "621"
retries: 0
last_error: ""
accumulator: acc/session-title-generation
---

Update living architecture, operator usage, and relevant public user docs. Reconcile ADR 0284 status/as-built details and acceptance proof locations. Generate docs and API contracts when required. Run complete Taskfile and offline demo gates on the assembled feature.

## Acceptance criteria
- Cross-cutting deliverables and Definition of done.
- verify: `TestSessionTitleGeneration_Scenario4_CoordinatorShutdownAndInventory`
