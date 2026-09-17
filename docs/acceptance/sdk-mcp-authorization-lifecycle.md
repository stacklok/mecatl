# TypeScript SDK MCP authorization lifecycle — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this adds durable public TypeScript SDK resource, stream, status, result, and control contracts for a stateful session-bound authorization workflow.
**Decision record:** [ADR 0348](../adr/0348-typescript-sdk-mcp-authorization-lifecycle.md)
**Phase:** ergonomic TypeScript SDK MCP authorization lifecycle
**Status:** in-progress, 2026-09-18. Implementation proceeds as a sequential stack layer above open Plan / Interface PR #1687 by explicit directing-user instruction and includes the attachment, termination, and correlation clarifications requested during review.
**Delivery:** Split. The public SDK object model, Run parking contract, single-consumption stream grammar, control routing, and disconnect semantics require Plan / Interface review before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1469](https://github.com/stacklok/mecatl/issues/1469)
**Plan PR:** [stacklok/mecatl#1687](https://github.com/stacklok/mecatl/pull/1687)
**Approved baseline:** absent until the Plan / Interface PR merges

An SDK consumer can receive a normal authorization-parked outcome from an ordinary
`Run`, bind that event's authorization ID to the existing `Session`, obtain the live
presentation URL, and perform one explicit recheck or cancellation through a typed
single-consumption flow. A continuation that parks again returns another typed
authorization handoff instead of being misclassified as a truncated run. The server
continues to own authorization, permissions, expiry, and all terminal outcomes.

The SDK does not adopt mecatui's phase machine. It supplies correlation, decoding,
exact-run continuation controls, request options, and protocol checks; applications
own rendering, browser launch, polling cadence, persistence, and recovery policy. This
specialized lifecycle supersedes only the ergonomic-resource limit in
[ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md), as recorded by
[ADR 0348](../adr/0348-typescript-sdk-mcp-authorization-lifecycle.md).

## Human decisions

- [x] Public object model — approve `Session.mcpAuthorization(authorizationId)` returning a reusable correlation handle, with each `recheck()` or `cancel()` producing a distinct single-consumption `McpAuthorizationFlow` and a discriminated pending, settled, completed, or chained-authorization result, instead of adding raw-shaped methods to `client.mcp`. — Decision: approved as proposed.
- [x] Initial Run handoff — approve additive `Run.outcome()` as the normal completed-or-authorization-required drain, clean iterator EOF after an authorization park, and a typed `RunAuthorizationRequiredError` from the existing completed-only `Run.result()` method so valid parking is never a `ProtocolError` or a stuck `Session`. — Decision: approved as proposed.
- [x] Dispatch and automatic-control options — approve lazy recheck/cancel dispatch on the first iterator `next()` or `result()`, with the operation's `RequestOptions` scoped only to that stream and a separate `permissionRequestOptions` value for automatic permission replies. — Decision: approved as proposed.
- [x] Recovery contract — approve explicit one-shot controls with no SDK polling, mutation retry, or transparent reconnect; a lost response is unrecoverable through this lifecycle alone when the server committed and cleared the original pending authorization before the application learned the continuation ID, while retained session activity and an already-observed run ID may permit explicit inspection or attachment. — Decision: approved as proposed.
- [x] Attachment recovery terminal — decide whether an explicit `Session.attach(continuationRunId)` must recognize a replayed chained authorization park or require application-owned watch closure. — Decision: a valid pending `authorization.required` for the exact attached run is terminal, just like `result`; attachment yields it, marks `live` false, clears pending asks, checkpoints it, and closes without application cleanup.
- [x] Flow termination contract — decide the observable outcome of caller abort, deadline, iterator return, transport loss, and client close, including responder, control, and registration lifetimes. — Decision: use the Scenario 5 termination matrix; explicit iterator return ends a pending read cleanly, external failures reject once with their normalized errors, flow-owned responders and automatic controls are aborted, late verdicts are suppressed, and later manual controls fail locally.
- [x] Correlation boundary — decide which origins the SDK can prove without widening the wire. — Decision: the server enforces session ownership through the session-affined request; the SDK checks the handle's authorization ID, learns the original non-empty call ID from the authoritative frame, requires the repeated original resolution to retain both IDs, pins the continuation run ID for every continuation frame, and validates a chained authorization as a different authorization with its own non-empty call ID.

## Interface contract

- **gRPC / protobuf:** No wire-shape change — the SDK reuses the existing `GetMcpAuthorizationPresentation`, `RecheckMcpAuthorization`, `CancelMcpAuthorization`, `ResolveRunAsk`, and `CancelRun` descriptors and their current messages and field numbers. The `Converse` source comment is corrected to state that the stream closes after either terminal `result` or a pending `authorization.required` park; regenerated bindings carry that documentation-only correction. The authorization-control relay makes the existing gRPC disconnect distinction explicit: control-stream EOF cancels the exact continuation only when the lost stream strands it on an ordinary permission ask, while runnable work remains detached and drains. The TypeScript RPC catalog changes the HTTP recheck/cancel request-body classification from `json` to `none` and declares response field `event`; the HTTP stream decoder then wraps each server-emitted bare `Event` as the descriptor's `{event}` response.
- **Exported Go APIs / interfaces:** None — the current Service and HarnessServer APIs already own presentation, exact authorization transition, continuation relay, ask resolution, and cancellation; this plan changes no Go symbol or engine surface.
- **Tool schemas:** None — no model-facing tool name, input schema, result schema, permission classification, or dispatch behavior changes.
- **CLI / config:** None — no flag, environment variable, settings key, default, precedence rule, browser behavior, or TUI command changes.
- **Events / persistence:** None — the lifecycle consumes the existing `authorization.required`, `authorization.resolved`, `permission.ask`, and `result` events without adding or changing an event, snapshot, event-log record, cursor, or store. The initial Run and a continuation may both close normally on `authorization.required` without a `result`. Presentation URLs remain live-only and unpersisted. The SDK returns detached decoded values and stores no credential or lifecycle truth outside live handles.
- **Security / authority:** `Session.mcpAuthorization()` binds only the existing session-affined operation bag and caller-supplied authorization ID; it grants no authority and makes no state assertion. The server enforces session ownership through that affined request because authorization events carry no independent session ID. The SDK compares the first authoritative frame with the handle's authorization ID, requires a non-empty original call ID, requires the repeated original resolution to retain both original IDs, pins the learned continuation run ID for every continuation frame, and validates a chained authorization as a different authorization with its own non-empty call ID. Manual and automatic controls use only observed continuation-run and ask IDs. The server remains definitive for ownership, pending correlation, status, expiry, authorization grant/denial, permission effects, continuation admission, and post-disconnect outcome. The SDK returns only an HTTP(S) presentation string, never opens it, and routes continuation mutations through exact-run `RunControls` without choosing a verdict. Automatic permission controls use only the separately supplied `permissionRequestOptions`; flow request options are not silently reused as mutation authority.
- **Compatibility / migration:** Additive hand-written TypeScript SDK minor surface: root, Node, and Deno exports gain `McpAuthorization`, `McpAuthorizationFlow`, `McpAuthorizationFlowOptions`, `McpAuthorizationOperation`, `McpAuthorizationResult`, `McpAuthorizationStatus`, `RunOutcome`, `RunCompletedOutcome`, `RunAuthorizationRequiredOutcome`, and `RunAuthorizationRequiredError`. Existing `Run.result()` remains completed-only, returns `RunResult`, and stays source-compatible, but a valid authorization park changes from an erroneous `ProtocolError` to `RunAuthorizationRequiredError`; consumers that want both valid outcomes migrate to additive `Run.outcome()`. Iteration over a parked Run now closes cleanly and releases the session. An exact-run `AttachedRun` also treats a valid pending `authorization.required` for its run as terminal: it yields and checkpoints the event, marks `live` false, clears pending asks, and closes just as it does for `result`. Session-wide activity remains open. Existing `Event`, `RunControls`, `McpInventory`, raw catalog, and `./gen` APIs otherwise remain source-compatible. Status-only flows work against servers that implement the existing authorization RPCs; permission resolution and explicit continuation cancellation additionally require their existing `prompt_free_controls` feature. API reports, generated reference, public guidance, an executable example, and the automated SDK changelog path record the addition.

The proposed public TypeScript surface is exact:

```ts
type McpAuthorizationStatus =
  | "pending"
  | "granted"
  | "denied"
  | "cancelled"
  | "expired"
  | "interrupted"
  | "failed"
  | "closed";

type McpAuthorizationOperation = "recheck" | "cancel";

interface McpAuthorizationFlowOptions {
  onPermissionAsk?: PermissionAskResponder;
  permissionRequestOptions?: RequestOptions;
}

type McpAuthorizationResult =
  | {
      readonly outcome: "pending";
      readonly status: "pending";
      readonly authorization: EventOf<"authorization.required">;
    }
  | {
      readonly outcome: "settled";
      readonly status: Exclude<McpAuthorizationStatus, "pending">;
      readonly authorization: EventOf<"authorization.resolved">;
    }
  | {
      readonly outcome: "completed";
      readonly status: Exclude<McpAuthorizationStatus, "pending">;
      readonly authorization: EventOf<"authorization.resolved">;
      readonly continuationRunId: string;
      readonly continuation: RunResult;
    }
  | {
      readonly outcome: "authorization_required";
      readonly status: Exclude<McpAuthorizationStatus, "pending">;
      readonly authorization: EventOf<"authorization.resolved">;
      readonly continuationRunId: string;
      readonly nextAuthorization: EventOf<"authorization.required">;
    };

interface RunCompletedOutcome {
  readonly outcome: "completed";
  readonly result: RunResult;
}

interface RunAuthorizationRequiredOutcome {
  readonly outcome: "authorization_required";
  readonly sessionId: string;
  readonly runId: string;
  readonly authorization: EventOf<"authorization.required">;
}

type RunOutcome = RunCompletedOutcome | RunAuthorizationRequiredOutcome;

interface Run {
  outcome(): Promise<RunOutcome>;
  result(): Promise<RunResult>;
}

class RunAuthorizationRequiredError extends InvalidStateError {
  readonly outcome: RunAuthorizationRequiredOutcome;
}

interface McpAuthorizationFlow extends AsyncIterable<Event> {
  readonly sessionId: string;
  readonly authorizationId: string;
  readonly operation: McpAuthorizationOperation;
  readonly continuationRunId: string | undefined;
  resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    requestOptions?: RequestOptions,
  ): Promise<void>;
  cancelContinuation(requestOptions?: RequestOptions): Promise<void>;
  result(): Promise<McpAuthorizationResult>;
}

interface McpAuthorization {
  readonly sessionId: string;
  readonly authorizationId: string;
  presentation(requestOptions?: RequestOptions): Promise<string>;
  recheck(
    options?: McpAuthorizationFlowOptions,
    requestOptions?: RequestOptions,
  ): McpAuthorizationFlow;
  cancel(
    options?: McpAuthorizationFlowOptions,
    requestOptions?: RequestOptions,
  ): McpAuthorizationFlow;
}

interface Session {
  mcpAuthorization(authorizationId: string): McpAuthorization;
}
```

`Run.outcome()`, `Run.result()`, and event iteration are three mutually exclusive
ways to claim the same Run. `Run.result()` remains a completed-only convenience;
when the consumed run parks, its `RunAuthorizationRequiredError.outcome` carries
the same detached handoff that `Run.outcome()` would have returned.

## In scope — 6 scenarios, in implementation order

### Scenario 1 — an ordinary Run hands off a parked authorization normally

The SDK reflects the engine's documented non-result
`RunOutcomeAuthorizationPending` rather than treating that valid lifecycle as a broken
stream; see [architecture](../architecture.md#typescript-sdk).

**Acceptance:**
- AC1.1: `Run.outcome()` drains one claimed Run and returns `RunCompletedOutcome` for exactly one terminal `result`, or `RunAuthorizationRequiredOutcome` when the final event is `authorization.required` with status `pending`, a non-empty authorization/call ID, and the Run's exact non-empty session/run correlation.
  - verify: vitest:sdk/typescript/test/run.test.ts — `Run outcome discriminates completion from authorization parking`
- AC1.2: Event iteration yields the valid final `authorization.required` and then closes normally; the completed-only `Run.result()` releases the stream and throws `RunAuthorizationRequiredError` carrying the same detached outcome, never `ProtocolError`.
  - verify: vitest:sdk/typescript/test/run.test.ts — `authorization parked Run iteration and result use normal handoff semantics`
- AC1.3: Authorization-park EOF, `outcome()`, `result()`, and iterator return after the parked event close the underlying response iterator, unregister the Run, clear `SessionImpl`'s busy state, and do not cancel or resolve the server's pending authorization; the same Session can immediately create its lifecycle handle.
  - verify: vitest:sdk/typescript/test/run.test.ts — `authorization parked Run releases SDK ownership without cancelling authorization`
- AC1.4: `outcome()`, `result()`, and iteration are mutually exclusive single-consumption modes; completed runs retain their existing `RunResult`, and EOF without either a terminal `result` or final valid authorization requirement remains `ProtocolError`.
  - verify: vitest:sdk/typescript/test/run.test.ts — `Run outcome preserves completed and malformed stream behavior`

### Scenario 2 — a session-bound handle presents one live authorization

The handle extends the established [TypeScript SDK session architecture](../architecture.md#typescript-sdk)
without importing mecatui state or widening the thin MCP inventory namespace.

**Acceptance:**
- AC2.1: `session.mcpAuthorization(authorizationId)` rejects an empty authorization ID locally; otherwise it synchronously returns an `McpAuthorization` with exact readonly session/authorization IDs and performs no compatibility probe, RPC, stream open, durable watch, or state assertion during construction.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization handle binds exact correlation without I/O`
- AC2.2: `presentation(requestOptions?)` makes one existing presentation RPC with automatic exact session affinity, preserves caller headers, callbacks, signal, and deadline, and returns the server's absolute HTTP(S) URL string without caching, opening, copying, rendering, or persisting it.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization presentation is live validated and application owned`
- AC2.3: An absent, relative, non-HTTP(S), or otherwise malformed presentation URL raises `ProtocolError`; server ownership, unknown/past authorization, expiry, lease, and availability refusals retain their normalized typed errors without exposing another session or credential.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization presentation preserves protocol and server failures`

### Scenario 3 — recheck and cancel are lazy correlated single-consumption flows

Each operation consumes the existing stateful stream described by
[ADR 0348](../adr/0348-typescript-sdk-mcp-authorization-lifecycle.md), not a generic
namespace response or TUI phase machine.

**Acceptance:**
- AC3.1: `recheck()` and `cancel()` synchronously return distinct flows with exact immutable session/authorization/operation values but perform no compatibility probe, registration, RPC, mutation, or timer start until the first iterator `next()` or `result()`; merely requesting an iterator claims it but remains transport-lazy.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization operations start only on first consumption`
- AC3.2: First consumption registers one client-owned stream, starts `timeoutMs`, observes an already-aborted caller signal before transport work, and issues exactly the named existing descriptor with one initial gRPC frame carrying only the handle's session/authorization IDs or the equivalent bodyless HTTP route. Establishment/server errors and header callbacks surface from that consuming `next()` or `result()`, not from flow construction.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization flow start preserves request timing and exact control`
- AC3.3: The first authoritative frame is a known authorization event with empty `runId`, the exact authorization ID, non-empty call ID, a known status, and required-`pending` versus resolved-terminal pairing; every mismatch, missing payload, unknown status, or malformed frame raises `ProtocolError`.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization flow validates the authoritative control result`
- AC3.4: Clean EOF after only the authoritative event returns discriminated `pending` or `settled`; denial, cancellation, expiry, interruption, failure, and closure are typed values rather than exceptions, and impossible event/status/result combinations are not representable by `McpAuthorizationResult`.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization status-only results are discriminated values`
- AC3.5: Iteration and `result()` are mutually exclusive and claim the flow once; a second iterator, a second `result()`, or cross-mode consumption raises `InvalidStateError` without opening or consuming another stream.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization flow is single consumption`

### Scenario 4 — a continuation completes or hands off one chained authorization

Continuation control reuses the exact-run resource approved by
[ADR 0347](../adr/0347-run-id-addressed-prompt-free-controls.md), preserving one
ergonomic behavior over gRPC and HTTP.

**Acceptance:**
- AC4.1: The first post-status event fixes one non-empty `continuationRunId`; every continuation event retains it, and the continuation contains exactly one repeated copy of the original resolved authorization payload before it closes. A changed run ID, missing/duplicate original resolution, second result, or event after a terminal result is `ProtocolError`.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization continuation validates run and repeated resolution grammar`
- AC4.2: Clean continuation EOF after exactly one terminal `result` returns `outcome: "completed"` with the ordinary `RunResult`; iteration yields every decoded event in wire order, and the lifecycle fabricates no `Run`, attachment, cursor, or successor.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization continuation returns one ordinary completed result`
- AC4.3: A continuation may instead end with one later `authorization.required` carrying status `pending`, a non-empty call ID, the same continuation run ID, and an authorization ID different from the control's original ID. Clean EOF then returns `outcome: "authorization_required"` with that exact `nextAuthorization`; EOF with neither a result nor this chained park remains `ProtocolError`.
  - verify: vitest:sdk/typescript/test/mcp-authorization.test.ts — `MCP authorization continuation hands off a chained authorization`
- AC4.4: `onPermissionAsk` receives only an observed ordinary ask plus a lifecycle-bound signal. Its explicit verdict uses only `permissionRequestOptions`; omission or abstention leaves the ask pending. Manual `resolveAsk()` uses only its own request options. Both address the exact observed ask/run through `RunControls`, while unknown, resolved, retracted, plan-originated, or mismatched asks are never guessed.
  - verify: vitest:sdk/typescript/test/mcp-authorization-controls.test.ts — `MCP authorization permission decisions and request options remain application owned`
- AC4.5: `cancelContinuation()` addresses only the observed continuation run through `RunControls.cancel`; either manual control before its required run/ask is observed fails locally, and absent `prompt_free_controls` fails with the existing typed feature refusal without cancelling or resolving something else.
  - verify: vitest:sdk/typescript/test/mcp-authorization-controls.test.ts — `MCP authorization continuation controls are exact run and feature gated`
- AC4.6: Stream, manual-control, and automatic-control request options independently preserve headers, callbacks, signals, per-request deadlines, session affinity, client-close state, normalized errors, and no-retry behavior on both transports; a plan-originated ask is yielded but requires an existing separate plan workflow or explicit continuation cancellation.
  - verify: vitest:sdk/typescript/test/mcp-authorization-controls.test.ts — `MCP authorization request options and unsupported plan asks stay separated`

### Scenario 5 — concurrency, cancellation, and recovery remain explicit

The SDK preserves the server-owned lifecycle and durable observation boundaries in the
[architecture](../architecture.md#typescript-sdk); it does not invent a client-side
authorization truth store.

Flow termination has one observable contract:

| End cause | Pending `next()` or `result()` | Responder and control lifetime | Later manual controls | SDK cleanup |
|---|---|---|---|---|
| Valid status-only or continuation EOF | `result()` resolves to the typed result; a pending or later iterator `next()` resolves with `{ done: true }` | Pending responders and automatic controls are aborted; late verdicts are ignored | Reject locally with `InvalidStateError` | Close the response iterator and release the registration, timer, and caller listener once |
| Explicit iterator `return()` | The pending `next()` and `return()` resolve with `done: true` | Pending responders and automatic controls are aborted; late verdicts are ignored | Reject locally with `InvalidStateError` | Close and release once without a server mutation |
| Caller signal | Rejects once, preserving a caller-supplied `MecatlError` or otherwise using the normalized caller-cancellation `TransportError`; later `next()` is done | Pending responders and automatic controls are aborted; late verdicts are ignored | Reject locally with `InvalidStateError` | Close and release once without replay or cancellation |
| Deadline | Rejects once with `ServerError` whose `status === Code.DeadlineExceeded`; later `next()` is done | Pending responders and automatic controls are aborted; late verdicts are ignored | Reject locally with `InvalidStateError` | Close and release once and clear the timer |
| Transport loss | Rejects once, preserving an incoming `MecatlError` or otherwise using the normalized `TransportError`; later `next()` is done | Pending responders and automatic controls are aborted; late verdicts are ignored | Reject locally with `InvalidStateError` | Close and release once without retry |
| Client close | Rejects once with the client's existing `InvalidStateError`; later `next()` is done | Pending responders and automatic controls are aborted; late verdicts are ignored | Reject locally with `InvalidStateError` | Close and release once with the client |

A manual control admitted before termination retains its caller-owned request lifetime
through setup and dispatch. Ending the flow neither aborts, replays, nor converts that
already-admitted request into an automatic control; an invocation admitted after
termination fails locally without starting an RPC.

**Acceptance:**
- AC5.1: Concurrent flows from one or more handles own independent iterators, request controls, pending-ask maps, registrations, and abort lifetimes. Session ownership is server-enforced through each session-affined request. The SDK rejects an authoritative event whose authorization ID differs from the handle, learns its non-empty original call ID from that frame, requires the repeated original resolution to retain both original IDs, pins one non-empty continuation run ID for every continuation frame, and accepts a chained pending authorization only when it has a different authorization ID and its own non-empty call ID. Controls address only observed continuation-run and ask IDs. No flow consumes another flow's iterator or pending ask.
  - verify: vitest:sdk/typescript/test/mcp-authorization-recovery.test.ts — `concurrent MCP authorization flows cannot cross consume or correlate`
- AC5.2: Valid EOF, caller signal, deadline, iterator return while `next()` is pending, transport loss, and client close follow the termination matrix exactly. Every path aborts pending responders and flow-owned automatic controls, suppresses late verdicts, rejects post-terminal `resolveAsk()` and `cancelContinuation()` locally without another RPC, preserves the caller-owned lifetime of a manual control admitted before termination, releases registration and transport resources once, and performs no automatic recheck, mutation replay, browser action, credential persistence, or claim about committed server state.
  - verify: vitest:sdk/typescript/test/mcp-authorization-recovery.test.ts — `MCP authorization flow cancellation releases only SDK owned resources`
- AC5.3: A fresh recheck after loss is a new one-shot mutation, not replay or guaranteed recovery: it can proceed only while the same authorization remains pending. If the lost control committed and cleared pending state before the caller observed its status or continuation ID, the server's not-found refusal is preserved and this lifecycle alone cannot reconstruct the lost outcome.
  - verify: vitest:sdk/typescript/test/mcp-authorization-recovery.test.ts — `MCP authorization recovery never overpromises replay`
- AC5.4: A caller that observed `continuationRunId`, or a deployment retaining suitable session activity, may explicitly inspect activity and attach to a still-observable run. After disconnect, exact-run attachment treats a replayed `authorization.required` as a valid park only when it carries the exact attached run ID, `pending` status, and non-empty authorization and call IDs. Attachment yields and checkpoints that event, marks `live` false, clears pending asks, and closes without waiting for a nonexistent `result`; session-wide activity remains open. The lifecycle itself never opens a durable watch, scans activity, reconnects, or guarantees event-log retention.
  - verify: vitest:sdk/typescript/test/mcp-authorization-recovery.test.ts — `disconnect attach replays chained authorization park as terminal`
- AC5.5: Real-server tests pin phase-specific disconnect behavior: gRPC detaches and drains ordinary continuation work but cancels a run stranded on an ordinary permission ask; HTTP requests cancellation for a still-active continuation and drains it; neither path destroys a follow-up authorization after its `authorization.required` park has committed, and terminal races remain server-authoritative.
  - verify: vitest:sdk/typescript/e2e/mcp-authorization.e2e.test.ts — `MCP authorization disconnect follows transport and park phase`

### Scenario 6 — public and real-wire coverage makes the workflow usable

The API is release surface under [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
and must work against the same-checkout daemon, not only injected transports.

**Acceptance:**
- AC6.1: The HTTP RPC catalog sends no body for recheck/cancel, declares response field `event`, and the generic HTTP SSE decoder wraps each bare server event for both descriptors. Injected plus real-wire gRPC TCP, gRPC UDS, and HTTP/SSE tests cover initial Run parking, presentation, pending recheck, granted/denied/cancelled resolution, completed and chained continuations, permission allow/deny, explicit continuation cancellation, request options, typed errors, and exact affinity with equivalent high-level results where server semantics coincide.
  - verify: vitest:sdk/typescript/e2e/mcp-authorization.e2e.test.ts — `MCP authorization works over gRPC TCP UDS and HTTP SSE`
- AC6.2: Root, Node, and Deno declarations export the exact interface contract; API Extractor reports, package tests, the generated SDK reference, and the runtime import matrix prevent an entry point or type from drifting.
  - verify: vitest:sdk/typescript/test/package.test.ts — `MCP authorization lifecycle is exported documented and API reviewed`
- AC6.3: A concise package-export-only example shows initial Run handoff, application-owned URL handling, explicit recheck cadence, permission response, all discriminated results, chained authorization, and bounded recovery without opening a browser or copying mecatui policy.
  - verify: vitest:sdk/typescript/test/examples.test.ts — `MCP authorization example uses only the public lifecycle`
- AC6.4: TSDoc, TypeScript SDK permissions/sessions guidance, architecture, implementation notes, generated reference, and the automated SDK changelog describe ownership, Run parking, states, single consumption, lazy dispatch, affinity, request cancellation, no-retry polling, recovery limits, phase-specific HTTP/gRPC disconnect behavior, permission authority, and URL secrecy.
  - verify: inspection — public documentation and generated API/changelog artifacts are content/build outputs rather than runtime behavior

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Browser opening, clipboard, rendering, instructions, or polling cadence | Application UI | The SDK returns the live URL and one-shot controls only. |
| Client credential or authorization-state persistence | Server/application storage | The SDK owns no credential store or authorization truth. |
| TUI phase, timers, automatic retry count, or generation state | `cmd/mecatui` | Do not make one client's presentation policy the SDK contract. |
| New authorization RPCs, events, statuses, error codes, or feature flags | Future server contract if evidence requires it | Reuse the shipped server protocol and open compatibility/error machinery. |
| Automatic continuation attachment or transparent stream reconnect | Explicit application recovery | Callers choose whether and where to persist and resume observation. |
| Changing server HTTP disconnect semantics | Separate server lifecycle decision | Document and test the current transport distinction rather than hiding it in the SDK. |
| A generic authorization namespace or raw descriptor aliases | Existing `./gen` and raw catalog | The ergonomic workflow is session-bound; raw access remains available. |
| A new prompt-free plan-approval control | Separate server/API decision | A lifecycle continuation yields a plan-originated ask but does not reinterpret it as an ordinary permission or add transport-specific authority. |
| Changes to `RunControls` or ordinary permission semantics | Existing SDK contracts | Reuse those exact controls and verdicts without widening their authority. |

## Definition of done

1. `task lint`, `task test`, `task docs`, `task site:build`, and `task api:check` pass.
2. `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:build`, `task sdk:api:check`, `task sdk:docs:check`, `task sdk:examples:typecheck`, `task sdk:deno`, and `task sdk:e2e` pass.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green.
5. API reports, generated SDK reference, package-export example, architecture, implementation notes, user guides, and the automated SDK changelog path are updated together.
6. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
7. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- A control can commit before its response is lost. The SDK cannot safely replay that mutation and may not learn a continuation run ID. A new recheck succeeds only if the original authorization is still pending; otherwise recovery depends on separately retained session activity and may be impossible.
- The current gRPC and HTTP server relays deliberately differ while a continuation is active after client disconnect. Both preserve a follow-up authorization that has already parked. The high-level API is transport-neutral for successful controls and explicit exact-run mutations, but cannot promise identical effects for an abruptly lost response without a separate server decision.
- Authorization status is a closed server state machine carried in a string field. The ordinary event union keeps the raw string open, while the lifecycle fails closed on an unknown status; a future status addition therefore requires an additive SDK update.
- A status-only flow can work without prompt-free controls, but a continuation that asks permission cannot progress through the transport-neutral high-level API on a server that lacks `prompt_free_controls`.
- A plan-originated ask has no prompt-free transport-parity control today. The lifecycle yields it but does not let `onPermissionAsk` or `resolveAsk()` misclassify it; application policy or explicit cancellation must handle the resulting park.
