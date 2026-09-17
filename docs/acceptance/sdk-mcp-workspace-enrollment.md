# TypeScript SDK MCP connector inventory and workspace enrollment — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — this adds durable public TypeScript SDK methods, projections, and closed workflow-state vocabularies for session-scoped MCP connector enrollment.
**Decision record:** [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md)
**Phase:** ergonomic TypeScript SDK MCP workspace enrollment
**Status:** proposed, 2026-09-17. The directing user approved the exact public SDK surface and explicitly authorized stacking implementation before the Plan / Interface PR merges.
**Delivery:** Split. The exported SDK contract, correlation rules, ephemeral presentation value, and terminal-state semantics merit approval before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1467](https://github.com/stacklok/mecatl/issues/1467).
**Plan PR:** pending creation from this proposed contract
**Approved baseline:** absent until the Plan / Interface PR merges

An SDK consumer can inspect one owned session's broker connector inventory and drive its
whole-bundle workspace enrollment without constructing generated requests or raw descriptor
calls. The surface binds each request to the existing `Session`, returns SDK-owned readonly
values, and preserves the server's opaque enrollment correlation.

The SDK does not become an enrollment controller. It neither polls nor stores workflow state,
opens a browser, logs or retains the ephemeral presentation URL, selects a connector, or
reimplements server eligibility and authorization. Applications decide when to call the methods
and how to present the returned state. This applies the thin public-surface boundary in
[ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md) to the existing broker
protocol described by [the architecture guide](../architecture.md#protected-resource-discovery).

## Human decisions

- [x] Public API location and signatures — Decision: add direct methods on the existing `Session` handle, with no nested `session.mcp` resource, new lifecycle handle, or duplicate `client.mcp` convenience.
- [x] State vocabulary — Decision: use SDK-owned closed constant/type pairs for every current connector and enrollment state, with future or empty state strings normalized to the typed `Unknown` member rather than widened to an untyped string or rejected.
- [x] Projection and correlation rules — Decision: return detached readonly values; require positive required-service counts, nonempty enrollment IDs, a different replacement ID from retry, exact cancel correlation with every known status terminal and `Unknown` left uninterpreted, and a pending-only absolute HTTP(S) presentation URL.
- [x] Capability and workflow policy — Decision: use the SDK's shared API-major negotiation followed by one target RPC, with ordinary typed server failures, no separate capability/eligibility preflight, no client-side eligibility decisions, no polling interval, and no automatic retry.
- [x] Failure visibility — Decision: expose `WorkspaceEnrollmentStatus.Failed` on the immediate control result as the only failure representation; connector inventory remains nonhistorical and reports `not_started` after terminal cleanup, matching the existing protocol rather than adding client-side state or a wire change.
- [x] Presentation URL custody — Decision: expose a nonempty ephemeral URL only on the immediate returned value; the SDK never opens, renders, logs, caches, or persists it.
- [x] Documentation and release notes — Decision: ship TSDoc, API reports, and one focused user guide in the implementation PR; cross-surface runnable examples remain owned by #1471, and the merged #1482 workflow generates the eventual SDK changelog entry instead of a manual edit here.

## Interface contract

- **gRPC / protobuf:** None — the SDK consumes the existing unary `ListSessionMcpConnectors`, `ConnectWorkspaceServices`, `RetryWorkspaceEnrollment`, and `CancelWorkspaceEnrollment` descriptors and their current messages. No field, number, route, generated declaration, capability bit, or error registry changes.
- **Exported Go APIs / interfaces:** None — server and mecatui Go APIs already implement and consume the complete protocol. This plan changes only the hand-written TypeScript SDK and its documentation.
- **Tool schemas:** None — connector inspection and workspace enrollment are client-to-server operations, not model-facing tools. No tool name, input, result, or dispatch behavior changes.
- **CLI / config:** None — no flag, environment variable, settings key, default, credential source, or precedence changes.
- **Events / persistence:** None — the SDK adds no event, snapshot field, durable state, cache, timer, or migration. It returns the server's current unary projection and keeps no enrollment state after a method resolves.
- **Security / authority:** Every operation uses the existing authenticated caller, matching session ownership, optional exact session-affinity header, broker binding, run-entry serialization, and server-side enrollment eligibility. Connector rows remain display-only and never become routing handles. The SDK accepts no backend selector or credential, and it does not expose broker secrets, callback state, authorization codes, or tokens. A nonempty presentation URL is an ephemeral application-facing launch value returned only to the caller; the SDK does not log, retain, or act on it.
- **Compatibility / migration:** This is an additive hand-written SDK minor surface under API major 1. Existing raw/generated calls, `client.mcp` methods, and existing session methods remain unchanged. Future or empty state strings normalize to the applicable typed `Unknown` member. Structurally malformed successes — including missing IDs, nonpositive required-service counts, invalid presentation URLs, unchanged retry correlations, and mismatched or known-nonterminal cancel correlations — fail closed with a generic `ProtocolError` that carries no rejected ID, state, URL, or cause. Server rejections retain established `ServerError` codes and request IDs. The root, Node, and Deno exports, API Extractor reports, and generated TypeScript SDK reference grow deliberately. #1471 owns cross-surface runnable examples, while the merged #1482 workflow owns the eventual changelog entry.

The exact SDK-owned public vocabulary and projections are:

```ts
export const McpConnectorAvailability = {
  Available: "available",
  Unavailable: "unavailable",
  Unknown: "unknown",
} as const;
export type McpConnectorAvailability =
  (typeof McpConnectorAvailability)[keyof typeof McpConnectorAvailability];

export const McpConnectorEnrollmentState = {
  NotRequired: "not_required",
  NotStarted: "not_started",
  Pending: "pending",
  Completed: "completed",
  Unknown: "unknown",
} as const;
export type McpConnectorEnrollmentState =
  (typeof McpConnectorEnrollmentState)[keyof typeof McpConnectorEnrollmentState];

export const McpConnectorCatalogueState = {
  Hidden: "hidden",
  Declared: "declared",
  Discovered: "discovered",
  Unknown: "unknown",
} as const;
export type McpConnectorCatalogueState =
  (typeof McpConnectorCatalogueState)[keyof typeof McpConnectorCatalogueState];

export interface McpConnectorStatus {
  readonly name: string;
  readonly catalogueState: McpConnectorCatalogueState;
  readonly toolCount: number;
}

export interface McpConnectorInventory {
  readonly availability: McpConnectorAvailability;
  readonly enrollmentState: McpConnectorEnrollmentState;
  readonly connectors: readonly McpConnectorStatus[];
  readonly totalConnectors: number;
  readonly truncated: boolean;
}

export const WorkspaceEnrollmentStatus = {
  Pending: "pending",
  Connected: "connected",
  Denied: "denied",
  Cancelled: "cancelled",
  Expired: "expired",
  Failed: "failed",
  Unknown: "unknown",
} as const;
export type WorkspaceEnrollmentStatus =
  (typeof WorkspaceEnrollmentStatus)[keyof typeof WorkspaceEnrollmentStatus];

export interface WorkspaceEnrollment {
  readonly enrollmentId: string;
  readonly status: WorkspaceEnrollmentStatus;
  readonly requiredServices: number;
  readonly presentationUrl?: string;
}
```

The exact additions to `Session` are:

```ts
interface Session {
  listMcpConnectors(options?: RequestOptions): Promise<McpConnectorInventory>;
  connectWorkspaceServices(options?: RequestOptions): Promise<WorkspaceEnrollment>;
  retryWorkspaceEnrollment(
    enrollmentId: string,
    options?: RequestOptions,
  ): Promise<WorkspaceEnrollment>;
  cancelWorkspaceEnrollment(
    enrollmentId: string,
    options?: RequestOptions,
  ): Promise<WorkspaceEnrollment>;
}
```

Among known `WorkspaceEnrollmentStatus` values, `pending` is the only nonterminal value.
`connected` is terminal success; `denied`, `cancelled`, `expired`, and `failed` are terminal
outcomes. `unknown` represents a future or empty wire value and triggers no SDK interpretation
or follow-up. A terminal response likewise triggers no SDK action. `connectWorkspaceServices()`
starts a bundle when none is pending and otherwise performs one server observation.
`retryWorkspaceEnrollment(id)` targets the exact prior correlation and returns a different
replacement correlation. `cancelWorkspaceEnrollment(id)` targets one exact correlation and
accepts the server's truthful terminal result, including `failed` when lost broker state is
settled. Applications may call again according to their own policy. After the SDK's established
shared API-major negotiation, each invocation performs exactly one target RPC.

Connector inventory is a current aggregate observation, not enrollment-attempt history. The
combined surface distinguishes ready (`completed`), pending, failed, and unavailable outcomes:
failure is available only as the immediate `WorkspaceEnrollmentStatus.Failed` control result,
while a later inventory read intentionally reports `not_started` after terminal cleanup. The SDK
does not cache that failure or manufacture a per-connector failed state.

`McpConnectorInventory.enrollmentState` is a separate aggregate observation vocabulary:
`not_required`, `not_started`, `pending`, `completed`, or `unknown`. It is not converted into a
`WorkspaceEnrollmentStatus`. Future or empty availability, aggregate-enrollment, catalogue, and
workspace-status strings normalize to their typed `Unknown` member. Likewise, `hidden` or
`unknown` catalogue state means a zero
`toolCount` must not be interpreted as a discovered empty catalogue. `availability` describes
whether the process-local broker snapshot can be read, not connector health, credential validity,
session installation, persistence, authorization freshness, or prompt readiness.

A `presentationUrl` is present only for `pending` and, when present, is an absolute HTTP(S) URL.
Losing the response or URL does not make `connectWorkspaceServices()` reproduce it: an application
that still knows the pending correlation explicitly calls `retryWorkspaceEnrollment(id)` to cancel
that correlation and obtain a replacement. Because the target mutation can commit before an abort,
deadline, client close, or transport failure is observed, the SDK neither rolls it back nor sends a
follow-up request automatically.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — one session exposes a typed connector snapshot

The bound `Session` projects the existing owner-scoped, read-only broker inventory without
widening the direct MCP namespace or turning display rows into routing authority. The server
semantics remain those documented by [ADR 0322](../adr/0322-broker-mcp-status.md).

**Acceptance:**
- AC1.1: `listMcpConnectors()` returns the exact detached readonly projection above, including ordered rows, total and truncation facts, and every declared availability, enrollment, and catalogue state.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment.test.ts#c2Vzc2lvbiBNQ1AgY29ubmVjdG9yIGludmVudG9yeSBwcm9qZWN0cyBldmVyeSB0eXBlZCBwcm90b2NvbCBzdGF0ZQ — `sdk/typescript/test/mcp-workspace-enrollment.test.ts :: "session MCP connector inventory projects every typed protocol state"`
- AC1.2: Future or empty state strings project the applicable typed `Unknown` member; a hidden/unknown zero count remains distinguishable from a discovered empty catalogue, and inventory reads invoke no enrollment or direct MCP operation.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment.test.ts#c2Vzc2lvbiBNQ1AgY29ubmVjdG9yIGludmVudG9yeSBwcm9qZWN0cyBldmVyeSB0eXBlZCBwcm90b2NvbCBzdGF0ZQ — `sdk/typescript/test/mcp-workspace-enrollment.test.ts :: "session MCP connector inventory projects every typed protocol state"`

### Scenario 2 — applications drive one whole-bundle enrollment by correlation

The SDK exposes the server's existing whole-bundle operations without copying mecatui's phase
machine. Eligibility and atomic replacement remain in `internal/adapter/server/workspace_enrollment.go`
as summarized in [the implementation notes](../design/IMPLEMENTATION-NOTES.md).

**Acceptance:**
- AC2.1: Connect performs one start-or-observe target RPC and projects pending, connected, denied, cancelled, expired, failed, and unknown responses. Every success has a nonempty enrollment ID and positive required-service count; a nonempty presentation URL is accepted only for pending when it is absolute HTTP(S), and the SDK never opens, renders, logs, caches, persists, or automatically polls it.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment.test.ts#d29ya3NwYWNlIGVucm9sbG1lbnQgbWV0aG9kcyBwcmVzZXJ2ZSBjb3JyZWxhdGlvbiBhbmQgc3RhdGUgdHJhbnNpdGlvbnM — `sdk/typescript/test/mcp-workspace-enrollment.test.ts :: "workspace enrollment methods preserve correlation and state transitions"`
- AC2.2: Retry sends the caller's exact prior enrollment ID and requires a different nonempty replacement ID; cancel sends the exact ID and requires that same returned ID, rejecting `pending` while accepting every known terminal status. A future/empty cancel status projects `Unknown` without being treated as terminal. Neither operation accepts a connector/backend selector or performs a second target request.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment.test.ts#d29ya3NwYWNlIGVucm9sbG1lbnQgbWV0aG9kcyBwcmVzZXJ2ZSBjb3JyZWxhdGlvbiBhbmQgc3RhdGUgdHJhbnNpdGlvbnM — `sdk/typescript/test/mcp-workspace-enrollment.test.ts :: "workspace enrollment methods preserve correlation and state transitions"`
- AC2.3: Missing IDs, nonpositive required-service counts, unchanged retry IDs, mismatched or known-nonterminal cancel results, terminal presentation URLs, and malformed URLs fail with a generic `ProtocolError` whose message, cause, and diagnostics contain no returned ID, state, or URL. Future or empty status strings instead project `Unknown`; unknown and terminal values never cause an automatic retry, reconnect, cancellation, or browser action.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment.test.ts#d29ya3NwYWNlIGVucm9sbG1lbnQgbWV0aG9kcyBwcmVzZXJ2ZSBjb3JyZWxhdGlvbiBhbmQgc3RhdGUgdHJhbnNpdGlvbnM — `sdk/typescript/test/mcp-workspace-enrollment.test.ts :: "workspace enrollment methods preserve correlation and state transitions"`

### Scenario 3 — transport, cancellation, errors, and release surface stay equivalent

All four methods use the descriptor-driven transport and error normalization established by
[the TypeScript SDK architecture](../architecture.md#typescript-sdk), with session affinity
added by the same bound-operation seam as existing `Session` methods.

**Acceptance:**
- AC3.1: Injected gRPC and HTTP clients use the shared API-major negotiation and call the four existing descriptor/route pairs with equivalent requests and projections while preserving caller headers, response callbacks, deadlines, exact session affinity, and exactly one target request per invocation.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment-transports.test.ts#d29ya3NwYWNlIGVucm9sbG1lbnQgaGFzIGVxdWl2YWxlbnQgZ3JwYyBhbmQgaHR0cCByZXF1ZXN0IHNlbWFudGljcw — `sdk/typescript/test/mcp-workspace-enrollment-transports.test.ts :: "workspace enrollment has equivalent grpc and http request semantics"`
- AC3.2: A pre-aborted call starts no target request. Mid-flight abort/deadline and client close retain the SDK's established cancellation/transport error normalization on both transports; authentication, `session_not_found`, `mcp_connector_unavailable`, `failed_precondition`, other server failures, transport faults, and supplied request IDs retain their established typed representations. No failure starts follow-up work.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment-transports.test.ts#d29ya3NwYWNlIGVucm9sbG1lbnQgY2FuY2VsbGF0aW9uIGFuZCBmYWlsdXJlcyBzdGF5IHR5cGVk — `sdk/typescript/test/mcp-workspace-enrollment-transports.test.ts :: "workspace enrollment cancellation and failures stay typed"`
- AC3.3: Abort, deadline, close, or a lost transport acknowledgement may occur after a target operation commits; the SDK performs no rollback or automatic observation/retry. An application that loses a pending presentation URL but retains its correlation explicitly calls retry to obtain a distinct replacement.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment-transports.test.ts#d29ya3NwYWNlIGVucm9sbG1lbnQgbG9zdCBhY2tub3dsZWRnZW1lbnRzIGRvIG5vdCB0cmlnZ2VyIGZvbGxvdy11cCB3b3Jr — `sdk/typescript/test/mcp-workspace-enrollment-transports.test.ts :: "workspace enrollment lost acknowledgements do not trigger follow-up work"`
- AC3.4: Root, Node, and Deno entry points export every constant, type, projection, and `Session` method above; API Extractor reports and generated SDK reference carry the same contract without exposing protobuf response types.
  - verify: vitest:sdk/typescript/test/mcp-workspace-enrollment.test.ts#d29ya3NwYWNlIGVucm9sbG1lbnQgZXhwb3J0cyBhbmQgZG9jdW1lbnRhdGlvbiBzdGF5IGNvbXBsZXRl — `sdk/typescript/test/mcp-workspace-enrollment.test.ts :: "workspace enrollment exports and documentation stay complete"`
- AC3.5: TSDoc and `user-docs/building/typescript-sdk/mcp-connectors.md`, linked from the SDK index, document the application flow, separate aggregate and control states, terminal/unknown outcomes, nonhistorical failure visibility, capability discovery, typed errors, ambiguous unary completion, ephemeral URL recovery, and application ownership of polling/browser/presentation policy. #1471 remains the issue-level dependency for runnable examples, and #1482 remains the generated changelog path.
  - verify: inspection — the focused guide and SDK index contain the stated contract; `task docs` and `task site:build` validate generated references and links.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| MCP authorization presentation/recheck/cancel lifecycle | [#1469](https://github.com/stacklok/mecatl/issues/1469) | Authorization has separate run correlation and streaming lifecycle semantics. |
| Browser opening, URL rendering, clipboard, polling cadence, overlays, toasts, and saved-target selection | Application policy | The SDK returns typed values and performs only explicit calls. |
| Per-connector connect/retry/cancel or a backend selector | Separate protocol design | The server contract owns one atomic whole-bundle enrollment. |
| Connector health probes, credential validity, current authorization, or prompt-readiness claims | Separate server capability | Inventory is broker-local publication status only. |
| Durable failed-attempt history or per-connector failure state | Separate protocol design | Existing inventory intentionally collapses terminal cleanup to `not_started`; only the immediate control response exposes `failed`. |
| Credential storage or server authorization/eligibility decisions | Not planned | Existing server and broker authority remains definitive. |
| Generic TUI builtin execution API | Not planned | Reusable capabilities are native typed operations, not command strings. |
| Runnable SDK example | [#1471](https://github.com/stacklok/mecatl/issues/1471) | Cross-surface examples are handled together. |
| Manual SDK changelog edit | Merged release automation [#1482](https://github.com/stacklok/mecatl/pull/1482) | The next SDK release PR generates the changelog from merged SDK commits. |

## Definition of done

1. Applicable `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:build`, `task sdk:api:check`, `task lint`, `task test`, `task docs`, and `task site:build` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green for runtime changes.
4. The implementation PR links the Plan / Interface PR and its full approved commit and reports interface conformance.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- No material behavior or public interface decision is delegated to implementation. All Human decisions above are approved; the directing user's stacking waiver changes PR ordering only, not this contract or merge authority.
- A presentation URL may embed short-lived authorization material. Returning it is necessary for application-owned browser/presentation policy, but any implementation that logs, caches, persists, or includes it in an error is contract drift.
- Capability bits are application guidance, not client-side authorization. A method may still race deployment or session state after a capability read, so only its typed server result is authoritative.
- Any unary control can commit before an abort, deadline, close, or transport fault hides its acknowledgement. Automatic retry would risk mutating the wrong correlation, so recovery remains an explicit application decision.
