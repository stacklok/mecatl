---
id: 02-automatic-materialization-admission
title: Streaming automatic admission over selected evidence
blocked_by: [01-learning-materializer]
status: in-progress
attempt: 1
branch: plan-scalable-reflection-evidence/02-automatic-materialization-admission-attempt-1
worktree: .scratch/worker-scalable-reflection-evidence-02-automatic-materialization-admission-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Task brief

Route automatic reflection through a full-source streaming admission scan followed by the bounded materializer from task 01. Preserve the current-span verification and all automatic policy controls, but remove raw retained size as an independent rejection condition. Materialize only after admission succeeds, then pass the bounded selected input to existing coordinator paths. Do not alter explicit reflection, coordinator identity, proposal persistence, transports, API snapshots, or documentation.

**Likely scope:** `internal/app/learning_controller.go`, automatic observer/controller wiring, and focused app/controller tests using offline sources.

**Invariants:** admission retains only bounded counters, coordinates, digests, and ranking state; automatic no-work exits allocate no coordinator state; selected input retains all existing hard limits; cancellation before admission never detaches work; existing cooldown, completed-cache, sensitivity, and reservation order remain authoritative.

## Acceptance criteria

- AC1.1: Automatic admission evaluates the full eligible source and verified current span incrementally, retaining only bounded counters/coordinates/digests/ranking state and never constructing or copying an unbounded `learning.Input`; an otherwise eligible trajectory over 256 KiB then reaches exactly one reflector call over bounded selected evidence.
  - verify: `TestScalableReflectionEvidence_Scenario1_AutomaticFullSourceAdmissionThenBoundedSelection`
- AC1.4: Only after automatic admission succeeds is a bounded selected `learning.Input` created; existing `MaxInputMessages`, `MaxInputEvents`, candidate/evidence limits, encoded coordinator bytes, and provider request/token limits remain hard bounds on it.
  - verify: `TestADR_0298_AdmissionPrecedesBoundedInputConstruction`
- AC3.2: Automatic materialization includes the complete verified current span in canonical projected form plus every connected paired-tool closure before spending remaining capacity.
  - verify: `TestScalableReflectionEvidence_Scenario3_AutomaticCurrentSpanClosureIsMandatory`
- AC5.1: Automatic no-evidence or unfit mandatory closure returns `skipped` with the applicable closed reason and creates no receipt, queue/singleflight entry, reservation, provider call, repository open/stage, proposal, or promotion.
  - verify: `TestScalableReflectionEvidence_Scenario5_AutomaticSkipLeavesNoAdmissionState`
- AC5.4: Increasing only excluded/unselected retained bytes cannot turn a normally eligible automatic selection into an oversize failure.
  - verify: `TestADR_0298_NoAutomaticRawSizeRejectionCompatibility`
- AC7.2: Automatic run cancellation before admission terminates the caller-run scan without a detached goroutine or published reflection job; detachment may occur only after successful bounded materialization and coordinator admission.
  - verify: `TestADR_0298_AutomaticCancellationStopsPreAdmissionMaterialization`
- AC8.3: Automatic policy admission, sensitivity, current-span verification, cooldown, completed cache, process/principal budgets, and reservation ordering remain enforced; an admitted selected input consumes reservation on timeout/failure/reflector abstention as before, while pre-selection skip consumes none.
  - verify: `TestADR_0298_AutomaticAdmissionCooldownBudgetReservationUnchanged`
