---
id: 05-driver-wire-hygiene
title: Make lifecycle driver errors uniform and Inspect bounded
blocked_by: [03-truncation-visibility]
status: done
branch: "plan-memory-lifecycle-hardening/05-driver-wire-hygiene"
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/memory-lifecycle-hardening
---

# Task brief

Make every lifecycle client method map driver `UNIMPLEMENTED` consistently, including the Inspect call reached during version-conflict handling. Add the additive `InspectMemoryRequest.max_revisions` field and validation bounds for version tokens, then make the local memory driver honor the bound with the most-recent N revisions. Regenerate protobuf contracts in this branch.

## Acceptance criteria

- AC6.1: All four `grpcdriver.MemoryLifecycleStore` client methods (`RememberVersioned`, `Inspect`, `ForgetVersioned`, `UndoLatest`) map a driver's `UNIMPLEMENTED` response identically.
  - verify: `TestADR_0226_UnimplementedMappedUniformlyAcrossLifecycleMethods`
- AC6.2: A version conflict against a driver missing `InspectMemory` surfaces a clean `ErrMemoryLifecycleUnsupported`-shaped error, not a raw `Unimplemented` status.
  - verify: `TestADR_0226_VersionConflictHandlesMissingInspect`
- AC6.3: `InspectMemoryRequest.max_revisions`, when set, bounds the local reference driver's returned revision count to the most recent N.
  - verify: `TestADR_0226_InspectHonorsMaxRevisions`
- AC6.4: `expected_version` and `version` carry the same `buf.validate` size-bound discipline as `entry`.
  - verify: inspection — proto/buf-lint review
