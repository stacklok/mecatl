# Broker MCP status — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** Broker-only mecak8s connector observability
**Status:** integrated, not landed, 2026-09-10. T01 broker inspection, T02 service/API/composition, and T03 client/UI/docs are integrated on the implementation accumulator; delivery remains pending final parent review and aggregate gates.
**Delivery:** Split. Original Plan / Interface PR #1305 merged; the directing human authorized a local-amendment exception for recording these decisions, without a separate amendment PR/merge.
**Expected tasks:** deferred to orchestration
**Issue:** None assigned.
**Plan PR:** Original [Plan / Interface PR #1305](https://github.com/stacklok/mecatl/pull/1305) merged; the decision-recording amendment is local on `plan/broker-mcp-status-decisions`, not separately merged.
**Approved baseline:** Original plan merge `7b07c4fb682468ec96077aa47d2807e08004055a`, supplemented by the explicitly approved local decision record; orchestration records its local commit alongside the original merged baseline.

Make mecatui `/mcp` useful against broker-only mecak8s: list configured connector names and show the owned session's enrollment and catalogue status without contacting upstreams. Preserve direct MCP resources/prompts and the existing enrollment controls. Inventory is not a connection test or a claim of current authorization validity.

This is an approved contract, not shipped behavior. [ADR 0322](../adr/0322-broker-mcp-status.md) explains the separate session-scoped read boundary.

## Human decisions

- [x] Delivery and feature scope — Decision: user approved broker connector inventory plus enrollment/catalogue status, with no probes or new persistence, followed by a Plan / Interface PR; no runtime implementation in that PR.
- [x] Preserve direct MCP semantics — Decision: use a separate broker inspection capability behind the same `/mcp` command; do not enable direct resources/prompts or switch deployment modes as a workaround. Broker and direct MCP are mutually exclusive supported compositions, so the conditional dual-capability blocker is dismissed; no mixed-mode UI or precedence rule is authorized.
- [x] Exact wire/internal contract — Decision: user explicitly approved the specified contract unchanged, including count-only rows, the 256-row response bound, omission of auth class/credential state, and aggregate enrollment states rather than per-connector “connected”.
- [x] Disclosure of configured connector display names before enrollment and unavailable-state behavior after restart — Decision: user explicitly approved requiring `OwnershipEnforced=true` and a verified principal for capability/API availability, then a nonnil matching persisted session owner before disclosure; transport authentication alone is insufficient. Ownerless/foreign/absent sessions remain indistinguishable. Show no historical success or unconditional reconnect instruction when live state is lost.
- [x] Broker-local published catalogue semantics, rather than installed or durable session authority — Decision: user explicitly approved reporting broker publication even if subsequent server engine rebuild or aggregate Save fails; explicitly disclaim session installation, persistence, prompt readiness and current authorization. Reads never repair those failures or settle a persisted pending prompt gate.

- [x] Existing transport gate only — Decision: user explicitly approved existing transport authentication plus enforced ownership, a verified principal and the wired inspector, with no separate listener toggle. Record this clarification locally under the waived amendment-PR requirement and continue implementation; all matching-owner checks remain mandatory.

## Interface contract

The user has explicitly approved this contract, including the local clarification that existing transport authentication applies without a separate listener toggle. The directing human stated “let’s just make the amendment locally no point in spamming the PRs again”, waiving only the separate amendment PR/merge. This local decision record belongs in the eventual implementation PR; no amendment merge is claimed. Changes to the contract still require plan review before implementation.

- **gRPC / protobuf:** Add `ServerCapabilities.mcp_connector_status` as `bool` field 29 in `contracts/proto/mecatl/v1/harness.proto`. True only when the bundled broker inspector is wired, `OwnershipEnforced=true`, and the capability request carries a verified principal after existing transport authentication. This capability does not authorize any particular session. Preserve `mcp` and `workspace_enrollment` semantics. Add `HarnessService.ListSessionMcpConnectors(ListSessionMcpConnectorsRequest) returns (ListSessionMcpConnectorsResponse)`. Request: `string session_id = 1`. Response: `string availability = 1`, `string enrollment_state = 2`, `repeated McpConnectorStatus connectors = 3`, `uint32 total_connectors = 4`, `bool truncated = 5`. Row `McpConnectorStatus`: `string name = 1`, `string catalogue_state = 2`, `uint32 tool_count = 3`. Strings use the closed producer vocabularies below; clients render future/empty values as unknown. No endpoint, tool name, auth class, credential, health, timestamp, or diagnostic fields. HTTP parity: `GET /v1/sessions/{id}/mcp/connectors`, same service method and existing JSON conventions, authenticated and session-affinity-routed, with `Cache-Control: no-store`. After existing transport authentication, the API rejects ownership-disabled deployments with FAILED_PRECONDITION/409 before session lookup; with ownership enabled, missing verified principal is UNAUTHENTICATED/401. Empty/malformed session ID is INVALID_ARGUMENT/400; absent/foreign/ownerless sessions are NOT_FOUND/404 identically; a verified matching owner of a non-broker-bound session or a deployment without the inspector gets FAILED_PRECONDITION/409. Existing transport authentication may reject before service dispatch; there is no separate listener-level inspection policy. Lost runtime state on an otherwise eligible owned session is a successful `unavailable` response, not a raw downstream error. Unexpected internal errors use existing sanitized INTERNAL/500 handling.
- **Exported Go APIs / interfaces:** No public `engine/` change. Add optional root-internal `internal/mcpbroker.ConnectorInspector` with `InspectConnectors(context.Context, session.SessionID, session.ExternalBinding) (ConnectorInventory, error)`. `ConnectorInventory` has `Availability string`, `EnrollmentState string`, `Connectors []ConnectorStatus`, `TotalConnectors uint32`, `Truncated bool`; `ConnectorStatus` has `Name string`, `CatalogueState string`, `ToolCount uint32`. Add `server.Config.MCPConnectorInspector brokercontract.ConnectorInspector` and `(*server.Service).ListSessionMcpConnectors(context.Context, session.SessionID) (brokercontract.ConnectorInventory, error)`. The bundled Runtime implements the optional inspector; do not widen mandatory `Service`/`Attachment` interfaces. Composition wires it only for the bundled broker. Inspector is read-only, examines existing logical state by exact binding, and returns detached values. No AttachSession, enrollment observation, credential refresh, network discovery, catalogue installation, or session persistence is allowed on this path.
- **Tool schemas:** None — `/mcp` is client UI, not a model tool; no tool catalogue, schema, permission, or model prompt changes.
- **CLI / config:** None — existing broker profiles provide connector descriptors; no new flag, config key, health policy, or deployment topology setting.
- **Events / persistence:** None — no event, snapshot field, durable terminal enrollment history, status cache, polling goroutine, or new broker ownership map. Descriptor/provenance data derives from existing process configuration and broker-local published catalogues and shares their lifetime. No saved “connected” state is inferred from persisted tool names. Existing enrollment and lazy-authorization transitions remain unchanged.
- **Security / authority:** Load without reopen/recovery only after enforcing the ownership/principal prerequisites above, and require a nonnil persisted owner matching the verified principal before consulting the inspector. The existing `authorizeSession` compatibility behavior when ownership is disabled is not sufficient; do not change that helper's behavior for unrelated APIs. Route/classify as caller-owned, not shared infrastructure, on every transport; enforce existing affinity and transport authentication, including adding the RPC to the descriptor-based affinity matrix and the HTTP session-route coverage. Inspect only the exact persisted nonempty broker binding under the current synchronization discipline. Reads must not create/adopt a generation, mutate authority, or initiate a network request. Display names are explicitly approved disclosure, not routing handles: repair invalid UTF-8, remove control/format characters, clamp to 128 runes, and never use the display value to route tools. Preserve configured order, return the first 256 rows with truthful total/truncated fields. Derive tool counts from structured connector provenance of the broker-local published catalogue, never parse tool-name prefixes. No URLs, paths, scopes, OAuth endpoints, presentation URLs, headers, tokens, private bindings, provider routing keys, raw errors, or tool definitions cross the response. Foreign/absent responses are identical and reveal no connector count. Refresh is local inspection only.
- **Compatibility / migration:** Additive protobuf capability/RPC; old clients ignore the new bit and old servers leave it false. No store migration or public engine API baseline change. Direct `ListMcpSources`, resources/prompts/groups and their semantics remain unchanged. New mecatui registers `/mcp` when either capability and its corresponding collaborator are available; broker-only mode never issues direct resource/prompt/group calls, including shortcuts. Server/client DTOs remain separated from UI proto types. Bind each response to the requested session and request generation so late responses cannot overwrite a switched session or a newer refresh. Implementation updates generated contracts through `task generate`, the tracked SDK's applicable generated/public surface, and public user docs in the same implementation PR.

### Status semantics

All catalogue and enrollment fields are **broker-local publication facts**, not assertions about the server-installed engine catalogue, persisted session authority, or prompt readiness. `internal/adapter/mcpbroker/workspace_enrollment.go` publishes `completedEnrollment` before `internal/adapter/server/workspace_enrollment.go` rebuilds the session engine, completes the aggregate and saves it. The inspector may therefore report completed/discovered after an engine-build or Save failure. It must neither hide that broker publication nor claim those later steps succeeded, retry them, clear pending state, or save a repair. Counts refer only to the broker's published routes.

An expired/terminal broker transaction can coexist with a persisted pending enrollment that still gates prompts. Reporting no active broker enrollment does not clear that gate or mean prompts are ready. The panel is titled “MCP inventory” and uses friendly labels: `not_required` → “No setup required”, `not_started` → “No active setup”, `pending` → “Setup in progress”, `completed` → “Catalogue ready”, and unknown → “Status unavailable”. Existing enrollment controls remain responsible for progression/settlement; this read does not invoke them.

`availability` is `available` or `unavailable`. Available means the exact committed logical broker incarnation exists and can be inspected; it does not mean a socket is connected. Closed local service handles alone do not lose status while that logical incarnation still exists. Missing/deleted/closed runtime state or binding mismatch yields `unavailable`; configured rows remain visible to the authorized owner, but each has `catalogue_state=unknown`, `tool_count=0`, and `enrollment_state=unknown`. Zero in an unknown row means unavailable information, not an empty catalogue.

`enrollment_state` is `not_required`, `not_started`, `pending`, `completed`, or `unknown`:

- `not_required`: no configured protected connectors.
- `completed`: a complete frozen authenticated enrollment catalogue is published in the broker for this exact incarnation. This is broker publication, not proof of engine installation, aggregate persistence or token validity.
- `pending`: an unexpired, nonterminal pre-prompt enrollment transaction exists and completion has not been published; includes granted consent awaiting the existing control path's discovery. Inspection must not call `ObserveWorkspaceEnrollment`, which can perform discovery.
- `not_started`: authoritative live state exists but there is no active/completed enrollment. This includes terminal attempts no longer active; it means “no active enrollment”, not “never attempted”. The UI renders it as “No active setup”.
- `unknown`: exact live state is unavailable. No stale pending snapshot is projected as a live transaction.

Precedence: unavailable overrides all; otherwise no protected connectors is not_required, completed precedes pending, then not_started. Expiry is computed against the existing broker clock without expiring/mutating a transaction. Terminal details stay on existing enrollment controls; this inventory is not attempt history. Lazy authorization is deliberately not enrollment: a credential obtained by calling a declared tool does not imply discovered tools or completed enrollment. The panel presents the concise “Catalogue status · not a live connection check” line; it does not assert current authorization.

Per-row `catalogue_state` is `hidden`, `declared`, `discovered`, or `unknown`. On an available incarnation, anonymous startup-discovered catalogues are discovered; protected static stand-ins are declared; protected connectors without admitted tools are hidden until discovery. Successful authenticated discovery is discovered even with zero tools. Count only tools in the currently broker-local published catalogue for that connector; observe a coherent pre-freeze or post-freeze snapshot, never a mixture. Here discovered describes connector-level successful discovery, not the provenance of every tool definition: the authoring baseline's `internal/adapter/mcpbroker/workspace_catalogue.go` preserves static definitions in the frozen catalogue. Count those admitted definitions too; changing that merge policy is out of scope. Failed enrollment does not erase still-valid static declarations. Unknown is reserved for unavailable facts or future client values. The UI maps these states to “Awaiting discovery”, “Tools declared”, “Tools discovered”, and “Status unavailable”; there is no health field or green health indicator.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — A broker-only server exposes truthful local inventory

The current [broker composition](../architecture.md) keeps global MCP separate. `internal/adapter/mcpbroker/runtime.go` owns logical-session state and `internal/adapter/mcpbroker/workspace_enrollment.go` owns the side-effecting discovery path that inspection must not invoke.

**Acceptance:**
- AC1.1: A real broker-only composition with ownership enforced and a verified principal advertises the new capability and keeps `mcp=false`; a direct-only composition retains existing capabilities and returns no broker capability. An unwired inspector, ownership disabled, or missing verified principal suppresses the new capability on every capability-bearing response; rejected transport authentication returns no capability response.
  - verify: `TestBrokerMCPStatus_Scenario1_Capabilities`
- AC1.2: Owned sessions show ordered configured rows, anonymous discovered tools, protected declared stand-ins, and hidden connectors distinctly. Lazy authorization alone does not change catalogue or enrollment-completion claims. Zero-tool successful discovery is distinguished from hidden/unknown.
  - verify: `TestBrokerMCPStatus_Scenario1_CatalogueStates`
- AC1.3: Pending, consent-granted awaiting discovery, completed, terminal/expired, and unavailable enrollment follow the exact vocabulary and precedence. A multi-backend freeze appears atomically; failure never fabricates a partial broker-local published catalogue or erases admitted static tools.
  - verify: `TestBrokerMCPStatus_Scenario1_EnrollmentStates`
- AC1.4: Repeated inventory reads perform zero network calls, attach/create operations, token refreshes, discovery/install calls, session saves, or authorization transitions, including when consent has just completed.
  - verify: `TestBrokerMCPStatus_Scenario1_ReadOnly`
- AC1.5: Offline failure injection after broker discovery but during server engine rebuild and, separately, aggregate Save leaves inventory reporting the broker-local completed/discovered catalogue without claiming installed/durable session authority. Inspection performs no retry, repair, rebuild, Save or pending-state change.
  - verify: `TestBrokerMCPStatus_Scenario1_PublicationBeforeSessionCommit`
- AC1.6: An expired or terminal broker attempt with a still-persisted pending aggregate reports no active broker enrollment, not prompt readiness. Offline fixtures assert the aggregate remains pending, no Save or settlement occurs, and the panel shows the concise catalogue-status line without implying prompt readiness.
  - verify: `TestBrokerMCPStatus_Scenario1_PersistedPendingGate`

### Scenario 2 — Ownership, restart and transport boundaries remain honest

The existing binding lifecycle is in `internal/adapter/server/mcp_broker.go`; owner concealment is in `internal/adapter/server/ownership.go`. Status reads must not reuse the mutating rebind path, preserving the exact-binding boundary of [ADR 0322](../adr/0322-broker-mcp-status.md).

**Acceptance:**
- AC2.1: gRPC and HTTP enforce the ownership-enabled prerequisite and verified principal before disclosure; cover ownership-disabled, missing-principal, ownerless, matching-owner, foreign and absent cases with the exact errors above and zero inspector calls on rejection. Capability tests cover the same deployment/principal matrix. Existing transport authentication and descriptor-based gRPC affinity matrix coverage include the new method; HTTP affinity covers the new session route. Invalid, unsupported and unexpected-error mappings follow the interface contract; HTTP responses are not cacheable.
  - verify: `TestBrokerMCPStatus_Scenario2_TransportAuthority`
- AC2.2: Restart, binding mismatch, deletion and runtime close cannot display stale success or cause reattachment. A local handle close does not conceal a still-existing logical session. Concurrent inspection/close/delete/freeze is race-safe and produces a coherent old/new snapshot or unavailable, never mixed authority.
  - verify: `TestBrokerMCPStatus_Scenario2_Lifecycle`
- AC2.3: Responses expose only the specified fields and bounded sanitized display names, truthful counts/truncation and valid UTF-8. Secret/URL/path/error sentinels in every private broker field never reach protobuf or HTTP output.
  - verify: `TestBrokerMCPStatus_Scenario2_Redaction`

### Scenario 3 — The existing /mcp command works on broker-only mecak8s

Extend the existing panel at `cmd/mecatui/ui/mcp.go` and capability gates at `cmd/mecatui/ui/builtins.go`, preserving [ADR 0310](../adr/0310-lazy-toolhive-static-tools.md)'s declared-versus-discovered distinction.

**Acceptance:**
- AC3.1: `/mcp` opens for broker capability, showing enrollment, connector name, catalogue state and tool count. Hidden/unknown rows use an em dash rather than implying zero discovered tools; truncation is explicit. Refresh rereads local state only. Existing `/tools-connect` and `/tools-cancel` remain the only explicit enrollment actions.
  - verify: `TestBrokerMCPStatus_Scenario3_Panel`
- AC3.2: Broker-only keys never invoke direct source/resource/prompt/group methods. Existing direct-mode rendering and refresh behavior remain unchanged; old-server and missing-collaborator cases degrade safely. Unknown wire values render unknown, not healthy.
  - verify: `TestBrokerMCPStatus_Scenario3_Compatibility`
- AC3.3: Switching sessions or refreshing twice cannot apply a stale response. Lost broker state says “Broker state unavailable”; no unconditional reconnect action promises recovery for a session with history. The panel shows only the concise “Catalogue status · not a live connection check” status line, not machine status tokens or verbose readiness/authorization caveats.
  - verify: `TestBrokerMCPStatus_Scenario3_StaleResponses`
- AC3.4: Operator docs explain `/mcp` in broker-only Kubernetes, local refresh versus probes, static/lazy versus enrolled catalogues, and restart limitations without suggesting global MCP as a workaround.
  - verify: inspection — documentation is the operator-facing proof; implementation updates `docs/usage/mecak8s.md`, `docs/tui.md`, and the existing applicable `user-docs/` deployment page, then runs `task docs` and `task site:build`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Active health, automatic polling/probes, last-call success | Separate proposal | No network side effects or new health semantics |
| Durable enrollment history, status caches, new persistence | Separate proposal | Exact process-local state or explicit unavailable |
| Per-connector OAuth controls, auth class, credential status | Separate proposal | Keep aggregate broker controls and count-only inventory |
| Multi-replica broker routing and restart recovery | Existing broker lifecycle work | Inventory does not change deployment support |
| Tool definitions/resources/prompts through broker | Separate capability proposal | Do not broaden direct MCP promises |

## Definition of done

1. Decision-recording amendment contains docs only and is included in the eventual implementation PR under the directing-human waiver of a separate amendment PR/merge; all human decisions are resolved above.
2. Implementation passes `task lint`, `task test`, `task api:check`, `task docs`, and `task site:build`; tests use offline broker fixtures only.
3. `task ac-trace-strict` resolves every named proof when marked landed; `go run ./cmd/mecademo` remains green.
4. Implementation links the merged plan baseline and reports conformance, generated-contract updates and public documentation changes.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

No material choice is delegated to implementation: all Human decisions are resolved, and this amendment records their approval without changing the contract. The new inspection method must not call the existing observe-enrollment helper because that helper triggers authenticated discovery. Existing living documentation about pre-enrollment static-tool invisibility predates ADR 0310; implementation must correct those statements rather than copy them. Protobuf field 29 is free at the authoring baseline and must be checked again before delivery; ADR 0322 is reserved by this plan. No new resource owner is intended; any implementation that needs one is contract drift, not permission to add a cache.
