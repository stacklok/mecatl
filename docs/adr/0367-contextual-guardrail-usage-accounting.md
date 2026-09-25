# ADR 0367 — Contextual guardrail usage ownership and ordered drain

- Status: Proposed
- Date: 2026-09-25
- Scope: contextual-review result contract and session-owned auxiliary usage
- Supersedes: ADR 0354 decisions 2 and 4 for contextual `ToolReviewer` and path-escape usage only
- Superseded by: none

## Context

The completed auxiliary-usage work records usage from direct helpers and former hook paths, but contextual action, inbound, and permission reviewers run a temporary checker engine and currently discard its token usage. Their parallel execution must not gain a session mutation path: contextual dispatch keeps private assessment records and resolves them in dispatcher order.

A guardrail review can occur in a worker session. Charging its delegation root hides the actual session that caused the checker call and prevents a future session-tree usage view from accurately displaying child costs. The path-escape route remains a main-session permission pre-check, but its narrow `modelhook.VerdictChecker` result also needs to carry usage.

## Decision

1. Break pre-v1 `agent.ToolReviewer.Review` to return `(ToolReviewResult, session.AuxiliaryUsage, error)`. Every implementation returns a complete result for any model usage it performs; non-model implementations return zero usage. This intentionally has no compatibility bridge.
2. A contextual review’s durable accounting belongs to its reviewed session—the session named by the review request—not a parent or delegation-root session. The Engine remaps valid returned provider/model totals to `UsageKindGuardrail`; it never attributes them to `main` usage or a budget.
3. Review workers return usage only in their private assessment record. The dispatcher merges it exactly once during the existing ordered drain, including usage observed before a retryable or terminal attempt error. Cancellation joins completed assessments and drains their reported usage while the run still owns their reviewed sessions. A result after ownership closure is dropped with bounded diagnostics; accounting never causes a lease reacquisition, session load/reload, replay, or persistence attempt.
4. The main-session-only path-escape pre-check changes its internal adapter to `modelhook.CheckResult{Verdict, Usage}`. It forwards usage through the existing request-scoped synchronous reporter only while the Engine owns that permission evaluation. Each physical check is accounted once, including a required post-wait permission re-evaluation.
5. Exact live usage is not a reason to violate dispatcher ordering. A future `/usage` surface may combine durable ledgers with a separate thread-safe, run-local pending projection; it must label it transient and remove it when the matching drain persists or run cleanup discards it. That feature is deferred to [#1945](https://github.com/stacklok/mecatl/issues/1945).

## Consequences

Contextual usage has exact source-session attribution and deterministic durable mutation ordering. The exported engine contract changes again while pre-v1, requiring API snapshots and a Changed/minor changelog entry. Existing custom `ToolReviewer` implementations must adopt the new tuple.

The ledger may lag a completed checker attempt until dispatcher drain, and post-ownership usage can be lost. This is preferred to nondeterministic concurrent durable mutation or replay solely for accounting. No prompt, checker output, evidence, raw provider error, credential, or client billing parameter enters the ledger.

## See also

- [ADR 0354](./0354-returned-auxiliary-usage-results.md)
- [ADR 0363](./0363-contextual-investigative-guardrails.md)
- [ADR 0080](./0080-guardrail-routed-escape-checking.md)
- [Contextual guardrail usage accounting acceptance plan](../acceptance/contextual-guardrail-usage-accounting.md)
- [Issue #1216](https://github.com/stacklok/mecatl/issues/1216)
- [Issue #1945](https://github.com/stacklok/mecatl/issues/1945)
