# Harness context source authority - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — separates harness source authority from execution across composition, trust, and public engine interfaces.
**Decision record:** [ADR 0354](../adr/0354-harness-context-source-authority.md)
**Phase:** harness context model and shared source binding
**Status:** draft, 2026-09-23. Model proposal and interface review only; runtime behavior is not implemented.
**Delivery:** Split. Resolve the human decisions and merge the Plan / Interface contract before implementation.
**Expected tasks:** deferred to orchestration
**Issue:** Relates to [#1811](https://github.com/stacklok/mecatl/issues/1811) and [PR #580](https://github.com/stacklok/mecatl/pull/580). Neither is closed here.
**Plan PR:** [#1814](https://github.com/stacklok/mecatl/pull/1814) (draft)
**Approved baseline:** absent until contract approval by merge

`HarnessContext` names the deployment-configured composition of admitted instruction and
customization sources supplied to a session. Sources may use APIs, host files, databases, or
explicitly selected execution files. Separate responsibilities do not require separate storage.
Composition defines enabled sources, ordering, and per-kind combination, collision, override,
and exclusion behavior. Transport and storage location do not select precedence or trust.

This PR changes the [domain model](../architecture/mecatl.modelith.md) and proposes the shared
interface correction. The next PR implements that contract. MicroVM and Redis integrations then
consume it in sibling changes, rather than introducing backend-specific source rules.

## Human decisions

- [x] Independent, flexible sources — Decision: deployment composition selects harness sources independently from execution. APIs, host files, and explicitly selected execution files are valid source implementations; execution placement never implicitly chooses them.
- [x] Layered composition and overrides — Decision: the operator can combine deployment, API/service, and repository contributions, including multiple sources of the same content kind. Resolution is per-kind, deterministic, and provenance-preserving. Repository context can be disabled without disabling execution; content overrides cannot change authorization.
- [x] Integration stack — Decision: model/contract first, shared implementation second, then PR #580 and a separate Redis #1811 integration.
- [ ] Composition configuration and resolution schema: review the exact programmatic/declarative representation of source ordering, entry identities, permitted overrides, exclusion, and per-kind combination policies. Preserve existing behavior as the compatibility default; do not invent a universal deep-merge algorithm or infer priority from file versus gRPC transport.
- [ ] Public API transition: approve the proposed workspace-free prompt interfaces below, or require a compatibility-adapter window. No breaking release or compatibility window has been selected.
- [ ] Restart and deployment reconfiguration: choose exact durable session source binding, or explicit deployment-controlled rebinding. If exact binding is selected, review its identity, owner scope, current authorization/revocation, storage interfaces, and legacy migration before implementation. Neither automatic adoption nor rejection of existing sessions or schedules is authorized here.

## Interface contract

This is a draft proposal, not permission to implement unresolved choices. The unchecked
items above block implementation dispatch; the contract must be completed before approval.

- **gRPC / protobuf:** No public API field, source selector, backend kind, or path is proposed.
  Command listing keeps its existing response shape. No new source RPC or storage field is
  proposed in this draft. If durable context identity is selected, exact trusted storage changes
  must be added here before approval; public clients must not gain source-selection authority.
- **Exported Go APIs / interfaces:** Proposed minimal interface correction in `engine/prompt`:
  `InstructionAssembler.Assemble(context.Context) ([]session.Message, error)`,
  `CommandExpander.Expand(context.Context, string) (string, bool, error)`, and
  `CommandLister.List(context.Context) ([]Command, error)`. The manifest helper becomes
  `AssembleWithManifest(context.Context, InstructionAssembler) ([]session.Message, []InstructionManifest, error)`.
  The existing `InstructionAssembler` already permits API-backed implementations; no additional
  project-instruction port or public five-source container is required merely for independence.
  The default `RootAssembler` gains `Source tool.Workspace`, an explicitly bound source reader;
  a nil Source means no configured project-instruction source and produces no fragments, never a
  fallback. `NewDirCommandExpander` becomes
  `NewDirCommandExpander(source tool.Workspace, dirs ...string) *DirCommandExpander`;
  a nil source yields no file commands. These source workspaces are supplied by composition,
  never selected by the engine from the current tool environment. The lower-level
  `DiscoverInstructions(context.Context, tool.Workspace)` remains an explicit discovery helper.
  Existing `CommandSource`, `SourceExpander`, `RulesSource`, `SkillSource`, and `AgentDefSource`
  retain their source-specific contracts. A required but unavailable configured source must fail
  resolution rather than be represented as nil optional absence. Server-internal command listing
  becomes source-bound as well; it does not accept an execution workspace per call. No new
  `engine/port` provider, `session.HarnessContextRef`, or generic registry is prescribed.
  These signature changes remove execution coupling; they alone do not define configurable
  layering. The exact per-kind composition API remains the open schema decision above. Reuse
  existing consumer-specific combinators where they satisfy that contract; do not require all
  source kinds to implement one merge interface or share one lifecycle.
- **Tool schemas:** None — model-facing tool arguments and results remain unchanged. `Skill`
  retains logical bundles and named assets. Execution Read/Write/Shell use their existing
  `Environment` capabilities; harness reads do not create read-ledger evidence.
- **CLI / config:** The exact source-layer and override configuration syntax is an open decision,
  not an implementation detail. It must express enabled sources and per-kind resolution, including
  deployment-file plus gRPC sources for helpdesk deployments and deployment/service/repository
  layers for coding deployments. Multiple sources of the same kind must be expressible. Source
  identity, entry collision keys, ordering, allowed replacements, and exclusions need exact
  definitions before approval. Existing source settings remain the compatibility default, resolved
  into source-bound adapters by trusted composition. The local deployment can configure its
  startup project root independently from execution, and programmatic integrations can bind other
  implementations. No `microvm`/`redis` switch or transport kind chooses priority or authority.
  Repository context must be independently disableable while repository execution remains usable.
  No-FS attenuates execution access without filtering logical sources by their storage backend.
- **Events / persistence:** No event or snapshot-field change is prescribed yet. Commands remain
  live per List/Expand; root project instructions refresh once per run; rules, skills, and agent
  definitions preserve their existing source-lifetime snapshots. The restart/reconfiguration
  policy is explicitly unresolved above. No implementation may infer source authority from a
  restored execution root or silently substitute an unrelated default on source failure.
- **Security / authority:** Source selection and admission belong to trusted deployment composition.
  Source bodies do not grant themselves a trust tier. Root project instructions retain project
  provenance and their existing user-role framing; source reads do not bypass project admission.
  Principal/tenant scope and current authorization must remain effective, including after restart;
  durable identity, if selected, must not freeze a revoked grant. Operator policy, hooks,
  credentials, and execution authority are not merged into customization content. A child inherits
  admitted source authority only subject to existing specialist, profile, and tool attenuation.
  Source bodies cannot select their own layer or precedence. Combining or replacing context
  preserves each contribution's provenance and admission constraints. Context instruction rules
  are distinct from governance PermissionRules: overriding content cannot weaken a deny, grant a
  tool, bypass source admission, or widen a child. Source-only command listing owner-authorizes
  the session and resolves its context without
  reattaching unrelated execution capabilities. If the explicitly chosen source itself uses
  execution files, its own backend availability remains a real dependency.
- **Compatibility / migration:** Removing workspace parameters and changing directory-expander
  construction would break exported Go APIs and require `task api:update` plus an engine changelog
  entry. A compatibility window remains a human decision. No legacy session/schedule rejection,
  migration, or adoption is authorized. Root discovery stays root-only: nonempty AGENTS.md wins;
  absent or whitespace-only AGENTS.md falls through to CLAUDE.md; both absent/empty means no
  project fragment; other read errors follow the existing assembly policy. Preserve trimming,
  framing, provenance manifests, and existing input limits; do not introduce ancestor traversal,
  new size caps, or trusted content roles incidentally.

## In scope - 5 scenarios, in implementation order

### Scenario 1 - Source selection and execution vary independently

The model's [Environment binding](../architecture/mecatl.modelith.md#environment) remains
Workspace, session ReadLedger, and optional coherent CommandRunner. Shared implementation proofs
use offline reference source/execution adapters, not a claim that later VM or Redis integration
has already passed.

**Acceptance:**
- AC1.1: The same configured source selection provides the same admitted context with two different execution namespaces, without deriving sources from either execution root.
  - verify: `TestADR_0354_HarnessContext_Scenario1_SourceIndependentOfExecution`
- AC1.2: Conflicting instructions and customizations planted only in an unselected execution namespace do not enter automatic context assembly, the command palette, or customization catalogs.
  - verify: `TestADR_0354_HarnessContext_Scenario1_UnselectedExecutionContentIgnored`
- AC1.3: An explicitly configured source can read execution files through their actual backend, and admitted contents reach the harness without reopening a virtual root as a host path.
  - verify: `TestADR_0354_HarnessContext_Scenario1_ExplicitExecutionFileSource`
- AC1.4: No-FS execution retains admitted logical sources, including file-backed sources inaccessible to agent file tools; file tools and Shell remain unavailable.
  - verify: `TestADR_0354_HarnessContext_Scenario1_NoFSKeepsConfiguredSources`

### Scenario 2 - Discovery and invocation share source authority

Use the existing logical-source contracts described in the [architecture guide](../architecture.md).
Without explicit layering, preserve the compatibility chain: directory commands, skills, driver
commands, then MCP prompts. That existing cross-kind chain is not the full per-kind layering and
override contract. Explicit compositions follow the approved resolution policy from Scenario 5;
ordinary misses and established fail-soft behavior remain distinct from authority fallback.

**Acceptance:**
- AC2.1: Palette listing and prompt expansion resolve through the same configured source chain and precedence, including a positive control with conflicting same-name execution content.
  - verify: `TestADR_0354_HarnessContext_Scenario2_PaletteAndExpansionAgree`
- AC2.2: A selected command source edit appears on the next List/Expand. This holds for API-backed fixtures and for explicitly selected execution-file sources; merely changing an unselected execution file has no effect.
  - verify: `TestADR_0354_HarnessContext_Scenario2_SelectedCommandsRemainLive`
- AC2.3: With an independent healthy source and unavailable execution backend, owner-authorized command listing succeeds without calling execution reattachment. An explicitly execution-file-backed source still reports its own backend failure according to its source contract.
  - verify: `TestADR_0354_HarnessContext_Scenario2_SourceOnlyDiscovery`

### Scenario 3 - Project instructions and customizations preserve their contracts

The source boundaries build on [ADR 0081](../adr/0081-rules-source-port.md),
[ADR 0108](../adr/0108-on-demand-logical-skill-assets.md), and
[ADR 0013](../adr/0013-agent-definitions.md). Storage sharing does not raise trust or merge read evidence.

**Acceptance:**
- AC3.1: Root instructions are read once per run from the configured source, refresh on a subsequent run, and remain ephemeral. Both manifest-enabled and ordinary assembly consume the same source with unchanged project provenance.
  - verify: `TestADR_0354_HarnessContext_Scenario3_ProjectInstructionsPerRun`
- AC3.2: The file-backed source preserves root-only AGENTS.md/CLAUDE.md precedence, whitespace-only fallback, absence, genuine read errors, and existing framing without reading unrelated execution files.
  - verify: `TestADR_0354_HarnessContext_Scenario3_RootDiscoveryCompatibility`
- AC3.3: Rules, skills, and agent definitions preserve their current lifetimes, admission, caps, and source-specific error behavior. Host, API, and execution-file storage do not confer a trust tier.
  - verify: `TestADR_0354_HarnessContext_Scenario3_ProvenanceAndSourceContracts`
- AC3.4: Source reads never update the execution ReadLedger, including when both capabilities reference the same files. Sharing a context source does not share session read evidence.
  - verify: `TestADR_0354_HarnessContext_Scenario3_SourceReadsDoNotAuthorizeEdits`
- AC3.5: Inherited sources do not widen a child's existing specialist catalog, profile, trust admission, or delegation capabilities; an isolated execution fork alone does not retarget its context sources.
  - verify: `TestADR_0354_HarnessContext_Scenario3_ChildAttenuationPreserved`

### Scenario 4 - Source failure does not change authority

The [proposed source-authority ADR](../adr/0354-harness-context-source-authority.md) separates
configuration failure from optional absence. Restart storage and migration proofs must be added
when the open human decision is resolved; this draft is not ready for implementation dispatch.

**Acceptance:**
- AC4.1: Optional absence and ordinary configured-chain lookup retain established behavior. Failure resolving a required source is reported rather than replaced by execution files, process cwd, or an unrelated default.
  - verify: `TestADR_0354_HarnessContext_Scenario4_NoUnconfiguredAuthorityFallback`
- AC4.2: Both shared-file and independent-source paths preserve source-specific error semantics; a configured fail-soft chain can still consult its next admitted source without inventing a new source.
  - verify: `TestADR_0354_HarnessContext_Scenario4_ConfiguredChainFailureSemantics`

### Scenario 5 - Hybrid context composition and controlled overrides

The [domain model](../architecture/mecatl.modelith.md#harnesscontext) defines context composition
independently from execution. The shared PR proves these scenarios with offline source-service
and filesystem reference adapters; actual mecak8s deployment and backend integration proofs remain
owned by their integration changes. Per-kind configuration details must be completed under Human
decisions before implementation, without weakening the agreed behaviors below.

**Acceptance:**
- AC5.1: A helpdesk composition combines deployment-file instructions with configured service-provided instructions, rules, and skills without a repository or command runner. Equivalent admitted source data served through a file or service adapter has the same precedence and trust.
  - verify: `TestADR_0354_HarnessContext_Scenario5_HelpdeskDeploymentAndServiceSources`
- AC5.2: A coding composition combines deployment instructions, organization service skills, and mounted-repository instructions, rules, and skills while file tools and Shell still use the coherent repository execution binding.
  - verify: `TestADR_0354_HarnessContext_Scenario5_HybridCodingContext`
- AC5.3: With planted same-name conflicts, the configured policy permits a selected repository skill to replace an organization default and a deployment command to replace a repository command. Listing and invocation/body retrieval choose the same definitions; source transport or discovery timing does not select a winner.
  - verify: `TestADR_0354_HarnessContext_Scenario5_PerKindOverrideResolution`
- AC5.4: Instruction and rule contributions combine, replace, or are excluded according to their approved per-kind policy, with retained provenance. They are not processed by an implicit recursive merge of arbitrary source objects. Disabling repository context leaves deployment/service context and repository execution available.
  - verify: `TestADR_0354_HarnessContext_Scenario5_CombineExcludeAndDisableRepositoryContext`
- AC5.5: A lower-admission contribution claiming deployment origin or requesting changed override policy cannot promote itself, bypass project admission, weaken a permission deny, or expand a child's tool catalog.
  - verify: `TestADR_0354_HarnessContext_Scenario5_ContextOverridesCannotGrantAuthority`

## Dependency stack

1. **This PR:** proposed model, ADR, and draft interface/acceptance contract. Resolve Human decisions before approval.
2. **Shared implementation PR:** source-bound interfaces, per-kind composition/resolution, and local composition, reference-adapter proofs, API compatibility treatment, and the approved restart behavior.
3. **PR #580:** consume the shared contract for MicroVM. Qualify configured host/API/execution-file sources independently from guest tools; preserve actual VM lifecycle and isolation proofs.
4. **Issue #1811 sibling PR:** consume the same contract with Redis execution. Retain exact Redis file access and shell-less semantics; prove independent and explicitly Redis-file-backed context selection.

The sibling integrations must not independently invent shared interfaces. Their concrete backend
proofs supplement the shared reference proofs rather than being prerequisites for PR 2.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Runtime implementation in this PR | Shared implementation PR | Model and contract only. |
| Concrete MicroVM integration and live journey | PR #580 | Dependent on the shared contract. |
| Concrete Redis correction | Issue #1811 | Separate sibling implementation. |
| Generic registry or one new transport per source noun | Future demonstrated need | Existing ports and APIs already support independent sources. |
| Ancestor-directory traversal within a file source or changed caps | Separate decision | Preserve root-only discovery within each file source; composing multiple configured sources is in scope. |
| Automatic deletion, rejection, or adoption of legacy state | Human decisions | No treatment has been authorized. |

## Definition of done

Before this draft can become proposed, resolve all open Human decisions and complete any resulting
interface, persistence, migration, and acceptance clauses. Run the acceptance-plan checker and
its fixtures, `task docs:model`, and `task docs`; render model Markdown from YAML, never by hand.
Record environment or baseline failures honestly rather than claiming these gates passed.

The later implementation requires `task test`, `task lint`, `task test:race`, `task api:check`,
`task docs`, applicable strict AC tracing, the offline demo, and independent panel review.
A merged Plan / Interface contract is the implementation baseline. This draft authorizes no code
change, migration, or claimed release behavior.
