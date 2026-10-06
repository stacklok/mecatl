# AGENTS instruction hierarchy - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — changes instruction applicability, source-relative mapping, and ephemeral project context.
**Decision record:** [ADR 0376](../adr/0376-agents-instruction-hierarchy.md)
**Phase:** Target-scoped project instructions
**Status:** proposed amendment, 2026-10-06. The product decisions in the merged base plan are approved; the four technical contracts selected below are proposed for human review, not yet approved, implemented, or shipped.
**Delivery:** Split. Implementation for #2110 follows this amended plan baseline only after human review/approval under the development process; no approval is inferred from this proposal. Humans merge PRs.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#2090](https://github.com/stacklok/mecatl/issues/2090).

The first model request receives guidance from the root through the starting folder of the selected, admitted source. As structured file tools encounter additional directories, the next model request receives their applicable guidance as bounded, ephemeral project context. Instructions guide the model; they do not authorize or gate tool execution. This plan owns proposed behavior, not current behavior. Implementation will update the [public instruction guide](../../user-docs/features/project-instructions-and-rules.md) and [context architecture](../architecture/context-and-compaction.md) only when the feature ships.

## Human decisions

- [x] Use best-effort discovery rather than strict pre-effect visibility — Decision: the operator approved root-to-starting-folder guidance within the selected admitted source and further directory scopes discovered through covered structured tools. No host-ancestor inference or new source authority. Same-batch Read/Edit and first-touch mutations can run before the new guidance reaches the model.
- [x] Define the instruction-source root deliberately — Decision: the default source selected at the starting subfolder uses that subfolder as its instruction-source root. It intentionally excludes parent and repository-root AGENTS files, even when their ancestors are trusted; neither Git-root discovery nor host-ancestor expansion occurs automatically. Only an explicitly selected admitted broader source may contribute its own ancestors.
- [x] Keep guidance ephemeral and snapshot-stable — Decision: automatically discovered bodies enter ephemeral project context, not persisted tool results, conversation, events, or snapshots. Retain loaded scopes *and their text* across follow-up user messages, approval continuations, and compaction in the same live session. Discover additional scopes lazily; never automatically reread loaded guidance when a scope is added or on a later request. Explicit ordinary Read remains unchanged. After process/server restart, reopening may lazily rediscover current guidance without persisting the previous snapshot.
- [x] Remove instruction-specific execution gates — Decision: no whole-batch reconsideration, per-effect instruction freshness checks, instruction approval fingerprints, or special retry machinery for Write, Copy, Move, or Remove. Existing Deny/Ask, effective-argument authorization, ReadLedger/CAS, and create-only writes remain in force. Guidance is not an authorization boundary.
- [x] Apply fallback in each directory — Decision: use AGENTS first, CLAUDE if AGENTS is missing or blank; a genuine read error never silently selects the fallback.
- [x] Bound guidance without blocking authorized tools — Decision: Configurable 64 KiB (65,536 bytes) default for combined retained automatic guidance, not per-file or cumulative reads; operators may raise it. Earlier loaded text wins, UTF-8-safe truncation/omission is labelled and warned, and authorized tools continue. This does not bound backend allocation or transport envelopes. This amendment selects a structural new-scope work bound below, not an unsupported measured latency ceiling.
- [x] Reuse existing source and transport paths — Decision: no new bounded-read protobuf/native/MicroVM operations or external MicroVM release gate. Use existing reads and envelope protections; do not claim new backend preallocation guarantees. A native source without a usable binding/run grant is reported unavailable and best-effort work continues. Native source enablement remains a separate dependency before claiming that source works, not a gate on hierarchy completion.
- [x] Use one composition path — Decision: extend the existing `Deps.Instructions` assembler/discovery/manifest and source-composition contracts in place. Migrate custom callers to the extended canonical contract; do not leave a deprecated or optional legacy path. Preserve source selection, trust, placement, exclusions, and whole-source combine/replace. No new transport or public API endpoint.
- [x] Distinguish child contexts — Decision: fresh-context subagents read current guidance from inherited admitted sources; conversation forks inherit the parent's loaded guidance snapshot. Both have independent subsequent discovery and budgets, and respect inherited exclusions and no-FS. Ordinary worktree forks of the same codebase support inherited guidance and nested discovery via existing trusted mapping/composition, without treating the child checkout as new source authority.
- [x] Proposed amendment: canonical interface — Decision: Select the exact in-place signatures, source binding, logical manifest fields, target normalization and wrapper/custom-caller migration in the interface contract below for human review; no optional legacy path.
- [x] Proposed amendment: config — Decision: Select the exact operator YAML/Go field, validation/default, and factory/child propagation below for human review.
- [x] Proposed amendment: ephemeral ownership — Decision: Select session aggregate and server attachment lifetimes, child behavior and cleanup below for human review; implementation must update ADR 0027 Lists 1/2.
- [x] Proposed amendment: discovery work — Decision: Select bounded structural reads and metadata accounting below in place of an unsupported numeric probe-count/latency promise, for human review.

## Interface contract

- **gRPC / protobuf:** None — reuse existing read operations and frame/envelope protections; no new bounded-read operation, request field, or public inspection endpoint. Whole-file backend reads may allocate before client-side truncation; document this limitation.
- **Exported Go APIs / interfaces:** Extend existing `prompt.InstructionAssembler.Assemble`, `prompt.AssembleWithManifest`, `prompt.DiscoverInstructions`, `prompt.InstructionManifest`, and `prompt.RootAssembler` in place as specified below. All custom assemblers and composition wrappers migrate to the same required method. Reuse `tool.WorkspaceReader` and existing discovery paths; no optional manifest/target-aware interface or legacy overload.
- **Tool schemas:** None — existing tool arguments and JSON schemas are unchanged. Use the corrected `tool.LocalFileOperands` for structured built-in Read/Edit/Write/Remove (`path`) and Copy/Move (`source`, `destination`). [Issue #2094](https://github.com/stacklok/mecatl/issues/2094) was fixed by [PR #2095](https://github.com/stacklok/mecatl/pull/2095), merged 2026-10-05 as [commit `f8b2d7f5843a2c3d04dbef3e4abf2b0b1611ffa5`](https://github.com/stacklok/mecatl/commit/f8b2d7f5843a2c3d04dbef3e4abf2b0b1611ffa5). The implementation baseline needs that fix or equivalent typed `session.ParseArgs` decoding and both-or-neither Copy/Move operands. Malformed or missing operands do not count as discovered targets; normal tool validation and authorization still apply. Do not infer targets from Shell or arbitrary custom/MCP arguments.
- **CLI / config:** Operator YAML `harness_context.project_instruction_max_bytes` maps to `*int permconfig.HarnessContextSection.ProjectInstructionMaxBytes`, then `int app.Config.ProjectInstructionMaxBytes` and `int agent.Deps.ProjectInstructionMaxBytes`. Omitted YAML or Go zero defaults to 65,536; explicitly supplied YAML zero, negatives, fractional/non-integers and overflow are invalid. Positive integers have no artificial upper cap or unlimited mode. Build rejects negative Go values. Apply parsed operator-only policy through the existing resolver to shared/per-session/child/factory composition; no project-file override, CLI flag or new source selector. The effective cap is set at build/new-session time; children inherit it, with no live hot-reload promise.
- **Events / persistence:** No new durable events, protobuf fields, or persisted instruction snapshots. Ephemeral session aggregate state and a server attachment map are specified below; explicit Read retains ordinary persistence. Warnings use injected diagnostics and existing `session.EvHook` / `HookPayload{Phase: "ProjectInstructions", Decision: HookAdvisory}` through existing client projections, not a new event kind. Only safe static reasons and logical scopes, never instruction bodies, raw backend errors, or host paths; forward only safe ProjectInstructions child advisories to parents and test the actual receiving client.
- **Security / authority:** Source reads stay within the selected, admitted binding, including child isolation and no-FS attenuation. No target opens a host ancestor, changes placement, grants native run access, or mints ReadLedger evidence. User requests outrank repository guidance but not system/developer rules or tool permissions. Deny/Ask, effective-argument authorization, CAS, read-before-edit, and create-only rules are unchanged. Instruction loading failures cannot grant or revoke tool authority.
- **Compatibility / migration:** Update all callers and exported API snapshots via `task api:update`, plus classified `engine/CHANGELOG.md`; run `task api:check`. Preserve one `Deps.Instructions` composition path, manifest/message alignment, root case and source trust. No legacy parallel pipeline, persisted-session migration, or rewrite of frozen predecessor ADRs. ADR 0376 proposes narrowly superseding root-only/per-run project guidance in [ADR 0359](../adr/0359-harness-context-source-authority.md) and [ADR 0043](../adr/0043-ephemeral-turn0-instruction-fragments.md) if approved.

### Selected declarations for review

```go
type InstructionAssembler interface {
    Assemble(ctx context.Context, directories []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []InstructionManifest, error)
}
func AssembleWithManifest(ctx context.Context, a InstructionAssembler, directories []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []InstructionManifest, error)
func DiscoverInstructions(ctx context.Context, source tool.WorkspaceReader, directory string) ([]session.Message, []InstructionManifest, error)
type RootAssembler struct {
    Source tool.WorkspaceReader
    SourceID string
    SourcePrefix string
}
type InstructionManifest struct {
    Kind, Provenance, SourceID, Directory, File string
    Partial, Omitted, HasGuidance bool
}
```

`AssembleWithManifest` only nil-safely delegates to the required method. `DiscoverInstructions` reads **one** source-relative directory (AGENTS first, then CLAUDE if AGENTS is missing/blank); a genuine error never falls back. Each emitted message has exactly one manifest row. `Directory` is the execution-relative logical scope, `File` the logical source-relative selected filename, `SourceID` a nonempty opaque logical binding identity supplied by composition (including source revision identity, never a host path). `HasGuidance` distinguishes a source's actual text from warning-only messages; warnings cannot win whole-source `replace`. `Partial`/`Omitted` label bounded guidance. Global contributors ignore directories/state and keep their existing lifetime. `RootAssembler.SourcePrefix` is a required trusted, valid in-root source-relative lexical prefix for the execution root (`.` for identity); real binding composition supplies it, never `filepath.Rel` between unrelated roots. The loop uses `LocalFileOperands` and existing lexical helpers to normalize covered operands to execution-relative directories, accepting in-root absolute operands as today; escaping targets are rejected for discovery only, without changing ordinary tool semantics. Pass `.` and retained directories; root assembly includes explicitly admitted broader-prefix ancestors above the execution root. Wrappers forward the same parameters and preserve unrelated contributors, ordering, and source admission.

```go
type InstructionScope struct {
    SourceID, Directory, File, Text string
    Partial, Omitted bool
    Examined bool
    Unavailable bool
}
type InstructionSnapshot struct {
    Directories []string
    Scopes []InstructionScope
    DiscoveryExhausted bool
}
```

These are concrete `engine/session` declarations, not a new interface. `InstructionScope.Directory` is a **source-relative** key; `Text` holds retained guidance, not model messages. Examined absence has empty `Text`, `Unavailable=false`, `Omitted=false`; read faults/unavailable scopes are examined and not automatically retried. `Session` owns a private `instructionSnapshot` with `InstructionSnapshot() InstructionSnapshot` and `ReplaceInstructionSnapshot(InstructionSnapshot)` aggregate methods, both deep-copying slices. Mutate at request/dispatch barriers; collect read-parallel results using existing synchronized dispatch bookkeeping. Neither scopes nor automatic bodies enter conversation, events or persisted session snapshots. Revalidate binding/source admission and exclusions before reuse, discarding ineligible entries without read refresh; changed binding/policy must never reuse now-unauthorized text.

### One selected-source composition path

Pass newly discovered targets through the required assembly/manifest path; starting and nested discovery share the implementation. Target-independent contributors remain global. Reuse the existing `MultiAssembler` composition and production source wrappers without bypassing trust admission or whole-source policy. The existing `replaceHarnessInstructions` seam preserves unrelated leaves and their order for value/pointer `MultiAssembler`, `RootAssembler`, and `RulesAssembler` forms. Migrate custom assemblers and callers; do not add a separate hierarchy assembler, independent `Deps` field, or legacy path. Reuse loaded selected-source text while assembling later requests; probe only newly encountered scopes. Keep operator-profile refresh on its existing independent path.

Covered names follow the effective tool registration: a run-overlay `ExtraTools` shadow of `Write` is opaque, while a trusted catalog registration under a covered built-in key commits to its operand semantics. Discover only successfully parsed covered operands; discovery must not weaken the tool's own effective-argument authorization after hooks. No new target-extraction interface or additional `Spec()` calls are required. Copy/Move consider both operands and directory namespace operations use operand-parent scopes, not recursive descendant scanning.

### Scope, selection, and retention

At startup, probe only the selected, admitted instruction-source root-to-starting-folder chain, including ancestors *inside that source*; never scan repository/module/sibling trees. Here, "instruction-source root" means the root of the selected instruction source, not a repository or Git root. The default source selected at the starting subfolder has that subfolder as its root, so it does not read parent or repository-root guidance. For the proposed contract, `cd repo/website && mecatui` starts from `repo/website` and excludes `repo/AGENTS.md`. An explicitly selected admitted broader source rooted at `repo` is the positive control: it may contribute `repo/AGENTS.md` before `repo/website/AGENTS.md`. This is an explicit source-selection choice, never automatic ancestor expansion. When covered structured tools encounter a new directory, probe its lexical ancestor chain only within the same trusted execution-to-source subtree mapping. Traversal never admits a source or loads outside the selected admitted source boundary. No automatic host/Git-root inference. For each directory, prefer nonblank `AGENTS.md`; use `CLAUDE.md` when AGENTS is missing or whitespace-only. A genuine read error is reported, never interpreted as absence. Validate UTF-8 before framing; keep successful text on character boundaries when truncating.

Frame each retained scope with its logical source and execution-relative directory. Ancestors apply root-to-leaf, with the nearest file winning conflicts; nonconflicting ancestor guidance remains. Siblings stay individually scoped, even if their bodies match, and do not govern each other. Identity mapping for a source selected at the execution root and an explicitly admitted broader source with a trusted subtree prefix remain supported; the latter may contribute source ancestors above the execution root. A source selected only at a subfolder cannot look above it. Lexical in-root aliases remain lexical scopes, with existing backend containment for file reads; out-of-root target strings do not license host discovery. No-FS runs retain global/root guidance without inventing file tools.

Retain loaded scopes and their text across provider requests, follow-up user messages, approval continuations, and compaction in the *same live session*, subject to the configured combined cap. In stable first-encounter order, account for starting-chain guidance before new scopes and retain what fits without evicting prior scopes; truncate valid UTF-8 at the boundary or omit subsequent content with explicit model labels and projected warnings. All retained bodies, including scopes suppressed by whole-source `replace`, count against the combined content cap; missing/notice-only sources do not contribute to replacement. A suppressed scope may remain in the snapshot without being emitted by the current whole-source policy; disclose omission. Adding a scope never rereads text already loaded, even if a file changed; explicit Read does not change the automatic snapshot. After restart or owner reattachment, rediscovery may observe current guidance. Source and exclusion admission must be revalidated before reuse.

The live embedded path retains the aggregate with the same `Session`. Server `Service` privately attaches a snapshot keyed by `(SessionID, IncarnationID)`, bounded by existing session capacity: hydrate an authorized store-loaded aggregate under existing run ownership/lease and publish the updated deep copy at settled/parked boundaries **before** releasing ownership. This covers both shared-engine and per-session-factory paths; engine object replacement must not own the snapshot. Evict on actual successful close/delete, service teardown/ownership loss, or incarnation change; process restart/owner reattachment starts empty and can rediscover. Messages, approvals and compaction in-process cannot drop the snapshot. No new port/store. During implementation record owner, lifetime, cleanup and restart state in ADR 0027 Lists 1/2.

A fresh-context subagent begins with an empty snapshot and independently loads *current* guidance from inherited admitted sources. A conversation fork deep-copies the parent's loaded snapshot, then attenuates exclusions/no-FS before use. Each child independently discovers subsequent scopes and accounts for its own content/metadata allowance. Ordinary worktree forks retain the known source mapping through the existing factory, not guessed Git roots or authority from the child checkout.

The existing `combine`/`replace` policy operates on whole selected-source contributions across the retained target set, not independently for each target. For example, if higher-priority source A has only `website/AGENTS.md` and lower-priority source B has root guidance, a retained `[website, services]` view may select A and omit B entirely: services receives no B fallback. A services-only run can select B. The website scope remains website-only; it does not govern services. With `combine`, contributing sources retain their operator-specified order. Diagnostics and the public guide must disclose whole-source omission and sibling limits rather than promise per-target fallback. Adding a target or reaching a bound must never promote an unselected source outside existing operator policy.

### Delivery and resource outcomes

Initial guidance is supplied before the first provider request. Newly encountered guidance enters the *next* provider request's ephemeral project context, not the current model decision or an automatic persisted tool result. Same-batch Read/Edit, first-touch Write/Copy/Move/Remove, and earlier approved operations can execute before that guidance is visible. There is no instruction-specific retry, dispatch pause, rollback, per-effect recheck, or approval fingerprint. Ordinary permission/rewrite checks still decide execution; ordinary explicit Read still returns its content to the conversation. Opaque Shell, MCP/custom tools, and ListDir/Glob/Grep do not activate nested scopes automatically. A model-visible instruction through the real factory must explain this limitation and encourage concrete structured tools when scope guidance matters.

Content and work limits protect instruction loading, not tool execution. Cap the UTF-8 bytes of **all retained automatic guidance text**, including currently suppressed sources, at the configured default 65,536 bytes; not per-file, cumulative read, framing, or backend allocation. Retain earlier scopes first; truncate at a UTF-8 boundary or omit, label partial/omitted for the model, and issue bounded safe advisories. Once content capacity is exhausted, make no new candidate reads; tools continue. No cap hot reload or extra backend preallocation promise.

For each new eligible structured operand, visit only previously unexamined selected-source/lexical-ancestor pairs; shared ancestors are visited once. At most two candidate reads per pair (AGENTS then CLAUDE on missing/blank), checking cancellation before **each** read. Examined absence/error is cached and never automatically retried. Give metadata a separate finite allowance equal in bytes to the configured content cap. Charge UTF-8 bytes of `SourceID + Directory + File` plus one per examined scope, and UTF-8 bytes plus one per retained target directory. Reserve scope metadata **before** probing, including the worst-case selected filename length (`AGENTS.md`/`CLAUDE.md`), then account for the actual selected file; reserve a target directory before retaining it. If metadata is full, set `DiscoveryExhausted` once, emit one safe omission advisory, stop new-scope bookkeeping and probes, keep previously loaded guidance, and continue tools. No per-rejected-path accumulation. Warning/duplicate-notice bookkeeping is bounded by the same metadata allowance: at most one notice per kind/source, or a single global notice if full. This structural contract replaces an unsupported numeric probe ceiling or production latency claim. Existing operand/envelope limits remain; a counted remote-reference fixture with injected latency asserts reads × configured fixture latency, not a measured production p95 or SLO.

Cancellation, genuine read error, containment failure and unavailable native binding/run access produce safe injected diagnostics and `EvHook` advisories projected to the actual client without switching to host storage. Only static reason codes and escaped logical scopes are projected, no raw errors, bodies or host paths. Ordinary request context-window handling applies after bounded guidance; if the complete request still cannot fit, retain existing request failure behavior rather than silently discarding unrelated conversation.

A native execution-backed source is usable only when its existing binding and run-access checks permit source reads. Missing BindingID or reattachment without an active run grant must be reported as unavailable, with best-effort execution continuing; this proposal grants neither access nor an alternate source. Enabling that native source and qualifying its actual factory-to-provider path is a separate issue/dependency before claiming native support. No bounded transport operation or external MicroVM runtime release is a hierarchy completion prerequisite.

## In scope - 5 scenarios, in implementation order

### Scenario 1 - Hierarchy reaches provider context

Extend [existing discovery](../../engine/prompt/builder.go) and the single `RootAssembler` path under [ADR 0376](../adr/0376-agents-instruction-hierarchy.md).

**Acceptance:**
- AC1.1: First request loads only the admitted root-to-starting-folder chain, never recursively scans repo/module/siblings. A covered structured tool working in a new module loads its applicable admitted guidance for the next request, not automatically into persisted results. Explicit Read retains its ordinary result behavior; path mentions alone do not activate scopes.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario1_ProviderHierarchy`
- AC1.2: In each directory, missing or blank AGENTS selects CLAUDE; a genuine AGENTS read error cannot select fallback. Ancestor order, nearest-conflict precedence, nonconflicting ancestors, and project/user-role provenance hold across root and nested discovery.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario1_FallbackCompatibility`
- AC1.3: A later sibling remains alongside earlier loaded scopes across follow-up messages and approval continuations within the live-session cap; siblings do not govern each other. Shared ancestors are deduplicated without deduplicating distinct scopes or global contributions. The website/services example proves whole-source replacement suppresses lower-priority B in a mixed view, while services-only selects B; suppressed retained bodies still count toward the cap, notice-only/missing sources cannot win replace, and actual client advisories identify omission.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario1_SiblingsAndSourcePrecedence`

### Scenario 2 - Discovery does not gate file effects

Preserve [dispatch and permission invariants](../../AGENTS.md#implementation-boundaries).

**Acceptance:**
- AC2.1: Same-batch Read/Edit and first-touch Write/Copy/Move/Remove execute under ordinary permissions even when new guidance has not reached the model; the next request includes discovered scopes. No instruction-specific deferral, retry result, fingerprint, or rollback is created. Every executed call keeps ordinary paired results.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario2_BestEffortEffects`
- AC2.2: The corrected `LocalFileOperands` supplies both Copy/Move operands; incomplete/malformed operand sets do not count as scope discovery or bypass normal tool validation. Effective-argument rewrites still receive normal authorization, and read-before-edit, CAS, create-only, and Deny/Ask checks hold without an instruction-specific effective-call gate.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario2_OperandsAndAuthorization`
- AC2.3: Shell, custom/MCP, ListDir/Glob/Grep, and an opaque run-overlay shadow do not trigger structured-scope discovery. A real factory delivers a model-visible limitation, while ordinary permissions for those tools remain unchanged.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario2_OpaqueCoverage`

### Scenario 3 - Authority remains with the admitted source

Retain [ADR 0359](../adr/0359-harness-context-source-authority.md)'s admission and placement separation.

**Acceptance:**
- AC3.1: Distinct source/execution roots, trusted subtree mapping, and virtual sources use only selected admitted logical paths. A default source selected at a starting subfolder does not read poisoned parent or repository-root guidance, even when that ancestor is trusted; an explicitly selected admitted broader source is the positive control and may contribute its own ancestors. Traversal never admits a source or loads outside its admitted boundary; unselected host/execution files or out-of-root operands cannot trigger an ancestor walk or new source selection.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario3_SourceMapping`
- AC3.2: Backend symlink containment, cancellation, and source authorization remain intact; discovery creates no execution ReadLedger evidence. Fresh-context subagents start empty and load current guidance from inherited admitted sources; conversation forks deep-copy the parent's snapshot and attenuate exclusions/no-FS before use. Both independently discover later scopes within their own caps. Known worktree mapping is inherited via the existing factory, never by trusting poisoned child checkout guidance or guessing a Git root. Changed source identity/policy discards ineligible cached scopes without rereading them.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario3_ContainmentAndChildren`
- AC3.3: Native execution-backed selection without a binding/run grant reports the source unavailable and continues authorized tools. A usable source requires an independently qualified native enablement path; an unavailable native source alone does not block hierarchy completion.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario3_NativeUnavailable`

### Scenario 4 - Bounded instruction loading stays visible

Use [injected diagnostics](../architecture/observability.md), distinct from durable events.

**Acceptance:**
- AC4.1: Omitted config and Go zero default to 65,536 bytes; explicit YAML zero, negative, fractional, noninteger and overflow fail, negative Go fails; positive raised caps propagate through the resolver and shared/per-session/child factories. Count all retained automatic guidance including suppressed sources, not a per-file, cumulative-read, or backend-allocation limit. Below, at and above cap, including multibyte UTF-8 boundaries, truncation/omission has exact model labels and safe advisories in the actual receiving client; authorized mutations continue.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario4_ConfiguredCombinedCap`
- AC4.2: Counted remote-reference discovery with injected latency covers missing/blank fallback, multiple sources, shared ancestors and disjoint deep targets. For N newly admitted source/directory pairs, reads <= 2N; no read after cancellation or content/metadata exhaustion, and metadata accounting (including target directories, examined scopes and notice bookkeeping) never exceeds the configured separate allowance. Cached absence/errors are not reread on subsequent requests; cost assertion is calls × fixture latency, not production latency/p95. No traversal outside admitted sources or claim that whole-file backend reads preallocate to content cap; existing envelopes hold.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario4_DiscoveryCosts`
- AC4.3: Genuine errors, containment failures, cancellation, metadata/content omission and unavailable native access emit safe bounded `ProjectInstructions`/`HookAdvisory` notices through actual wire/client projection, including selectively forwarded child notices without forwarding unrelated hooks; no raw errors/bodies/host paths and no wrong-namespace fallback. Existing request-token accounting reflects framed text. Aggregate deep-copy getters/setters and the server `(SessionID, IncarnationID)` attachment cover both shared-engine and per-session factories, store-loaded runs, settled/parked publication before ownership release, capacity, successful close/delete, ownership loss, teardown and incarnation isolation. Guidance survives messages, approvals and compaction in the same live session; restart/owner reattachment rediscovers, while external edits to already examined scopes do not refresh them (ordinary explicit Read remains current). No persistence in conversation/events/stored snapshots.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario4_SnapshotContinuationsAndRestart`

### Scenario 5 - Composition and documentation match the shipped scope

Follow [ADR 0376](../adr/0376-agents-instruction-hierarchy.md), [engine compatibility](../../engine/COMPATIBILITY.md) and the [documentation ownership contract](../development-process.md#documentation-change-review).

**Acceptance:**
- AC5.1: Factory-to-provider offline proof uses the one required four-argument `Deps.Instructions` assembly path, nil-safe `AssembleWithManifest` delegation, migrated custom callers without deprecated/optional contracts, source admission/mapping from real binding, one logical manifest row per message, and value/pointer MultiAssembler/RootAssembler/RulesAssembler composition preserving unrelated leaves. Target-independent contributors ignore targets/state and retain their lifetime. Existing source/execution and MicroVM offline client tests stay green; a fixture does not prove native enablement complete.
  - verify: `TestADR_0376_AgentsHierarchy_Scenario5_FactoryConformance`
- AC5.2: During implementation, the owning public guide and context architecture describe starting-folder chain, lazy next-request discovery, live-session snapshots and restart rediscovery, operator-only configurable combined cap and structural work bound, per-directory fallback, whole-source replacement, trust prerequisites, and opaque-tool/first-touch limitations. ADR 0027 Lists 1/2 record owner, lifetime, cleanup, and restart state. Run config reference generation and `task api:update`; API snapshots and classified `engine/CHANGELOG.md` match the approved signature. Structural tests claim delivery, not live-model obedience.
  - verify: inspection — review owning pages, ADR 0027 inventory, config reference and API/changelog diff; run `task generate`, `task api:check`, `task docs`, and `task site:build`.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| On-demand path-scoped rule files | [#1974](https://github.com/stacklok/mecatl/issues/1974) | Retain current rules behavior. |
| Real Redis instruction/command qualification | [#1811](https://github.com/stacklok/mecatl/issues/1811) | Shared-source conformance does not claim Redis integration. |
| Native source enablement without existing binding/run access | Separate issue/dependency | Report unavailable; do not claim that source works until qualified. |
| AGENTS.override.md, imports, new home/global conventions | Separate proposal | Not established here. |
| Shell affected-path inference, persisted scope snapshots, watchers, public inspection API | Separate proposal | Ordinary authority and existing diagnostics suffice for this plan. No automatic live refresh or stale-cache machinery. |

## Definition of done

1. Obtain human approval of this proposed amendment's exact technical contracts before #2110 implementation uses it as baseline; selection in this document is not approval or proof of shipment.
2. Focused scenario/integration tests pass offline with isolated stores; `task test`, `task lint`, and `task test:race` pass on the implementation candidate.
3. Include generated config reference and `task api:update` output and classified changelog entries; pass `task api:check`, `task docs`, `task site:build`, and `task ac-trace-strict` when marked landed.
4. `go run ./cmd/mecademo` shows tool call, permission ask/approval, and result.
5. `/panel-review` has no ship blockers. The implementation PR identifies this approved amendment baseline. Humans merge PRs.

## Known risks

Whole-source replacement can suppress fallback for another target; the combined cap can omit newly encountered text. A first-touch mutation may complete before its scope reaches a request. Guidance edited externally during a live session stays snapshot-stable until restart rediscovery (ordinary Read is unaffected). Existing whole-file read paths can allocate before truncation, and fixture-based read costs do not establish production latency or native source enablement.
