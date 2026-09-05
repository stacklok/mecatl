---
id: 03-explicit-materialization-lifecycle
title: Explicit materialization outcomes and Build lifecycle
blocked_by: [01-learning-materializer]
status: done
attempt: 1
branch: plan-scalable-reflection-evidence/03-explicit-materialization-lifecycle-attempt-1
worktree: .scratch/worker-scalable-reflection-evidence-03-explicit-materialization-lifecycle-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/scalable-reflection-evidence
---

# Task brief

Integrate explicit reflection with the task-01 materializer and introduce the Build-owned synchronous pre-admission lifecycle gate. Explicit reflection must use the same selected evidence as automatic reflection without a raw-size rejection, preserve persisted provider/model selection and off-mode behavior, and return the closed abstention vocabulary before coordinator work. The lifecycle must cancel and join active caller-run scans during `Built.Close` without spawning a materialization goroutine per job. Do not change automatic admission policy, coordinator identity, proposal manifest persistence, client wire/UI surfaces, API snapshots, or documentation.

**Likely scope:** `internal/app/learning.go`, Build lifecycle ownership/close paths, explicit reflection construction, `internal/adapter/server/learning.go` error seams, and focused offline tests.

**Invariants:** cancellation and closure retain existing typed errors rather than becoming abstention; outward materialization text is stable, bounded, UTF-8-safe, and source-free; pre-admission failure creates no coordinator/repository/provider work; explicit reflection reuses the completed session's persisted provider/model and is available in learning mode off without enabling automatic infrastructure.

## Acceptance criteria

- AC1.2: Explicit reflection of the same completed session succeeds through the same selector and gives the reflector byte-identical selected evidence; it is not rejected for raw retained size.
  - verify: `TestScalableReflectionEvidence_Scenario1_ExplicitLargeTrajectoryMatchesAutomatic`
- AC5.2: Explicit no-safe evidence returns successful `abstained/no_eligible_evidence`; an applicable unfit mandatory closure returns `abstained/mandatory_span_exceeds_bounds`, with the same no-work guarantee.
  - verify: `TestScalableReflectionEvidence_Scenario5_ExplicitClosedAbstentionReasons`
- AC5.3: Clients receive only stable harness-authored text mapped from the closed vocabulary. Cancellation and close retain typed cancellation/closed errors rather than successful abstention; no arbitrary source/provider/repository error becomes a reason.
  - verify: `TestADR_0300_MaterializationDispositionReasonAndErrorMatrix`
- AC6.4: Provider, persistence, validation, queue, timeout, and source-mismatch faults retain their existing non-Internal typed mapping; diagnostics and errors remain bounded/content-free.
  - verify: `TestADR_0300_NonMaterializationFaultsRetainTypedMappings`
- AC7.1: Explicit cancellation during a blocked/large streaming scan returns the existing typed cancellation promptly and leaves no queue, receipt, singleflight, reservation, provider, repository-open, proposal, or promotion state.
  - verify: `TestScalableReflectionEvidence_Scenario7_ExplicitCancellationStopsMaterialization`
- AC7.3: `Built.Close` closes the materialization gate, rejects new scans, cancels and joins all active scans, then performs existing coordinator queued/running shutdown; it cannot return while materialization-owned work remains.
  - verify: `TestScalableReflectionEvidence_Scenario7_BuiltCloseCancelsAndJoinsMaterialization`
- AC7.4: Race/leak proofs show no per-job materialization goroutine, double receipt/proposal, post-close admission, leaked active operation, or provider call after cancellation/close.
  - verify: `TestADR_0300_MaterializationCancelCloseRaceAndNoPerJobGoroutine`
- AC8.1: Explicit reflection reloads the session's persisted provider/model after restart, keeps a configured reflection model on that provider, and never silently uses the process default; selection is provider-neutral.
  - verify: `TestScalableReflectionEvidence_Scenario8_ExplicitUsesPersistedProviderModel`
- AC8.2: In learning mode `off`, explicit reflection lazily materializes and then uses the existing synchronous coordinator/repository path, while no automatic observer, controller, worker, provider call, or eager proposal repository exists.
  - verify: `TestScalableReflectionEvidence_Scenario8_OffModeExplicitOnly`
