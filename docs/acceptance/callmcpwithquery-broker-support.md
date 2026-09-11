# CallMcpWithQuery broker support — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** broker-backed MCP result narrowing
**Status:** proposed, 2026-09-09. Implementation exists locally under the directing user's explicit workflow waiver, live-verified against a real broker-backed deployment (see "Implementation evidence" below); the directing user has now explicitly requested a PR be opened. No merge is claimed — human review and merge remain required.
**Delivery:** Split. The directing user explicitly waived the plan spine for this local implementation; this is not a claim of plan approval.
**Expected tasks:** implementation, aggregate verification, and live end-to-end verification completed locally under the explicit waiver; no merge is claimed.
**Issue:** [stacklok/mecatl#1304](https://github.com/stacklok/mecatl/issues/1304).
**Plan PR:** this PR.
**Approved baseline:** absent; implementation was explicitly authorized without one.

`CallMcpWithQuery` remains the existing single model-facing `{server, tool, args?, jq_filter}` tool. Direct-manager calls keep their current path. For a broker-backed tool, it invokes only through the current session's already attached broker wrapper, then returns only the bounded jq projection; it never bypasses the attachment with a harness-owned upstream connection or records a successful raw response. This does not prohibit the concrete broker transport's existing connections: anonymous routes still connect to the configured `profile.URL`, as the baseline `anonymousCaller` does; protected routes still use the broker endpoint. The query transport preserves those destinations and filters before returning across the attachment boundary.

**Implementation steer:** Individual implementation workers must not run full-repository `task test` or `task lint`; run only task-scoped tests and lint/build checks. The accumulator/final verification stage owns full-repository gates once after integration.

## Human decisions

- [x] **Broker operation:** Should broker-backed queries use the session's existing attachment and its ordinary route invocation/authorization gates? — Decision: yes. The attachment performs one bounded query operation through the same frozen route and authorization path as its registered tool wrapper; no alternate manager, connection, or authorization path is allowed.
- [x] **Projection boundary:** Should the raw broker result cross from the attachment to the harness for filtering? — Decision: no. The attachment returns only the bounded filtered result or a bounded error; jq uses ADR 0063's JSON selection and limits.
- [x] **Scope:** Should this plan redesign target-aware engine dispatch, permissions, pending authorization, or lifecycle behavior? — Decision: no. Those existing behaviors, including the authority evaluator's `CallMcpWithQuery` target capability check, remain unchanged.

## Interface contract

- **gRPC / protobuf:** None — no protocol, message, or field changes.
- **Exported Go APIs / interfaces:** No `engine/` API change. Add only a root-internal attachment query seam returning the bounded projection, not raw results or upstream handles. The catalogued broker query wrapper implements existing `tool.AuthorizationRequester`: `RequestAuthorization(ctx, call)` delegates to the exact native target requester (or returns `required=false` for an unprotected target); `AbortAuthorization(ctx, authorization)` cancels the exact reference through the bound attachment, preserving the interface's cleanup/fail-closed contract. Presentation, status, and cancellation remain the Service's existing attachment operations, not new requester methods.
- **Tool schemas:** None — retain the schema. Register one session-bound `CallMcpWithQuery` when eligible attachment targets exist, including broker-only factories with `globalMgr == nil`; preserve the existing usage instruction in the built tool specification.
- **CLI / config:** None — no flags, settings, defaults, or routing controls.
- **Events / persistence:** No shape changes or mapping cache. `PendingAuthorization.Call` retains the hook-effective query envelope (including jq). Request and execute deterministically derive the same native call ID, exact target name, and byte-identical normalized args from that envelope and the bound attachment; broker `callHash` and replay claims therefore agree. Live/restarted continuation looks up the query wrapper by the persisted envelope name and returns filtered output; unavailable broker state or binding mismatch fails closed, never adopts another source. Raw successful payloads never enter conversation, events, audit, or durable log.
- **Security / authority:** A broker query resolves only through the current session attachment's frozen catalogue and invokes the same wrapper route, including existing authorization. Unknown, stale, closed, foreign-session, or unenrolled routes fail before upstream invocation; no direct fallback, rebind, hedge, retry, or replay follows a possibly delivered call.
- **Compatibility / migration:** Direct-manager behavior, existing engine dispatch/permission/hook/authorization behavior, and broker attachment lifecycle remain unchanged. This is additive broker support for the existing tool.

## In scope — 2 scenarios, in implementation order

### Scenario 1 — Broker-attached query returns only a bounded projection

The broker already exposes attachment-bound frozen wrappers whose `Execute` acquires an attachment operation and revalidates its route (`internal/adapter/mcpbroker/runtime.go` (`sessionTool.Execute`)). The new query operation must reuse that path rather than bypass it; [ADR 0063](../adr/0063-mcp-structured-failclosed-callmcpwithquery.md) supplies the in-memory jq contract.

**Acceptance:**
- AC1.1: A broker-backed `CallMcpWithQuery` with the existing schema executes once through the current session attachment and returns the bounded jq projection, never the successful raw payload.
  - verify: `TestCallMcpWithQueryBrokerSupport_Scenario1_BoundedAttachmentProjection`
- AC1.2: Protected broker routes retain their existing authorization behavior; foreign-session, stale, closed, unknown, or unenrolled targets fail closed without a direct-manager fallback or upstream call.
  - verify: `TestCallMcpWithQueryBrokerSupport_Scenario1_AttachmentIsolationAndAuthorization`
- AC1.3: Invalid jq fails before invocation; after delivery may have occurred, filter or transport failure is bounded and does not retry, rebind, hedge, or replay the upstream call.
  - verify: `TestInvariant_call_mcp_with_query_broker_at_most_once_no_raw_result`
- AC1.4: The real broker-only session factory (`globalMgr == nil`) registers exactly one query wrapper for eligible attachment targets, with the unchanged schema and existing model-visible usage instruction; no eligible targets means no broker query registration.
  - verify: `TestCallMcpWithQueryBrokerSupport_Scenario1_BrokerOnlyFactory`, `TestCallMcpWithQueryBrokerSupport_BrokerOnlyServiceRegistration`
- AC1.5: A protected query without a grant parks through the catalogued requester. Grant resumes through the query envelope with filtered output; deny/cancel executes nothing. Live and restarted host continuation preserve the exact native call/hash and jq; lost broker state fails closed. Abort uses the exact authorization reference, never another session's transaction.
  - verify: `TestCallMcpWithQueryBrokerSupport_Scenario1_AuthorizationContinuation`

### Scenario 2 — Direct-manager query remains unchanged

The existing direct implementation owns structured-content-first selection and bounded jq evaluation (`internal/adapter/mcp/callmcpwithquery.go` (`callMcpWithQueryTool.Execute`)); broker attachment presence must not alter that path, as retained by [ADR 0063](../adr/0063-mcp-structured-failclosed-callmcpwithquery.md).

**Acceptance:**
- AC2.1: Direct-manager queries preserve their existing connection, JSON-source precedence, jq caps, schema, and result behavior.
  - verify: `TestCallMcpWithQueryBrokerSupport_Scenario2_DirectCompatibility`
- AC2.2: Existing target authority evaluation remains intact: `CallMcpWithQuery` spends the addressed `mcp__<server>__<tool>` capability without changing ordinary permission, hooks, dispatch, pending authorization, or audit semantics.
  - verify: `TestADR_0234_CallMcpWithQueryBrokerSupport_AuthorityUnchanged`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Target-aware engine dispatcher or permission migration | Separate proposal | This broker-support fix does not alter engine lifecycle semantics. |
| `PendingAuthorization`/snapshot/API changes | Separate proposal | No new parked-call representation is needed. |
| Client MCP, inline/specialist MCP, remote broker transport, or harness connections bypassing the broker attachment | Later scoped plan | The current session attachment is the sole broker authority; existing concrete broker transport connections are unchanged. |
| Raw-result persistence or a general JSON-query facility | Not planned | ADR 0063 keeps filtering bounded and in memory. |

## Definition of done

1. The scenario proofs land and `task ac-trace-strict` resolves them when this plan becomes `landed`.
2. Final integration runs applicable `task lint`, `task test`, `task docs`, and `task api:check`; `go run ./cmd/mecademo` remains green for runtime changes.
3. The implementation PR links this approved Plan / Interface PR and reports internal-seam and compatibility conformance.
4. Update living architecture and public `user-docs/` for shipped broker query support in the implementation PR; run `task site:build`.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Implementation evidence (workflow waiver)

The directing user explicitly waived the acceptance-plan spine and requested this
local implementation without approval, merge, push, or PR. The following focused
proofs ran in this workspace:

- `TestCallMcpWithQueryBrokerSupport_Scenario1_BoundedAttachmentProjection` — the
  attachment invokes the dedicated bounded-query transport exactly once; the
  ordinary/raw caller is a test failure and only the projection is returned.
- `TestCallMcpWithQueryBrokerSupport_Scenario1_AttachmentIsolationAndAuthorization`
  — unknown and closed attachment targets fail before the query transport.
- `TestInvariant_call_mcp_with_query_broker_at_most_once_no_raw_result` — invalid
  jq is rejected before target invocation.
- `TestCallMcpWithQueryBrokerSupport_PostDeliveryFailureIsBoundedAndNotReplayed`
  — a query-transport/projection failure after delivery is rendered without its
  raw canary and is not replayed.
- `TestCallMcpWithQueryBrokerSupport_Scenario1_AuthorizationDelegatesExactNativeCall`
  — request and abort use the normalized native target/call identity.
- `TestCallMcpWithQueryBrokerSupport_Scenario1_BrokerOnlyFactory` — the real
  broker-only factory emits exactly one query specification to the model, with
  byte-identical schema and description; no targets means no query tool.
- `TestCallMcpWithQueryBrokerSupport_Scenario1_AuthorizationContinuation` — a real
  query envelope parks through the Service and broker requester. Both the live
  host and a reconstructed host over the saved snapshot resume grants to the
  projection only; deny/cancel execute nothing. The broker transaction binds the
  exact normalized native hash, foreign abort/use fails, repeated execution is
  refused, and lost state cannot adopt a new binding. Restart simulates a host
  crash with broker state surviving, not graceful shutdown (which cancels asks).
- `TestCallMcpWithQueryBrokerSupport_BundledToolHiveProjection` — the bundled
  process's anonymous transport calls an offline Streamable HTTP MCP server once
  per query, selects structured content over contradictory text, narrows an
  over-25KB result, and returns bounded errors for jq failure/oversized output.
- `TestCallMcpWithQueryBrokerSupport_UnenrolledTargetDoesNotInvoke` — an unenrolled
  protected process advertises no query tool and refuses a forged wrapper target.
- `TestCallMcpWithQueryBrokerSupport_Scenario2_DirectCompatibility` — direct
  structured/text precedence, error behavior and output cap remain unchanged.
- `TestADR_0234_CallMcpWithQueryBrokerSupport_AuthorityUnchanged` — the real broker
  query wrapper spends its addressed native capability; another reachable MCP
  capability cannot authorize the query.

Focused broker, direct-query, factory, and Service-registration proofs passed with
`-race`. Final integration passed `task lint`, `task test` (including standalone
module checks), `task docs`, `task api:check`, `task site:build`, and the full
offline `go run ./cmd/mecademo` session. Review found no remaining blockers or
important findings; one advisory remains to link existing direct-manager
connectivity proofs alongside AC2.1. This evidence does not claim approval,
merge, or landed status.

**Live end-to-end verification** (real kind cluster, Keycloak, and the
connector-gateway.example.com fixture backend, mecak8s broker-only mode — no
direct/global MCP manager configured): confirmed `CallMcpWithQuery` is
registered in a broker-only session (previously absent entirely, per the
tracked gap in issue #1304); confirmed it routes through the session's
existing broker attachment and its normal lazy per-tool authorization (the
same grant machinery, server debug logs, and continuation path as an ordinary
protected tool call — no separate/bypass authorization surfaced); confirmed a
direct call to the same tool legitimately exceeds mecatl's 25000-byte output
cap, and confirmed `CallMcpWithQuery` with a genuinely narrowing jq filter
(`keys`) successfully returns the bounded projection where the direct call
cannot. A non-narrowing filter (`.`) correctly hits the query wrapper's own
oversized-output rejection rather than the raw payload — expected per AC1.3,
though its error message is generic rather than actionable; tracked as a
known, non-blocking follow-up (see "Deferred decisions and known risks").

## Deferred decisions and known risks

- The implementation may place the bounded projection in the attachment or its concrete broker adapter, but it must preserve the fixed behavior above and reuse the registered wrapper's route/authorization path.
- A remote operation can succeed before jq evaluation reports an error; the result must say so without replaying the call.
- `attachmentQueryTool.Execute` (`internal/adapter/mcpbroker/query.go`) currently
  flattens every failure from the underlying tool's `Execute` (a pre-transport
  replay-ledger rejection, a genuine transport failure, and an oversized-output
  rejection after a successful call) into one generic message ("target may have
  succeeded; automatic replay refused..."). Live-verified this is misleading in
  the oversized-output case specifically: the direct-call path already gives
  actionable guidance ("narrow your filter, e.g. try a specific field"), and
  the query wrapper should too rather than reusing wording meant for a
  genuinely ambiguous transport outcome. Non-blocking for this PR (the
  underlying behavior is correct — outputs are never silently truncated or
  replayed), but worth a small follow-up to distinguish the failure reasons.
