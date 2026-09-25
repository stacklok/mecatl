# ADR 0347 — Run-ID-addressed prompt-free controls

- Status: Proposed
- Date: 2026-09-16
- Scope: `engine/agent.Run` ask resolution, `HarnessService`, the HTTP session-control routes, and the TypeScript SDK's session/run-control surface
- Supersedes: ADR 0288 Decision 6; ADR 0304 Decision 3 only for its teams-alone ergonomic-resource constraint
- Superseded by: ADR 0362 Decision 1 for Decision 8's exclusion of plan-originated asks from run-addressed controls

## Context

The TypeScript SDK can start and control an owned run through its `Converse`
stream, and it can durably observe a run through `session.attach()`. A client
that reloads, restarts, or hands work to another process loses the owned stream,
however. Knowing the durable session and run IDs is enough to watch the run but
not enough to resolve its permission ask, cancel it over gRPC, steer it, or
retract a pending steer through one high-level typed API.

The existing transports cannot safely be hidden behind the same client method.
gRPC has no prompt-free control RPC. HTTP cancel and steer are bounded unary
routes, but HTTP approval may switch to an unbounded SSE response when it
rehydrates an awaiting run after server restart. Aborting that response cancels
the newly resumed run, while leaving it unread may wedge its relay. ADR 0288
therefore deferred attached approval and steering instead of presenting a false
cross-transport abstraction.

Controls also race run termination. Existing controls accept the optional
`expected_run_id` guard for compatibility, but a reusable detached control must
never fall back to "whatever run is current." In particular, an unqualified
late steer may promote its input to a new run, while a caller that explicitly
names a run has not authorized a successor.

ADR 0304 made teams the only ergonomic resource added during the v0.1 surface
audit. Run controls now have their own durable identity, correlation, transport,
and request-option contract. Treating them as ad hoc methods on a watch would
couple mutation authority to observation and would still leave a client that
knows a run ID but does not need a stream without an API.

## Decision

1. Add four additive unary `HarnessService` methods: `ResolveRunAsk`,
   `CancelRun`, `SteerRun`, and `CancelRunSteer`. Give each an injective HTTP
   mirror under `/v1/sessions/{id}/controls/`: `resolve-ask`, `cancel`, `steer`,
   and `cancel-steer`. The existing `Converse` frames and legacy `/approve`,
   `/cancel`, `/steer`, and `/cancel-steer` routes remain unchanged.
2. Every new request carries a non-empty `session_id` and a required non-empty
   `expected_run_id`. Ask resolution additionally carries `ask_id` and a
   non-unspecified `ApprovalVerdict`; steer carries text and/or ordered `Content`
   plus an optional client-minted `message_id`; steer cancellation carries the
   optional correlation ID. The server remains authoritative for ownership,
   ask lifecycle, run lifecycle, media validation, and permission effects. The
   SDK reuses `PromptInput` with its existing encoding: text fragments flatten
   with one newline and media retain order among media; the wire does not claim
   arbitrary text/media interleaving. An empty string steer is rejected locally.
   HTTP accepts only the exact bounded snake-case objects and lowercase enum
   strings recorded by the acceptance plan; unknown request keys, trailing JSON,
   legacy aliases, and numeric enum spellings fail closed.
3. Keep the response bounded and operation-specific. Resolve and cancel
   responses echo the addressed run ID, and resolve also echoes the ask ID.
   Steer and steer-cancel responses echo run and message IDs and use the existing
   `SteerOutcome` vocabulary, narrowed respectively to
   `accepted|appended` and `retracted|none_pending`. Required HTTP acknowledgement
   keys remain present even when `message_id` is empty; the SDK validates their
   raw JSON presence and type before protobuf normalization while ignoring
   unrelated unknown response keys for additive compatibility. Every strict
   control follows one state matrix: ended, cancelling, cancelled, and replacement
   targets are `stale_run_control` without revealing a successor ID; only ask
   resolution may rehydrate a matching persisted awaiting run, while cancel on
   that run reports `no_active_run` and steer/retraction report stale.
4. Make ordinary ask resolution atomic and observable. Add
   `agent.(*Run).ResolveOrdinaryAsk`, returning the closed `AskResolution`
   result (`NotPending`, `Resolved`, or `PlanOriginated`) for either the run's own
   ask or a surfaced child ask. It leaves plan-originated asks pending and keeps
   `Run.Approve` as the source-compatible, all-origin, no-result wrapper. The
   service maps not-pending and plan-originated results to the registered
   `ask_not_pending` and `plan_resolution_required` errors and sets its
   persistence race marker only after a real resolution. A restored snapshot's
   pending ask ID and origin are validated before an engine is launched.
5. Make prompt-free ask resolution acknowledgement-only. If resolving a
   persisted awaiting run rehydrates it, the server takes ownership of its
   engine and relay after atomic acceptance. Decode, authentication, ownership,
   validation, and pre-acceptance cancellation still use the unary request
   context; the accepted run uses a server-owned, cancellation-detached context
   governed by ordinary lease loss, drain, and service close. It records and
   persists every event and stays reachable by later controls. No control response
   becomes an event stream, and returning or cancelling the acknowledgement does
   not cancel or wedge the run.
6. Advertise the complete four-operation surface with the open feature string
   `prompt_free_controls`. A client gates every high-level operation on that
   feature and does not emulate a missing operation through legacy HTTP routes
   or by opening `Converse`. Feature support is all-or-nothing on one listener.
7. Add `Session.controls(runId): RunControls` to the TypeScript SDK. The handle
   is a lightweight session/run binding, creates no watch, and exposes
   `resolveAsk`, `cancel`, multimodal `steer`, and `cancelSteer`. It reuses
   `PermissionVerdict`, `PromptInput`, and `RequestOptions`; every request option
   applies to compatibility probing and the control RPC. Steer methods return
   SDK-owned, operation-narrowed acknowledgements after validating the response
   correlation and outcome.
8. Keep existing `Run` and `AttachedRun` signatures source-compatible. Owned runs
   continue to use their stream. `AttachedRun.cancel` retains its current transport
   behavior, while the existing `Promise<never>` approval/resolve/steer methods
   remain local compatibility deferrals under `attached_run_controls`; they do
   not become conditionally successful merely because the server advertises
   `prompt_free_controls`, and they direct applications to
   `session.controls(attached.runId)`. Plan-originated asks retain the dedicated
   live-plan and `Session.resolvePlan()` choreography; detached `resolveAsk` is
   for policy, hook-originated, and surfaced-child permission asks only.
9. Put steer retraction behind the same mutation gates as steer. Before touching
   the pending inbox, the service validates its captured run-entry generation,
   rejects cancelling state, compares the exact run, and proves a live held lease
   under the service lock. Drain, cancellation, replacement, generation change,
   and lease loss therefore linearize before or after one atomic retraction rather
   than reaching an unowned or successor inbox.

The linked acceptance plan owns exact field numbers, TypeScript signatures,
route JSON, validation, compatibility, and verification details.

## Consequences

A client can reload with only a session ID and run ID, reconnect observation if
needed, and independently issue every ordinary run control over gRPC or HTTP.
Mutation no longer requires owning or manufacturing an event stream, and
request headers, deadlines, and cancellation remain ordinary unary-call
options.

Four RPCs and four strict HTTP routes add public surface and raw-catalog rows.
This is more explicit than one polymorphic control envelope, but each descriptor
keeps one request, response, server boundary, and injective HTTP mapping under
ADR 0304's completeness gate. The legacy route family remains as compatibility
surface, so server helpers must prevent semantic drift between old and new
entry points.

Acknowledgement-only rehydration transfers relay ownership to the server. A run
that reaches another permission ask may therefore remain parked without any
control request staying open; the durable watch remains the observation path.
As with any unary mutation, a client disconnect after server acceptance but
before receiving the acknowledgement is ambiguous and must be reconciled from
authoritative session/activity state.

Strict run addressing intentionally gives up late-steer promotion. That is the
only safe meaning for a handle constructed with a specific run ID, and it
prevents delayed controls from mutating a successor. Applications that
deliberately want unqualified promotion must continue to use the existing raw
legacy surface.

The new feature requires a coordinated server and SDK release. New clients fail
typed and locally against older servers; old clients continue using unchanged
stream and HTTP controls against new servers.

The result-returning ordinary-ask seam is an additive engine API change. Its
snapshot and changelog move with the implementation so embedders can rely on the
atomic result rather than attempting a racy inspection around `Run.Approve`.

## See also

- [SDK run controls acceptance plan](../acceptance/sdk-run-controls.md)
- [ADR 0248 — SDK compatibility and error contract](./0248-sdk-compatibility-and-error-contract.md)
- [ADR 0249 — Durable run identity](./0249-durable-run-identity.md)
- [ADR 0252 — HTTP steer endpoint](./0252-http-steer-endpoint.md)
- [ADR 0279 — TypeScript SDK architecture](./0279-typescript-sdk-architecture.md)
- [ADR 0288 — TypeScript SDK durable attachment](./0288-typescript-sdk-durable-attachment.md)
- [ADR 0304 — TypeScript SDK public surface completeness](./0304-typescript-sdk-public-surface-and-release.md)
- [TypeScript SDK architecture](../architecture.md#typescript-sdk)
- [Implementation notes](https://github.com/stacklok/mecatl/blob/773c6c4220c6cc8afa9e80976eb2e739efdce367/docs/design/IMPLEMENTATION-NOTES.md#typescript-sdk--sdktypescript-m1m4-public-surface-and-post-v010-deno-integration-adrs-0279-0288-0292-0304-0328-0337-0338-and-0339)
