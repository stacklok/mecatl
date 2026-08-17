---
id: 01-wire-attribution
title: Force injection scanning at the memory driver boundary
blocked_by: []
status: done
branch: "plan-memory-lifecycle-hardening/01-wire-attribution"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/memory-lifecycle-hardening
---

# Task brief

In `internal/adapter/grpcdriver`, separate the untrusted wire caller's persisted attribution provenance from the classification used to decide whether a memory write is model-authored for injection scanning. Apply the forced classification only to the `RememberVersioned` and legacy `RememberEntry` server handlers. Preserve all existing in-process behavior and ordinary wire provenance. Do not change Forget or Undo.

## Acceptance criteria

- AC1.1: A `RememberVersioned` call over the `MemoryStoreService` gRPC RPC with `Attribution.Writer` unset or set to `"user"` on an instruction-shaped `user/`-scoped value is rejected with `ErrInstructionMemory`, identically to a model-authored in-process call.
  - verify: `TestADR_0226_WireAttributionCannotBypassInjectionScan`
- AC1.2: A legacy `RememberEntry` call over the same gRPC RPC on an instruction-shaped `user/`-scoped value is likewise rejected, closing the pre-existing zero-attribution gap on that handler.
  - verify: `TestADR_0226_LegacyRememberEntryWireCannotBypassInjectionScan`
- AC1.3: An in-process call with genuine `MemoryAttribution.Writer == MemoryWriterUser` on an instruction-shaped `user/` value is still accepted — the legitimate human-preference exemption is unchanged.
  - verify: `TestADR_0226_InProcessAttributionExemptionPreserved`
- AC1.4: A `RememberVersioned`/`RememberEntry` call over the gRPC RPC with a genuine, non-instruction-shaped value persists the revision's `Writer`/`Origin` exactly as the request's `Attribution` reported — the scan-classification override does not corrupt persisted provenance for an ordinary write.
  - verify: `TestADR_0226_WireProvenancePreservedUnderScanOverride`
- AC1.5: No code path other than the scan-classification input reads `MemoryAttribution.Writer`/`Origin` for a security or authorization decision.
  - verify: inspection — a repo-wide grep for comparisons against `.Writer`/`.Origin` confirms the single classification site; not independently unit-testable as one assertion.
