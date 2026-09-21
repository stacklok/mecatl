# Workspace enrollment preserves session authority — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds narrow durable provenance for dynamically installed broker capabilities so authority replacement remains exact across persistence and successors.
**Decision record:** [ADR 0350](../adr/0350-durable-workspace-enrollment-broker-authority.md)
**Phase:** workspace MCP broker enrollment
**Status:** proposed, 2026-09-21. Prepared from [issue #1740](https://github.com/stacklok/mecatl/issues/1740); the operator selected no repair for already-corrupted sessions and terminal whole-bundle failure for broker/non-broker registration-key conflicts.
**Delivery:** Split. The change alters persisted session authority provenance, snapshot/successor behavior, and terminal enrollment failure semantics.
**Expected tasks:** 3.
**Issue:** [stacklok/mecatl#1740](https://github.com/stacklok/mecatl/issues/1740).
**Plan PR:** absent until opened.
**Approved baseline:** absent until approved.

Workspace enrollment reconfigures an idle existing session: it builds a session engine with the authenticated broker bundle and persists the same aggregate. Completion must preserve all non-broker capabilities, replace only the known dynamic broker bundle, and never widen a deliberately attenuated session. A rare duplicate between an enrolled broker registration and a non-broker registration fails the complete enrollment rather than silently redirecting or partially publishing tools.

## Human decisions

- [x] Treatment of already-corrupted sessions — Decision: leave them narrowed; no heuristic, migration, or automatic repair infers their former authority. A fresh session receives the corrected semantics.
- [x] Broker/non-broker registration-key collision policy — Decision: fail the complete enrollment terminally before publication; expose the safe conflicting registration key only, preserve the prior authority/runtime state, and require operator configuration correction before another explicit enrollment.

## Interface contract

- **gRPC / protobuf:** None — existing workspace-enrollment RPCs, routes, requests, and presentation-safe status vocabulary retain their current shape. A collision is represented through the existing terminal failure/error path.
- **Exported Go APIs / interfaces:** None — provenance and composition projections remain private to the existing session, snapshot, catalog-assembly, and server packages; no public engine API changes.
- **Tool schemas:** None — tools remain advertised by the existing catalog and authority-disclosure paths. A failed enrollment installs no partial broker wrapper.
- **CLI / config:** No new flag or setting. `/tools-connect` and `/mcp` retain their commands; a collision reports a terminal setup failure and an explicit retry remains available after configuration correction.
- **Events / persistence:** Add a private, bounded snapshot ledger for exact dynamically installed broker registration keys and an explicit presence bit; a present empty set is valid and differs from absent legacy provenance. Completion replaces the ledger atomically with the capability set; restore and successor paths preserve and validate it. A legacy snapshot with absent provenance cannot begin enrollment and remains unrepaired.
- **Security / authority:** Completion removes only the prior persisted dynamic broker keys, retains every other capability—including opaque MCP-resource and latent member capabilities—and adds only keys proven to have registered in the completed broker composition. Any overlap between either the incoming or prior broker-key set and the current non-broker registration set fails closed before candidate publication. Terminal cleanup may persist only the cleared pending control; it cannot publish candidate wrappers, binding, authority, or provenance. Permission mode and posture remain unable to widen persisted authority.
- **Compatibility / migration:** Fresh sessions and sessions with committed provenance receive exact replacement semantics. Already-corrupted legacy snapshots remain narrowed, receive no heuristic repair, and are refused before any enrollment-side mutation; a fresh session is the supported recovery path. No schema migration or compatibility reader is added.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — successful enrollment replaces only the dynamic broker bundle

Registration keys, not refreshed `Tool.Spec()` calls, are catalog metadata ([catalog](../../engine/tool/catalog.go)). The session aggregate owns durable authority ([workspace enrollment aggregate](../../engine/session/workspace_enrollment.go)); authority disclosure remains the runtime filter ([authority disclosure](../../engine/agent/authority_disclosure.go)). The dynamic broker-key ledger described by [ADR 0350](../adr/0350-durable-workspace-enrollment-broker-authority.md) permits exact bundle replacement without treating every carried capability as a catalog registration.

**Acceptance:**
- AC1.1: A successful initial enrollment retains all carried non-broker capabilities, including core, delegation, memory, skill, global/client-MCP, opaque MCP-resource, and latent member capabilities, and adds every enrolled broker key that actually registered in the final catalog.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario1_PreservesUnrelatedCapabilities`
- AC1.2: A successful broker refresh removes the prior committed broker keys, adds the replacement broker keys, and leaves all other authority capabilities unchanged.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario1_ReplacesDynamicBrokerBundle`
- AC1.3: Enrollment does not restore a capability deliberately excluded from the prior authority, even if the rebuilt catalog exposes that capability.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario1_DoesNotWidenAttenuatedAuthority`

### Scenario 2 — provenance is durable and publication is failure-atomic

A session's durable authority is copied through normal successor paths, while broker attachments are process-local ([broker attachment](../../internal/adapter/server/mcp_broker.go)). [ADR 0350](../adr/0350-durable-workspace-enrollment-broker-authority.md) requires the private dynamic-broker key set to follow the authority snapshot and successor without disclosing broker runtime or credential material.

**Acceptance:**
- AC2.1: Resume and fork preserve a successfully committed authority and dynamic broker-key set, and rebuilt engines disclose exactly the authorized, currently registered tools.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_PersistsAcrossResumeAndFork`
- AC2.2: Engine construction, attachment commit, aggregate completion, or snapshot persistence failure leaves the prior authority and dynamic broker-key set unchanged and publishes no partial replacement.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_FailureIsAtomic`
- AC2.3: A legacy snapshot without dynamic-broker provenance remains narrowed and is not widened, guessed, or automatically repaired; its enrollment control fails before reset, discovery, browser consent, attachment mutation, or runtime mutation and directs recovery to a fresh session.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_LegacySnapshotIsNotRepaired`
- AC2.4: A present empty dynamic-broker ledger remains distinguishable from missing legacy provenance through snapshot, restore, and fork; malformed or oversized present provenance fails closed.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_ProvenancePresenceAndBounds`

### Scenario 3 — a registration collision fails the whole enrollment safely

Workspace enrollment is a complete authenticated bundle: a failure exposes no partial protected catalog (the broker deployment guide at `user-docs/building/deployment/mecak8s.md`). Exact registration keys select the executable tool, so a broker candidate must not replace or be silently omitted behind a non-broker key.

**Acceptance:**
- AC3.1: When either an enrolled broker candidate or a prior recorded broker key conflicts with a current non-broker registration key, the server rejects the full enrollment before candidate wrapper, binding, authority, or provenance publication; no broker tool from that bundle becomes executable. It may persist only terminal cleanup that clears the pending control, while a destructive refresh leaves broker runtime unavailable.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario3_ConflictFailsWholeEnrollment`
- AC3.2: The terminal failure gives the owning client the safe conflicting registration key and an actionable retry-after-configuration-correction message, while omitting OAuth URLs, endpoints, callbacks, credentials, tokens, and connector configuration.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario3_ConflictPresentationIsSafe`
- AC3.3: Mecatui’s existing enrollment controls show the terminal setup failure, do not send a prompt retained pending successful enrollment, and offer the existing explicit retry path after the operation settles.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario3_TerminalConflictUX`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Automatic repair or migration of already-corrupted authority snapshots | Future explicitly approved compatibility work | The persisted set cannot distinguish accidental loss from deliberate attenuation; legacy sessions refuse enrollment before side effects and require a fresh session. |
| Origin metadata for every catalog entry | Future need for general source-aware authorization | Only the dynamic broker bundle needs durable provenance. |
| Partial broker enrollment or per-tool collision omission | None | Workspace enrollment remains complete and atomic at the catalog boundary. |
| New RPCs, events, flags, config keys, or model-facing notices | None | Existing controls and tool definitions are sufficient. |

## Definition of done

1. The Plan / Interface PR is merged and its commit is recorded as the approved baseline before implementation begins.
2. Offline aggregate, snapshot, server, broker, gRPC/HTTP, and mecatui tests prove every named acceptance criterion without a live model, OAuth provider, browser, or Kubernetes cluster.
3. `task lint`, `task test`, `task api:check`, `task docs`, and `task site:build` pass; `go run ./cmd/mecademo` remains green.
4. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
5. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance and no unwaived `/panel-review` blocker.

## Deferred decisions and known risks

- Catalog registration outcomes must be captured at composition time without extra `Tool.Spec()` calls, in accordance with the [catalog metadata rule](../../AGENTS.md).
- The private snapshot representation and package-private helper placement are implementation details, provided ADR 0350's exact replacement, collision, and failure-atomicity rules hold.
