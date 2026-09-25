# ADR 0362 — Resolve plan asks by exact run and ask

- Status: Proposed
- Date: 2026-09-24
- Scope: the plan approval gate, daemon control API, TypeScript SDK, and Studio's plan-review verdicts
- Supersedes: ADR 0347 Decision 8 only for its exclusion of plan-originated asks from run-addressed controls

## Context

The `PresentPlan` gate surfaces a `permission.ask` with a durable run ID and ask ID. Ordinary asks already have an acknowledged `Session.controls(runId).resolveAsk(askId, verdict)` path. Plan asks instead use an owned `Run` responder or `Session.resolvePlan(verdict)`. The latter reaches `ApprovePlan` with only a session ID and chooses the currently parked plan ask. A browser may retain an older plan card while a newer ask appears, and Studio can run on a different BFF replica from the one that streamed the original run. A session-only plan verdict therefore cannot prove the operator approved the plan they saw.

The existing `ApprovePlan` stream also rejects a live run; Studio's ordinary run stream can still be live when `PresentPlan` asks. Reusing that API for a live card would either fail or require process-local callback state. [ADR 0351](./0351-mecatl-studio-in-repo-web-ui.md) keeps Studio behind a stateless BFF that consumes a published SDK. [ADR 0347](./0347-run-id-addressed-prompt-free-controls.md) already establishes exact, acknowledged controls and a server-owned relay after a restored ordinary ask.

ADR 0069's live `Converse` approval leaves the proceed prompt to its stream owner. Mecatui and the SDK's `query()` send that prompt when they observe `plan_approved`. A separate client can know the same run and ask IDs, so exact addressing alone cannot decide who starts execution. Starting a server continuation for every strict live verdict would race those clients and could make either execution or its event stream disappear from the approving client.

## Decision

1. Add a dedicated, acknowledged `ResolvePlanAsk` control for both a live plan ask and a persisted awaiting plan ask. Every request must name the authorized session, expected durable run ID, exact ask ID, and one explicit verdict. Validate run identity, plan origin, pending state, cancellation state, and ownership atomically before applying the verdict. A mismatch or duplicate never falls forward to the current ask. Keep ordinary `ResolveRunAsk` rejecting plan asks.
2. Make continuation ownership explicit at run start. An optional `server_owned_plan_continuation` flag on a normal prompt defaults to false and is carried in the HTTP prompt mirror and published SDK run options. An opted-in live run permits plan verdicts only through `ResolvePlanAsk`; a legacy `Converse` plan-approval frame is rejected without consuming that ask. A strict live verdict on a run that did not opt in is rejected before consumption. Studio opts in and never submits its own proceed prompt. Unchanged mecatui and SDK `query()` runs remain client-owned and keep their ADR 0069 choreography. A persisted awaiting plan ask has no live stream owner; its exact control uses the server-owned continuation regardless of the old run-start flag. The flag is run-local and does not change session snapshots or permission policy.
3. Preserve the three plan meanings: allow once approves and changes to default mode, allow always approves and changes to accept edits, and deny requests iteration in plan mode. Reserve server-owned continuation capacity before accepting an allow. After the approved plan run reaches `StopPlanApproved`, its reserved continuation has priority over a competing prompt or scheduler start; it starts at most one fresh execution run. Release that reservation exactly once on every terminal or removal path. On deny, start none. The acknowledged control is independent of the HTTP response lifetime.
4. Append and publish a session-scoped `plan.continuation_failed` event when an accepted allow reaches `StopPlanApproved` but its server-owned proceed run cannot start while this daemon still holds the session lease. Its structured payload carries only the approved plan run ID and ask ID; it carries no raw error, prompt, tool arguments, URL, header, or credential. The original plan run retains its one `result` terminal; a failed start does not invent a second terminal or a run that never began. Existing activity reports the approved terminal, a subsequent execution run if one starts, or this known failure. A process or session lease loss before the failure can be recorded remains uncertain, never completed execution. After lease loss, the former owner neither appends nor publishes an unrecorded failure.
5. Add an injective HTTP mirror and a distinct open server feature identifier `exact_plan_ask_control`, advertised only when strict verdicts, run-start opt-in, and failure projection are implemented on both transports. Add `RunControls.resolvePlanAsk` to the TypeScript SDK, with exact correlation, feature gating, typed errors, and ambiguous-ack reconciliation like ordinary run controls. Studio uses it only after that SDK version is published. `Session.resolvePlan()` and the legacy session-only `ApprovePlan` remain for existing clients, but Studio never falls back to them. Browser verdicts stay inside Studio's authenticated same-origin BFF. External MCP authorization remains a separate session-and-authorization-ID lifecycle.

## Consequences

- A stale card, repeated click, second tab, or lost response cannot silently approve a newer plan. After an ambiguous response the browser must refresh authoritative activity before offering another action.
- The daemon must handle two run lifecycles and own the continuation after acknowledging an opted-in live or restored ask. A non-opted live ask remains with its stream owner; a second client cannot force a competing continuation through the strict control. A process failure or session lease loss between accepted approval and continuation may leave an uncertain outcome that the UI must show honestly.
- The public protobuf, HTTP, engine, event, and SDK surfaces grow additively. Studio waits for the released SDK under ADR 0351. Existing clients retain their session-only or owned-stream plan path for compatibility; this decision does not claim to remove it.

## See also

- [Acceptance plan: Studio chat approvals, review, and Escape](../acceptance/studio-chat-approvals-review-escape.md)
- [Agent loop and plan gate](../architecture/agent-loop.md)
- [ADR 0069 — Plan approval gate](./0069-plan-approval-gate.md)
- [ADR 0347 — Run-ID-addressed prompt-free controls](./0347-run-id-addressed-prompt-free-controls.md)
- [ADR 0351 — Mecatl Studio](./0351-mecatl-studio-in-repo-web-ui.md)
