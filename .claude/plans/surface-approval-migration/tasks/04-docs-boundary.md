---
id: 04-docs-boundary
title: Finalize surface migration documentation and boundary tests
blocked_by: [03-approval-render-input]
status: done
branch: "plan-surface-approval-migration/04-docs-boundary"
worktree: ""
issue: "555"
retries: 0
last_error: ""
accumulator: acc/surface-approval-migration
---

# Task brief

Finish the migration record after the code is assembled. Update the living
surface migration design and implementation notes to describe the shipped
approval surface and frame-scoped hit dispatch; remove obsolete approval
exclusions; strengthen structural boundary tests; regenerate documentation.
Do not add unrelated UI product changes.

## Acceptance criteria

- AC4.1: Approval vocabulary, state, rendering, hit-ID cache, and input behavior remain confined to approval and shared hit-dispatch files; Model contains only registration, durable effects, and generic routing.
  - verify: `TestApprovalSurfaceStructuralBoundary`
- AC4.3: The living surface-migration design records the ephemeral-ID/frame-cache protocol, Model/surface ownership split, and the deliberately deferred multi-window manager, and retires its obsolete approval-surface exclusion.
  - verify: inspection — `task docs` validates citations and generated `llms.txt`
