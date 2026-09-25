# Contextual guardrail usage accounting — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds a pre-v1 exported reviewer result contract and settles durable session ownership and ordered persistence of guardrail-model usage across concurrent dispatch.
**Decision record:** [ADR 0367](../adr/0367-contextual-guardrail-usage-accounting.md)
**Phase:** contextual-review usage accounting
**Status:** landed in this implementation candidate, 2026-09-25. The operator explicitly authorized this delta contract and implementation in the existing implementation PR; authoritative on merge.
**Delivery:** Split. This delta is stacked in the existing implementation PR under explicit operator authorization; normal human merge and verification gates remain required.
**Expected tasks:** 2
**Issue:** [stacklok/mecatl#1216](https://github.com/stacklok/mecatl/issues/1216).

This delta completes accounting for contextual action, inbound-result, and substitution-floor permission reviews without changing the completed auxiliary-usage or contextual-guardrails contracts. Each review returns all provider-reported usage, attributed to the session whose action or result was reviewed. The dispatcher records completed reviews in its existing ordered drain, so parallel review workers never mutate a durable session directly.

The main-session-only path-escape pre-check returns the same usage result through its narrow adapter. It remains permission-bound, deny-dominant, and does not add a path-scoped rule or checker route. A future `/usage` surface and its transient pending projection are tracked separately in [#1945](https://github.com/stacklok/mecatl/issues/1945).

## Human decisions

- [x] Contextual review ownership is the reviewed session, including a worker session, rather than its delegation root or parent. — Decision: a session-tree UI can aggregate child ledgers without relocating the real source cost.
- [x] Contextual review usage is staged in private assessment records and merged by the dispatcher during its ordered drain. — Decision: preserve no-session-mutation parallel-review invariants; cancellation joins completed reviews and drains their reported usage while ownership remains valid.
- [x] `agent.ToolReviewer.Review` is an intentional pre-v1 breaking change rather than an additive compatibility interface. — Decision: return `(ToolReviewResult, session.AuxiliaryUsage, error)`; custom non-model reviewers return zero usage.
- [x] Path-escape checking uses the same returned-result discipline through `modelhook.CheckResult{Verdict, Usage}`. — Decision: the route remains a main-session permission pre-check and reports only through the synchronous request-scoped owner callback.
- [x] Live pending usage is deferred. — Decision: durable accounting is deterministic; [#1945](https://github.com/stacklok/mecatl/issues/1945) owns a later run-local pending projection for `/usage`.

## Interface contract

- **gRPC / protobuf:** None — existing session and session-summary `token_usage` projections expose the newly recorded `guardrail` totals without wire changes.
- **Exported Go APIs / interfaces:** Break `agent.ToolReviewer.Review(context.Context, ToolReviewRequest, ReviewEvidenceSource)` to return `(ToolReviewResult, session.AuxiliaryUsage, error)`. The Engine remaps its returned buckets to `session.UsageKindGuardrail` while preserving valid provider/model attribution. Custom reviewers that perform no model work return zero `session.AuxiliaryUsage`.
- **Tool schemas:** None — no model-visible tool schema changes; the contextual reviewer’s internal evidence and submit tools remain unchanged.
- **CLI / config:** None — existing `models.slots.guardrail` continues to select the checker route without a new setting.
- **Events / persistence:** The dispatcher records a completed contextual review’s usage exactly once in the reviewed session’s existing `token_usage[guardrail]` bucket before its final ordered action/result resolution. No event, pending-usage record, or per-attempt ledger is persisted. A completed retry aggregates every physical attempt’s reported partial usage once. Path-escape usage is synchronously reported through the existing request-scoped callback only while the main-session owner is active.
- **Security / authority:** A reviewer receives no session mutation capability. Parallel workers retain private usage records until dispatcher drain; no accounting path reacquires a lease, reloads, replays, or persists a session after ownership loss. Provider/model identity comes only from server-selected guardrail composition. Accounting stores no prompt, output, evidence, raw error, credential, or client billing input.
- **Compatibility / migration:** Intentional pre-v1 breaking engine API change. Regenerate `engine/api/*.txt` and add a Changed (breaking, pre-v1 minor) `engine/CHANGELOG.md` entry. There is no old/new reviewer bridge or migration; existing custom reviewers must adopt the new return tuple.

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
  - verify: `TestContextualGuardrailUsage_Scenario2_OrderedDrainRecordsCompletedReviews`, `TestContextualGuardrailUsage_Scenario2_CancellationDrainsCompletedUsage`
- AC2.2: A review result arriving after its run/session ownership ends is dropped with bounded diagnostics and never reloads, reacquires a lease, replays, or persists solely for accounting.
  - verify: `TestContextualGuardrailUsage_Scenario2_LateUsageIsDropped`
- AC2.3: The main-session path-escape pre-check returns `modelhook.CheckResult` and records reported checker usage once through the request-scoped reporter, including partial usage returned with a failed or retried checker call; repeat permission evaluation counts each physical check once. Child engines never arm or report through the route.
  - verify: `TestContextualGuardrailUsage_Scenario2_PathEscapeUsage`, `TestContextualGuardrailUsage_Scenario2_PathEscapeFailureAndChildIsolation`

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
