# ADR 0348 — TypeScript SDK MCP authorization lifecycle

- Status: Proposed
- Date: 2026-09-18
- Scope: `sdk/typescript/` MCP authorization ergonomics over the existing HarnessService presentation, recheck, cancel, run-control, and event contracts
- Supersedes: ADR 0304 Decision 3 only for admitting an MCP authorization lifecycle as another justified ergonomic resource; Team and RunControls remain unchanged
- Superseded by: none

## Context

The server already owns the complete MCP authorization transition. An agent run can
emit `authorization.required` with safe session-local correlation, while
`GetMcpAuthorizationPresentation` returns the live browser URL and the
`RecheckMcpAuthorization` and `CancelMcpAuthorization` streams report the authoritative
status and any continuation run. The generated TypeScript surface and both raw
transports can reach those operations, but an SDK consumer must currently assemble
descriptors, manufacture the bidirectional first frame, decode the event stream, and
correlate its continuation by hand.

That choreography is stateful. A recheck that observes `pending` has no continuation.
A granted, denied, cancelled, expired, interrupted, failed, or closed authorization may
start a continuation run that repeats the control result once among its events.
That run may emit ordinary permission asks and either reach one terminal result or park
again on a different `authorization.required`. The first control result itself has no
run ID, so a client learns the optional continuation ID only from the stream.

The same non-result terminal shape already exists before this lifecycle begins. A normal
`Session.run()` may publish `authorization.required` and close with the engine's durable
`RunOutcomeAuthorizationPending`. The current `RunImpl` instead requires every stream to
end in `result`, raises `ProtocolError` for the valid park, and can retain the Session's
local busy flag when iteration returns after the handoff. An ergonomic authorization API
must first make that existing Run outcome representable and release its transport
resources without cancelling the newly pending authorization.

The transports also have different control mechanics and response envelopes. gRPC can
send permission and cancel frames on the bidirectional stream. The HTTP mirror is one
bodyless request with an SSE response, emits each `Event` bare rather than inside the
descriptor's `{event}` envelope, and has no reverse channel. The additive prompt-free run
controls from [ADR 0347](./0347-run-id-addressed-prompt-free-controls.md) provide one safe
common control path after the continuation run ID is known.

ADR 0304 favored thin namespaces and originally admitted only Team as a new ergonomic
resource. MCP authorization now has the same reasons for an exception: durable
correlation, a stateful stream, application-supplied permission decisions, optional
continuation ownership, and single-consumption rules cannot be represented honestly by
a generated response type or an unstructured namespace promise.

## Decision

### 1. Bind authorization to the existing Session resource

Add `Session.mcpAuthorization(authorizationId): McpAuthorization`. Construction is
synchronous and local. The handle stores only the session-affined SDK operations and the
exact non-empty authorization ID; it performs no request, opens no stream, and asserts no
authorization state.

Do not add a second top-level authorization namespace or put a session ID back into each
method. The existing `Session` is the SDK's durable owner-scoped resource and already
applies the exact affinity hint to every operation. `client.mcp` remains the thin MCP
inventory namespace established by ADR 0304.

### 2. Make an authorization-parked Run a normal handoff

Add `Run.outcome(): Promise<RunOutcome>` as a third mutually exclusive
single-consumption mode beside iteration and the existing completed-only `result()`.
`RunOutcome` discriminates `completed`, which carries the unchanged `RunResult`, from
`authorization_required`, which carries the exact `authorization.required` event plus
its session and run correlation.

Iteration yields a valid final authorization requirement and then closes normally.
`result()` keeps its source-compatible `Promise<RunResult>` signature, but a parked Run
raises `RunAuthorizationRequiredError` carrying the same typed outcome instead of
misreporting `ProtocolError`. EOF with neither one terminal result nor one final valid
authorization requirement remains a protocol error.

Every parked completion path closes the underlying response iterator, unregisters the
Run, and releases `SessionImpl`'s busy flag. Iterator return after observing the park does
the same. Releasing this SDK ownership does not send cancellation or alter the server's
pending authorization, so the same Session can immediately create an authorization
handle.

### 3. Keep URL presentation separate and application-owned

`McpAuthorization.presentation(requestOptions?)` performs exactly one
`GetMcpAuthorizationPresentation` call and returns the validated absolute HTTP(S) URL as
a string. It does not cache the URL, open a browser, copy to a clipboard, render
instructions, persist anything, or infer success. A malformed or absent URL is a
`ProtocolError`; owner, pending-state, expiry, and availability refusals remain the
server's typed errors.

### 4. Each control call creates one lazy single-consumption flow

`McpAuthorization.recheck(flowOptions?, requestOptions?)` and
`McpAuthorization.cancel(flowOptions?, requestOptions?)` each return a distinct
`McpAuthorizationFlow`. The methods do not poll or retry. Repeated rechecks are explicit
new flows, so the application chooses timing and backoff and concurrent calls cannot
share an iterator or control queue accidentally.

The flow binds immutable `sessionId`, `authorizationId`, and `operation` properties. It
is an `AsyncIterable<Event>` and also exposes `result()`. As with `Run`, callers choose
iteration or `result()` exactly once. A second claim, including a second iterator, raises
`InvalidStateError` before consuming another frame.

Flow construction and iterator acquisition are transport-lazy. The first iterator
`next()` or `result()` starts the compatibility check, client stream registration,
request timeout, and exactly one control RPC. An already-aborted caller signal prevents
transport work. Stream establishment and server errors, plus header callbacks, surface
from that first consuming operation; an unconsumed flow performs no server mutation and
owns no registered transport resource. The operation's `RequestOptions` apply only to
that stream, not to later permission mutations.

### 5. Validate a closed control result and one optional continuation

The first decoded frame must be `authorization.required` or
`authorization.resolved`, carry the handle's exact authorization ID, a non-empty call
ID, an empty run ID, and one status from the closed `McpAuthorizationStatus` union:
`pending | granted | denied | cancelled | expired | interrupted | failed | closed`.
`pending` pairs with `authorization.required`; every other value pairs with
`authorization.resolved`. A mismatch is a `ProtocolError`, not a different lifecycle.

EOF after only that authoritative status is valid and means there was no continuation.
`result()` returns a discriminated `pending` result for `authorization.required`, or a
`settled` result for a terminal `authorization.resolved` without a continuation.
Server-declared denial and other terminal statuses are values, not thrown errors.

If another frame follows, its first non-empty run ID fixes `continuationRunId`, and every
continuation frame must retain it. The continuation must contain exactly one repeated
copy of the original resolved authorization payload, including the original authorization
and call IDs. Other continuation events are correlated by the pinned run ID rather than
the original call ID. It then ends in exactly one of two
ways: one terminal `result` with no following event, returned as `outcome: "completed"`
with an ordinary `RunResult`; or a different `authorization.required` with pending
status, its own non-empty call ID, and the continuation run ID, returned after clean EOF as
`outcome: "authorization_required"` with `nextAuthorization`. Missing or duplicate
original resolution, a changed run ID, a second result, a same-ID or malformed chained
authorization, an event after a terminal result, or continuation EOF without either
valid ending is `ProtocolError`.

This four-way discriminated result prevents impossible independent status/event/result
combinations. Iteration still yields the existing decoded `Event` union in wire order.
Transport, protocol, and server failures remain errors. The ordinary exported `Event`
union keeps its open string status for forward-compatible raw observation; only this
lifecycle validates the server's currently closed state machine.

### 6. Reuse prompt-free exact-run controls for transport parity

`McpAuthorizationFlowOptions` contains the existing `onPermissionAsk` responder plus
`permissionRequestOptions`, used only for automatic verdict mutations. The flow exposes
`resolveAsk(askId, verdict, requestOptions?)` for manual resolution and
`cancelContinuation(requestOptions?)` for explicit continuation cancellation. Manual
calls use only their own options. Both controls are available only after the flow has
observed the exact continuation run and the relevant ask where applicable.

Both gRPC and HTTP use `Session.controls(continuationRunId)` for these mutations. The
SDK deliberately does not expose the gRPC-only reverse stream as a second public
semantics. This preserves exact-run stale guards, request options, typed errors, and one
behavior across transports. A server without `prompt_free_controls` may still report a
status-only authorization result, but a continuation that needs a permission decision
or explicit cancellation fails with the existing typed unsupported-feature error.

Manual and automatic controls address only the observed continuation run and ask IDs.
A manual control admitted before flow termination retains its caller-owned request lifetime
through setup and dispatch. Invocations after termination fail locally without starting an
RPC.

The responder is application policy. `undefined` leaves an ask pending for manual
resolution; `deny`, `allow_once`, and `allow_always` are sent to the server unchanged.
The SDK never infers a verdict, upgrades a denial, or treats an authorization status as
permission authority. A plan-originated ask is yielded but is never given to this
ordinary responder or accepted by `resolveAsk()`; it requires an existing separate plan
workflow where applicable or explicit continuation cancellation. This ADR adds no new
prompt-free plan-approval authority.

### 7. Request cancellation is not retry or guaranteed recovery

The `RequestOptions` supplied to `presentation`, `recheck`, and `cancel` retain headers,
callbacks, deadlines, caller signals, client-close cancellation, and automatic session
affinity. A request abort ends that request. A caller-supplied `MecatlError` is preserved;
other caller aborts and transport losses are normalized to `TransportError`, deadlines
reject with `ServerError` whose `status === Code.DeadlineExceeded`, and client close uses
the client's existing `InvalidStateError`. It does not cause an automatic recheck, cancel,
durable watch, or browser action.

Valid EOF resolves `result()` to its typed result and resolves a pending or later iterator
`next()` with `{ done: true }`. Explicit iterator `return()` also completes a pending read
cleanly. Every terminal path closes the response iterator, registration, timer, and caller
listener once; aborts responders and flow-owned automatic controls; suppresses late
verdicts; and makes later manual controls fail locally. An already-admitted manual control
keeps only its caller-owned request lifetime.

The existing server remains authoritative for what happened before a disconnect. The
authorization-control relay makes the gRPC distinction explicit: it detaches and drains
ordinary runnable work after losing its control stream, but cancels the exact continuation
when control-stream EOF strands it on an ordinary permission ask. HTTP
requests cancellation for a still-active continuation when its SSE request is lost and
drains the result. Neither transport destroys a follow-up authorization after that Run
has already committed its authorization park, because cancellation is then inert. The
SDK documents these phase-specific effects rather than claiming one transport-neutral
disconnect outcome.

Once a continuation run ID was observed, an application can use the ordinary durable
`Session.attach(runId)` and activity APIs where supported. Exact-run attachment treats a
replayed `authorization.required` as a terminal park only when it carries the attached run
ID, `pending` status, and non-empty authorization and call IDs. It yields and checkpoints
that event, marks the attachment not live, clears pending asks, and closes without waiting
for a `result`; session-wide activity remains open. Suitable retained session activity may
also reveal correlation after loss, but this lifecycle neither scans nor guarantees that
log. Before the ID or authoritative status is observed, a lost mutation
is ambiguous and may be unrecoverable: a fresh exact-correlation recheck is a new
one-shot control that succeeds only if the original authorization remains pending. If
the prior control committed and cleared it, the same-ID recheck returns the server's
not-found refusal rather than replaying the committed result. No control is
automatically replayed.

### 8. Reuse the wire and correct both HTTP classifications

No protobuf wire-shape, Go API, event, snapshot, persistence, feature, or error-code
changes. The gRPC authorization-control relay implements the disconnect behavior in
Decision 7 without adding a method or message: EOF while the continuation is parked on an
ordinary permission ask cancels that exact run, while EOF during runnable work leaves it
detached to drain. The `Converse` source comment is corrected to document closure after
either a terminal `result` or a pending `authorization.required` park, and regenerated
bindings carry that comment. The SDK continues to invoke the three existing HarnessService descriptors.
The RPC catalog classifies the HTTP recheck and cancel routes as `requestBody: "none"`,
matching the server's correlation-only path contract; the current `"json"`
classification incorrectly serializes `{}` and must be corrected. Those rows also set
`responseField: "event"`, and the generic HTTP stream decoder applies that field to each
bare SSE document before protobuf decoding. Converse's existing special-case wrapper is
therefore no longer the only HTTP stream capable of decoding a bare server `Event`.

The root, Node, and Deno entry points export the new lifecycle types. API Extractor,
generated reference, executable examples, architecture, implementation notes, public
SDK guidance, and the automated SDK changelog path remain the compatibility gates.

## Consequences

Applications receive a discoverable path from an `authorization.required` event to a
presentation URL and a typed recheck/cancel lifecycle without importing generated
descriptors. The session-bound handle removes repeated correlation parameters, while a
new flow per control prevents unrelated operations from sharing consumption state.

Ordinary Run consumers can treat authorization parking as a normal outcome and release
the Session immediately. The additive `outcome()` path preserves `result()` for
completed-only callers, at the cost of another Run consumption method and a new typed
error for callers that use that narrower convenience on a parked Run.

The SDK gains two related resource types instead of stretching the MCP inventory
namespace into a state machine. This is a deliberate second exception to ADR 0304's
original teams-only constraint, following ADR 0347's narrower RunControls exception.

Status-only rechecks stay cheap and policy-free. Lazy dispatch means constructing or
requesting an iterator from a flow has no server effect; applications must call
`next()` or `result()`. Applications that want polling, browser launch, persistence, or
reconnect policy must implement those choices explicitly.

Using prompt-free run controls avoids a gRPC-only ergonomic branch, but it makes those
controls a requirement only when an authorization continuation asks permission or needs
explicit cancellation. Automatic replies need their own declared request options, and
plan-originated asks remain outside this ordinary-permission surface. Existing raw
consumers may continue using the bidirectional frames directly.

Disconnects can remain ambiguous because the two server transports differ while a
continuation is active, and terminal controls consume the pending correlation instead
of retaining a replayable outcome. The lifecycle exposes the continuation run ID as soon
as it is observed and documents phase-specific, best-effort durable reconciliation
rather than promising an unsafe replay. Chained authorization parking remains a normal
result and survives either transport's late cancellation behavior.

## See also

- [SDK MCP authorization acceptance plan](../acceptance/sdk-mcp-authorization-lifecycle.md)
- [ADR 0304 — TypeScript SDK public surface completeness](./0304-typescript-sdk-public-surface-and-release.md)
- [ADR 0347 — Run-ID-addressed prompt-free controls](./0347-run-id-addressed-prompt-free-controls.md)
- [TypeScript SDK architecture](../architecture.md#typescript-sdk)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md#typescript-sdk--sdktypescript-m1m4-public-surface-and-post-v010-deno-integration-adrs-0279-0288-0292-0304-0328-0337-0338-and-0339)
