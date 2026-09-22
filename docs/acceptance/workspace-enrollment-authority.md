# Workspace enrollment preserves session authority — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — adds narrow durable provenance for dynamically installed broker capabilities so authority replacement remains exact across persistence and successors.
**Decision record:** [ADR 0350](../adr/0350-durable-workspace-enrollment-broker-authority.md)
**Phase:** workspace MCP broker enrollment
**Status:** proposed, 2026-09-22. Revision of [issue #1740](https://github.com/stacklok/mecatl/issues/1740)'s proposed plan; it remains proposed until the Plan / Interface PR receives human merge approval.
**Delivery:** Split. The change alters persisted session authority provenance, snapshot/successor behavior, and terminal enrollment failure semantics.
**Expected tasks:** 3.
**Issue:** [stacklok/mecatl#1740](https://github.com/stacklok/mecatl/issues/1740).
**Plan PR:** [#1741](https://github.com/stacklok/mecatl/pull/1741).
**Approved baseline:** absent until approved.

Workspace enrollment reconfigures an owner-authorized idle session. A completed refresh replaces only its prior broker registration-key bundle: it retains all other carried capabilities and preserves absence of non-broker capabilities. `/tools-connect` is not authorization to override a persisted restriction. A rare registration-key collision between the broker bundle and the non-broker catalog fails the whole enrollment before publication.

## Human decisions

- [x] Treatment of already-corrupted sessions — Decision: leave them narrowed; no heuristic, migration, or automatic repair infers their former authority. A fresh session receives the corrected semantics.
- [x] Broker/non-broker registration-key collision policy — Decision: fail the complete enrollment terminally before publication; expose the safe conflicting registration key only, preserve the prior durable authority/provenance, and require operator configuration correction before another explicit enrollment.
- [x] Broker-specific attenuation — Decision: preserve exclusion of a previously recorded broker key that is absent from carried authority. The full replacement bundle still registers, but completion does not restore that key to authority. A key absent from the prior broker ledger is genuinely new and remains eligible to be added.

## Authority boundary

Enrollment must not override carried restrictions or widen narrowed child authority. The replacement formula applies to the existing enrollable root/successor flow, not to arbitrary capability editing or a new child-enrollment grant.

The previously asserted child journey is blocked in the traced implementation. A child inherits the parent's owner (`engine/agent/subagent.go`, `stampDelegatedLabels` call), so ownership alone would pass, but its constructors in `engine/agent/child_environment.go` do not copy `ExternalBinding`. `internal/adapter/server/workspace_enrollment.go` (`workspaceEnrollmentTarget`) calls `openBrokerAttachment` with `bindingRequired=true`; `internal/adapter/server/mcp_broker.go` rejects an empty binding before lookup or `AttachSession`. That error is not `ErrBrokerBindingMismatch`, so it cannot enter the rebind branch. Clear/fork cannot turn the child into an enrollable successor: `internal/adapter/server/service.go` (`managementSession`) rejects non-main kinds and delegation prefixes before successor creation. A missing root-kind filter in enrollment is therefore not proof of child reachability. No supported child-widening journey is established; custom hosts or manually edited snapshots are outside this plan, not implicit authorization. If implementation establishes a different supported journey, stop for a narrow plan decision rather than regrant authority.

## Interface contract

- **gRPC / protobuf:** None — existing workspace-enrollment RPCs, routes, requests, and presentation-safe status vocabulary retain their current shape. A collision uses the existing terminal failure/error path.
- **Exported Go APIs / interfaces:** Add `session.Session.WorkspaceEnrollmentBrokerKeys() ([]string, bool)` and `session.Session.RestoreWorkspaceEnrollmentBrokerKeys([]string, bool) error`. The accessor returns an independent exact registration-key set and its presence bit; the restore method validates copied provenance during idle snapshot restoration or successor construction, before history/control restoration. `CompleteWorkspaceEnrollment(pending, exactTools)` keeps its signature but its documented `exactTools` value becomes the completed broker registration-key bundle, replacing only the prior bundle. The new accessors are Added/minor. The existing completion method's changed replacement semantics and legacy refusal require separate behavioral compatibility review under `engine/COMPATIBILITY.md`; unchanged signatures do not make them additive (Changed is breaking under that policy). During implementation, run `task api:update`, include `engine/api/session.txt`, and record the additions and behavioral classification in `engine/CHANGELOG.md`. The exported `sessnap.Snapshot` and `eventsource.SessionMeta` fields specified below are reference-adapter API additions: `engine/adapter/*` is excluded from the guarded stability surface, so they require no new core API baseline. Document their same-revision host obligations and test restoration parity; the core accessor and completion compatibility obligations still apply.
- **Tool schemas:** None — tools remain advertised by the existing catalog and authority-disclosure paths. A failed enrollment installs no partial broker wrapper.
- **CLI / config:** No new flag or setting. `/tools-connect` and `/mcp` retain their commands; they do not grant authority to restore, add, or remove unrelated persisted capabilities. A collision reports a terminal setup failure and an explicit retry remains available after configuration correction.
- **Events / persistence:** Add the exported snapshot fields below. `sessnap.Of`/`Restore` use the aggregate accessors. Clear/fork copy the exact ledger and presence bit alongside carried authority, including absent legacy provenance; a fresh attachment does not mean an empty ledger. No completed enrollment or runtime is copied. Persist registration keys only, never endpoints, connector metadata, OAuth state, credentials, schemas, or runtime handles.

```go
// sessnap.Snapshot
WorkspaceEnrollmentBrokerKeys        []string `json:"workspace_enrollment_broker_keys,omitempty"`
WorkspaceEnrollmentBrokerKeysPresent bool     `json:"workspace_enrollment_broker_keys_present,omitempty"`

// eventsource.SessionMeta (host-supplied metadata, not a JSON contract)
WorkspaceEnrollmentBrokerKeys        []string
WorkspaceEnrollmentBrokerKeysPresent bool
```

  Presence uses normal bool/`omitempty` semantics. Missing or explicit `false` with zero keys means legacy-absent; `true` with omitted keys or `[]` means present-empty; `true` with a valid nonempty array means present-nonempty. JSON `null` for the slice also decodes as zero keys, without changing the presence bit. Reject nonempty keys with missing/false presence, null/non-boolean presence, non-array/non-string key data, and invalid key sets. Reuse `ValidWorkspaceEnrollmentToolNames`: at most 256 unique, nonempty, valid-UTF-8 keys, at most 256 bytes each and 32 KiB combined, with no Unicode control characters. Validate raw JSON before decoding can silently normalize malformed input; test the bounds at and beyond each limit. Canonical writes omit zero-length keys and false presence, but preserve true presence even for empty keys.

  `eventsource.Fold` currently restores `Authority` from `SessionMeta`; its stream has no workspace-enrollment provenance or pending-enrollment reconstruction. Add the two exported metadata fields above and restore their copied values through the aggregate method after authority binding, before history/control restoration (including the awaiting early return). Authority and ledger must come from the **same committed authority revision**, not stale creation metadata after enrollment. Hosts must persist/read them consistently; Fold cannot detect a stale but structurally valid pair from events. If exact provenance is unavailable, restore absent provenance and refuse enrollment at the server guard, never default it to present-empty. Invalid metadata fails reconstruction. Pending workspace enrollment still requires snapshot restoration; this change adds no event semantics or pending-enrollment support to Fold.
- **Security / authority:** Let `excluded prior broker keys = prior broker keys − prior authority`. Completion computes `(prior authority − prior broker keys) + (newly registered broker keys − excluded prior broker keys)` and records the complete new broker bundle as provenance. It therefore preserves exclusion of a previously recorded broker key, admits a genuinely new broker key, cannot restore or add an unrelated capability absent from prior authority, and cannot subtract a non-broker capability. The existing owner, idle, no-live-run, no-conflicting-control, lease, and broker-configuration checks remain the enrollment boundary; no arbitrary capability-removal interface is introduced. Registration collisions fail before candidate engine/attachment/authority/provenance publication. Unauthorized or concealed callers receive the existing not-found-style failure and no key or state disclosure.
- **Compatibility / migration:** Existing snapshots without provenance are legacy and cannot begin enrollment. In `workspaceEnrollmentTarget`, immediately after `Store.Load` and owner authorization succeed, the service checks that the restored ledger is present and valid. It refuses absent or invalid provenance before the current completed-session `Reopen`/`saveSession`, `ResetWorkspaceEnrollment`, broker attachment open/rebind, discovery, browser consent, or any candidate runtime/authority mutation; a fresh session is the recovery path. Snapshot-format addition is backward readable but intentionally not a compatibility repair. The API baseline and classified changelog are implementation deliverables, not prerequisites for plan approval.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — successful enrollment replaces only the dynamic broker bundle

Registration keys—not refreshed `Tool.Spec()` calls—are catalog metadata ([catalog](../../engine/tool/catalog.go)). The aggregate currently binds a durable authority and completes enrollment through `CompleteWorkspaceEnrollment` ([authority](../../engine/session/principal.go), [enrollment](../../engine/session/workspace_enrollment.go)). The server permits only the owner-authorized, idle, broker-configured session path and reopens a completed session before that check ([control](../../internal/adapter/server/workspace_enrollment.go)). [ADR 0350](../adr/0350-durable-workspace-enrollment-broker-authority.md) limits the ledger to broker keys rather than catalog-origin metadata for every capability.

**Restriction trace:** `internal/adapter/permconfig/permconfig.go` (`rulesFromConfig`) converts user/project/operator `permissions.deny` into permission rules, not capability deletions; `engine/adapter/permpolicy/permpolicy.go` (`Evaluate`) folds configured and learned rules with the session mode. These denies/asks remain effective for broker and non-broker calls. `engine/tool/catalog.go` applies plan-mode advertisement/dispatch filtering, and `internal/app/catalog.go` (`assembleCatalog`) retains no-FS profile exclusions. `internal/app/root_authority.go` (`mintRootAuthority`) mints catalog/resource/latent capabilities; `internal/adapter/server/service.go` (`setPerSessionLabels`) adds broker/client keys only for fresh authority, explicitly not for carried authority. `engine/agent/authority_delegation.go` (`deriveDelegatedAuthority`) narrows child capabilities, but the binding and successor gates traced above prevent that child from entering the supported enrollment flow. Root creation and main-session successors do not expose arbitrary per-key capability editing. As a defensive aggregate rule, completion preserves exclusion of a prior broker-ledger key absent from carried authority while admitting a key absent from the prior ledger as genuinely new. Permission/mode/profile restrictions continue to govern all calls, and the change authorizes no child widening.

**Acceptance:**
- AC1.1: Initial enrollment retains every carried non-broker capability and adds every broker registration key that actually registered in the final catalog.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario1_PreservesUnrelatedCapabilities`
- AC1.2: Refresh removes only prior recorded broker keys, adds only replacement broker keys, and leaves every other carried capability unchanged, preserving absence of non-broker capabilities.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario1_ReplacesDynamicBrokerBundle`
- AC1.3: Enrollment never restores an excluded non-broker capability even if the rebuilt catalog exposes it. If a prior broker-ledger key is absent from carried authority, completion registers the full new bundle but excludes that key from replacement authority; a genuinely new broker key absent from the prior ledger remains eligible to be added. Configured permission denies/asks, mode filtering, and profile exclusions remain enforced for subsequent calls, including broker calls; authority's non-tool axes remain unchanged. Narrowed children gain no enrollment or authority-widening permission from this change; retain the empty-binding refusal and main-only successor gate.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario1_DoesNotWidenAttenuatedAuthority`, `TestWorkspaceEnrollmentAuthority_Scenario1_PreservesSupportedRestrictions`

### Scenario 2 — provenance survives restart and successors without copying runtime

`rehydrateSession` deliberately builds without broker tools and does not resurrect a broker attachment from persisted binding ([rehydration](../../internal/adapter/server/service.go)). Clear and fork carry authority but allocate a new session and fresh broker attachment ([successors](../../internal/adapter/server/placement_successor.go)). [ADR 0350](../adr/0350-durable-workspace-enrollment-broker-authority.md) requires the ledger to follow carried authority across both boundaries so a subsequent explicit refresh can subtract inherited old keys.

**Acceptance:**
- AC2.1: A real snapshot/store restart preserves a committed authority and present broker-key ledger, but rehydrates no broker runtime, attachment, or wrappers. Ordinary prompts remain usable only through the non-broker runtime; an owner must explicitly refresh to install broker tools. An event-log system of record obtains identical ledger provenance only by supplying the exact presence bit and keys through `eventsource.SessionMeta`; no `session.Event` reconstructs enrollment provenance.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_RestartDoesNotResurrectBrokerRuntime`
- AC2.2: The subsequent explicit refresh subtracts the pre-restart recorded broker keys and commits only the newly registered bundle, preserving the remaining authority exactly.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_RefreshSubtractsRestoredBrokerKeys`
- AC2.3: Resume and authority-carrying successors, including clear and fork, preserve the exact committed ledger and presence bit alongside authority. Clear has empty history and fork carries valid history; both create fresh attachments/bindings without copying completed enrollment or source runtime. Their later explicit refresh subtracts inherited old broker keys. Neither a ledger nor carried authority proves a live connection.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_PersistsAcrossResumeAndFork`, `TestWorkspaceEnrollmentAuthority_Scenario2_SuccessorsStartFreshBrokerEnrollment`
- AC2.4: Raw-JSON tests exercise the exact interface encoding: missing and explicit false presence with omitted/empty/null keys remain legacy-absent; true with omitted/empty/null keys stays present-empty; true with valid nonempty keys stays present-nonempty through marshal/unmarshal. Reject nonempty keys without true presence, null/non-boolean presence, non-array/non-string key data, duplicates, empty/control-bearing/invalid-UTF-8 keys, and all three size-bound violations. Prove the same cases through direct aggregate/snapshot restore and successor copy. Enrollment rejects absent provenance at the load-and-owner-auth guard before reopen/persist, reset, attachment rebind, discovery, consent, or runtime mutation and directs recovery to a fresh session. Eventsource tests prove same-revision authority/ledger parity for all three presence states, including the awaiting reconstruction branch; malformed metadata fails reconstruction, and unavailable provenance remains enrollment-refused, never present-empty.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_ProvenancePresenceAndBounds`, `TestWorkspaceEnrollmentAuthority_Scenario2_LegacySnapshotIsNotRepaired`
- AC2.5: Engine construction, attachment commit, aggregate completion, or snapshot persistence failure leaves prior authority and broker-key provenance unchanged and publishes no partial replacement. Terminal cleanup may clear only the pending control; destructive refresh need not restore withdrawn broker runtime. Fault injection proves this ordered fail-closed behavior, not a cross-resource transaction.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario2_FailureIsAtomic`

### Scenario 3 — a rare cross-source collision fails the whole enrollment safely

This is a cross-source safeguard at Mecatl's catalog-registration boundary, not a reimplementation of ToolHive's internal prefix or conflict resolution. [ADR 0350](../adr/0350-durable-workspace-enrollment-broker-authority.md) requires composition to report broker registration outcomes from the one final catalog build. It must register candidate tools through the error-returning path, not `MustRegister`, and hand a bounded safe duplicate-key error to the server without an additional `Tool.Spec()` call.

**Acceptance:**
- AC3.1: A current non-broker registration matching either an incoming broker key or a prior recorded broker key fails the whole enrollment before candidate wrapper, binding, authority, or provenance publication. No broker tool from that bundle becomes executable; destructive refresh may leave broker runtime unavailable while prior durable authority/provenance remains unchanged.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario3_ConflictFailsWholeEnrollment`
- AC3.2: The internal catalog/factory handoff returns the exact successfully registered broker keys on success, or a typed/safe duplicate-key failure on collision. It performs no extra `Tool.Spec()` calls and never panics through `MustRegister` for dynamic broker registration.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario3_RegistrationMetadataAndDuplicateHandoff`
- AC3.3: An owner receives only the safe conflicting key and retry-after-configuration-correction guidance. An unauthorized collision caller receives the existing concealed failure, with no key disclosure or mutation; neither path exposes OAuth URLs, endpoints, callbacks, credentials, tokens, or connector configuration.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario3_ConflictPresentationAndOwnership`, `TestWorkspaceEnrollmentAuthority_Scenario3_ConflictPresentationIsSafe`
- AC3.4: Mecatui's existing enrollment controls show the terminal setup failure, do not send a prompt retained pending successful enrollment, and offer the existing explicit retry path after the operation settles.
  - verify: `TestWorkspaceEnrollmentAuthority_Scenario3_TerminalConflictUX`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Automatic repair or migration of already-corrupted authority snapshots | Future explicitly approved compatibility work | The persisted set cannot distinguish accidental loss from deliberate attenuation; legacy sessions refuse enrollment before side effects and require a fresh session. |
| Origin metadata for every catalog entry | Future need for general source-aware authorization | Persist only the completed workspace-enrollment broker registration keys. |
| Arbitrary authority editing/removal | Future separately authorized authority management | This change only replaces the known broker bundle during the existing enrollment flow. |
| Partial broker enrollment or per-tool collision omission | None | Workspace enrollment remains complete and atomic at the catalog boundary. |
| ToolHive prefix/conflict policy | ToolHive | Mecatl only protects its cross-source final registration boundary. |
| New RPCs, events, flags, config keys, or model-facing notices | None | Existing controls and tool definitions are sufficient. |

## Definition of done

1. The Plan / Interface PR is merged and its commit is recorded as the approved baseline before implementation begins.
2. Offline aggregate, snapshot, server, broker, gRPC/HTTP, and mecatui tests prove every named acceptance criterion without a live model, OAuth provider, browser, or Kubernetes cluster.
3. `task lint`, `task test`, `task api:check`, `task docs`, and `task site:build` pass; `go run ./cmd/mecademo` remains green.
4. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
5. The implementation PR links the Plan / Interface PR and approved commit and reports interface conformance and no unwaived `/panel-review` blocker.

## Deferred decisions and known risks

- The composition handoff may be a narrow addition to the existing root-internal `SessionEngineResult`/factory contract or an equivalent internal result; it must expose only exact registration keys and a safe duplicate-key classification, not general catalog provenance.
- If the existing factory cannot distinguish a prior key now owned by a non-broker registration without expanding that narrow handoff, implementation must stop for a plan amendment rather than infer origin or weaken the collision rule.
