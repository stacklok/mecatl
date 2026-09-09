# Live authenticated MCP metadata replaces static stand-ins — acceptance plan

**Contract:** human-reviewed/v1
**Phase:** Session-scoped MCP broker protected-tool admission
**Status:** landed candidate, 2026-09-09. Local implementation and verification are complete; authoritative only when these changes merge.
**Delivery:** Split. This changes the ADR-governed model-visible protected-tool catalogue and its authority-sensitive read-only classification, so it requires Plan / Interface review before implementation.
**Expected tasks:** 2
**Issue:** [#1319](https://github.com/stacklok/mecatl/issues/1319)
**Plan PR:** <added when opened>
**Approved baseline:** `17ff18154` (local plan commit explicitly approved for execution in this workspace; no remote Plan PR requested)

Static protected-tool declarations make an operator-selected initial surface available before ToolHive authorization. After successful pre-prompt enrollment, [ADR 0310](../adr/0310-lazy-toolhive-static-tools.md) says the complete authenticated catalogue replaces those visible stand-ins. Today, `stageAuthenticatedRoutes` removes a live definition whose name collides with a declaration and reinstates the declaration's description, schema, and read-only classification. It also retains a declared tool that authenticated discovery no longer returns. Consequently, each session discards its own authenticated metadata and membership for declared names.

The approved contract makes static declarations pre-authentication placeholders only. Successful pre-prompt enrollment atomically replaces them with that session's complete authenticated catalogue, including its descriptions, schemas, and read-only hints. A successful lazy bundle grant also replaces declared placeholders with their authenticated metadata, but does not publish undeclared tools. Both paths retain the authenticated-metadata admission boundary and the distinction between model-visible metadata and execution: protected calls continue through ToolHive with the live authenticated backend.

## Human decisions

- [x] For a name present in both a static protected-tool declaration and authenticated discovery, should authenticated metadata replace the declaration after successful enrollment, should the declaration remain an operator override, or should a field-level hybrid apply? — Decision: authenticated discovery controls post-enrollment tool membership, description, and schema. Static declarations are pre-authentication placeholders only. A declared tool absent from authenticated discovery disappears from the enrolled catalogue.
- [x] If authenticated metadata wins, which successful authorization paths replace placeholders, and must an existing session refresh on another trigger or cadence? — Decision: successful pre-prompt enrollment performs authenticated discovery once and atomically freezes the complete session catalogue. A successful lazy bundle grant performs one all-backend discovery and atomically replaces or removes declared placeholders only; undeclared tools remain hidden. Later runs and ToolHive token refreshes do not refresh either result. A fresh session performs fresh authenticated discovery; no refresh control, timer, generation, or invalidation path is added.
- [x] Does `ReadOnly` have a distinct operator-trust purpose that requires static precedence, or is ToolHive's authenticated `ReadOnly` hint the post-enrollment input? — Decision: authenticated discovery controls `ReadOnly` after enrollment, matching ordinary MCP tools. An absent live hint defaults conservatively to `false`. Static `ReadOnly` applies only to the pre-authentication stand-in. Existing permission rules remain independent and deny-dominant; the hint controls plan-mode visibility and read-parallel versus mutate-serial dispatch.

## Interface contract

- **gRPC / protobuf:** None — session catalogue changes remain internal; the existing public enrollment controls and tool-call wire shapes do not gain fields.
- **Exported Go APIs / interfaces:** The internal `internal/mcpbroker.Attachment` boundary gains `RefreshGrantedAuthorizationCatalogue(context.Context, session.ExternalAuthorization) ([]tool.Tool, error)` so the server can request an exact granted-bundle refresh and rebuild from the returned snapshot. No importable `engine/` API changes.
- **Tool schemas:** Before authorization, each declared qualified MCP tool uses its configured static input schema. After either successful authorization path, the same declared name uses the authenticated schema and a declaration absent from authenticated discovery disappears. Pre-prompt enrollment additionally admits authenticated tools that were not declared; lazy authorization does not.
- **CLI / config:** None — no flag or key changes. Existing `mcp.servers[].auth.oauth.tools` declarations retain their initial pre-authentication role and are not post-authentication overrides.
- **Events / persistence:** None — the frozen catalogue remains attachment/session-runtime state. The existing exact opaque broker-incarnation reload boundary remains unchanged; no metadata cache, refresh generation, credential, or new record is persisted.
- **Security / authority:** Every authenticated definition continues through the shared qualified-name, UTF-8, size, JSON-object-schema, private-material, and collision checks before all-or-nothing publication. After enrollment, an authenticated `ReadOnly` hint controls plan-mode visibility and dispatch batching exactly as it does for ordinary MCP tools; an absent hint is `false`. Static metadata cannot create a direct upstream path, bypass configured permissions, survive omission from authenticated discovery, or cause undeclared tools to appear through lazy authorization.
- **Compatibility / migration:** Internal behavior correction, with no wire or config migration. Existing declarations remain usable before authorization. After enrollment, operators can no longer use a static declaration as an implicit description/schema/read-only pin; living architecture, implementation notes, and release documentation must identify declarations as pre-authentication placeholders.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — Authenticated discovery atomically replaces static stand-ins

A session begins with two static protected declarations, then completes the existing pre-prompt ToolHive enrollment. Authenticated discovery returns one same-name definition with deliberately different description, input schema, and read-only value, omits the second declaration, and adds one previously undeclared authenticated tool. The frozen model-visible catalogue contains the complete live result exactly once. This corrects `internal/adapter/mcpbroker/workspace_catalogue.go` (`stageAuthenticatedRoutes`) while preserving the all-or-nothing freeze described in [architecture.md](../architecture.md).

**Acceptance:**
- AC1.1: Before successful enrollment, the initial catalogue exposes both declared protected tools with their configured static metadata and read-only classifications, so they can start ADR 0310's existing lazy-authorization path.
  - verify: `TestADR_0310_StaticProtectedToolIsVisibleBeforeEnrollment`
- AC1.2: After successful enrollment, a same-name authenticated definition appears exactly once with its live description, schema, and `ReadOnly` value; the corresponding static values do not remain or merge into it.
  - verify: `TestADR_0310_AuthenticatedCatalogueReplacesDeclaredToolMetadata`
- AC1.3: A declared tool omitted by authenticated discovery disappears, while an authenticated tool that was not statically declared appears only after pre-prompt enrollment succeeds.
  - verify: `TestADR_0310_AuthenticatedCatalogueReplacesDeclaredMembership`
- AC1.4: Invalid authenticated metadata, a collision with an externally occupied name, discovery failure, or a process-close race publishes neither a mixed nor a partial catalogue; the prior catalogue remains authoritative.
  - verify: `TestADR_0310_AuthenticatedReplacementFailsAtomically`

### Scenario 2 — Each session receives safely admitted live metadata and remains broker-routed

Two fresh attachments discover different valid descriptions and schemas for the same declared tool, such as identity-bearing descriptions for two authenticated users. Each freezes its own result rather than the shared static placeholder. Invalid live values still fail before publication. Invocation continues to use ToolHive's broker credential and authenticated backend; this changes metadata admission, not execution authority, preserving [ADR 0310](../adr/0310-lazy-toolhive-static-tools.md)'s ToolHive-owned authorization boundary and the normal-permissions rule in [architecture.md](../architecture.md).

**Acceptance:**
- AC2.1: Two fresh attachments whose authenticated discovery returns different valid metadata for the same declared name each freeze their own description, schema, and read-only classification; neither receives the static values or the other attachment's values.
  - verify: `TestADR_0310_AuthenticatedDeclaredMetadataIsSessionSpecific`
- AC2.2: Authenticated definitions pass the single protected-route admission boundary for qualified name, valid UTF-8, bounded description/schema, JSON-object schema, private-material exclusion, and global collision detection; any failure publishes no protected subset.
  - verify: `TestADR_0310_AuthenticatedReplacementUsesAdmissionBoundary`
- AC2.3: Invoking a frozen formerly-declared tool uses the existing broker-routed execution path with ToolHive's opaque credential; it does not directly contact or authorize an upstream backend, and configured mecatl permission rules remain effective.
  - verify: `TestADR_0310_ReplacedDeclaredToolRemainsBrokerRouted`
- AC2.4: A live `ReadOnly: true` tool is available in plan mode and read-batchable; a live false or absent hint is unavailable in plan mode and mutate-serial, regardless of the static placeholder's former value.
  - verify: `TestADR_0310_AuthenticatedReadOnlyHintReplacesStaticHint`

### Scenario 3 — Lazy authorization refreshes declarations without widening membership

A successful declared-tool call authorizes the configured ToolHive bundle. Before resuming that parked call, the broker queries every protected backend and atomically replaces declared placeholders with admitted live metadata. Declared tools omitted by discovery disappear, while undeclared tools remain hidden. The session engine is rebuilt from that exact snapshot; later runs and token refreshes do not rediscover it. Successful pre-prompt enrollment remains the complete-catalogue path. See [ADR 0310](../adr/0310-lazy-toolhive-static-tools.md).

**Acceptance:**
- AC3.1: The exact granted bundle authorization queries every configured protected backend; matching declared tools use live description, schema, and `ReadOnly`, and omitted declarations disappear.
  - verify: `TestADR_0319_LazyGrantReplacesDeclaredMetadata`
- AC3.2: Undeclared authenticated tools remain hidden after lazy authorization, while pre-prompt enrollment continues to admit the complete authenticated catalogue.
  - verify: `TestADR_0319_LazyGrantReplacesDeclaredMetadata`, `TestADR_0310_AuthenticatedCatalogueReplacesDeclaredMembership`
- AC3.3: A backend query, metadata validation, lifecycle race, or forged authorization publishes no partial catalogue and leaves the prior declarations authoritative.
  - verify: `TestADR_0319_LazyGrantRefreshFailureIsAtomic`, `TestADR_0319_LazyGrantReplacesDeclaredMetadata`
- AC3.4: The parked authorization continuation rebuilds its session engine from the exact refreshed tool snapshot before resuming the granted call.
  - verify: `TestAuthenticatedMCPMetadataReplacement_Scenario2_RebuildsParkedContinuation`, `TestAuthenticatedMCPMetadataReplacement_Scenario2_ReplacesJustParkedRun`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Wiring broker-routed `CallMcpWithQuery` backends and its independent read-only permission concern | Separate broker meta-tool work | The handover identifies it as a distinct code path and scope. |
| Catalogue reload, cadence, token-refresh coupling, or admin invalidation | Follow-on ADR and plan | This contract deliberately keeps one immutable catalogue per session. |
| New static post-auth metadata overrides | Separate config/authority design | Existing static fields are pre-authentication placeholders only. |
| Changing ToolHive OAuth grants, browser flow, token custody, or upstream execution | Existing ADR 0310/0311 behavior | This plan changes model-visible metadata replacement only. |
| Admitting undeclared tools through lazy authorization | Explicitly prohibited | ADR 0310 says lazy authorization does not add undeclared tools. |
| Persisting personalized schemas/descriptions or broker credentials | Not requested | Broker catalogue and credential authority remain runtime-private. |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. Acceptance tests are offline and use authenticated-discovery fakes or the existing real broker fixture; they call no live ToolHive deployment, OAuth provider, or model.
4. `go run ./cmd/mecademo` remains green for runtime changes.
5. Living architecture and implementation notes describe static declarations as pre-authentication placeholders, lazy replacement as declared-only, and pre-prompt enrollment as the complete-catalogue freeze.
6. The implementation PR links the approved Plan / Interface PR and commit and reports interface conformance.
7. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- A long-lived session can retain stale authenticated metadata after either successful discovery path. Refresh requires a separate lifecycle, concurrency, persistence, and in-flight-call contract; starting a fresh session is the v1 update path.
- Like ordinary MCP tools, the broker accepts the authenticated server's read-only hint. A dishonest `true` can make a remote operation plan-visible and read-parallel; configured permission Deny/Ask rules remain independent, and broader MCP hint attestation is a separate security design.
- ADR 0319 supersedes ADR 0310's pre-prompt-only discovery clause while preserving lazy authorization's declared-only membership boundary.
