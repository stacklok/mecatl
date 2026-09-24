# ADR 0362 — Resolve plan asks by exact run and ask

- Status: Proposed
- Date: 2026-09-24
- Scope: the plan approval gate, daemon control API, TypeScript SDK, and Studio's plan-review verdicts
- Supersedes: ADR 0347 Decision 8 only for its exclusion of plan-originated asks from run-addressed controls

## Context

The `PresentPlan` gate surfaces a `permission.ask` with a durable run ID and ask ID. Ordinary asks already have an acknowledged `Session.controls(runId).resolveAsk(askId, verdict)` path. Plan asks instead use an owned `Run` responder or `Session.resolvePlan(verdict)`. The latter reaches `ApprovePlan` with only a session ID and chooses the currently parked plan ask. A browser may retain an older plan card while a newer ask appears, and Studio can run on a different BFF replica from the one that streamed the original run. A session-only plan verdict therefore cannot prove the operator approved the plan they saw.

The existing `ApprovePlan` stream also rejects a live run; Studio's ordinary run stream can still be live when `PresentPlan` asks. Reusing that API for a live card would either fail or require process-local callback state. [ADR 0351](./0351-mecatl-studio-in-repo-web-ui.md) keeps Studio behind a stateless BFF that consumes a published SDK. [ADR 0347](./0347-run-id-addressed-prompt-free-controls.md) already establishes exact, acknowledged controls and a server-owned relay after a restored ordinary ask.

## Decision

1. Add a dedicated, acknowledged `ResolvePlanAsk` control for both a live plan ask and a persisted awaiting plan ask. Every request must name the authorized session, expected durable run ID, exact ask ID, and one explicit verdict. Validate run identity, plan origin, pending state, cancellation state, and ownership atomically before applying the verdict. A mismatch or duplicate never falls forward to the current ask. Keep ordinary `ResolveRunAsk` rejecting plan asks.
2. Preserve the three plan meanings: allow once approves and changes to default mode, allow always approves and changes to accept edits, and deny requests iteration in plan mode. Reserve server-owned continuation capacity before accepting an allow. The daemon starts the follow-on execution run after the approved plan run reaches `StopPlanApproved`; on deny it starts none. The acknowledged control is independent of the HTTP response lifetime. Existing durable events and session activity report the result and any continuation. A continuation failure is reported as failure or uncertainty, never as completed execution.
3. Add an injective HTTP mirror and a distinct open server feature identifier `exact_plan_ask_control`. Add `RunControls.resolvePlanAsk` to the TypeScript SDK, with the same exact correlation, feature gate, typed errors, and ambiguous-ack reconciliation discipline as the ordinary run controls. Studio uses it only after that SDK version is published. `Session.resolvePlan()` and the legacy session-only `ApprovePlan` remain for existing clients, but Studio never falls back to them.
4. Keep browser verdicts inside Studio's authenticated, same-origin BFF. Its product route includes session, run, and ask IDs; the BFF maps the three plan labels to SDK verdicts and validates the echoed acknowledgement. No browser code imports the SDK or daemon protocol. External MCP authorization remains a separate session-and-authorization-ID lifecycle; it is not a permission or plan verdict.

## Consequences

- A stale card, repeated click, second tab, or lost response cannot silently approve a newer plan. After an ambiguous response the browser must refresh authoritative activity before offering another action.
- The daemon must handle two run lifecycles and own the continuation after acknowledging a live or restored ask. This is more work than a Studio-only route, and a process failure between accepted approval and continuation may leave an uncertain outcome that the UI must show honestly.
- The public protobuf, HTTP, engine, and SDK surfaces grow additively. Studio waits for the released SDK under ADR 0351. Existing clients retain their session-only plan path for compatibility; this decision does not claim to remove it.

## See also

- [Acceptance plan: Studio chat approvals, review, and Escape](../acceptance/studio-chat-approvals-review-escape.md)
- [Agent loop and plan gate](../architecture/agent-loop.md)
- [ADR 0069 — Plan approval gate](./0069-plan-approval-gate.md)
- [ADR 0347 — Run-ID-addressed prompt-free controls](./0347-run-id-addressed-prompt-free-controls.md)
- [ADR 0351 — Mecatl Studio](./0351-mecatl-studio-in-repo-web-ui.md)
