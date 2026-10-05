# AGENTS instruction hierarchy - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — changes instruction applicability, source-relative mapping, and ephemeral project context.
**Decision record:** [ADR 0376](../adr/0376-agents-instruction-hierarchy.md)
**Phase:** Target-scoped project instructions
**Status:** draft, 2026-10-05. The choices recorded below are authorized; exact material interfaces and discovery limits still need human review. No implementation approval, merge, or shipment is claimed.
**Delivery:** Split. Plan/interface PR first; implementation follows the approved plan checkpoint. The operator requested an implementation PR stacked on the plan PR, subject to explicit pre-merge approval of the identified plan commit; otherwise plan merge is the approval event.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#2090](https://github.com/stacklok/mecatl/issues/2090).

The first model request receives guidance from the root through the starting folder of the selected, admitted source. As structured file tools encounter additional directories, the next model request receives their applicable guidance as bounded, ephemeral project context. Instructions guide the model; they do not authorize or gate tool execution. This plan owns proposed behavior, not current behavior. Implementation will update the [public instruction guide](../../user-docs/features/project-instructions-and-rules.md) and [context architecture](../architecture/context-and-compaction.md) only when the feature ships.

## Human decisions

- [x] Use best-effort discovery rather than strict pre-effect visibility — Decision: the operator approved root-to-starting-folder guidance within the selected admitted source and further directory scopes discovered through covered structured tools. No host-ancestor inference or new source authority. Same-batch Read/Edit and first-touch mutations can run before the new guidance reaches the model.
- [x] Define the instruction-source root deliberately — Decision: the default source selected at the starting subfolder uses that subfolder as its instruction-source root. It intentionally excludes parent and repository-root AGENTS files, even when their ancestors are trusted; neither Git-root discovery nor host-ancestor expansion occurs automatically. Only an explicitly selected admitted broader source may contribute its own ancestors.
- [x] Keep guidance ephemeral — Decision: automatically discovered bodies appear in the next request's project context, not automatically in persisted tool results. Explicit ordinary Read keeps its existing result/history behavior. Retain encountered scopes within a run under a bounded context rather than replacing the active set on every batch; make source combine/whole-source replace and sibling isolation explicit.
- [x] Remove instruction-specific execution gates — Decision: no whole-batch reconsideration, per-effect instruction freshness checks, instruction approval fingerprints, or special retry machinery for Write, Copy, Move, or Remove. Existing Deny/Ask, effective-argument authorization, ReadLedger/CAS, and create-only writes remain in force. Guidance is not an authorization boundary.
- [x] Apply fallback in each directory — Decision: use AGENTS first, CLAUDE if AGENTS is missing or blank; a genuine read error never silently selects the fallback.
- [x] Bound guidance without blocking authorized tools — Decision: 32 KiB total instruction-content starting default. Safely truncate oversized text, and show truncation/omission in model context and a user-visible warning through existing diagnostics/projections. Exhausting content or discovery work stops further automatic instruction loading, not ordinary tool execution. No unbounded traversal or invalid UTF-8. Exact discovery numeric budget requires evidence; speculative 4,096/16,384 probe counts are not approved.
- [x] Reuse existing source and transport paths — Decision: no new bounded-read protobuf/native/MicroVM operations or external MicroVM release gate. Use existing reads and envelope protections; do not claim new backend preallocation guarantees. A native source without a usable binding/run grant is reported unavailable and best-effort work continues. Native source enablement remains a separate dependency before claiming that source works, not a gate on hierarchy completion.
- [x] Use one composition path — Decision: retain `Deps.Instructions`, existing assembler/discovery/manifest and operator source selection, trust, placement, exclusions, combine/replace. Keep the [PR #2095](https://github.com/stacklok/mecatl/pull/2095) operand-helper prerequisite and composition support; avoid a second pipeline or instruction-specific effective-call deferral logic.
- [ ] Approve the exact exported assembly signature and manifest scope fields, target normalization/subtree mapping, custom-assembler migration, and how the existing composition wrappers forward targets. The sketches below are draft interface choices, not approved declarations.
- [ ] Approve a measured numeric discovery-work ceiling and the exact bounded run-retention policy, including behavior when a new scope cannot fit, after adapter-cost evidence. The 32 KiB content starting default is approved; probe counts and retention capacity are not.

## Interface contract

- **gRPC / protobuf:** None — reuse existing read operations and frame/envelope protections; no new bounded-read operation, request field, or public inspection endpoint. Whole-file backend reads may allocate before client-side truncation; document this limitation.
- **Exported Go APIs / interfaces:** Proposed in-place extension of `InstructionAssembler.Assemble`, optional manifest-aware assembly, `InstructionManifest` scope metadata, `RootAssembler`, and `DiscoverInstructions` with explicit target input. Reuse `tool.WorkspaceReader` and the existing discovery paths; no new instruction result type, second assembly dependency, or new reader interface. Exact signature/fields remain an unchecked human decision, so this is not yet an implementation-ready API contract.
- **Tool schemas:** None — existing tool arguments and JSON schemas are unchanged. Use the corrected `tool.LocalFileOperands` for structured built-in Read/Edit/Write/Remove (`path`) and Copy/Move (`source`, `destination`). [Issue #2094](https://github.com/stacklok/mecatl/issues/2094) was fixed by [PR #2095](https://github.com/stacklok/mecatl/pull/2095), merged 2026-10-05 as [commit `f8b2d7f5843a2c3d04dbef3e4abf2b0b1611ffa5`](https://github.com/stacklok/mecatl/commit/f8b2d7f5843a2c3d04dbef3e4abf2b0b1611ffa5). The implementation baseline needs that fix or equivalent typed `session.ParseArgs` decoding and both-or-neither Copy/Move operands. Malformed or missing operands do not count as discovered targets; normal tool validation and authorization still apply. Do not infer targets from Shell or arbitrary custom/MCP arguments.
- **CLI / config:** None — no new source selector, feature toggle, or YAML key. Source registrations retain operator-selected order, admission, and whole-source `combine`/`replace` semantics.
- **Events / persistence:** None — discovered bodies and scope state stay run-local, not automatically persisted in tool history, events, or snapshots. Existing manifests account for request contributions; existing injected diagnostics/projections carry safe truncation, omission, and source-unavailable warnings. Explicit Read results retain their existing persistence contract.
- **Security / authority:** Source reads stay within the selected, admitted binding, including child isolation and no-FS attenuation. No target opens a host ancestor, changes placement, grants native run access, or mints ReadLedger evidence. User requests outrank repository guidance but not system/developer rules or tool permissions. Deny/Ask, effective-argument authorization, CAS, read-before-edit, and create-only rules are unchanged. Instruction loading failures cannot grant or revoke tool authority.
- **Compatibility / migration:** Change the existing exported assembler/discovery contract in place after the unchecked signature decision; update callers, API snapshots, and a classified engine changelog entry. Preserve one `Deps.Instructions` composition path, manifest/message alignment, the root case with no new scopes, and existing source trust. No legacy parallel pipeline, persisted-session migration, or rewrite of frozen predecessor ADRs. ADR 0376 would narrowly supersede root-only/per-run project guidance in [ADR 0359](../adr/0359-harness-context-source-authority.md) and [ADR 0043](../adr/0043-ephemeral-turn0-instruction-fragments.md) if approved.

### One selected-source composition path

Pass discovered targets through the existing assembly/manifest path; root and nested discovery share the implementation. Target-independent contributors remain global. Reuse the existing `MultiAssembler` composition and production source wrappers without bypassing trust admission or whole-source policy. The existing `replaceHarnessInstructions` seam must preserve unrelated leaves and their order for value/pointer `MultiAssembler`, `RootAssembler`, and `RulesAssembler` forms. Avoid a separate hierarchy assembler, independent `Deps` field, or exported scope-cache interface. Existing run-local static-leaf snapshots may be retained where appropriate; selected-source contributions must be reassembled for the next request so newly encountered scopes can appear. Keep operator-profile refresh on its existing independent path. Exact call signatures, manifest field names, and custom-source lifetime rules require the unchecked interface decision above.

Covered names follow the effective tool registration: a run-overlay `ExtraTools` shadow of `Write` is opaque, while a trusted catalog registration under a covered built-in key commits to its operand semantics. Discover only successfully parsed covered operands; discovery must not weaken the tool's own effective-argument authorization after hooks. No new target-extraction interface or additional `Spec()` calls are required. Copy/Move consider both operands and directory namespace operations use operand-parent scopes, not recursive descendant scanning.

### Scope, selection, and retention

At startup, resolve the selected instruction-source root-to-starting-folder chain, including ancestors *inside that source*. Here, "instruction-source root" means the root of the selected instruction source, not a repository or Git root. The default source selected at the starting subfolder has that subfolder as its root, so it does not read parent or repository-root guidance. For the proposed contract, `cd repo/website && mecatui` starts from `repo/website` and excludes `repo/AGENTS.md`. An explicitly selected admitted broader source rooted at `repo` is the positive control: it may contribute `repo/AGENTS.md` before `repo/website/AGENTS.md`. This is an explicit source-selection choice, never automatic ancestor expansion. When covered structured tools encounter additional directories, resolve their lexical ancestor chains within the same trusted execution-to-source subtree mapping. No traversal above the selected instruction-source root or automatic host/Git-root inference. For each directory, prefer nonblank `AGENTS.md`; use `CLAUDE.md` when AGENTS is missing or whitespace-only. A genuine read error is reported, never interpreted as absence. Validate UTF-8 before framing; keep successful text on character boundaries when truncating.

Frame each retained scope with its logical source and execution-relative directory. Ancestors apply root-to-leaf, with the nearest file winning conflicts; nonconflicting ancestor guidance remains. Siblings stay individually scoped, even if their bodies match, and do not govern each other. Identity mapping for a source selected at the execution root and an explicitly admitted broader source with a trusted subtree prefix remain supported; the latter may contribute source ancestors above the execution root. A source selected only at a subfolder cannot look above it. Lexical in-root aliases remain lexical scopes, with existing backend containment for file reads; out-of-root target strings do not license host discovery. No-FS runs retain global/root guidance without inventing file tools.

Keep encountered scopes available for later requests in the run subject to the 32 KiB total instruction-content starting default. The minimal proposed retention rule is stable first-encounter order: reserve room for the starting chain, retain each newly encountered scope while capacity remains, and do not evict an earlier scope merely because a batch targets a sibling. If another scope cannot fit, label it omitted and stop loading new scopes for that budget while continuing ordinary authorized tools. Refresh retained text for the next request within the same aggregate bound; label truncated text explicitly, never present it as complete. The exact discovery-work ceiling and retention details need evidence and human approval. Compaction does not persist automatic bodies, and a new run starts with its own bound and current admitted source.

The existing `combine`/`replace` policy operates on whole selected-source contributions across the retained target set, not independently for each target. For example, if higher-priority source A has only `website/AGENTS.md` and lower-priority source B has root guidance, a retained `[website, services]` view may select A and omit B entirely: services receives no B fallback. A services-only run can select B. The website scope remains website-only; it does not govern services. With `combine`, contributing sources retain their operator-specified order. Diagnostics and the public guide must disclose whole-source omission and sibling limits rather than promise per-target fallback. Adding a target or reaching a bound must never promote an unselected source outside existing operator policy.

### Delivery and resource outcomes

Initial guidance is supplied before the first provider request. Newly encountered guidance enters the *next* provider request's ephemeral project context, not the current model decision or an automatic persisted tool result. Same-batch Read/Edit, first-touch Write/Copy/Move/Remove, and earlier approved operations can execute before that guidance is visible. There is no instruction-specific retry, dispatch pause, rollback, per-effect recheck, or approval fingerprint. Ordinary permission/rewrite checks still decide execution; ordinary explicit Read still returns its content to the conversation. Opaque Shell, MCP/custom tools, and ListDir/Glob/Grep do not activate nested scopes automatically. A model-visible instruction through the real factory must explain this limitation and encourage concrete structured tools when scope guidance matters.

Content and work limits protect instruction loading, not tool execution. Use 32 KiB *total instruction text* as the starting default across automatically selected contributions, counting retained text without pretending the request-frame or backend-read allocation is capped by it. On overflow, truncate at a valid UTF-8 boundary or omit later text, visibly label affected scopes for the model, and send a safe user-visible warning via existing injected diagnostics/projection surfaces. Count discovery candidates including missing/blank AGENTS and fallback CLAUDE; share ancestor work where sensible. Choose a finite numeric probe budget only after measuring selected-source adapter cost and latency. Stop new instruction probes at the budget; never recursively enumerate a repository. Cancellation, unreadable files, containment failures, and an unavailable native binding/run grant are reported without switching to host storage or blocking otherwise authorized tool execution. Existing read paths and transport envelopes remain in force, without a claim of backend-side preallocation bounds. Ordinary request context-window handling applies after inclusion of bounded guidance; if a complete request still cannot fit, use the existing request failure behavior rather than silently discarding unrelated conversation.

A native execution-backed source is usable only when its existing binding and run-access checks permit source reads. Missing BindingID or reattachment without an active run grant must be reported as unavailable, with best-effort execution continuing; this proposal grants neither access nor an alternate source. Enabling that native source and qualifying its actual factory-to-provider path is a separate issue/dependency before claiming native support. No bounded transport operation or external MicroVM runtime release is a hierarchy completion prerequisite.

## In scope - 5 scenarios, in implementation order

### Scenario 1 - Hierarchy reaches provider context

Extend [existing discovery](../../engine/prompt/builder.go) and the single `RootAssembler` path under [ADR 0376](../adr/0376-agents-instruction-hierarchy.md).

**Acceptance:**
- AC1.1: First request includes admitted root-to-starting-folder guidance; an encountered nested target causes its ancestor and nearest directory instructions to appear in the next request, not automatically in persisted results. Explicit Read retains its ordinary result behavior. Mentioning a path in chat alone has no nested activation guarantee.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario1_ProviderHierarchy`
- AC1.2: In each directory, missing or blank AGENTS selects CLAUDE; a genuine AGENTS read error cannot select fallback. Ancestor order, nearest-conflict precedence, nonconflicting ancestors, and project/user-role provenance hold across root and nested discovery.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario1_FallbackCompatibility`
- AC1.3: A later sibling stays in scope alongside earlier encountered scopes within the run bound; siblings do not govern each other. Shared ancestors are deduplicated without deduplicating distinct scopes or global contributions. The website/services example proves whole-source replacement suppresses lower-priority B in a mixed view, while services-only selects B; diagnostics identify omission.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario1_SiblingsAndSourcePrecedence`

### Scenario 2 - Discovery does not gate file effects

Preserve [dispatch and permission invariants](../../AGENTS.md#implementation-boundaries).

**Acceptance:**
- AC2.1: Same-batch Read/Edit and first-touch Write/Copy/Move/Remove execute under ordinary permissions even when new guidance has not reached the model; the next request includes discovered scopes. No instruction-specific deferral, retry result, fingerprint, or rollback is created. Every executed call keeps ordinary paired results.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario2_BestEffortEffects`
- AC2.2: The corrected `LocalFileOperands` supplies both Copy/Move operands; incomplete/malformed operand sets do not count as scope discovery or bypass normal tool validation. Effective-argument rewrites still receive normal authorization, and read-before-edit, CAS, create-only, and Deny/Ask checks hold without an instruction-specific effective-call gate.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario2_OperandsAndAuthorization`
- AC2.3: Shell, custom/MCP, search/listing tools, and an opaque run-overlay shadow do not trigger structured-scope discovery. A real factory delivers a model-visible limitation, while ordinary permissions for those tools remain unchanged.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario2_OpaqueCoverage`

### Scenario 3 - Authority remains with the admitted source

Retain [ADR 0359](../adr/0359-harness-context-source-authority.md)'s admission and placement separation.

**Acceptance:**
- AC3.1: Distinct source/execution roots, trusted subtree mapping, and virtual sources use only selected admitted logical paths. A default source selected at a starting subfolder does not read poisoned parent or repository-root guidance, even when that ancestor is trusted; an explicitly selected admitted broader source is the positive control and may contribute its own ancestors. An unselected host/execution file or out-of-root operand cannot cause an ancestor walk or a new source selection.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario3_SourceMapping`
- AC3.2: Backend symlink containment, cancellation, and source authorization remain intact; discovery creates no execution ReadLedger evidence. No-FS and isolated/direct-write children retain existing source inheritance, exclusions, and independent bounded run scope state without reading poisoned child checkout guidance.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario3_ContainmentAndChildren`
- AC3.3: Native execution-backed selection without a binding/run grant reports the source unavailable and continues authorized tools. A usable source requires an independently qualified native enablement path; an unavailable native source alone does not block hierarchy completion.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario3_NativeUnavailable`

### Scenario 4 - Bounded instruction loading stays visible

Use [injected diagnostics](../architecture/observability.md), distinct from durable events.

**Acceptance:**
- AC4.1: Across automatically selected sources, the 32 KiB starting content default truncates on UTF-8 character boundaries or omits later text, marks exactly which guidance is partial in model context, and emits a safe user-visible diagnostic/projection warning. Invalid UTF-8 is never delivered. Crossing content or discovery-work ceilings stops additional instruction loading without stopping a normally authorized mutation.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario4_BoundedGuidance`
- AC4.2: Counted remote-adapter discovery (including absent candidates, per-directory fallback, multiple sources, and disjoint deep targets) motivates a finite numeric budget and latency assumption before approval. No next probe after exhaustion, no traversal outside root, and no false claim that existing whole-file reads preallocate to the content bound; existing envelope limits remain effective.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario4_DiscoveryCosts`
- AC4.3: Missing/blank files are absence; genuine read errors, containment failure, cancellation, and unavailable native access produce safe warnings without silent fallback to another namespace. Existing request-token accounting reflects the actual framed text; automatic bodies do not accumulate in history or snapshots across continuation, compaction, or a new run.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario4_WarningsAndContinuation`

### Scenario 5 - Composition and documentation match the shipped scope

Follow [ADR 0376](../adr/0376-agents-instruction-hierarchy.md), [engine compatibility](../../engine/COMPATIBILITY.md) and the [documentation ownership contract](../development-process.md#documentation-change-review).

**Acceptance:**
- AC5.1: Factory-to-provider offline proof uses one `Deps.Instructions` path, actual source admission, request manifests aligned to emitted fragments, and value/pointer MultiAssembler/RootAssembler/RulesAssembler composition preserving unrelated leaves. Existing source/execution and MicroVM offline client tests stay green; a fixture does not prove a separate native enablement issue complete.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario5_FactoryConformance`
- AC5.2: The owning public guide and context architecture are updated during implementation to describe starting-folder chain, best-effort next-request discovery, retained bounded scopes, per-directory fallback, whole-source replacement, trust prerequisites, and opaque-tool/first-touch limitations. API snapshots and classified changelog match the finally approved signatures; structural tests claim delivery, not live-model obedience.
  - verify: inspection — review owning pages and API/changelog diff; run `task api:check`, `task docs`, and `task site:build`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| On-demand path-scoped rule files | [#1974](https://github.com/stacklok/mecatl/issues/1974) | Retain current rules behavior. |
| Real Redis instruction/command qualification | [#1811](https://github.com/stacklok/mecatl/issues/1811) | Shared-source conformance does not claim Redis integration. |
| Native source enablement without existing binding/run access | Separate issue/dependency | Report unavailable; do not claim that source works until qualified. |
| AGENTS.override.md, imports, new home/global conventions | Separate proposal | Not established here. |
| Shell affected-path inference, persisted scope snapshots, watchers, public inspection API | Separate proposal | Ordinary authority and existing diagnostics suffice for this plan. |

## Definition of done

1. Resolve unchecked interface and budget decisions and approve the exact plan commit before implementation; do not infer approval from these recorded design choices.
2. Focused scenario/integration tests pass offline with isolated stores; `task test`, `task lint`, and `task test:race` pass on the implementation candidate.
3. Include `task api:update` output and classified changelog entries; pass `task api:check`, `task docs`, `task site:build`, and `task ac-trace-strict` when marked landed.
4. `go run ./cmd/mecademo` shows tool call, permission ask/approval, and result.
5. `/panel-review` has no ship blockers. The implementation PR identifies the approved plan baseline and any explicitly approved amendments. Humans merge PRs.

## Deferred decisions and known risks

Exact exported interface and discovery/retention ceilings remain unchecked human decisions, not implementation discretion. Whole-source replacement can suppress fallback guidance for another target, and a full budget can leave newly encountered instructions undiscovered. A first-touch mutation may complete before its scope reaches a request; external writers may change guidance between observations. Existing whole-file read paths and backend-dependent latency require measurement; neither content truncation nor an offline fixture proves backend preallocation bounds or native source enablement.
