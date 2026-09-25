# SDK high-level parity coverage — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Bounded — this adds a reviewed coverage inventory, regression gates, consumer guidance, and two additive TypeScript session methods over existing RPCs without changing a durable server contract or authority boundary.
**Decision record:** None — [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md) and [ADR 0348](../adr/0348-typescript-sdk-mcp-authorization-lifecycle.md) already define the SDK shape; the new methods fit that shape.
**Phase:** SDK/TUI high-level parity, documentation, and examples
**Status:** proposed, revised 2026-09-25. Decisions resolved; plan and documentation checks passed. Awaiting issue-owner review.
**Delivery:** Split, with an explicit review-sequence exception. The directing user requested separate Plan / Interface and Implementation PRs in one `gh stack` for joint review before the plan merges. The merged-plan entry gate of `/plan-orchestrate` was waived for preparing this implementation candidate, not for approving or merging it.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1471](https://github.com/stacklok/mecatl/issues/1471)
**Plan PR:** [#1916](https://github.com/stacklok/mecatl/pull/1916), pending issue-owner review
**Implementation PR:** [#1917](https://github.com/stacklok/mecatl/pull/1917), stacked for joint review
**Approved baseline:** absent until the Plan / Interface PR merges

SDK consumers can find and use the reusable server workflows exposed through `mecatui` without invoking TUI command strings or importing TUI internals. The coverage record distinguishes an RPC reachable through the raw transport from an RPC actually invoked by a public `Client`, `Session`, namespace, or lifecycle operation. The TUI audit classifies each builtin by its core reusable outcome; its picker, rendering, and local policy remain application concerns.

## Human decisions

- [x] High-level RPC coverage — Decision: count an RPC as covered only when a public SDK operation actually invokes its descriptor; an equivalent user outcome through another RPC does not credit the unused descriptor.
- [x] Raw-only exceptions — Decision: `HarnessService.StreamSessionEvents` and `HarnessService.StreamSessionLive` are the exact intentionally raw-only set, each with a short rationale; growing or shrinking that set requires an explicit test change.
- [x] Mixed TUI builtins — Decision: classify the core reusable outcome, not every incidental RPC or UI action. A picker around a typed SDK operation remains SDK-backed; a TUI-authored workflow built from generic operations does not become an SDK API merely because it calls the server.
- [x] Client boundary — Decision: the TypeScript SDK does not import `cmd/mecatui` or its command registry. The TUI classification is an audit of a separate client, not a runtime dependency or a generic SDK command API.
- [x] TUI registry drift — Decision: inventory the 31 actual builtin declarations, including capability-gated and debug entries; leave the separate `knownBuiltinNames`/`builtinNameRegistry` and slash-command dispatch behavior unchanged in this issue.
- [x] Guardrail diagnostics — Decision: the two gRPC-only guardrail RPCs added after the first plan draft have reusable session-scoped outcomes. Public `Session.guardrailCoverage()` and `Session.guardrailReviewDetail()` invoke their exact descriptors; the TUI still owns its overlay and display policy. HTTP callers receive the SDK's existing unsupported-feature error until the server provides HTTP routes.
- [x] Release evidence — Decision: update API Extractor reports and generated reference for the intentional guardrail methods. The existing SDK release workflow generates the eventual `CHANGELOG.md` entry; do not hand-edit released or upcoming version entries in this implementation.
- [x] Review sequence — Decision: open the plan and implementation as two stacked PRs for a combined human review. Neither PR is merged; the plan commit remains the implementation candidate's unapproved baseline.

## Interface contract

- **gRPC / protobuf:** None — no service, message, field number, route, generated binding, or transport behavior changes. The coverage inventory enumerates the existing public `HarnessService` and `ScheduleService` descriptors; HTTP-only controls cannot satisfy RPC coverage.
- **Exported Go APIs / interfaces:** None — TUI classification and verification stay package-private or test-only under `cmd/mecatui/ui`; no engine, server, or exported Go symbol changes.
- **Tool schemas:** None — no model-visible tool name, input/output schema, permission rule, or dispatch behavior changes.
- **CLI / config:** None — no command name, slash-command dispatch, flag, configuration key, default, or precedence changes. In particular, the eight actual builtins missing from the separate known-name registry are classified without modifying that registry.
- **Events / persistence:** None — no event kind, stream payload, cursor, durable record, session snapshot, or migration changes. `StreamSessionEvents` and `StreamSessionLive` retain their current low-level semantics.
- **Security / authority:** Server-owned validation, permission, lifecycle, and authorization state remain authoritative. SDK examples may show typed protocol choreography and correlation but must not open a browser, retain credentials, infer authorization success, or copy the TUI's local polling and presentation policy. Coverage metadata grants no runtime authority.
- **Compatibility / migration:** Add `Session.guardrailCoverage(options?)` and `Session.guardrailReviewDetail(reviewId, options?)`, returning the generated response types for the two existing gRPC-only RPCs. This is an additive public TypeScript API change; HTTP transport calls fail with the SDK's existing unsupported-feature error. Raw RPC reachability and gRPC/HTTP parity remain separately tested. The SDK and TUI gain no mutual dependency. Update public API reports and generated reference; SDK changelog entries continue through the existing release PR workflow.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — every public RPC has an honest high-level classification

The checked-in RPC inventory lives as a test-only row table in `sdk/typescript/test/high-level-surface.test.ts`, beside its coverage assertions. It is keyed by the generated descriptors, not by an independently maintained count. Each row identifies exactly one of `Session` method, SDK namespace operation, lifecycle object, or intentionally raw-only. A covered row names its public SDK member and the descriptor it invokes. A raw-only row has a nonempty rationale. [ADR 0304](../adr/0304-typescript-sdk-public-surface-and-release.md) keeps raw transport parity separate from the ergonomic surface, and [the RPC catalog](../../sdk/typescript/src/rpc-catalog.ts) currently contains 86 public descriptors. That production catalog remains transport metadata rather than becoming the high-level inventory.

**Acceptance:**

- AC1.1: The classification key set exactly equals the current `HarnessService` and `ScheduleService` descriptor key set, with no duplicate, stale, or unclassified row; a newly added public RPC fails CI until classified.
  - verify: vitest:sdk/typescript/test/high-level-surface.test.ts#UlBDIGNsYXNzaWZpY2F0aW9ucyBleGFjdGx5IGNvdmVyIHB1YmxpYyBkZXNjcmlwdG9ycw — `RPC classifications exactly cover public descriptors`
- AC1.2: Every non-raw row names an existing public SDK operation that invokes that row's descriptor. The check does not infer coverage from a related method, generated types alone, or a raw transport call. Existing focused operation tests continue to prove stateful behavior.
  - verify: vitest:sdk/typescript/test/high-level-surface.test.ts#Y292ZXJlZCBSUENzIGhhdmUgcHVibGljIGludm9jYXRpb24gcGF0aHM — `covered RPCs have public invocation paths`
- AC1.3: The exact raw-only set is `HarnessService.StreamSessionEvents` (whole-log readback without a cursor or follow) and `HarnessService.StreamSessionLive` (process-local, gRPC-only live subscription). Each row records why the SDK's durable `Session.activity()`/`attach()` over `WatchSessionEvents` does not invoke or replace that lower-level RPC. Altering the set fails a separate exact-set assertion.
  - verify: vitest:sdk/typescript/test/high-level-surface.test.ts#cmF3LW9ubHkgUlBDIGV4Y2VwdGlvbnMgYXJlIGV4YWN0IGFuZCByZWFzb25lZA — `raw-only RPC exceptions are exact and reasoned`
- AC1.4: The existing raw-reachability and transport parity tests remain independent; neither an HTTP-only control nor a raw catalog entry by itself qualifies as high-level coverage.
  - verify: vitest:sdk/typescript/test/rpc-catalog.test.ts#SFRUUC1vbmx5IGNvbnRyb2xzIGRvIG5vdCBzYXRpc2Z5IFJQQyBjb3ZlcmFnZQ — `HTTP-only controls do not satisfy RPC coverage`; vitest:sdk/typescript/test/high-level-surface.test.ts#Y292ZXJlZCBSUENzIGhhdmUgcHVibGljIGludm9jYXRpb24gcGF0aHM — `covered RPCs have public invocation paths`
- AC1.5: `Session.guardrailCoverage()` and `Session.guardrailReviewDetail()` invoke `ListGuardrailCoverage` and `GetGuardrailReviewDetail` respectively with this session's identity and caller headers. HTTP calls report the existing unsupported-feature error because these RPCs have no HTTP routes.
  - verify: vitest:sdk/typescript/test/guardrails.test.ts#aW52b2tlcyB0aGUgZXhhY3QgZ3VhcmRyYWlsIFJQQ3Mgd2l0aCBzZXNzaW9uIGFmZmluaXR5 — `invokes the exact guardrail RPCs with session affinity`; vitest:sdk/typescript/test/guardrails.test.ts#cmVwb3J0cyB1bmF2YWlsYWJsZSBIVFRQIHJvdXRlcyB3aXRob3V0IHNlbmRpbmcgYSBndWFyZHJhaWwgcmVxdWVzdA — `reports unavailable HTTP routes without sending a guardrail request`

### Scenario 2 — every TUI builtin has an explicit SDK-boundary decision

The checked-in TUI inventory lives as a test-only row table in `cmd/mecatui/ui/sdk_high_level_parity_test.go`. Its guard enumerates [actual builtin declarations](../../cmd/mecatui/ui/builtins.go), including gated and debug entries, without using the incomplete known-name registry as its source. A builtin has exactly one category: SDK-backed, application/presentation-only, operator/configuration-only, or debug-only. SDK-backed means a named typed SDK operation supplies the command's core reusable server outcome; presentation details stay with the client. Every other row gives a short reason the command should not become an SDK API. The SDK imports no Go TUI code; see the independent client boundary in [architecture](../architecture.md).

The current classifications are:

| Category | Builtins |
|---|---|
| SDK-backed | `clear`, `title`, `session`, `retry`, `compact`, `mcp`, `mcp-refresh`, `agents`, `team`, `skills`, `soul`, `usermodel`, `reflections`, `reflect`, `dream`, `models`, `effort`, `worktrees`, `schedule`, `sessions`, `tools-connect`, `tools-cancel`, `posture`, `guardrails` |
| Application/presentation-only | `help` (local help overlay), `quit` (application exit), `diagnostics` (TUI-authored client-state report and prompt) |
| Operator/configuration-only | `connect` (saved target, login, and credential UX), `learning` (local learning-mode setting), `learning-sensitivity` (local sensitivity setting) |
| Debug-only | `debug-ask` (synthetic permission ask injection) |

`mcp-refresh` uses `Client.mcp.refresh` for direct MCP sources and `Session.connectWorkspaceServices` for broker enrollment. The TUI owns the capability-based choice between those typed operations. `posture` combines server compatibility with session guardrail coverage; `guardrails` presents the session coverage result. Review-detail display remains TUI-owned.

**Acceptance:**

- AC2.1: The checked-in classification contains all 31 current builtin declarations and the seven per-builtin non-SDK rationales above, with no duplicate or unclassified entry. CI fails on a new builtin declaration without a classification, even when that builtin is capability-gated or debug-only.
  - verify: `TestSDKHighLevelParity_Scenario2_AllBuiltinDeclarationsClassified`
- AC2.2: The SDK-backed rows identify existing public typed SDK operations for their reusable outcomes and explain any application-owned picker, display, or policy. No TUI name or command string is added to the public SDK.
  - verify: inspection — compare the checked-in rows with the public SDK method reports and the TUI handlers during plan implementation review
- AC2.3: The TUI dispatch registry and fallback behavior remain byte-identical; the classification guard observes builtin declarations rather than making them a shared runtime registry.
  - verify: `TestSDKHighLevelParity_Scenario2_BuiltinDispatchUnchanged`; inspection — no production change to `cmd/mecatui/ui/builtins.go` dispatch or `sdk/typescript/src/` TUI imports

### Scenario 3 — SDK workflows are documented and runnable from public exports

The existing SDK guides under `user-docs/building/typescript-sdk/` and [examples](../../sdk/typescript/examples/README.md) cover the sibling issue surfaces, but the cross-workflow examples and authority explanation remain due here. The implementation adds focused executable examples for session inspection/mutation/successors, pre-session capability discovery, and MCP workspace enrollment, then verifies the existing MCP authorization lifecycle example against the same public-export rule. Each example states its required server capability and runtime prerequisites. The normal offline SDK e2e gate runs the four actual example entry points against controlled daemon or protocol fixtures and checks their observable results; a test of equivalent SDK calls alone is insufficient. [ADR 0348](../adr/0348-typescript-sdk-mcp-authorization-lifecycle.md) keeps URL display and recheck cadence application-owned.

**Acceptance:**

- AC3.1: Runnable examples demonstrate the session lifecycle, capability discovery, MCP enrollment, and MCP authorization using only published package entry points. They appear in the examples inventory and compile through `task sdk:examples:typecheck`. `task sdk:e2e` launches each actual example entry point against an offline fixture, drives any interactive authorization step, and asserts a completed workflow or the documented server-owned state. The gate requires no external service or manual input; the inventory documents prerequisites for running the examples outside the fixture.
  - verify: vitest:sdk/typescript/test/examples.test.ts#ZXZlcnkgcHVibGlzaGVkIGV4YW1wbGUgdHlwZWNoZWNrcyBhZ2FpbnN0IHBhY2thZ2UgZXhwb3J0cw — `every published example typechecks against package exports`; vitest:sdk/typescript/e2e/workflow-examples.e2e.test.ts#YWxsIGZvdXIgd29ya2Zsb3cgZXhhbXBsZXMgZXhlY3V0ZSBhZ2FpbnN0IG9mZmxpbmUgZml4dHVyZXM — `all four workflow examples execute against offline fixtures`; inspection — run instructions and required capabilities accompany each example
- AC3.2: User guidance explains the authority split: the server decides validation, authorization, and lifecycle truth; the SDK supplies session binding, typed requests/results, streaming, cancellation, and correlation; applications own pickers, browser opening, polling cadence, filtering, clipboard behavior, credentials, and local preferences. Guidance links the four workflows without suggesting TUI command invocation.
  - verify: `task docs` — validates the user-doc links and generated references; editorial review — validates the prose and examples against the stated authority boundary
- AC3.3: Living architecture points to the two test-only inventories and describes the separate TUI/SDK clients. API Extractor reports and generated public SDK reference match the shipped surface; no manual changelog entry bypasses the generated SDK release flow.
  - verify: inspection — compare the release workflow, architecture update, implementation diff, and SDK API and reference gate results

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| TUI known-name registry and slash-command fallback discrepancies | separate TUI work | Eight declared builtins are absent from that registry; changing it alters gated-command and workspace-command behavior, so this coverage effort only audits actual declarations. |
| New convenience API for the two low-level event streams | future SDK proposal if a concrete consumer requires it | They remain deliberately raw-only under the exact exception set. |
| Generic SDK execution of TUI builtins, local provider configuration, credentials, saved-target selection, UI preferences, and debug prompts | outside SDK boundary | [Parent story #1466](https://github.com/stacklok/mecatl/issues/1466) assigns these to applications or operators. |

## Definition of done

1. Applicable `task sdk:lint`, `task sdk:typecheck`, `task sdk:test`, `task sdk:examples:typecheck`, `task sdk:e2e`, `task sdk:api:check`, `task sdk:docs:check`, `task lint`, `task test:race`, and `task docs` gates pass on the implementation candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. The implementation PR links the approved Plan / Interface PR and commit, reports the exact classified RPC and builtin sets, and explains any intentional surface change.
4. `/panel-review` reports no ship blockers or unwaived reviewer failures.
