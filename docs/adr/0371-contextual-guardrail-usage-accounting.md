# ADR 0371 — Contextual guardrail usage ownership and ordered drain

- Status: Proposed
- Date: 2026-09-25
- Scope: contextual-review result contract and session-owned auxiliary usage
- Supersedes: ADR 0354 decision 2 for contextual `ToolReviewer` results and decision 4 for guardrail-hook reporting; path-escape usage is returned through `PermissionPolicy.Evaluate`, not a reporter callback
- Superseded by: ADR 0373 for decision 6's reflection accounting only

## Context

The completed auxiliary-usage work records usage from direct helpers and former hook paths, but contextual action, inbound, and permission reviewers run a temporary checker engine and currently discard its token usage. Their parallel execution must not gain a session mutation path: contextual dispatch keeps private assessment records and resolves them in dispatcher order.

A guardrail review can occur in a worker session. Charging its delegation root hides the actual session that caused the checker call and prevents a future session-tree usage view from accurately displaying child costs. The path-escape route remains a main-session permission pre-check, but its narrow `modelhook.VerdictChecker` result also needs to carry usage.

## Decision

1. Break pre-v1 `agent.ToolReviewer.Review` to return `(ToolReviewResult, session.AuxiliaryUsage, error)`. Every implementation returns a complete result for any model usage it performs; non-model implementations return zero usage. This intentionally has no compatibility bridge.
2. A contextual review’s durable accounting belongs to its reviewed session—the session named by the review request—not a parent or delegation-root session. The Engine remaps valid returned provider/model totals to `UsageKindGuardrail`; it never attributes them to `main` usage or a budget.
3. Review workers return usage only in their private assessment record. The dispatcher merges it exactly once during the existing ordered drain, including usage observed before a retryable or terminal attempt error. Cancellation joins completed assessments and drains their reported usage while the run still owns their reviewed sessions. A result after ownership closure is dropped with bounded diagnostics; accounting never causes a lease reacquisition, session load/reload, replay, or persistence attempt.
4. `port.PermissionPolicy.Evaluate` returns `port.PermissionResult{Decision governance.PermissionDecision, Usage session.AuxiliaryUsage}`. The main-session-only path-escape pre-check places its checker usage in that result for every permission evaluation, including failed, retried, and required post-wait re-evaluations. The Engine records it while the owning run remains active. Generic `HookRunner` requests do not receive an auxiliary-usage reporting seam: configured shell hooks are not model-backed guardrail checks, and the path-escape check runs at the permission seam. This supersedes ADR 0354 decision 4's reporter clause.
5. Exact live usage is not a reason to violate dispatcher ordering. A future `/usage` surface may combine durable ledgers with a separate thread-safe, run-local pending projection; it must label it transient and remove it when the matching drain persists or run cleanup discards it. That feature is deferred to [#1945](https://github.com/stacklok/mecatl/issues/1945).
6. On definitive session lease loss, the service verifies its held lease, invalidates its local mutation capability, records a local-loss tombstone, retracts any durable awaiting ask, and cancels live work. A durably parked external authorization remains available for successor handoff; `Run.Cancel()` is intentionally a no-op for that completed parked run. Normal cancellation retains join-and-drain behavior. External-authorization parking drains and durably saves auxiliary usage after child drain and before publishing the pending event. Completion-observer and explicit-reflection usage cross the existing capability and ownership checks. Explicit reflection synchronizes ownership admission and aggregate mutation, releases that lock, then uses the existing capability-guarded save.

## Consequences

Contextual usage has exact source-session attribution and deterministic durable mutation ordering. The exported engine contract changes again while pre-v1: custom reviewers return usage, and `PermissionPolicy.Evaluate` returns a `PermissionResult` that includes auxiliary usage. API snapshots and Changed (breaking, pre-v1 minor) changelog entries are required; there is no compatibility bridge.

The ledger may lag a completed checker attempt until dispatcher drain, and post-ownership usage can be lost. `SessionMutationCapability` is process-local admission, not backend lease-token fencing: invalidation cannot interleave with admitted aggregate mutation, while a backend save admitted before invalidation may finish. This is preferred to nondeterministic concurrent durable mutation or replay solely for accounting. No prompt, checker output, evidence, raw provider error, credential, or client billing parameter enters the ledger.

## See also

- [ADR 0354](./0354-returned-auxiliary-usage-results.md)
- [ADR 0363](./0363-contextual-investigative-guardrails.md)
- [ADR 0080](./0080-guardrail-routed-escape-checking.md)
- [Contextual guardrail usage accounting acceptance plan](../acceptance/contextual-guardrail-usage-accounting.md)
- [Issue #1216](https://github.com/stacklok/mecatl/issues/1216)
- [Issue #1945](https://github.com/stacklok/mecatl/issues/1945)
