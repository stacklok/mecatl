---
id: 03-truncation-visibility
title: Expose memory history truncation end to end
blocked_by: [02-undo-semantics]
status: done
branch: "plan-memory-lifecycle-hardening/03-truncation-visibility"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/memory-lifecycle-hardening
---

# Task brief

Add an additive `Truncated` signal to `tool.MemoryRecord`; populate it in the reference and local stores from their retention state; thread `history_truncated` through the memory driver proto, generated contracts, server, and client; and expose it in Inspect tool output. Regenerate contracts and any required engine API surface in this branch, with the compatible changelog note if the public API changes.

## Acceptance criteria

- AC3.1: `tool.MemoryRecord.Truncated` is `true` whenever the backing store's history for that key has been truncated by its retention cap, for both the in-memory reference store and the local file store.
  - verify: `TestADR_0226_MemoryRecordExposesTruncation`
- AC3.2: The wire `MemoryRecord.history_truncated` field round-trips from the local store's truncation signal through the gRPC server to the client's `tool.MemoryRecord.Truncated`.
  - verify: `TestADR_0226_WireHistoryTruncatedRoundTrips`
- AC3.3: `Inspect*`'s model-facing tool result includes an explicit truncation note when `Truncated` is true.
  - verify: `TestADR_0226_InspectToolSurfacesTruncationNote`
