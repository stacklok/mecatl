---
id: 02-undo-semantics
title: Make lifecycle undo strictly backward and bounded
blocked_by: []
status: done
branch: "plan-memory-lifecycle-hardening/02-undo-semantics"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/memory-lifecycle-hardening
---

# Task brief

State `MemoryLifecycleStore`'s no-redo, strictly-backward undo contract. In both memory stores, use the same origin/undone predicate for restore-source selection as target selection. Prune obsolete undo markers whenever retention truncates history. Keep the configured retention behavior and avoid changing permission floors.

## Acceptance criteria

- AC2.1: `MemoryLifecycleStore`'s doc comment states the no-redo, strictly-backward undo contract.
  - verify: inspection — doc-comment review, documentation-only
- AC2.2: `UndoLatest`'s restore-source uses the same origin/undone filter as its target-selection scan, so a Remember→Undo→Undo→Remember→Undo sequence never restores a value that was itself already undone.
  - verify: `TestADR_0226_UndoNeverRestoresAlreadyUndoneRevision`
- AC2.3: Repeated `Undo` calls on the same key, with no intervening `Remember`, walk strictly backward and terminate in a "no mutation remains to undo" error rather than looping or restoring a stale value.
  - verify: `TestADR_0226_RepeatedUndoWalksStrictlyBackward`
- AC2.4: `r.undone` entries for a key are pruned in lockstep with `retainLatest`'s truncation, so the map does not grow unboundedly across the store's lifetime.
  - verify: `TestADR_0226_UndoneMapPrunedWithRetainLatest`
