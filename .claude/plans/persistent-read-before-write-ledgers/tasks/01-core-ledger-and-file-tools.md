---
id: 01-core-ledger-and-file-tools
title: Storage-independent ledger contract and fail-closed file tools
blocked_by: []
status: done
branch: "plan-persistent-read-before-write-ledgers/01-core-ledger-and-file-tools"
worktree: ""
issue: "888"
retries: 0
last_error: ""
accumulator: acc/persistent-read-ledgers
---

# Task brief

Refactor the read-before-write ledger into an error-bearing, context-aware capability in `engine/tool`, with a fresh in-memory default that can be selected independently from file-content storage. Add the reusable ledger conformance suite and migrate every Workspace implementation/fake required to keep both Go modules compiling. Drive Read/Edit/Write through the revised seam and preserve exact opaque versions, I/O-free lexical keying, absent-versus-failure classification, stale-read refusal, create-only writes, final conditional CAS, and honest post-mutation record failures. Update the intentional engine API baseline and `engine/CHANGELOG.md`; do not edit the acceptance plan, ADR, living architecture docs, or generated `llms.txt`.

## Acceptance criteria

- AC1.1: Recording a read stores the exact opaque `FileVersion` supplied by the corresponding version-bearing read, and lookup returns that same valid token without reading file contents, preserving [ADR-0208](../adr/0208-execution-environment.md)'s no-I/O evidence contract.
  - verify: `TestInvariant_persistent_read_ledger_exact_version`
- AC1.2: Relative and ordinary in-root absolute spellings converge through the existing I/O-free lexical key rule; physical aliases may conservatively miss without filesystem inspection.
  - verify: `TestInvariant_persistent_read_ledger_lexical_key`
- AC1.3: Two Workspaces over one file-content backend can select different ledger instances, and a record in one session is absent from the other.
  - verify: `TestPersistentReadLedgers_Scenario1_IndependentSessionLedgers`
- AC1.4: With no durable ledger selected, the standard composition uses a fresh in-memory ledger and retains the existing per-live-Workspace reset behaviour.
  - verify: `TestPersistentReadLedgers_Scenario1_DefaultMemoryLifecycle`
- AC3.1: If recording a successful Read fails, the tool reports that the read evidence was not retained and a later Edit or existing-file Write is refused until a Read is recorded successfully.
  - verify: `TestPersistentReadLedgers_Scenario3_ReadRecordFailureFailsClosed`
- AC3.2: An unavailable or corrupt ledger lookup refuses Edit and existing-file Write before `ReplaceFile` is called; it is never treated as an unrecorded-but-otherwise-authorized read.
  - verify: `TestPersistentReadLedgers_Scenario3_LookupFailurePreventsMutation`
- AC3.3: An absent ledger entry preserves the existing read-before-edit/read-before-overwrite refusal, while a stale recorded version preserves the changed-since-read refusal.
  - verify: `TestInvariant_read_before_edit`
- AC3.4: A mutation between the tool's current `ReadVersion` and final `ReplaceFile` remains model-visible and never overwrites the concurrent bytes, regardless of ledger backend.
  - verify: `TestEditConditionalReplaceRejectsConcurrentChange`, `TestWriteConditionalReplaceRejectsConcurrentChange`
- AC3.5: New-file Write remains create-only and does not require a ledger entry; concurrent creators still produce exactly one winner.
  - verify: `TestPersistentReadLedgers_Scenario3_CreateOnlyUnchanged`
- AC3.6: If persisting the new version after a successful create or replace fails, the tool reports both facts without rollback, and the next existing-file mutation is refused until another successful Read records evidence.
  - verify: `TestPersistentReadLedgers_Scenario3_PostMutationRecordFailure`
- AC3.7: Existing osfs, memfs, ACP, no-fs, remoteenv, and file-tool conformance/regression suites remain green under the revised ledger seam.
  - verify: inspection — each adapter's existing conformance entry point runs under `task test`; the aggregate gate, rather than a fabricated cross-module test name, proves this matrix
- AC3.8: ACP ledger record/lookup remains independently synchronized from its file RPC path, so a parked file RPC does not block an otherwise local in-memory ledger operation.
  - verify: `TestFSWorkspaceLedgerNotBlockedByParkedRPC`
