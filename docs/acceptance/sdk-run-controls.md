# TypeScript SDK run-ID-addressed controls — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this adds durable public protobuf, HTTP, generated Go, and TypeScript SDK contracts for mutating an explicitly addressed run without owning its event stream.
**Decision record:** [ADR 0346](../adr/0346-run-id-addressed-prompt-free-controls.md)
**Phase:** ergonomic detached run controls
**Status:** proposed, 2026-09-16. The user approved the strict run binding and public SDK shape; ready for Plan / Interface review.
**Delivery:** Split. The new wire methods, bounded rehydration behavior, stale-run rule, and exported SDK resource need human review before implementation changes public contracts.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1630](https://github.com/stacklok/mecatl/issues/1630).
**Plan PR:** added when opened
**Approved baseline:** absent until the Plan / Interface PR merges

Let an SDK consumer that knows a session ID and run ID resolve an ordinary permission ask, cancel the run, steer with text or ordered media, or retract pending steering without owning or creating an event stream. The client binds every operation to that exact run, carries normal request options, and receives bounded typed acknowledgements over both gRPC and HTTP.

The new surface is independent of durable observation: `session.controls(runId)` does not attach or subscribe. Server ownership, permission, run-lifecycle, and media checks remain authoritative, and a delayed control never targets a successor run.

## Human decisions

- [x] Expose controls on a run-ID-addressed resource. — Decision: `session.controls(runId)` returns a lightweight `RunControls`; control authority does not depend on holding the original `Run` or an `AttachedRun` watch.
- [x] Support both transports without a synthetic stream. — Decision: add prompt-free bounded unary gRPC methods and exact HTTP mirrors; no SDK operation opens `Converse` or consumes SSE to obtain an acknowledgement.
- [x] Require strict run identity. — Decision: every new wire request carries non-empty `expected_run_id`; no new API offers the legacy unqualified behavior.
- [x] Reject late steering instead of promoting it. — Decision: a late, absent, or replacement-run steer returns typed `stale_run_control` and never creates a follow-up run.
- [x] Reuse established SDK inputs and request controls. — Decision: `resolveAsk` takes `PermissionVerdict`, `steer` takes `PromptInput`, and every method accepts `RequestOptions` in the approved signatures.
- [x] Make ordinary ask acceptance observable and atomic. — Decision: add an exported result-returning engine seam that distinguishes resolved, not-pending, and plan-originated asks before mutation, including surfaced child asks; keep `Run.Approve` as the compatibility no-result wrapper.
- [x] Separate request and resumed-run lifetimes. — Decision: authentication, validation, ownership, exact-run checks, and acceptance honor the unary request context; once a rehydrated run is accepted, its engine context and relay are server-owned and survive return, deadline, or caller cancellation until ordinary run/lease/drain shutdown.
- [x] Keep existing owned/attached controls source-compatible. — Decision: existing signatures do not change. Owned `Run` controls keep using their stream. Legacy `AttachedRun` methods do not silently acquire new semantics: `cancel` retains its existing transport behavior and `approve`/`resolveAsk`/`steer` remain local compatibility deferrals that direct callers to `session.controls(runId)` without consulting the now-advertised server feature.
- [x] Use one exact stale/error matrix. — Decision: replacement or ended targets fail `stale_run_control` without disclosing a successor ID; a matching persisted awaiting run may be rehydrated only by ask resolution, while cancel reports `no_active_run` and steer/retraction report `stale_run_control`; unknown/resolved and plan-originated asks have distinct registered errors.
- [x] Strengthen pending-steer retraction. — Decision: `cancelSteer` crosses the same generation, cancelling-state, exact-run, and live lease gates as steer before touching the inbox, with the transition linearized under the service lock.
- [x] Keep HTTP control JSON explicit and strict. — Decision: the four request and response objects, lowercase enum strings, raw-response presence checks, body bounds, unknown-request rejection, and trailing-value rejection are contract, while unrelated unknown response fields remain forward-compatible.
- [x] Reuse `PromptInput` with its real encoding semantics. — Decision: text fragments flatten with one `\n`, media retain order relative to other media rather than arbitrary text/media interleaving, and an empty string steer is rejected locally before transport.

## Interface contract

- **gRPC / protobuf:** Add unary `HarnessService.ResolveRunAsk(ResolveRunAskRequest) returns (ResolveRunAskResponse)`, `CancelRun(CancelRunRequest) returns (CancelRunResponse)`, `SteerRun(SteerRunRequest) returns (SteerRunResponse)`, and `CancelRunSteer(CancelRunSteerRequest) returns (CancelRunSteerResponse)`. Append these messages after the current run-control messages without renumbering existing fields. `ResolveRunAskRequest` fields are `session_id = 1`, `expected_run_id = 2`, `ask_id = 3`, `verdict = 4`; `ResolveRunAskResponse` fields are `run_id = 1`, `ask_id = 2`. `CancelRunRequest` fields are `session_id = 1`, `expected_run_id = 2`; `CancelRunResponse` has `run_id = 1`. `SteerRunRequest` fields are `session_id = 1`, `expected_run_id = 2`, `text = 3`, `parts = 4`, `message_id = 5`; `SteerRunResponse` fields are `outcome = 1`, `run_id = 2`, `message_id = 3`. `CancelRunSteerRequest` fields are `session_id = 1`, `expected_run_id = 2`, `message_id = 3`; `CancelRunSteerResponse` has the same three response fields. Both outcome fields reuse `SteerOutcome`. Request session/run/ask IDs are non-empty where present, verdict must be `DENY`, `ALLOW_ONCE`, or `ALLOW_ALWAYS`, steer has non-empty text and/or parts, and the existing media and 64-code-point message-ID bounds apply. Existing `Converse` frames and legacy HTTP control routes do not change.
- **Exported Go APIs / interfaces:** Generated `mecatlv1.HarnessServiceClient` and `HarnessServiceServer` gain the four unary methods and eight request/response message types above. In `engine/agent`, add `type AskResolution uint8` with `AskResolutionNotPending`, `AskResolutionResolved`, and `AskResolutionPlanOriginated`, plus `func (*Run) ResolveOrdinaryAsk(askID string, verdict session.ApprovalVerdict) AskResolution`. The method atomically resolves a pending non-plan ask on the run or a surfaced child; a plan result leaves the ask and child route pending. Existing `(*Run).Approve` keeps its signature and all-origin, unknown-is-no-op behavior. This additive engine API requires `task api:update` and an `engine/CHANGELOG.md` Added entry. Server-only transitions add registered `ErrAskNotPending` / `ask_not_pending` and `ErrPlanResolutionRequired` / `plan_resolution_required`, both FailedPrecondition / HTTP 409, and reuse `ErrStaleRunControl` elsewhere.
- **Tool schemas:** None — these are client-to-server run controls, not model-visible tools; permission, Shell, subagent, team, and steer tool/event schemas remain unchanged.
- **CLI / config:** None — no flag, environment variable, settings key, default, or precedence changes. Listener enablement still determines whether the corresponding transport is reachable.
- **Events / persistence:** No event or snapshot schema changes. Existing approval, steer echo/outcome, permission-retract, and result events remain the durable observation path. The unary request context owns all work until a rehydrated resolution is atomically accepted. Acceptance transfers the run to a cancellation-detached, server-owned engine/relay context that preserves ordinary event logging, awaiting/terminal persistence, auto-approval observation, `FinishRun`, lease loss, drain, and service-close cancellation; returning or cancelling the unary request cannot cancel or wedge it. The acknowledgement never carries events. The in-memory pending-steer reset-on-crash contract is unchanged.
- **Security / authority:** Every method uses the existing authenticated caller, optional exact `X-Mecatl-Session-ID` affinity, session-ownership check, lease/generation gate, permission-ask lifecycle, and atomic live-run transition. `expected_run_id` is mandatory and compared at the service transition that would mutate the run. Foreign/missing sessions retain absence-equivalent errors; a stale exact run returns registered `stale_run_control`, and `checkExpectedRun` no longer includes the replacement run ID in caller-visible detail. `resolveAsk` accepts policy, hook-originated, and surfaced-child permission asks but not plan-originated asks; the latter retain the dedicated live plan responder and `Session.resolvePlan()` authority/choreography. `cancelSteer` validates the captured run-entry generation, rejects a cancelling state, and proves the live lease while holding the same lock that linearizes inbox mutation. Request headers and secret-shaped transport metadata are neither persisted nor reflected in acknowledgements.
- **Compatibility / migration:** This is an additive protobuf, exported engine, and SDK minor surface under API major 1. The server advertises the complete four-operation set with new open feature identifier `prompt_free_controls`, added to Go's feature registry and TypeScript's `ServerFeature`. `session.controls()` creates the handle locally, but every operation gates on that feature using the same `RequestOptions` as its unary call. Missing feature support raises typed `UnsupportedFeatureError`; an advertised-but-missing RPC remains a typed server/protocol failure. The SDK never falls back to legacy HTTP routes or opens `Converse`. Existing raw/generated calls and owned `Run` controls remain unchanged. `AttachedRun` signatures remain unchanged: its approval/resolve/steer methods, whose return types permit no successful value, continue to reject locally with `UnsupportedFeatureError("attached_run_controls")` and direct callers to `session.controls(runId)`; its legacy `cancel` keeps its current transport behavior. These methods never treat advertised `prompt_free_controls` as support for a signature that cannot expose the new contract. Existing `http_steer`, unqualified late-steer promotion, and old-server behavior remain unchanged. The raw RPC catalog, HTTP route inventory, protobuf generation, engine API snapshots/changelog, API Extractor reports, generated SDK reference, and Go/TypeScript parity gates grow deliberately. User docs and an executable example change in the implementation PR; the release workflow from merged PR #1482 generates the SDK changelog entry, so no manual SDK changelog edit is made.

The HTTP mirrors use exact snake-case request and acknowledgement shapes. Optional keys may be omitted; required response keys must be present even when `message_id` is empty:

```text
POST /v1/sessions/{id}/controls/resolve-ask
request  {"expected_run_id": string, "ask_id": string, "verdict": "deny"|"allow_once"|"allow_always"}
response {"run_id": string, "ask_id": string}

POST /v1/sessions/{id}/controls/cancel
request  {"expected_run_id": string}
response {"run_id": string}

POST /v1/sessions/{id}/controls/steer
request  {"expected_run_id": string, "text"?: string, "parts"?: [{"kind":"image"|"audio", "mime_type":string, exactly one of "data":base64 or "url":https-url}], "message_id"?: string}
response {"outcome":"accepted"|"appended", "run_id":string, "message_id":string}

POST /v1/sessions/{id}/controls/cancel-steer
request  {"expected_run_id": string, "message_id"?: string}
response {"outcome":"retracted"|"none_pending", "run_id":string, "message_id":string}
```

All four bodies are non-empty, bounded by the existing prompt/control HTTP limit, reject `session_id`, unknown keys, trailing JSON, non-string enum spellings, and uppercase or legacy enum aliases. HTTP verdict, content-kind, and outcome values are exact lowercase strings; gRPC uses the declared protobuf enums and rejects unspecified or numeric-unknown outcomes. The SDK inspects the registered raw HTTP JSON before protobuf normalization so presence and JSON type are proved for every required key, including an empty correlated `message_id`; unrelated unknown response keys remain ignored for additive compatibility.

The exact SDK surface is:

```ts
interface RunSteerOptions {
  messageId?: string;
}

interface RunSteerAcknowledgement {
  readonly outcome: "accepted" | "appended";
  readonly runId: string;
  readonly messageId: string;
}

interface RunSteerCancellationAcknowledgement {
  readonly outcome: "retracted" | "none_pending";
  readonly runId: string;
  readonly messageId: string;
}

interface RunControls {
  readonly sessionId: string;
  readonly runId: string;
  resolveAsk(
    askId: string,
    verdict: PermissionVerdict,
    requestOptions?: RequestOptions,
  ): Promise<void>;
  cancel(requestOptions?: RequestOptions): Promise<void>;
  steer(
    prompt: PromptInput,
    options?: RunSteerOptions,
    requestOptions?: RequestOptions,
  ): Promise<RunSteerAcknowledgement>;
  cancelSteer(
    options?: RunSteerOptions,
    requestOptions?: RequestOptions,
  ): Promise<RunSteerCancellationAcknowledgement>;
}

interface Session {
  controls(runId: string): RunControls;
}
```

The service result matrix is normative:

| Authoritative state after ownership/generation/lease checks | `resolveAsk` | `cancel` | `steer` | `cancelSteer` |
|---|---|---|---|---|
| matching live, non-cancelling run | resolve one pending ordinary/root-or-surfaced-child ask; `ask_not_pending` for unknown/resolved; `plan_resolution_required` for plan origin | accept once; a lost terminal/cancelling race is stale | `accepted` or `appended`; a lost terminal race is stale | `retracted` or `none_pending` |
| matching persisted awaiting run, no live run | exact ordinary ask rehydrates; wrong/resolved ask is `ask_not_pending`; plan origin is `plan_resolution_required` | `no_active_run` | `stale_run_control` | `stale_run_control` |
| addressed run ended, was cancelled, or is cancelling | `stale_run_control` | `stale_run_control` | `stale_run_control` | `stale_run_control` |
| a replacement run is current | `stale_run_control` | `stale_run_control` | `stale_run_control` | `stale_run_control` |

An unknown or foreign session is `session_not_found` for every row. A lease owned elsewhere remains `session_leased_elsewhere`; draining, request cancellation, and other registered transition failures retain their existing typed codes. No stale error detail includes the current/replacement run ID.

## In scope — 5 scenarios, in implementation order

### Scenario 1 — one feature exposes four bounded prompt-free controls

The wire change replaces ADR 0288's deferred transport asymmetry with explicit unary operations while preserving [ADR 0304's descriptor/HTTP completeness contract](../adr/0304-typescript-sdk-public-surface-and-release.md).

**Acceptance:**
- AC1.1: The generated descriptor contains exactly the four declared unary RPCs, field numbers, validation annotations, request/response types, and operation-specific response shapes; code generation is fresh for Go and TypeScript.
  - verify: `TestSDKRunControls_Scenario1_ProtobufContract`
- AC1.2: Each RPC has one distinct strict HTTP mirror under `/controls/`, each route calls the same Service transition as its gRPC peer, and the raw catalog/inventory maps all four descriptors injectively with no change to `Converse` or legacy control routes.
  - verify: `TestSDKRunControls_Scenario1_TransportRouteParity`
- AC1.3: `prompt_free_controls` is advertised on both listeners only with the complete four-operation implementation; the feature registry, TypeScript known-feature constant, ready document, and compatibility projections stay in parity.
  - verify: `TestSDKRunControls_Scenario1_FeatureRegistryParity`
- AC1.4: Resolve-after-restart returns one bounded JSON or gRPC acknowledgement. If it rehydrates the same persisted awaiting run, exactly one cancellation-detached server relay records, persists, and finishes or re-parks it without tying run lifetime to the request or opening an event response.
  - verify: `TestSDKRunControls_Scenario1_RehydratedResolveIsBounded`
- AC1.5: The unary request context controls decoding, authentication, ownership, validation, and pre-acceptance cancellation. A successfully accepted rehydrated resolution transfers both the engine and relay to the server-owned lifecycle, so returning the acknowledgement, cancelling the caller, or expiring its deadline cannot cancel or wedge that run.
  - verify: `TestSDKRunControls_Scenario1_RehydratedResolveTransfersContextOwnership`
- AC1.6: HTTP uses the four exact request/response objects and lowercase enum spellings in the interface contract. Bodies are bounded and reject missing `expected_run_id`, empty required IDs, unspecified/aliased/numeric verdicts, unknown request fields, trailing JSON, invalid media, oversized message IDs, and path/body ambiguity before any run transition.
  - verify: `TestSDKRunControls_Scenario1_StrictHTTPDecode`

### Scenario 2 — `session.controls(runId)` is a typed transport-parity resource

The ergonomic surface extends the established [TypeScript SDK architecture](../architecture.md#typescript-sdk) without coupling mutation to the durable-watch lifecycle.

**Acceptance:**
- AC2.1: The root, Node, and Deno entry points export `RunControls`, `RunSteerOptions`, `RunSteerAcknowledgement`, and `RunSteerCancellationAcknowledgement`; `Session.controls(runId): RunControls` returns synchronously and the handle exposes readonly `sessionId` and `runId` without probing, watching, or registering a live run.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#c2Vzc2lvbiBjb250cm9scyBleHBvc2VzIHRoZSBydW4gY29udHJvbHMgcmVzb3VyY2Ugd2l0aG91dCBhdHRhY2hpbmc — `sdk/typescript/test/run-controls.test.ts :: "session controls exposes the run controls resource without attaching"`
- AC2.2: The exact methods are `resolveAsk(askId, verdict, requestOptions?)`, `cancel(requestOptions?)`, `steer(prompt, options?, requestOptions?)`, and `cancelSteer(options?, requestOptions?)`, where both steer option positions accept `{ messageId?: string }`; the input aliases are the existing `PermissionVerdict`, `PromptInput`, and `RequestOptions`.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#cnVuIGNvbnRyb2xzIHNpZ25hdHVyZXMgcmV1c2UgYXBwcm92ZWQgaW5wdXRzIGFuZCBvcHRpb25z — `sdk/typescript/test/run-controls.test.ts :: "run controls signatures reuse approved inputs and options"`
- AC2.3: Every method carries the handle's exact `expected_run_id`, gates on `prompt_free_controls`, performs one control RPC without retry or fallback, and preserves request headers, response header/trailer callbacks, caller cancellation, deadlines, session affinity, client-close cancellation, and typed SDK/server errors over injected gRPC and HTTP transports.
  - verify: vitest:sdk/typescript/test/run-controls-transports.test.ts#cnVuIGNvbnRyb2xzIHByZXNlcnZlIHJlcXVlc3Qgb3B0aW9ucyBhbmQgZ2F0ZSBldmVyeSBvcGVyYXRpb24 — `sdk/typescript/test/run-controls-transports.test.ts :: "run controls preserve request options and gate every operation"`
- AC2.4: Existing owned `Run` stream controls, disposal, and watch bookkeeping remain unchanged; controls neither require nor create an attachment. `AttachedRun` signatures stay unchanged, its legacy cancel retains current transport behavior, and its approval/resolve/steer compatibility methods reject locally as `attached_run_controls` even when `prompt_free_controls` is advertised, directing the caller to `session.controls(attached.runId)`.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#bGVnYWN5IHJ1biBhbmQgYXR0YWNoZWQgY29udHJvbHMga2VlcCB0aGVpciBjb21wYXRpYmlsaXR5IGNvbnRyYWN0 — `sdk/typescript/test/run-controls.test.ts :: "legacy run and attached controls keep their compatibility contract"`

### Scenario 3 — ask resolution and cancellation touch only the addressed run

Run identity follows [ADR 0249](../adr/0249-durable-run-identity.md), while permission and lifecycle authority stay on the server.

**Acceptance:**
- AC3.1: `agent.Run.ResolveOrdinaryAsk` atomically returns resolved, not-pending, or plan-originated and handles both the run's own ask and a surfaced child ask; plan-originated and not-pending outcomes do not consume an ask. Existing `Run.Approve` remains the all-origin, no-result compatibility wrapper, and the intentional API addition is recorded in the engine snapshot and changelog.
  - verify: `TestSDKRunControls_Scenario3_AtomicOrdinaryAskResolution`
- AC3.2: `resolveAsk` sends one ordinary ask ID and the full `deny|allow_once|allow_always` verdict. The server maps unknown/resolved to `ask_not_pending` and plan-originated to `plan_resolution_required` without setting the persistence race marker, mutating mode, or starting a continuation; valid root or surfaced-child resolution echoes the exact root run/ask correlation before the SDK resolves `void`.
  - verify: `TestSDKRunControls_Scenario3_ResolveOrdinaryAsk`
- AC3.3: `cancel` signals only the exact current run and validates the echoed run ID before resolving `void`; its terminal `cancelled` result remains observable only through an owned stream or durable watch.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#Y2FuY2VsIHZhbGlkYXRlcyBleGFjdCBydW4gYWNrbm93bGVkZ2VtZW50IGJlZm9yZSByZXNvbHZpbmc — `sdk/typescript/test/run-controls.test.ts :: "cancel validates exact run acknowledgement before resolving"`
- AC3.4: Every operation follows the normative service matrix. An ended, cancelling, cancelled, or replacement target is `stale_run_control`; only exact ask resolution may rehydrate a matching persisted awaiting run, while cancel returns `no_active_run` and steer/retraction are stale in that row. Delayed controls never land on a newer run, and stale details reveal no replacement run ID.
  - verify: `TestADR_0346_StaleControlCannotMutateSuccessor`
- AC3.5: Missing/mismatched acknowledgement IDs, absent response fields, wrong response JSON types, and malformed enum values raise `ProtocolError` and never appear as successful controls. HTTP validation consults raw JSON so an omitted empty `message_id` is distinguishable from a present empty string; unrelated unknown response fields remain compatible.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#cmVzb2x2ZSBhbmQgY2FuY2VsIHJlamVjdCBtYWxmb3JtZWQgYWNrbm93bGVkZ2VtZW50cw — `sdk/typescript/test/run-controls.test.ts :: "resolve and cancel reject malformed acknowledgements"`

### Scenario 4 — strict multimodal steer and retraction preserve correlation

Steering reuses the existing [multimodal content contract](../adr/0251-multimodal-steer.md) but adopts the exact-run meaning approved for detached controls.

**Acceptance:**
- AC4.1: `steer` accepts the existing string or text/image/audio `PromptInput` and applies the existing local source/MIME/count/size/session-capability checks. Structured text fragments flatten with one `\n`, media retain their order relative to other media, and the wire carries flattened text alongside that media list; the API makes no arbitrary text/media interleaving claim. `steer("")` and an otherwise empty instruction fail locally before feature probing or transport.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#c3RlZXIgcHJlc2VydmVzIGZsYXR0ZW5lZCB0ZXh0IGFuZCBvcmRlcmVkIG1lZGlhIGFjcm9zcyB0cmFuc3BvcnRz — `sdk/typescript/test/run-controls.test.ts :: "steer preserves flattened text and ordered media across transports"`
- AC4.2: A caller-supplied `messageId` is transmitted verbatim; omission transmits empty correlation rather than inventing one. A valid steer returns only `{ outcome: "accepted"|"appended", runId, messageId }` after exact response-correlation validation.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#c3RlZXIgcHJlc2VydmVzIG9wdGlvbmFsIG1lc3NhZ2UgY29ycmVsYXRpb24gYW5kIG5hcnJvd3Mgb3V0Y29tZXM — `sdk/typescript/test/run-controls.test.ts :: "steer preserves optional message correlation and narrows outcomes"`
- AC4.3: A steer that loses the terminal race or names an absent/replacement run returns typed `stale_run_control`, does not return `too_late`, and never promotes or starts a successor run.
  - verify: `TestADR_0346_StrictSteerNeverPromotes`
- AC4.4: `cancelSteer` returns only `{ outcome: "retracted"|"none_pending", runId, messageId }`; it crosses the captured generation, non-cancelling, exact-run, and live-lease gates under the service lock before atomically retracting the complete pending bundle. It remains idempotent when the exact live run has no pending bundle and cannot retract already-drained history.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#Y2FuY2VsU3RlZXIgcmV0dXJucyBuYXJyb3dlZCBjb3JyZWxhdGVkIGFja25vd2xlZGdlbWVudHM — `sdk/typescript/test/run-controls.test.ts :: "cancelSteer returns narrowed correlated acknowledgements"`
- AC4.5: Concurrent drain, cancellation, registry replacement, generation invalidation, and lease loss linearize before or after retraction: success reflects the winning exact-live transition, and every losing arm returns its registered error without touching a successor or an unowned inbox.
  - verify: `TestSDKRunControls_Scenario4_CancelSteerRaceAndLeaseGates`
- AC4.6: Wrong-operation outcomes, unspecified/numeric-unknown outcomes, changed message/run IDs, promoted metadata, missing required raw JSON keys, malformed JSON, and transport-native malformed messages fail with `ProtocolError` rather than widening either acknowledgement union.
  - verify: vitest:sdk/typescript/test/run-controls.test.ts#c3RlZXIgY29udHJvbHMgcmVqZWN0IG1hbGZvcm1lZCBhY2tub3dsZWRnZW1lbnRz — `sdk/typescript/test/run-controls.test.ts :: "steer controls reject malformed acknowledgements"`

### Scenario 5 — real-wire and documentation coverage make reload use practical

The high-level API must work against the same-checkout daemon, not only injected transports, and its release surface follows [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md).

**Acceptance:**
- AC5.1: Node real-wire tests cover gRPC TCP/UDS and HTTP for successful root and surfaced-child resolve, cancel, text/media steer, retraction, the full stale/error matrix, request cancellation/deadline, and an acknowledgement-only approval rehydrate across daemon restart without live provider credentials.
  - verify: vitest:sdk/typescript/e2e/run-controls.e2e.test.ts#cnVuIGNvbnRyb2xzIHdvcmsgb3ZlciBncnBjIHRjcCB1ZHMgYW5kIGh0dHA — `sdk/typescript/e2e/run-controls.e2e.test.ts :: "run controls work over grpc tcp uds and http"`
- AC5.2: Unit tests cover absent feature, advertised-but-unimplemented transport, headers/deadlines/signals, client close, media capability refusal, optional message IDs, every valid outcome, and every malformed acknowledgement arm.
  - verify: vitest:sdk/typescript/test/run-controls-transports.test.ts#cnVuIGNvbnRyb2xzIGNvdmVyIGZlYXR1cmUgb3B0aW9ucyBlcnJvcnMgYW5kIG1hbGZvcm1lZCBtYXRyaWNlcw — `sdk/typescript/test/run-controls-transports.test.ts :: "run controls cover feature options errors and malformed matrices"`
- AC5.3: API Extractor reports and generated SDK reference contain the exact exported surface. `durable-attachment.ts` demonstrates persisting a run ID and using `session.controls(runId)` after obtaining a fresh session handle; the examples project compiles only through published package exports.
  - verify: vitest:sdk/typescript/test/package.test.ts#cnVuIGNvbnRyb2xzIGFyZSBleHBvcnRlZCBkb2N1bWVudGVkIGFuZCBhcGkgcmV2aWV3ZWQ — `sdk/typescript/test/package.test.ts :: "run controls are exported documented and api reviewed"`
- AC5.4: TSDoc plus the TypeScript SDK durable-activity, permissions/plans, and sessions/runs guides explain watch/control independence, strict stale behavior, request options, media/correlation acknowledgements, old-server feature refusal, plan separation, and ambiguous lost unary acknowledgements. The generated release workflow, not this implementation PR, owns the changelog entry.
  - verify: inspection — user documentation and generated reference are content/build artifacts rather than runtime behavior

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Changing or removing `Run` and `AttachedRun` signatures | Later compatibility cleanup | Preserve current signatures. Owned controls keep their stream behavior; legacy attached deferrals direct callers to `Session.controls()`. |
| Plan approval/denial continuation choreography | Existing `Session.resolvePlan()` and live plan responder | `RunControls.resolveAsk` handles ordinary permission asks only. |
| Unqualified late-steer promotion | Existing raw/legacy control surface | Every new control is exact-run and never promotes. |
| Child-run cancellation or steering | Future child-control design | The new resource addresses one root session run, not a delegation child. |
| React/Studio state integration | Downstream client work | This is transport-neutral SDK functionality. |
| Python or other language SDKs | Separate SDK plans | Protobuf additions are available, but no new ergonomic client is designed here. |
| Pending-steer durability across process loss | Future persistence design | Preserve the in-memory reset-on-crash contract. |
| Automatic mutation retries or idempotency keys | Future mutation reliability design | Unary uncertainty is reconciled from authoritative state; the SDK never retries controls. |

## Definition of done

1. `task generate`, `task lint`, `task test`, `task docs`, `task site:build`, and `task api:check` pass.
2. `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:build`, `task sdk:api:check`, `task sdk:docs:check`, `task sdk:examples:typecheck`, and `task sdk:e2e` pass.
3. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
4. `go run ./cmd/mecademo` remains green.
5. Generated Go/TypeScript contracts, the raw RPC catalog, HTTP route inventory, known-feature projections, API reports, generated SDK reference, executable example, living architecture/implementation notes, and public user guides are updated together.
6. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance.
7. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- A unary mutation can be accepted even when its acknowledgement is lost. The SDK does not retry; applications reconcile through `snapshot()`, `attach()`, or `activity()` according to the operation.
- A server-owned rehydrated relay may park again on a later ask with no request open. This is intentional; controls and durable observation remain independent.
- Retaining legacy HTTP and stream controls alongside the new strict routes creates two adapter entry families. Shared Service transitions and parity tests must prevent semantic drift.
- Existing `AttachedRun` control limitations remain visible and use the explicit local `attached_run_controls` deferral until a later compatibility decision changes or deprecates that surface.
