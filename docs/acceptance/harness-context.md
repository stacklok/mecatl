# Harness context source authority - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — separates harness source authority from execution across composition, trust, and public engine interfaces.
**Decision record:** [ADR 0354](../adr/0354-harness-context-source-authority.md)
**Phase:** harness context model and shared source binding
**Status:** proposed, 2026-09-23. Model and exact interface contract ready for human Plan / Interface review; runtime behavior is not implemented.
**Delivery:** Split docs → shared implementation → PR #580 / Redis siblings. The operator explicitly authorizes the shared implementation to proceed as a draft stacked PR from the exact proposed-docs commit before this plan merges. This narrow exception does not mark the plan approved, authorize any merge, relax contract-drift stops, or remove either human merge gate.
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
- [x] Composition configuration and resolution schema — Decision: trusted composition registers source IDs, and operator-only configuration names enabled IDs plus a highest-precedence-first source order for each content kind. Instructions combine in that order or select the first nonempty source in `replace` mode. Commands, rules, skills, and agent definitions resolve exact-name collisions by order, with exact operator-declared exclusions and named lower-source overrides. Resolution performs no recursive merge and assigns no transport-derived precedence.
- [x] Public API transition — Decision: make the workspace-free `engine/prompt` API break directly, with source-bound filesystem adapters. The implementation PR updates API snapshots and `engine/CHANGELOG.md`; it does not retain workspace-taking compatibility overloads or adapters.
- [x] Restart and deployment reconfiguration — Decision: on harness restart, existing sessions and schedules rebind to the current deployment composition under current authorization. Configuration changes may change resumed context. Do not add a durable `HarnessContextRef`, reject legacy state, or infer a binding from stored execution placement.

## Interface contract

The contract below records operator-selected direction for human review. It is not merged approval.
The operator's narrow delivery exception permits a draft stacked shared-implementation PR to start
from this exact proposed-docs commit before plan merge; that draft must stop on contract drift and
cannot merge before the Plan / Interface contract. Human merge gates remain for both PRs, and the
stack remains docs → shared implementation → PR #580 / Redis sibling integrations.

- **gRPC / protobuf:** No public API field, source selector, backend kind, path, or durable context
  reference is added. Command listing keeps its existing response shape. Public clients cannot select
  source IDs or composition policy.
- **Exported Go APIs / interfaces:** Make these clean breaks in `engine/prompt`:
  `InstructionAssembler.Assemble(context.Context) ([]session.Message, error)`,
  `CommandExpander.Expand(context.Context, string) (string, bool, error)`, and
  `CommandLister.List(context.Context) ([]Command, error)`. Change the manifest helper to
  `AssembleWithManifest(context.Context, InstructionAssembler) ([]session.Message, []InstructionManifest, error)`.
  `RootAssembler` becomes `RootAssembler{Source tool.Workspace}` and
  `NewDirCommandExpander(source tool.Workspace, dirs ...string) *DirCommandExpander`. Those adapters
  bind a source workspace at construction and never receive the execution workspace at call time. A
  nil source means that optional adapter contributes nothing; failure to construct or authorize a
  required configured source is an error and cannot be laundered into nil. Keep
  `DiscoverInstructions(context.Context, tool.Workspace)` as the explicit lower-level helper. Keep
  `CommandSource`, `SourceExpander`, `RulesSource`, `SkillSource`, and `AgentDefSource` unchanged.
  Server-internal command listing becomes source-bound too. Do not add an `engine/port` provider,
  `session.HarnessContextRef`, exported five-source container, or generic lifecycle interface.
  The reference composition shape is internal and kind-specific:

  ```go
  type HarnessSourceID string

  type HarnessSourceScope struct {
      Principal *session.Principal
      Profile   string
  }

  type HarnessSourceScopeKind uint8

  const (
      HarnessSourceScopeProcess HarnessSourceScopeKind = iota
      HarnessSourceScopePrincipal
  )

  type HarnessProvenancePolicy struct {
      Fixed           string
      PreserveAllowed []string
  }

  type HarnessSourceRegistration[T any] struct {
      ID         HarnessSourceID
      Scope      HarnessSourceScopeKind
      Provenance HarnessProvenancePolicy
      Bind       func(context.Context, HarnessSourceScope) (T, func() error, error)
  }

  type CommandSourceBinding interface {
      prompt.CommandExpander
      prompt.CommandLister
  }
  ```

  `internal/app.Config` exposes exactly five programmatic registration fields so every existing
  `cmd/` composition root can populate them before calling `app.Build`:

  ```go
  HarnessInstructionSources []HarnessSourceRegistration[prompt.InstructionAssembler]
  HarnessCommandSources     []HarnessSourceRegistration[server.CommandSourceBinding]
  HarnessRulesSources       []HarnessSourceRegistration[prompt.RulesSource]
  HarnessSkillSources       []HarnessSourceRegistration[tool.SkillSource]
  HarnessAgentDefSources    []HarnessSourceRegistration[tool.AgentDefSource]
  ```

  `server.CommandSourceBinding` is the same two-method interface shown above, owned in the consumer
  package to avoid an `app` dependency. These five fields are separate maps after validation; the
  generic descriptor only removes registration boilerplate. It is not an engine port, a public
  engine bundle, or a requirement that one backend implement all five kinds. A command binding
  supplies List and Expand from one resolved object.

  Each registration declares process or principal scope. Process scope is valid only for an adapter
  explicitly documented as caller-neutral and concurrency-safe. Principal-scoped `Bind` receives the
  authoritative stored session owner and `Session.Profile`; its result is never cached or reused
  across principals. A nil cleanup function is valid for a source with nothing to close; non-nil
  cleanup must be idempotent.

  `HarnessProvenancePolicy` has exactly one mode. `Fixed` stamps one validated existing tier on every
  contribution. `PreserveAllowed` is available only to trusted compatibility adapters whose one
  registration legitimately contains mixed existing tiers, such as local user/project/explicit
  contributions; every adapter-produced entry tier must belong to that nonempty validated set.
  Setting both modes, neither mode, duplicate/unknown tiers, or using preserve mode for an untrusted
  remote payload fails startup. Remote driver registrations use a fixed `driver` tier and cannot
  promote an origin-like payload field. Root project instructions remain fixed `project`; instruction
  manifests otherwise retain each existing assembler kind and admission tier. Project ingestion is
  decided before registration binding and resolution, so exclusions and overrides can select only
  already-admitted contributions. No selected body supplies its own provenance policy.
- **Tool schemas:** None — model-facing tool arguments and results remain unchanged. `Skill` retains
  logical bundles and named assets. Execution Read/Write/Shell use their existing `Environment`
  capabilities; harness reads do not create read-ledger evidence.
- **CLI / config:** Add this strict operator-tier-only schema to user-global `settings.yaml` and
  explicitly supplied operator settings files:

  ```yaml
  harness_context:
    enabled_sources: [deployment, organization, repository]
    kinds:
      instructions:
        sources: [deployment, organization, repository]
        mode: combine
        exclude:
          - source: repository
      commands:
        sources: [repository, deployment]
        mode: combine
        exclude:
          - source: repository
            name: local-only
        overrides:
          - name: review
            winner: deployment
            replaces: [repository]
      rules:
        sources: [deployment, organization, repository]
        mode: combine
      skills:
        sources: [organization, repository]
        mode: combine
        overrides:
          - name: build
            winner: repository
            replaces: [organization]
      agent_defs:
        sources: [deployment, repository]
        mode: combine
  ```

  Trusted deployment composition registers each `HarnessSourceID` before policy validation. IDs match
  `[a-z0-9][a-z0-9._-]*`; registration is unique per ID and content kind. One ID can register
  several existing kind-specific interfaces, but it does not imply shared storage or one provider
  object. Existing settings register reserved compatibility IDs: `local` for source-bound local
  instruction/customization adapters, `skills` for the command view of the resolved skill source,
  `driver` for configured content-source driver clients, and `mcp` for MCP prompts. An integration
  can register deployment-specific IDs such as `organization` or `repository` through the same
  composition maps before `app.Build` resolves policy; an ID has no effect until operator policy
  enables and orders it.

  `enabled_sources` is a unique allowlist. An explicit `harness_context` block must contain all five
  kind mappings and each mapping must state `mode`; an empty `sources` list explicitly disables that
  kind. Every enabled ID must appear in at least one kind. Every `kinds.<kind>.sources` entry must be
  enabled and registered for that kind. Exclusion and override source IDs must occur in that kind's
  `sources` list. Unknown, duplicate, disabled, unused, or unsupported references fail startup.
  `sources` is highest precedence first. The closed kinds are `instructions`, `commands`, `rules`,
  `skills`, and `agent_defs`; the closed modes are `combine` and `replace`. Unknown fields and values
  fail strict operator parsing. A project-tier `harness_context` block is ignored in full with a
  value-free warning, even for a trusted project. Source bodies and project files cannot register IDs
  or alter policy.

  In `combine` mode, instruction messages concatenate by source order. Instructions have no entry
  name: their exclusions name only `source`, and `overrides` is invalid. In `replace` mode, the first
  post-exclusion source that returns a nonempty instruction contribution supplies the whole kind.
  For commands, rules, skills, and agent definitions, the collision key is the existing validated
  logical `Name`, compared exactly and case-sensitively. `combine` forms a union; the first candidate
  in source order wins each collision. For a named kind, `exclude` requires both `source` and `name`
  and removes exactly that candidate. A named override changes only its named collision. When its
  winner is present, first remove the present candidates whose sources occur in `replaces`, then
  choose the first candidate in configured source order from the winner plus every present
  non-replaced candidate. Thus an earlier non-replaced source still blocks the winner: for sources
  `[A, B, C]`, winner `C`, and `replaces: [A]`, `B` wins when present and `C` wins when `B` is absent.
  When the winner is absent, use normal ordered resolution over all post-exclusion candidates.
  Duplicate overrides, duplicate entries in `replaces`, an empty `replaces`, a winner also listed in
  `replaces`, unknown sources, instruction names, and overrides under `replace` fail startup. An
  override is also invalid when the winner already precedes every replaced source, or when an exact
  exclusion removes its winner or every replaced candidate: these structurally no-op declarations
  must be deleted rather than accepted ambiguously. `replace` for a named kind takes the complete
  post-exclusion entry set from the first nonempty source and consults no lower source. Resolvers do
  not merge command bodies, rule fields, skill bundles/assets, or agent definitions.

  A registered source returning no entries, or a named lookup returning its established not-found
  result, is a miss. For one stable source observation, List and named body retrieval apply the same
  visible-name set and winner algorithm; a source that lists a name but cannot expand/retrieve it is
  rejected by conformance, as is a named expansion that was absent from that stable listing. Live
  sources may change between separate List and Expand calls, so a later update may legitimately
  produce a different observation; this contract adds no cross-call transaction or frozen metadata
  snapshot. A backend error remains an error or fail-soft result according to that existing source
  contract; policy resolution never converts failure into a miss or adds an unconfigured fallback.
  Existing per-entry source caps and validators run before resolution, and existing consumer aggregate
  limits run afterward. Composition adds no body-size or count bypass.

  With no `harness_context` block, composition synthesizes the current behavior as explicit bindings:
  local root instructions and directory commands bind the configured startup project source;
  existing rules, skills, and agent-definition resolution retains its present admission and ordering;
  commands retain directory, skill, driver, then MCP precedence. The startup project source may use
  the same files as execution, but composition constructs it independently rather than deriving it
  from a session `Environment`. Existing source flags and URLs continue to register those compatibility
  bindings. An explicit policy can instead layer operator-registered deployment, service, and
  repository IDs. No `microvm` or `redis` switch, backend type, or discovery time chooses priority.
- **Events / persistence:** Add no event or snapshot field. Commands remain live per List/Expand;
  instructions observe their configured sources once per run; rules, skills, and agent definitions
  preserve their snapshot semantics. Policy and process-scoped snapshot sources resolve once in
  `app.Build`. A principal-scoped snapshot source resolves once when that session context binding is
  created in the current Build and remains stable for that binding; it is not rebuilt per command,
  turn, or tool call. A principal-scoped live command source remains live only within its bound
  principal.

  Source-only command discovery replaces the workspace-taking server lister with this consumer-local
  seam:

  ```go
  type CommandSourceBinding interface {
      prompt.CommandExpander
      prompt.CommandLister
  }

  type CommandSourceResolver interface {
      Borrow(context.Context, session.SessionID, *session.Principal, string) (CommandSourceBinding, func(), error)
      Retire(session.SessionID)
  }
  ```

  `ListCommandsForSession` first loads and owner-authorizes the session, then calls `Borrow` with the
  session's authoritative stored `ID`, cloned `Owner`, and `Profile`; it never trusts request identity
  as binding metadata and does not reattach execution merely to list independent sources. Existing
  ownerless local/system-principal behavior remains valid. The Build-owned resolver serializes first
  creation per session ID, verifies that every reuse has the same principal/profile, snapshots once,
  and retries a later call after failed creation rather than caching a poisoned entry. The returned
  release function borrows one in-flight reference. `Retire` is idempotent, prevents new borrows, and
  delays cleanup until active calls release; actual session teardown paths invoke it, and `Built.Close`
  retires and closes all remaining bindings. Agent command expansion borrows the same binding and
  policy. A source explicitly backed by execution files may itself authorize and bind that backend;
  independent discovery does not require execution reattachment.

  Process-scoped resources close from `Built.Close`; session/principal-scoped resources close when
  their binding retires and again safely at Build shutdown. The implementation adds this owned cache,
  and every other connection/cache/binding that outlives one call, to ADR 0027's resource inventory;
  this model-only PR does not add a runtime inventory row. On process restart, Build resolves current
  configuration and current source authorization, and reused sessions and scheduled fires create
  fresh bindings from that composition. This intentional rebinding can change resumed context.
  Existing state needs no migration and is neither adopted into a stored context identity nor rejected
  for lacking one.
- **Security / authority:** Source registration, selection, exclusions, and overrides belong to
  trusted deployment composition and operator-tier configuration. Provenance follows each admitted
  source and never a transport, path, or body claim. Project-sourced contributions still pass the
  separate project-ingestion gate; naming or overriding `repository` never grants project trust.
  Harness-context registration and source roots do not retarget the separately constructed
  permission resolver or its session-base project governance root. Context resolution cannot
  weaken a permission deny, add an allow, grant a tool, alter hooks or credentials, or widen a child. Children inherit only the parent's admitted context subject to the
  named specialist's configured prompt, skills, tools, profile, project admission, and delegation
  attenuation. Source-only command listing owner-authorizes the session and resolves its current
  principal-scoped context without reattaching unrelated execution. In unauthenticated local mode,
  the existing local/system-principal behavior remains valid rather than treating a nil external
  principal as failure. A source explicitly backed by execution files still requires and authorizes
  that source backend. Existing sessions and schedules reauthorize under current policy on restart;
  rebinding never preserves a revoked grant.
- **Compatibility / migration:** The workspace-free signatures are an intentional exported-engine
  break. The shared implementation PR updates `engine/api/*.txt` with `task api:update` and records
  the break in `engine/CHANGELOG.md` under `engine/COMPATIBILITY.md`; callers update atomically, with
  no compatibility-adapter window. Existing persisted sessions and schedules require no destructive
  migration. Root discovery remains root-only: nonempty AGENTS.md wins; absent or whitespace-only
  AGENTS.md falls through to CLAUDE.md; both absent/empty means no project fragment; other read errors
  follow existing assembly policy. Preserve trimming, framing, provenance manifests, freshness,
  admission, source-specific failure behavior, and current caps. Do not add ancestor traversal or
  trusted content roles.

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
- AC2.1: For a stable three-source fixture, palette listing and prompt expansion/body retrieval expose the same visible names and select the same winners, including a positive control with conflicting same-name execution content; a listed name without a retrievable body and a retrieved name absent from the stable listing fail conformance.
  - verify: `TestADR_0354_HarnessContext_Scenario2_PaletteAndExpansionAgree`
- AC2.2: A selected command source edit between calls appears on the next List/Expand observation. This holds for API-backed fixtures and for explicitly selected execution-file sources; merely changing an unselected execution file has no effect, and no cross-call transaction snapshot is implied.
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
- AC3.3: Rules, skills, agent definitions, and instruction manifests preserve their current lifetimes, admission, caps, and source-specific error behavior. Fixed-origin sources stamp their registered tier; trusted mixed local adapters preserve only allowed per-entry user/project/explicit tiers; a fixed-driver remote source cannot promote a payload tier. Root instructions remain project-tier and project admission occurs before overrides.
  - verify: `TestADR_0354_HarnessContext_Scenario3_ProvenanceAndSourceContracts`
- AC3.4: Source reads never update the execution ReadLedger, including when both capabilities reference the same files. Sharing a context source does not share session read evidence.
  - verify: `TestADR_0354_HarnessContext_Scenario3_SourceReadsDoNotAuthorizeEdits`
- AC3.5: Inherited sources do not widen a child's existing specialist catalog, profile, trust admission, or delegation capabilities; an isolated execution fork alone does not retarget its context sources.
  - verify: `TestADR_0354_HarnessContext_Scenario3_ChildAttenuationPreserved`

### Scenario 4 - Source failure does not change authority

The [source-authority ADR](../adr/0354-harness-context-source-authority.md) separates
configuration failure from optional absence. Restart proofs exercise current-policy rebinding without
a durable context identity or legacy-state rejection.

**Acceptance:**
- AC4.1: Optional absence and ordinary configured-chain lookup retain established behavior. Failure resolving a required source is reported rather than replaced by execution files, process cwd, or an unrelated default.
  - verify: `TestADR_0354_HarnessContext_Scenario4_NoUnconfiguredAuthorityFallback`
- AC4.2: Both shared-file and independent-source paths preserve source-specific error semantics; a configured fail-soft chain can still consult its next admitted source without inventing a new source.
  - verify: `TestADR_0354_HarnessContext_Scenario4_ConfiguredChainFailureSemantics`
- AC4.3: After restart, an existing session and a scheduled fire resolve the current operator policy and current authorization. A changed configuration can change resumed context without a stored context reference, destructive migration, or fallback to stored execution placement.
  - verify: `TestADR_0354_HarnessContext_Scenario4_RestartRebindsCurrentPolicy`
- AC4.4: Concurrent first use creates one principal-scoped binding per session ID; same-owner/profile calls reuse it without sharing snapshots, live lookups, or caches across owners. Failed creation is retryable, mismatched principal/profile reuse fails closed, retirement waits for in-flight borrowers and is idempotent, and actual session teardown plus Build shutdown close the binding. Caller-neutral process sources remain safely shared.
  - verify: `TestADR_0354_HarnessContext_Scenario4_PrincipalScopedBindingIsolation`

### Scenario 5 - Hybrid context composition and controlled overrides

The [domain model](../architecture/mecatl.modelith.md#harnesscontext) defines context composition
independently from execution. The shared PR proves these scenarios with offline source-service
and filesystem reference adapters; actual mecak8s deployment and backend integration proofs remain
owned by their integration changes. The exact per-kind policy in the Interface contract governs
these proofs.

**Acceptance:**
- AC5.1: A helpdesk composition combines deployment-file instructions with configured service-provided instructions, rules, and skills without a repository or command runner. Equivalent admitted source data served through a file or service adapter has the same precedence and trust.
  - verify: `TestADR_0354_HarnessContext_Scenario5_HelpdeskDeploymentAndServiceSources`
- AC5.2: A coding composition combines deployment instructions, organization service skills, and mounted-repository instructions, rules, and skills while file tools and Shell still use the coherent repository execution binding.
  - verify: `TestADR_0354_HarnessContext_Scenario5_HybridCodingContext`
- AC5.3: A three-source adversarial fixture proves named override resolution after exclusion: only candidates named in `replaces` are removed when the winner is present, an earlier non-replaced candidate still wins, the configured winner wins when that blocker is absent, and an absent winner restores normal order. Listing and invocation/body retrieval choose the same definitions for commands, rules, skills, and agent definitions. Duplicate/unknown/self-replacing and structurally no-op declarations fail startup; source transport or discovery timing never selects a winner.
  - verify: `TestADR_0354_HarnessContext_Scenario5_PerKindOverrideResolution`
- AC5.4: Instruction and rule contributions combine, replace, or are excluded according to their approved per-kind policy, with retained provenance. They are not processed by an implicit recursive merge of arbitrary source objects. Disabling repository context leaves deployment/service context and repository execution available.
  - verify: `TestADR_0354_HarnessContext_Scenario5_CombineExcludeAndDisableRepositoryContext`
- AC5.5: A lower-admission contribution claiming deployment origin or requesting changed override policy cannot promote itself, bypass project admission, weaken a permission deny, or expand a child's tool catalog.
  - verify: `TestADR_0354_HarnessContext_Scenario5_ContextOverridesCannotGrantAuthority`

## Dependency stack

1. **This PR:** proposed model, ADR, and exact interface/acceptance contract for human review.
2. **Shared implementation PR:** source-bound interfaces; exact operator-only per-kind policy;
   real local `app.Build` and server discovery wiring; offline conformance over independent source
   bindings; API snapshots and changelog; and current-policy restart rebinding. Inactive types or
   test-only composition do not satisfy this task.
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
| Automatic deletion or rejection of legacy state | None | Current-policy rebinding requires no destructive migration. |

## Definition of done

This proposed contract is complete for human Plan / Interface review. Run the acceptance-plan
checker and its fixtures plus `task docs`; render model Markdown from YAML, never by hand. Record
environment or baseline failures honestly rather than claiming these gates passed.

The later implementation requires `task test`, `task lint`, `task test:race`, `task api:check`,
`task docs`, applicable strict AC tracing, the offline demo, and independent panel review. The exact
proposed docs commit is the authorized baseline for a draft stacked implementation PR only. The plan
remains proposed until merged; no implementation PR may merge ahead of it, and neither draft status
nor this exception claims release behavior or waives either human merge gate.
