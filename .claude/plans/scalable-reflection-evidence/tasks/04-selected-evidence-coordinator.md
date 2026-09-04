---
id: 04-selected-evidence-coordinator
title: Selected-evidence coordinator identity and limits
blocked_by: [02-automatic-materialization-admission, 03-explicit-materialization-lifecycle]
status: pending
attempt: 0
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Task brief

Switch coordinator deduplication, singleflight, deterministic proposal identity inputs, and resource accounting from raw/input-local trajectory identity to the task-01 selected-evidence identity. Keep one selected input per reflector call and preserve all existing coordinator queue, receipt, fairness, timeout, cancellation, and close controls around that bounded job. Do not change materializer protocol/projection, proposal manifest storage/detail/approval, transport/UI mapping, API snapshots, or documentation.

**Likely scope:** learning coordinator and proposal-ID paths in `internal/app`, with focused coordinator tests and offline reflector fakes.

**Invariants:** selected-evidence identity is exactly protocol, source identity boundary, and selected aggregate digest; invocation mode, host signals, and comparison-only facts do not perturb it; principal/project partition and candidate identity remain proposal-ID inputs; no chunking, multipass reflection, or candidate merge.

## Acceptance criteria

- AC2.3: Automatic and explicit attempts over identical selected source evidence share identity `(protocol, identity boundary, selected digest)` and singleflight, even when invocation mode, host signals, or existing-memory comparison context differ; changing selected evidence or its identity boundary cannot alias.
  - verify: `TestScalableReflectionEvidence_Scenario2_SelectedEvidenceIdentityBoundary`
- AC2.4: Deterministic proposal IDs incorporate selected-evidence identity plus partition and candidate identity; retry converges, while host signals, invocation mode, and comparison-only existing facts do not perturb selected-evidence identity.
  - verify: `TestScalableReflectionEvidence_Scenario2_SelectedDigestDrivesProposalID`
- AC2.5: One materialized selected input performs at most one provider call; no chunking, multi-pass extraction, or candidate merge is introduced.
  - verify: `TestADR_0298_OneSelectedInputOneProviderCall`
- AC8.4: Coordinator global/per-principal count limits, selected-job and aggregate queued-byte limits, fair FIFO rotation, receipt capacity, timeout, cancellation, singleflight, and close behavior remain enforced around the selected-evidence job.
  - verify: `TestADR_0298_CoordinatorResourceSafetyUnchanged`
