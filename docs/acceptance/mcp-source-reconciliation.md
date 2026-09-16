# MCP source reconciliation — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — changes process-wide MCP publication, runtime ownership, a public refresh control, and one importable aggregate API.
**Decision record:** [ADR 0345](../adr/0345-mcp-source-reconciliation.md)
**Phase:** Minimal stale direct/global MCP source reconciliation
**Status:** proposed, 2026-09-16. Directing-human decisions are settled; ready for amended Plan / Interface review.
**Delivery:** Split. Runtime publication and additive public controls require contract review. The directing human explicitly authorizes a stacked implementation PR before this Plan PR merges, based on the exact amended plan commit and targeting `plan/mcp-source-reconciliation`; this does not approve or merge either PR, and contract-drift gates remain.
**Expected tasks:** deferred to orchestration after parent advisory review.
**Issue:** [#1511](https://github.com/stacklok/mecatl/issues/1511) — tracks the work; this plan does not close it.
**Plan PR:** [#1527](https://github.com/stacklok/mecatl/pull/1527)
**Approved baseline:** absent until the Plan / Interface PR merges; the authorized stacked implementation records the exact proposed plan commit it follows.

This amendment replaces the prior digest/binding/revocation design with the original stale-source scope. It preserves current exact-name authority semantics: disappearance filters availability without deleting the durable grant, exact-name reappearance remains authorized, and same-name endpoint/schema/read-only drift remains the same name grant. Automatic ToolHive polling, notification-driven reconciliation, and explicit `/mcp-refresh` additions remain required.

## Human decisions

- [x] Authority semantics — Decision: retain existing exact-name `session.Authority.CapabilitySet.Tools`; add no digest, `DirectMCPBinding`, revoked-record list, migration, or credential-free URL policy. Current availability filters durable grants.
- [x] Removal and reappearance — Decision: successful source removal is automatically absent at the next operation boundary; stale calls get a permanent unavailable result. The durable name may remain, and exact-name reappearance is authorized. This is not permanent revocation.
- [x] Additions — Decision: only explicit owner `/mcp-refresh` stable-unions current active direct MCP names into the existing ceiling, preserving unrelated names. Refresh is not durable direct-subset replacement.
- [x] Triggers — Decision: bounded jittered polling is enabled only for ToolHive; MCP list-change notifications and manual requests share one serialized/coalesced bounded reconciler.
- [x] Source failure — Decision: consultation failure retains source LKG; successful empty is authoritative withdrawal.
- [x] Candidate failure — Decision: candidates are complete/all-or-nothing. Any desired-server construction failure closes the candidate and retains the previous runtime; this may delay an unrelated removal, reported as stale/degraded, until a later retry.
- [x] Runtime ownership — Decision: one immutable current direct runtime plus bounded retiring runtimes; operation/run pins protect active use and are inherited by delegation. Revision tags rebuild stale shared/cached engines; idle engine existence does not lease a runtime.
- [x] Refresh lifecycle — Decision: idle and quiescent completed ordinary roots are eligible without reopening completed state. Failed/cancelled and every active/pending/non-root form are rejected. Mutation exclusion and confirmed save are required only when the union adds authority.
- [x] Unified UX — Decision: `/mcp-refresh` capability-routes to direct `RefreshMcpSources` or existing broker `ConnectWorkspaceServices`; broker consent/cancellation/disclosure remains distinct, and `/tools-connect` remains a deprecated broker-only alias.

## Interface contract

- **gRPC / protobuf:** Add unary `HarnessService.RefreshMcpSources(RefreshMcpSourcesRequest) returns (RefreshMcpSourcesResponse)`. Request: `string session_id = 1`. Response: `uint64 revision = 1`, `bool changed = 2`, where changed means the completed request either published a runtime revision or added session authority. Add direct-only `ServerCapabilities.mcp_refresh = 30`; field 30 is unallocated on synchronized `origin/main` (`2a0c9cb11bedc6ef88503f504bbd4f94a6d31690`). Extend `ListMcpSourcesResponse` with `uint64 revision = 2`, `bool stale = 3`, and `bool reconciling = 4`; source rows remain the current published/pre-shadow inventory and the call performs no independent probe. Add bodyless HTTP `POST /v1/sessions/{id}/mcp-refresh`. Existing broker RPC/HTTP/capability surfaces are unchanged.
- **Exported Go APIs / interfaces:** Add only `engine/session.(*Session).GrantToolAuthority([]string) error`. It validates and stable-unions tool names into already-bound authority; preserves unrelated authority axes and all other aggregate state; accepts only idle or completed state with no pending control; and never reopens completed state. Ordinary-root/provenance/owner checks stay in Service/composition. No new authority field, digest helper, agent run option, source port, or ToolHive import enters `engine/`.
- **Tool schemas:** No model-visible tool is added or changed. Current catalog membership intersects existing exact-name authority. An authority-granted but catalog-absent call returns the exact permanent error `tool is currently unavailable; do not retry unless the catalog changes`; an ungranted absent name retains existing unknown-tool behavior.
- **CLI / config:** Add no flag, key, URL rule, selector, credential input, or poll tuning. ToolHive discovery alone enables bounded automatic polling. Mecatui adds argument-free `/mcp-refresh`: direct-only capability invokes only `RefreshMcpSources`; broker-only capability invokes only `ConnectWorkspaceServices` with existing consent/cancellation/disclosure. Both/neither incompatible modes or missing collaborators fail closed. `/tools-connect` remains a deprecated broker-only alias and `/tools-cancel` is unchanged.
- **Events / persistence:** Add no event, snapshot revision, binding, revoked-name record, or migration. `Authority.CapabilitySet.Tools` remains the durable name ceiling. Explicit refresh performs at most one confirmed snapshot save only when its stable union adds names; runtime removal/status/revisions remain process state.
- **Security / authority:** Refresh authenticates ownership before caller-selected locking. Authority addition requires `runEntryMu`, mutation lease/capability, fresh authoritative load, ordinary-main taxonomy, eligible lifecycle state, same-process liveness exclusion, aggregate union, and successful save. A no-op refresh does not acquire mutation authority or save. Automatic reconciliation never widens authority or launches consent. Existing endpoint/credential validation and URL projection remain unchanged.
- **Compatibility / migration:** Existing sessions and snapshots require no migration. Existing granted names remain granted even while unavailable. Same-name source/endpoint/schema/read-only drift follows current name semantics. Additive protobuf fields are ignored by old clients. Broker/client MCP behavior and configured-over-ToolHive name precedence remain unchanged.

## In scope — 4 scenarios, in implementation order

### Scenario 1 — Ordered sources reconcile automatically and boundedly

The production Build path constructs one reconciler over the existing ordered source resolver. ToolHive polling, current-runtime list notifications, and manual requests enter the same bounded coalescing path defined by [ADR 0345](../adr/0345-mcp-source-reconciliation.md).

**Acceptance:**
- AC1.1: Configured/static entries retain precedence over ToolHive collisions and pre-shadow source rows remain visible. Source failure retains LKG; successful empty withdraws that source. The production source-resolution path, not a synthetic merge helper alone, proves all cases.
  - verify: `TestMCPSourceReconciliation_Scenario1_OrderedSourcesLKGAndEmpty`
- AC1.2: Polling is absent without ToolHive and bounded/jittered with it. Tool/resource/prompt notifications from the current runtime and manual requests serialize behind one active cycle with at most one coalesced successor; stale-runtime notifications are ignored and caller cancellation does not cancel shared work.
  - verify: `TestMCPSourceReconciliation_Scenario1_ProductionTriggerMatrix`
- AC1.3: Source, server, tool/list, cycle, and retained-runtime bounds fail stale with bounded secret-safe diagnostics; automatic cycles never invoke OAuth/browser consent and shutdown joins polling/reconciliation.
  - verify: `TestADR_0345_ReconciliationBoundsConsentAndShutdown`

### Scenario 2 — Complete runtimes publish atomically and drain safely

A candidate builds a complete manager/provider/tool/source snapshot and generation-bound `assembleCatalog` contribution. Publication atomically swaps current runtime only after candidate completion, preserving [ADR 0345](../adr/0345-mcp-source-reconciliation.md)'s consistency boundary.

**Acceptance:**
- AC2.1: A complete candidate publishes additions and removals, including successful empty. Any connect/initialize/list/validation failure closes the entire candidate, retains the previous usable runtime, marks status stale, and delays otherwise valid changes until a later successful retry.
  - verify: `TestMCPSourceReconciliation_Scenario2_AllOrNothingPublication`
- AC2.2: The real shared and session factory paths cover shared/default, selector, no-FS, client-MCP, mode-specific, specialist, and debug engines. Revision mismatch rebuilds before use; a debug name ceiling admits no additions and fails when selected names are absent. `assembleCatalog` remains the single complete registration path.
  - verify: `TestMCPSourceReconciliation_Scenario2_ProductionEngineRevisionMatrix`
- AC2.3: A production-path race matrix covers prompt start, failed-step retry, restored approval, direct `RunTeam`, Subagent, Parallel, Team member, named specialist, referenced-agent MCP, in-run prompt expansion, and out-of-run list/read/get resource/prompt calls. Each operation observes one runtime across schema, lookup, authority, permission, dispatch, provider, and manager; delegation inherits the root pin. Retiring runtimes close once after pins drain, and a full retirement set defers publication without force-close.
  - verify: `TestMCPSourceReconciliation_Scenario2_RuntimeConsistencyMatrix`

### Scenario 3 — Name authority filters availability and widens only explicitly

The current runtime contributes only active direct names. Existing authority remains name-based and the aggregate supplies one narrow union operation under [ADR 0345](../adr/0345-mcp-source-reconciliation.md).

**Acceptance:**
- AC3.1: A removed granted name is absent from model specs and dispatch, and a stale generated call gets exactly `tool is currently unavailable; do not retry unless the catalog changes`. An ungranted absent name stays an unknown-tool error. Exact-name reappearance automatically becomes available under the existing grant; same-name endpoint/schema/read-only drift requires no regrant.
  - verify: `TestMCPSourceReconciliation_Scenario3_NameAuthorityAvailabilityMatrix`
- AC3.2: `Session.GrantToolAuthority` stable-unions validated names into bound authority in idle and completed states, preserves unrelated names/axes, conversation/history, placement, owner, counters, and the exact lifecycle state, and rejects pending controls or other states without mutation.
  - verify: `TestADR_0345_GrantToolAuthorityPreservesAggregateState`
- AC3.3: Delegated authority remains the existing name intersection. Children started before publication inherit the pinned old runtime; children started after publication use the new active set. Resume permits an already granted same name when currently available and returns the permanent unavailable result when absent, without digest or migration state.
  - verify: `TestMCPSourceReconciliation_Scenario3_DelegationAndResumeNameSemantics`

### Scenario 4 — Explicit direct refresh and unified UX preserve boundaries

Direct refresh reconciles shared state first, then adds only missing active direct names to one eligible owned root. Broker refresh remains the existing enrollment operation in [ADR 0335](../adr/0335-idle-session-broker-workspace-refresh.md).

**Acceptance:**
- AC4.1: A Service-level matrix covers idle and quiescent completed success without reopening; no-op success with no lease/save; addition with owner preflight, `runEntryMu`, mutation lease/capability, fresh load, state plus liveness proof, one aggregate union, and one confirmed save; and rejection of active/running/awaiting/authorizing/failed/cancelled/child/schedule/debug/broker-conflicting sessions. Save failure leaves durable authority and model/history unchanged. The proof uses the real Service control, not direct aggregate calls or a fake pin-only harness.
  - verify: `TestMCPSourceReconciliation_Scenario4_ServiceRefreshMutationMatrix`
- AC4.2: gRPC, HTTP, and mecatui direct paths return the current revision and exact changed semantics for unchanged, runtime-only change, authority-only change, both, stale candidate, cancellation, ownership concealment, and unsupported states. `ListMcpSources` returns cached revision/stale/reconciling and performs no probe.
  - verify: `TestMCPSourceReconciliation_Scenario4_DirectTransportStatusMatrix`
- AC4.3: Mecatui routes direct-only `mcp_refresh` solely to `RefreshMcpSources` without consent and broker-only `workspace_enrollment` solely to `ConnectWorkspaceServices` with destructive-reconnection disclosure, consent/presentation, observation, cancellation, and existing outcomes. Both bits, neither bit, or a missing collaborator fail closed; `/tools-connect` remains broker-only even when direct refresh exists, and client-provided MCP is untouched.
  - verify: `TestMCPSourceReconciliation_Scenario4_UnifiedCommandRoutingMatrix`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Digest/canonical-JSON capability identity, endpoint identity normalization, `DirectMCPBinding`, revoked records, or legacy migration | Separate security proposal if ever desired | Existing exact-name authority is preserved. |
| Live operator settings/CLI/environment reload | Future operator/admin design | Configured input remains process-start state; only ToolHive is polled. |
| Project-tier MCP | Existing trust/config policy | It remains ignored. |
| Broker/protected MCP and client-provided MCP lifecycle | Existing ADRs/session lifecycle | Only mecatui's preferred command spelling is shared. |
| Partial-active candidate publication or connection reuse | Future optimization with its own ownership proof | Complete candidates retain the previous runtime on any construction failure. |
| Mid-operation catalog/manager mutation | Prohibited | Immutable runtime revisions are the consistency boundary. |
| Poll/bound tuning surface | Not planned | Finite values are internal constants with boundary tests. |
| Shipped architecture/user documentation in this Plan PR | Implementation PR | Living/user docs describe shipped behavior only. |

## Definition of done

1. Stacked implementation records the exact amended plan commit and targets `plan/mcp-source-reconciliation`; parent advisory review precedes any plan push. Plan merge remains approval and humans alone merge.
2. The named production-path tests prove all 12 ACs, including automatic ToolHive polling, notifications, LKG/empty, complete-candidate failure, runtime consistency, name reappearance, no-op versus widening refresh, transport parity, and broker/client isolation.
3. The sole exported engine addition, `Session.GrantToolAuthority`, is recorded by `task api:update` and as Added/minor in `engine/CHANGELOG.md`. No other engine API widening is needed.
4. ADR 0027 inventories the poller/timer, reconciler/coalescing state, source LKG/status, immutable current and retiring runtimes, revision-tagged caches, and run/operation pins with restart decisions.
5. Implementation updates `docs/architecture.md`, `docs/architecture/extensibility.md`, `docs/design/IMPLEMENTATION-NOTES.md`, owning `user-docs/` MCP/mecatui pages, generated API/config references, and ADR 0057's index annotation.
6. `task generate`, `task lint`, `task test`, `task api:check`, `task docs`, `task site:build`, `task ac-trace-strict`, and `go run ./cmd/mecademo` pass at implementation completion.
7. The implementation PR remains stacked until plan merge, reports exact interface conformance, and has no unwaived review blocker. This plan tracks #1511 but does not close it.

## Deferred decisions and known risks

- Exact poll, cooldown, source/server/tool/list, connect, and retirement bounds are finite tested implementation constants.
- Complete candidates can delay a successful removal while an unrelated desired server fails. Cached stale/degraded status and all three triggers make this visible and retryable; partial-active publication is intentionally not required.
- Cached engines may reference a retired runtime object but cannot use it: every root operation pins current runtime and verifies the engine revision first. Engine close paths must not own the shared direct manager; only runtime retirement closes it.
- Refresh can grant names from a runtime superseded immediately after the pin is released. This is safe under name authority: the grant remains durable but unavailable until that exact name is current, and no live catalog is widened by the save.
