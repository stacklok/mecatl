# Surface approval migration orchestration

**Acceptance plan:** `docs/acceptance/surface-approval-migration.md`  
**Accumulator:** `acc/surface-approval-migration`, based on `surface-models-migration` (PR #731), not `main`  
**PR base:** `surface-models-migration`

The work is deliberately serial: each task changes the same approval routing
boundary. Git ancestry and the task frontmatter are the authoritative state.
