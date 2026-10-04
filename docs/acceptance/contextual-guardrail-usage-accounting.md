# Contextual guardrail usage accounting — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds a pre-v1 exported reviewer result contract and settles durable session ownership and ordered persistence of guardrail-model usage across concurrent dispatch.
**Decision record:** [ADR 0371](../adr/0371-contextual-guardrail-usage-accounting.md)
**Phase:** contextual-review usage accounting
**Status:** landed in this implementation candidate, 2026-09-25. The operator explicitly authorized this delta contract and implementation in the existing implementation PR; authoritative on merge.
**Delivery:** Split. This delta is stacked in the existing implementation PR under explicit operator authorization; normal human merge and verification gates remain required.
**Expected tasks:** 2
**Issue:** [stacklok/mecatl#1216](https://github.com/stacklok/mecatl/issues/1216).

This delta completes accounting for contextual action, inbound-result, and substitution-floor permission reviews while preserving the returned-usage contracts for direct auxiliary helpers. It narrows the request-scoped usage reporter to the main-session path-escape permission evaluation; generic `HookRunner` calls no longer install one. Each contextual review returns all provider-reported usage, attributed to the session whose action or result was reviewed. The dispatcher records completed reviews in its existing ordered drain, so parallel review workers never mutate a durable session directly.

The main-session-only path-escape pre-check returns the same usage result through its narrow adapter. It remains permission-bound, deny-dominant, and does not add a path-scoped rule or checker route. A future `/usage` surface and its transient pending projection are tracked separately in [#1945](https://github.com/stacklok/mecatl/issues/1945).

## Human decisions

- [x] Contextual review ownership is the reviewed session, including a worker session, rather than its delegation root or parent. — Decision: a session-tree UI can aggregate child ledgers without relocating the real source cost.
- [x] Contextual review usage is staged in private assessment records and merged by the dispatcher during its ordered drain. — Decision: preserve no-session-mutation parallel-review invariants; cancellation joins completed reviews and drains their reported usage while ownership remains valid.
- [x] `agent.ToolReviewer.Review` is an intentional pre-v1 breaking change rather than an additive compatibility interface. — Decision: return `(ToolReviewResult, session.AuxiliaryUsage, error)`; custom non-model reviewers return zero usage.
- [x] Path-escape checking returns the permission decision and usage together. — Decision: `port.PermissionPolicy.Evaluate` returns `port.PermissionResult{Decision governance.PermissionDecision, Usage session.AuxiliaryUsage}`. The main-session path-escape pre-check supplies its checker usage in that result on every evaluation, including errors and post-wait re-evaluation; the Engine records it only while the owning run remains active.
- [x] Live pending usage is deferred. — Decision: durable accounting is deterministic; [#1945](https://github.com/stacklok/mecatl/issues/1945) owns a later run-local pending projection for `/usage`.
- [x] Lease loss closes the local mutation path and stops live work. — Decision: on definitive loss, the service verifies its held lease, invalidates the local mutation capability, records a local-loss tombstone, retracts any durable awaiting ask, and cancels live work. A durably parked authorization remains available for successor handoff; `Run.Cancel()` keeps its normal join-and-drain behavior and is a no-op for that parked handoff.

## Interface contract

- **gRPC / protobuf:** None — existing session and session-summary `token_usage` projections expose the newly recorded `guardrail` totals without wire changes.
- **Exported Go APIs / interfaces:** Break `agent.ToolReviewer.Review(context.Context, ToolReviewRequest, ReviewEvidenceSource)` to return `(ToolReviewResult, session.AuxiliaryUsage, error)`. Break `port.PermissionPolicy.Evaluate` to return `port.PermissionResult{Decision governance.PermissionDecision, Usage session.AuxiliaryUsage}` so its main-session path-escape pre-check returns checker usage with every evaluation. `session.AuxiliaryUsage` remains a returned-result wrapper, distinct from the durable ledger map. The Engine remaps reviewer and permission-evaluation buckets to `session.UsageKindGuardrail` while preserving valid provider/model attribution. Custom reviewers that perform no model work return zero `session.AuxiliaryUsage`. `(*agent.Run).Cancel()` remains a no-argument cancellation request.
- **Tool schemas:** None — no model-visible tool schema changes; the contextual reviewer’s internal evidence and submit tools remain unchanged.
- **CLI / config:** None — existing `models.slots.guardrail` continues to select the checker route without a new setting.
- **Events / persistence:** The dispatcher records a completed contextual review’s usage exactly once in the reviewed session’s existing `token_usage[guardrail]` bucket before its final ordered action/result resolution. External-authorization parking drains and durably saves pending auxiliary usage before publishing the authorization-pending event. Completion-observer reflection usage is discarded after a bounded structured diagnostic; no post-observer save is performed solely for it. No event, pending-usage record, or per-attempt ledger is persisted. A completed retry aggregates every physical attempt’s reported partial usage once. The main-session path-escape pre-check returns usage in each `PermissionPolicy.Evaluate` result; the Engine records that result for every evaluation while the run remains active, including error and post-wait re-evaluation.
- **Security / authority:** A reviewer receives no session mutation capability. Parallel workers retain private usage records until dispatcher drain. On definitive lease loss, the service verifies the held lease under its lock, invalidates the local mutation capability, records the local-loss tombstone, then retracts any durable awaiting ask and cancels live work. A durably parked authorization remains the handoff point for a successor; `Run.Cancel()` is intentionally a no-op for that completed parked run. Explicit reflection retains its original authorized source-session load but has no accounting-only lock, reload, lease-linked context, or save, regardless of ownership ([ADR 0373](../adr/0373-reflection-usage-without-session-writes.md)). A backend save admitted before invalidation may finish; this process-local gate is not backend lease-token fencing. No accounting path reacquires a lease, reloads, replays, or starts persistence after ownership loss. Provider/model identity comes only from server-selected guardrail composition. Accounting stores no prompt, output, evidence, raw error, credential, or client billing input.
- **Compatibility / migration:** Intentional pre-v1 breaking engine API changes: custom reviewers adopt the returned usage tuple, and `PermissionPolicy.Evaluate` returns `port.PermissionResult` rather than a bare decision. `Run.Cancel()` retains its no-argument call shape. Regenerate `engine/api/*.txt` and add Changed (breaking, pre-v1 minor) changelog entries. There is no old/new reviewer or permission-result bridge.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — Contextual reviews return child-owned guardrail usage

Every direct contextual review has a real reviewed session. The checker’s temporary utility session is not a durable owner; its reported model usage belongs to the reviewed main or worker session under `guardrail`, preserving the guardrail slot’s selected provider/model. This follows the existing session-owned ledger and review-request binding in [ADR 0363](../adr/0363-contextual-investigative-guardrails.md).

**Acceptance:**
- AC1.1: Action, inbound-result, and substitution-floor permission reviews return and record all reported checker usage in the reviewed session’s `guardrail` bucket, with the actual guardrail provider/model, while leaving main usage and router budgeting unchanged.
  - verify: `TestContextualGuardrailUsage_Scenario1_ReviewsRecordOnReviewedSession`
- AC1.2: A failed or retried contextual-review attempt contributes each provider-reported partial usage exactly once; approval, release-once, cancellation, and a non-model reviewer never fabricate or repeat usage.
  - verify: `TestContextualGuardrailUsage_Scenario1_RetryAndTerminalUsageCountedOnce`
- AC1.3: Real composed worker engines retain applicable contextual review coverage; their returned checker usage is recorded on that worker session and never redirected to the delegation root.
  - verify: `TestContextualGuardrailUsage_Scenario1_ComposedWorkerRecordsOwnUsage`

### Scenario 2 — Ordered drain and path escape preserve ownership fences

The contextual-review dispatcher already serializes final resolution after parallel assessments. Usage follows that same record, rather than giving worker goroutines a durable mutation capability. The independent path-escape pre-check remains on its established permission seam from [ADR 0080](../adr/0080-guardrail-routed-escape-checking.md).

**Acceptance:**
- AC2.1: Parallel action and inbound reviews retain private usage until the dispatcher drains records in original order, then record each completed review once. Cancellation joins and drains completed parallel records and a completed serial assessment before closing an otherwise valid owner.
  - verify: `TestContextualGuardrailUsage_Scenario2_OrderedDrainRecordsCompletedReviews`, `TestContextualGuardrailUsage_Scenario2_CancellationDrainsCompletedUsage`, `TestContextualGuardrailUsage_Scenario2_SerialCancellationDrainsCompletedUsage`
- AC2.2: Requested cancellation joins and drains completed usage while the run remains active. On definitive lease loss, the service invalidates local mutation capability, records its local-loss tombstone, retracts any durable awaiting ask, and cancels live work; a durably parked authorization remains available for successor handoff. No result from the lost owner's in-memory session is persisted solely for accounting. Completed results returned after run closure are dropped with bounded diagnostics. External-authorization parking drains and saves valid non-reflection usage before publishing its pending event; explicit reflection neither locks nor reloads/saves the source session solely to account for returned usage, even with a current owner.
  - verify: `TestContextualGuardrailUsage_Scenario2_LateUsageIsDropped`, `TestLeaseRenewLossCancelsRun`, `TestMutationCapabilityInvalidateWaitsForAdmittedMutation`, `TestExternalAuthorizationParkPersistsDrainedAuxiliaryUsage`, `TestReflectSessionOwnershipLossCannotOverwriteSuccessor`
- AC2.3: The main-session path-escape pre-check returns usage in `port.PermissionResult` and records each evaluation’s reported checker usage once, including partial usage from a failed or retried checker call and a post-wait re-evaluation. Child engines never arm the route.
  - verify: `TestContextualGuardrailUsage_Scenario2_PathEscapeUsageProductionFlow`, `TestContextualGuardrailUsage_Scenario2_PathEscapeFailureAndChildIsolation`, `TestContextualGuardrailUsage_Scenario2_PathEscapeRetryAndRepeatedChecksPersist`, `TestContextualGuardrailUsage_Scenario2_PathEscapeTerminalFailurePersistsPartial`, `TestContextualGuardrailUsage_Scenario2_PathEscapeReevaluationAfterAwaiting`, `TestContextualGuardrailUsage_Scenario2_ComposedChildNeverArmsPathEscape`

## Out of scope

| Item | Defer-to | Decision |
| --- | --- | --- |
| `/usage` UI and live pending projection | [#1945](https://github.com/stacklok/mecatl/issues/1945) | Pending usage is transient UI state, not canonical accounting. |
| Price, billing reconciliation, and historical backfill | Later accounting work | Provider-reported token usage remains the sole unit. |
| Cross-process recovery of an undrained review | Later lifecycle decision | Ownership loss drops usage rather than fabricating durability. |

## Definition of done

1. Focused `engine/agent` and `internal/app` tests, `task api:check`, `task lint`, `task test`, `task test:race`, and `task docs` pass on the final candidate.
2. `task ac-trace-strict` resolves every named proof when this plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. `engine/api/*.txt` and `engine/CHANGELOG.md` document the reviewer signature change.
5. The implementation PR reports this delta contract and interface conformance.
6. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- The canonical ledger updates at ordered dispatcher drain, not as a live per-token stream; a future UI can overlay explicitly transient pending usage.
- Reported provider usage remains best effort: a process crash or ownership loss can drop post-call accounting rather than justify replay.
- `SessionMutationCapability` is a process-local admission fence, not a backend fencing token: aggregate mutation is synchronized with invalidation, while a backend save admitted before invalidation may finish.
