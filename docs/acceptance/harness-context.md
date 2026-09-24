# Harness context source authority - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — separates harness source authority from execution across composition, trust, and public engine interfaces.
**Decision record:** [ADR 0359](../adr/0359-harness-context-source-authority.md)
**Phase:** shared source binding and session-effective inventories
**Status:** draft, 2026-09-24. Amendment to the contract approved in #1814; implementation remains in #1875. Exact binding and inventory interfaces are under review.
**Delivery:** Split. Merge this Plan / Interface amendment before bringing #1875 into conformance; backend integrations follow the shared implementation.
**Expected tasks:** deferred to orchestration
**Issue:** Relates to [#1811](https://github.com/stacklok/mecatl/issues/1811), [#580](https://github.com/stacklok/mecatl/pull/580), and [#1579](https://github.com/stacklok/mecatl/pull/1579) / [#1614](https://github.com/stacklok/mecatl/pull/1614). This contract closes none of them.
**Plan PR:** [#1878](https://github.com/stacklok/mecatl/pull/1878), draft amendment to merged [#1814](https://github.com/stacklok/mecatl/pull/1814).
**Approved baseline:** #1814 merged as `2329ae936fefd72df667e5b8c56d3ded5d53578a`. This amendment requires its own approval by merge.

`HarnessContext` names the deployment-configured composition of admitted instruction and
customization sources supplied to a session. Sources may use APIs, host files, databases, or
explicitly selected execution files. Separate responsibilities do not require separate storage.
Composition defines enabled sources, ordering, and per-kind combination, collision, override,
and exclusion behavior. Transport and storage location do not select precedence or trust.

Two coding sessions belonging to the same owner can execute in different worktrees.
When the operator selects their execution files as context, each session must list and
consume its own commands and instructions. When the operator selects one host or API
source instead, both sessions use that source without attaching their execution backends.
The session's human-facing inventories describe the same resolved skills and agent
definitions that its model uses. An execution fork alone never retargets inherited context.

This plan owns the shared interfaces and reference proofs. The backend integrations use
those interfaces rather than introduce backend-specific source selection or resolution.

## Human decisions

- [x] Independent, flexible sources — Decision: deployment composition selects harness sources independently from execution. APIs, host files, and explicitly selected execution files are valid source implementations; execution placement never implicitly chooses them.
- [x] Layered composition and overrides — Decision: the operator can combine deployment, API/service, and repository contributions, including multiple sources of the same content kind. Resolution is per-kind, deterministic, and provenance-preserving. Repository context can be disabled without disabling execution; content overrides cannot change authorization.
- [x] Integration stack — Decision: amend this shared contract, then bring #1875 into conformance; MicroVM #580, Redis #1811, and native Kubernetes #1614 are sibling integrations. Native plan #1579 must also adopt the shared contract before its implementation is declared conformant.
- [x] Session-bound execution-file sources — Decision: explicitly selected execution-file sources acquire the exact authorized session backend during creation, discovery, and reload. Same-owner/profile sessions remain distinguishable; independent sources do not require execution attachment.
- [x] Session-effective inventories — Decision: human skill and agent inventories use the session's admitted resolved context, including winning definitions and applicable restrictions. Private definitions are not merged into process-wide snapshots; inventory membership grants no invocation authority.
- [x] Child inheritance — Decision: an isolated execution fork preserves inherited bound source authority within the child's restrictions. Reconstruction reauthorizes the inherited source instead of selecting child execution files as fallback.
- [ ] Cross-parent child resume: approve the proposed original-parent ID/incarnation restriction below, or require an explicit original-anchor reconstruction design that preserves cross-parent resume. Existing ownership-only resume permits a same-owner different parent; changing that behavior requires a separate affirmative decision. The implementation must not choose between these alternatives.
- [x] In-place decision amendment — Decision: the directing operator authorized amending this plan and its existing ADR because implementation has not landed. Renumber the HarnessContext ADR to 0359 to resolve the 0357 collision; preserve the unrelated model-stream decision.
- [x] Composition configuration and resolution schema — Decision: trusted composition registers source IDs, and operator-only configuration names enabled IDs plus a highest-precedence-first source order for each content kind. Instructions combine in that order or select the first nonempty source in `replace` mode. Commands, rules, skills, and agent definitions resolve exact-name collisions by order, with exact operator-declared exclusions and named lower-source overrides. Resolution performs no recursive merge and assigns no transport-derived precedence.
- [x] Public API transition — Decision: make the workspace-free `engine/prompt` API break directly, with source-bound filesystem adapters. The implementation PR updates API snapshots and `engine/CHANGELOG.md`; it does not retain workspace-taking compatibility overloads or adapters.
- [x] Binding-generation retirement — Decision: the operator explicitly authorizes close to retire a binding generation, not a session ID forever. Explicit owner-authorized supported reload activates a fresh generation under current authorization; retired generations never reopen, stale borrows do not reactivate them, old releases cannot affect replacements, and Build shutdown prevents activation. This clarification is authorized for the draft stack; it does not claim approval by plan merge.
- [x] Restart and deployment reconfiguration — Decision: on harness restart, existing sessions and schedules rebind to the current deployment composition under current authorization. Configuration changes may change resumed context. Do not add a durable `HarnessContextRef`, reject legacy state, or infer a binding from stored execution placement.

## Interface contract

The base contract was approved through #1814. This amendment proposes the exact
session-binding and inventory changes for a new human review checkpoint. It does
not authorize runtime changes or backend-specific alternatives before approval.

- **gRPC / protobuf:** Keep `HarnessService.ListAgents` and `ListSkills` and add
  `string session_id = 1 [(buf.validate.field).string.min_len = 1];` to each
  currently empty request. Keep both response messages and their existing metadata
  fields unchanged. Retain `GET /v1/agents` and `GET /v1/skills`, with required
  `session_id` query parameters and the same session-affinity checks as
  `ListCommands`. These are session inventories, not deployment catalogs; an empty
  ID is InvalidArgument / HTTP 400, not a request for global defaults. Missing and
  foreign-owned sessions both return NotFound / HTTP 404 before any source binds.
  Binding/inventory errors use sanitized Internal / HTTP 500; an unavailable
  effective child binding uses FailedPrecondition / HTTP 412. Preserve existing
  cancellation/deadline mapping. No source ID, root, environment selector, body,
  generation, or durable context reference is added to public requests or replies.
  Rows describe admitted definitions; membership is not an assertion that a tool
  exists or that its invocation would be permitted.
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
  Server discovery borrows a session-effective binding. Do not add an `engine/port`
  provider, `session.HarnessContextRef`, or exported engine five-source container.
  The app registration descriptor remains kind-specific:

  ```go
  type HarnessSourceID string

  type HarnessSourceScope struct {
      SessionID             session.SessionID
      Principal             *session.Principal
      Profile               string
      AcquireExecutionFiles func(context.Context) (tool.Workspace, func() error, error)
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
      ID             HarnessSourceID
      Scope          HarnessSourceScopeKind
      Provenance     HarnessProvenancePolicy
      ExecutionFiles bool
      Bind           func(context.Context, HarnessSourceScope) (T, func() error, error)
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

  Each registration declares process or principal scope. Process scope receives
  zero `HarnessSourceScope` and is valid only for a caller-neutral, concurrency-safe
  adapter. Principal scope binds once per session generation, receiving the
  authoritative session ID, cloned owner, and profile. Same-owner/profile sessions
  never substitute for each other's identity. Cleanup is idempotent; nil is valid
  when there is nothing to close.

  `ExecutionFiles` defaults to false and is a programmatic registration property,
  not a new YAML source selector. True requires principal scope and fixed `project`
  provenance; incompatible registrations fail startup. Only a selected, admitted
  registration with this property receives `AcquireExecutionFiles`; other
  registrations receive nil. Process registrations never receive the capability.
  A true registration calls the capability while binding, and its cleanup retains
  and releases the acquired source lease. It cannot request another session, ref,
  or root. The callback exposes a read-only workspace view, rejects file and
  namespace mutations, and supplies neither a runner nor an execution read ledger.

  Server composition captures the exact source session/ref in the callback. For a
  newly created root or scheduled fire these are the reserved consumer ID and its
  authorized placement; for inherited child context they remain the parent's
  source anchor. No source selection is inferred from these values. An explicitly
  selected unavailable execution-file source reports an error; no-FS does not
  invent execution storage. Independent file/API sources remain usable under
  no-FS and while unrelated execution is unavailable.

  Acquisition owns a separate source lease or a reference-counted borrow from an
  authorized provisional binding. It must work before initial session publication;
  it cannot require that a newly reserved ID already be loadable from the store.
  It never closes the execution owner's binding or deletes its retained placement.
  Every failed creation/activation releases only resources acquired by that attempt.
  Session publication follows successful required-source and engine construction.
  Failed publication retires the unpublished context generation. Actual runs still
  independently acquire their execution authority.

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

  `/agents` and `/skills` inspect the active session. Update the Go client methods
  to `ListAgents(context.Context, string) ([]Agent, error)` and
  `ListSkills(context.Context, string) ([]Skill, error)`, with the string being the
  required session ID; their UI interfaces/commands forward that same ID and
  affinity. Create, resume, clear, successor adoption, and session switch invalidate
  prior inventory requests; stale replies cannot populate another session's view.
  Without an active session, explain that a session must be created or selected.
  With an empty result, say that no definitions are admitted in this session.
  Do not hide these inspectors because startup `ServerCapabilities.agents` or
  `.skills` is false. Those legacy deployment hints are not session availability.
  Inventory rows are labeled context definitions, not permission grants or enabled
  tools; displaying a definition never enables an action or bypasses tool checks.
  Existing learned-skill administration retains its separate authorization gates.
  No extra deployment/global inventory endpoint or new configuration switch is added.
- **Events / persistence:** Add no event or snapshot field. Commands remain live per List/Expand;
  instructions observe their configured sources once per run; rules, skills, and agent definitions
  preserve their snapshot semantics. Policy and process-scoped snapshot sources resolve once in
  `app.Build`. A principal-scoped snapshot source resolves once when that session context binding is
  created in the current Build and remains stable for that binding; it is not rebuilt per command,
  turn, or tool call. A principal-scoped live command source remains live only within its bound
  principal.

  Discovery and execution borrow one internal `server.HarnessContextBinding`.
  Replace `Config.Commands` / `CommandSourceResolver` with `Config.HarnessContext`
  / `HarnessContextResolver`; keep the existing kind-specific registration ports.

  ```go
  // internal/adapter/server; never a public request or durable record.
  type HarnessContextRequest struct {
      SessionID             session.SessionID
      Owner                 *session.Principal
      Profile               SessionProfile
      SourceSessionID       session.SessionID
      SourceRef             session.EnvironmentRef
      AcquireExecutionFiles func(context.Context) (tool.Workspace, func() error, error)
  }

  type HarnessContextInventory struct {
      Agents []*mecatlv1.AgentInfo
      Skills []*mecatlv1.SkillInfo
  }

  type HarnessContextBinding interface {
      CommandSourceBinding
      Inventory(context.Context) (HarnessContextInventory, error)
  }

  type HarnessContextActivation interface {
      Binding() HarnessContextBinding
      Commit() (func(), error)
      Abort()
  }

  type HarnessContextResolver interface {
      Borrow(context.Context, HarnessContextRequest) (HarnessContextBinding, func(), error)
      PrepareActivation(context.Context, HarnessContextRequest) (HarnessContextActivation, error)
      Retire(session.SessionID, HarnessContextBinding)
  }

  type SessionContextEngineFactory func(
      context.Context, HarnessContextRequest, HarnessContextBinding, ProviderSelector,
      []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool,
  ) (SessionEngineResult, error)

  // engine/agent; injected on the selected child engine.
  // Session arguments are authoritative read-only metadata, not mutation handles.
  type ChildContextBinder func(context.Context, *session.Session, *session.Session) (func(), error)

  func (s *Service) ListAgentsForSession(context.Context, session.SessionID) ([]*mecatlv1.AgentInfo, error)
  func (s *Service) ListSkillsForSession(context.Context, session.SessionID) ([]*mecatlv1.SkillInfo, error)
  ```

  The factory's existing string argument remains a governance/project-policy root;
  it is not a harness source selector. Its profile argument must equal the request
  profile. The request replaces the factory's positional session ID/owner arguments.
  The supplied binding is the sole context used by the factory; it must not borrow
  or activate another generation. The service retains the source lease through
  construction and attaches its release to the adopted engine's close path.
  `SourceSessionID` and `SourceRef` identify capability acquisition only, not source
  selection. The server constructs them from validated placement or authorized
  stored lineage, never from public request fields or incidental context values.
  The callback captures the same tuple and the deployment's authorization scope;
  changed callback identity cannot silently change the captured authority. Reuse
  validates owner, profile, source session, and exact ref as well as consumer ID.

  `ListCommandsForSession`, `ListAgentsForSession`, and `ListSkillsForSession` first
  load and owner-authorize the target session, construct the request, and borrow
  the same binding generation used by its engine. Replace process snapshot and
  `LiveSkills` fallbacks with this binding. Classify all three service methods as
  caller-owned. Independent discovery constructs a lazy acquisition capability but
  never calls it unless a selected source requires those execution files.
  Ownerless local/system-principal behavior remains valid.

  Inventory projects the already-resolved registry and skill catalog, sorted by
  exact logical name. It does not re-list raw external sources or resolve winners
  again. Base external skill/agent snapshots keep the binding lifetime. Existing
  learned-skill publication uses that session's owner/project partitions and the
  same atomic catalog as its Skill tool; publishing an overlay never rebuilds
  external sources or mutates another session's catalog. Runtime publication is
  evaluated separately against each eligible binding's frozen external-name set:
  external definitions win in that binding; a learned definition can be visible
  in another binding of the same partition where no external name collides. Do not
  union external inventories into a partition-wide veto or overwrite one binding's
  external snapshot with another's. On the next existing bounded publication point,
  each eligible binding observes activation/archive changes through its own catalog;
  eager cross-session fan-out is not required. Durable learning mutation admission
  remains unchanged; this is the runtime publication/visibility contract, not a new
  authorization path for activating learned content. Tests must distinguish durable
  activation from whether a particular session publishes that learned definition.
  Named specialists retain
  their existing preload/tool restrictions; a root inventory does not union child
  catalogs. A child inventory requires its live or normally resumed effective child
  binding, including attenuation. If that binding cannot be reconstructed, return
  FailedPrecondition rather than expose a broader parent or global inventory.

  First creation remains single-flight and retryable on failure. A borrow holds
  one exact generation. `Retire(sessionID, expectedBinding)` atomically retires
  only the current generation identified by that exact resolver-owned binding;
  a nil, foreign, or no-longer-current binding is a no-op. Binding identity is
  stable for the generation, never inferred from equal content or request fields.
  Retirement prevents new borrows and drains existing users. Session lifecycle
  teardown holds the existing per-session serialization and supplies its current
  exact binding. Delayed cleanup carries its originally captured binding, never
  looks up a replacement by session ID, and cannot retire that replacement.
  Failed creation targets only its own binding, not an existing borrowed binding.
  This uses the private binding identity, not a public or durable generation selector.
  Supported owner-authorized reload uses `PrepareActivation` under the existing
  per-session lifecycle serialization. A prepared replacement is private: inventory
  and ordinary Borrow still see the prior committed generation or its retired
  error. Construct the engine against `activation.Binding()`, not by borrowing
  again. Only after engine construction and all registration preconditions succeed
  may the service commit the candidate and adopt the engine under the same
  serialization. Discovery participates in that publication barrier.

  `Commit` checks that the prepared revision is still current and admission is
  open, publishes the generation, retires only the exact replaced generation for
  draining, and transfers one release lease to the engine owner. Preparation,
  commit, abort, and engine-close callbacks capture their exact binding identities;
  none performs unfenced retirement by session ID. A release drops only its own
  lease and does not retire a generation. `Abort` is idempotent and releases only
  candidate resources; a failed prepare/build/registration/commit leaves the
  previous engine and inventory paired.
  An already-active matching preparation borrows that generation without replacing
  it; abort does not retire it. Abort after successful commit is a no-op. Repeated
  Commit is an error without another lease/publication. Failed commit must be
  followed by Abort. Initial source-only Borrow can publish a binding before any
  engine exists; the later engine must adopt that binding. Failure constructing
  that later engine releases its borrow without retiring the existing binding.
  Initial creation has no public inventory until its session is published; failed
  publication retires only the attempt's binding using its exact identity and
  releases its lease, without deleting retained execution files or saved transcripts.
  No public transaction or generation handle is added.

  An individual waiting caller can cancel promptly without cancelling another
  caller's acquisition. Cancellation of the acquisition owner aborts that attempt;
  remaining callers may retry rather than reuse a poisoned entry. Cancellation
  after lease acquisition closes attempt-owned resources and prevents late
  publication. A successful binding is not tied to the initiating RPC context's
  lifetime. Build shutdown separately cancels pending attempts, releases late
  results, permanently closes admission, and retires all its owned generations
  while draining existing users. Old teardown, cleanup, and release callbacks
  cannot retire or evict replacements; neither ordinary nor stale Borrow
  reactivates retirement.

  Process-scoped resources close from `Built.Close`; session/principal-scoped resources close only
  after their generation retires and its final borrower releases, including during Build shutdown.
  The implementation adds this owned cache,
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
  that source backend.

  `agent.Deps` adds optional `BindChildContext ChildContextBinder`. Composition
  installs it on each selected, fully attenuated child engine, capturing that
  engine's actual resolved inventory and the inherited parent source generation;
  it must not copy a broader root inventory callback unchanged. A nil callback
  preserves embedding behavior when no harness context is configured.

  Subagent, Parallel, and parented Team paths invoke the selected child's callback
  with parent and child metadata after IDs, relationship, effective profile, and
  restrictions are known, but before child publication or execution. Supported
  resume invokes it after fresh lineage/ownership validation and before rehoming
  or running. The callback registers the same child-effective binding for inventory
  lookup and independently retains the parent's generation. Its idempotent close
  callback retires/unregisters only its captured exact child binding under the
  same expected-binding fence, then releases its retained parent lease after the
  final child use, including cancellation and background execution. It never
  retires the parent's generation or a replacement child binding. Every failed
  child publication or start performs the same attempt-scoped cleanup; a failing
  binder cleans up its own partial registration and lease before returning an error.
  This callback exposes no server type, source selector, or persistence field in
  the engine.

  Snapshot-kind inheritance retains the parent's already-admitted external
  snapshot, even if the underlying service/files change before child construction;
  it does not rerun Bind/List for those sources. Live commands and per-Run
  instructions keep reading the inherited selected source. Child restrictions and
  its authorized learned-skill view apply without changing external winners.
  Parent context or execution-owner teardown cannot invalidate the backend while
  a child source lease is held; providers must retain the actual readable backend,
  not just an in-memory counter. New borrows after retirement remain prohibited.

  **Proposed resume rule, pending the Human decision above:** a persisted delegated
  child resumes only under the original `Relationship.ParentSessionID` and
  `ParentIncarnation`. Validate both before any new source binding, workspace fork,
  or mutation. A same-owner unrelated parent is not sufficient. Matching-parent
  resume uses that parent's current-policy/current-authorization context and the
  supported child attenuation path; the child's fresh execution fork is not a
  source anchor. Missing parent authority or insufficient persisted restrictions
  fail closed without deleting the child transcript. This uses existing lineage
  rather than persisting a `HarnessContextRef` or inferring from execution files.
  Unparented Team sessions use their own explicitly composed source authority;
  a missing relationship is never invented. Cross-parent original-anchor lookup
  is not implicitly authorized by this proposal.

  Clear/Fork main-session successors and each scheduled fire are new context
  consumers: when current policy explicitly selects their execution files, bind
  their server-authorized placement. Schedule origin metadata routes results and
  does not silently select context. On restart, existing sessions and schedules
  reauthorize under current policy; rebinding cannot preserve a revoked grant.
- **Compatibility / migration:** The workspace-free signatures are an intentional exported-engine
  break. The shared implementation PR updates `engine/api/*.txt` with `task api:update` and records
  the break in `engine/CHANGELOG.md` under `engine/COMPATIBILITY.md`; callers update atomically, with
  no compatibility-adapter window. Existing persisted sessions and schedules require no destructive
  migration. Root discovery remains root-only: nonempty AGENTS.md wins; absent or whitespace-only
  AGENTS.md falls through to CLAUDE.md; both absent/empty means no project fragment; other read errors
  follow existing assembly policy. Preserve trimming, framing, provenance manifests, freshness,
  admission, source-specific failure behavior, and current caps. Do not add ancestor traversal or
  trusted content roles.

  Requiring session IDs on inventory RPCs is a deliberate behavioral break: old
  empty requests fail loudly instead of receiving private or global fallback data.
  Migrate in-repository gRPC/HTTP clients, TUI tests, examples, and SDK transport
  catalogs atomically. Generated TypeScript requests use `sessionId`; the HTTP
  catalog carries `session_id` exactly as it does for `ListCommands`. Regenerate
  protobufs, SDK types, and API/reference output from sources; do not edit generated
  files by hand. Any Studio integration uses the published SDK, never a local-path
  replacement. Record the wire/client break in the owning release artifacts.
  Original-parent resume rejection is an additional proposed behavioral break,
  pending the explicit decision above; preserve stored sessions and transcripts.
  Rename the implementation's `TestADR_0357_HarnessContext_*` proofs to
  `TestADR_0359_HarnessContext_*`; leave model-stream ADR 0357 proofs unchanged.

## In scope - 8 scenarios, in implementation order

### Scenario 1 - Source selection and execution vary independently

The model's [Environment binding](../architecture/mecatl.modelith.md#environment) remains
Workspace, session ReadLedger, and optional coherent CommandRunner. Shared implementation proofs
use offline reference source/execution adapters, not a claim that later VM or Redis integration
has already passed.

**Acceptance:**
- AC1.1: The same configured independent sources with identical source observations provide the same admitted context with two different execution namespaces, without deriving sources from either execution root. Explicit execution-file selection is the distinct case covered by AC1.3 and Scenario 6.
  - verify: `TestADR_0359_HarnessContext_Scenario1_SourceIndependentOfExecution`
- AC1.2: Conflicting instructions and customizations planted only in an unselected execution namespace do not enter automatic context assembly, the command palette, or customization catalogs.
  - verify: `TestADR_0359_HarnessContext_Scenario1_UnselectedExecutionContentIgnored`
- AC1.3: An explicitly configured source can read execution files through their actual backend, and admitted contents reach the harness without reopening a virtual root as a host path.
  - verify: `TestADR_0359_HarnessContext_Scenario1_ExplicitExecutionFileSource`
- AC1.4: No-FS execution retains admitted logical sources, including file-backed sources inaccessible to agent file tools; file tools and Shell remain unavailable.
  - verify: `TestADR_0359_HarnessContext_Scenario1_NoFSKeepsConfiguredSources`

### Scenario 2 - Discovery and invocation share source authority

Use the existing logical-source contracts described in the [architecture guide](../architecture.md).
Without explicit layering, preserve the compatibility chain: directory commands, skills, driver
commands, then MCP prompts. That existing cross-kind chain is not the full per-kind layering and
override contract. Explicit compositions follow the approved resolution policy from Scenario 5;
ordinary misses and established fail-soft behavior remain distinct from authority fallback.

**Acceptance:**
- AC2.1: For a stable three-source fixture, palette listing and prompt expansion/body retrieval expose the same visible names and select the same winners, including a positive control with conflicting same-name execution content; a listed name without a retrievable body and a retrieved name absent from the stable listing fail conformance.
  - verify: `TestADR_0359_HarnessContext_Scenario2_PaletteAndExpansionAgree`
- AC2.2: A selected command source edit between calls appears on the next List/Expand observation. This holds for API-backed fixtures and for explicitly selected execution-file sources; merely changing an unselected execution file has no effect, and no cross-call transaction snapshot is implied.
  - verify: `TestADR_0359_HarnessContext_Scenario2_SelectedCommandsRemainLive`
- AC2.3: With an independent healthy source and unavailable execution backend, owner-authorized command listing succeeds without calling execution reattachment. An explicitly execution-file-backed source still reports its own backend failure according to its source contract.
  - verify: `TestADR_0359_HarnessContext_Scenario2_SourceOnlyDiscovery`

### Scenario 3 - Project instructions and customizations preserve their contracts

The source boundaries build on [ADR 0081](../adr/0081-rules-source-port.md),
[ADR 0108](../adr/0108-on-demand-logical-skill-assets.md), and
[ADR 0013](../adr/0013-agent-definitions.md). Storage sharing does not raise trust or merge read evidence.

**Acceptance:**
- AC3.1: Root instructions are read once per run from the configured source, refresh on a subsequent run, and remain ephemeral. Both manifest-enabled and ordinary assembly consume the same source with unchanged project provenance.
  - verify: `TestADR_0359_HarnessContext_Scenario3_ProjectInstructionsPerRun`
- AC3.2: The file-backed source preserves root-only AGENTS.md/CLAUDE.md precedence, whitespace-only fallback, absence, genuine read errors, and existing framing without reading unrelated execution files.
  - verify: `TestADR_0359_HarnessContext_Scenario3_RootDiscoveryCompatibility`
- AC3.3: Rules, skills, agent definitions, and instruction manifests preserve their current lifetimes, admission, caps, and source-specific error behavior. Fixed-origin sources stamp their registered tier; trusted mixed local adapters preserve only allowed per-entry user/project/explicit tiers; a fixed-driver remote source cannot promote a payload tier. Root instructions remain project-tier and project admission occurs before overrides.
  - verify: `TestADR_0359_HarnessContext_Scenario3_ProvenanceAndSourceContracts`
- AC3.4: Source reads never update the execution ReadLedger, including when both capabilities reference the same files. Sharing a context source does not share session read evidence.
  - verify: `TestADR_0359_HarnessContext_Scenario3_SourceReadsDoNotAuthorizeEdits`
- AC3.5: Inherited sources do not widen a child's existing specialist catalog, profile, trust admission, or delegation capabilities; an isolated execution fork alone does not retarget its context sources.
  - verify: `TestADR_0359_HarnessContext_Scenario3_ChildAttenuationPreserved`

### Scenario 4 - Source failure does not change authority

The [source-authority ADR](../adr/0359-harness-context-source-authority.md) separates
configuration failure from optional absence. Restart proofs exercise current-policy rebinding without
a durable context identity or legacy-state rejection.

**Acceptance:**
- AC4.1: Optional absence and ordinary configured-chain lookup retain established behavior. Failure resolving a required source is reported rather than replaced by execution files, process cwd, or an unrelated default.
  - verify: `TestADR_0359_HarnessContext_Scenario4_NoUnconfiguredAuthorityFallback`
- AC4.2: Both shared-file and independent-source paths preserve source-specific error semantics; a configured fail-soft chain can still consult its next admitted source without inventing a new source.
  - verify: `TestADR_0359_HarnessContext_Scenario4_ConfiguredChainFailureSemantics`
- AC4.3: After restart, an existing session and a scheduled fire resolve the current operator policy and current authorization. A changed configuration can change resumed context without a stored context reference, destructive migration, or fallback to stored execution placement.
  - verify: `TestADR_0359_HarnessContext_Scenario4_RestartRebindsCurrentPolicy`
- AC4.4: Concurrent first use creates one principal-scoped binding generation per session ID; same-owner/profile calls reuse it without sharing snapshots, live lookups, or caches across owners. Failed creation is retryable, mismatched principal/profile reuse fails closed, and retirement is idempotent and lets existing borrowers drain. Actual session teardown supplies its current exact binding under lifecycle serialization. Explicit owner-authorized supported reload can activate a fresh generation under current source authorization; stale queued Borrow alone cannot reactivate it. After replacement, Retire with the old binding is a no-op: delayed old teardown, cleanup, and releases cannot retire, evict, or close the replacement, which remains borrowable and paired with its engine. Build shutdown retires all generations and prevents later activation. Caller-neutral process sources remain safely shared.
  - verify: `TestADR_0359_HarnessContext_Scenario4_PrincipalScopedBindingIsolation`

### Scenario 5 - Hybrid context composition and controlled overrides

The [domain model](../architecture/mecatl.modelith.md#harnesscontext) defines context composition
independently from execution. The shared PR proves these scenarios with offline source-service
and filesystem reference adapters; actual mecak8s deployment and backend integration proofs remain
owned by their integration changes. The exact per-kind policy in the Interface contract governs
these proofs.

**Acceptance:**
- AC5.1: A helpdesk composition combines deployment-file instructions with configured service-provided instructions, rules, and skills without a repository or command runner. Equivalent admitted source data served through a file or service adapter has the same precedence and trust.
  - verify: `TestADR_0359_HarnessContext_Scenario5_HelpdeskDeploymentAndServiceSources`
- AC5.2: A coding composition combines deployment instructions, organization service skills, and mounted-repository instructions, rules, and skills while file tools and Shell still use the coherent repository execution binding.
  - verify: `TestADR_0359_HarnessContext_Scenario5_HybridCodingContext`
- AC5.3: A three-source adversarial fixture proves named override resolution after exclusion: only candidates named in `replaces` are removed when the winner is present, an earlier non-replaced candidate still wins, the configured winner wins when that blocker is absent, and an absent winner restores normal order. Listing and invocation/body retrieval choose the same definitions for commands, rules, skills, and agent definitions. Duplicate/unknown/self-replacing and structurally no-op declarations fail startup; source transport or discovery timing never selects a winner.
  - verify: `TestADR_0359_HarnessContext_Scenario5_PerKindOverrideResolution`
- AC5.4: Instruction and rule contributions combine, replace, or are excluded according to their approved per-kind policy, with retained provenance. They are not processed by an implicit recursive merge of arbitrary source objects. Disabling repository context leaves deployment/service context and repository execution available.
  - verify: `TestADR_0359_HarnessContext_Scenario5_CombineExcludeAndDisableRepositoryContext`
- AC5.5: A lower-admission contribution claiming deployment origin or requesting changed override policy cannot promote itself, bypass project admission, weaken a permission deny, or expand a child's tool catalog.
  - verify: `TestADR_0359_HarnessContext_Scenario5_ContextOverridesCannotGrantAuthority`

### Scenario 6 - Same-owner sessions acquire distinct selected execution sources

Two coding sessions share an owner and profile but execute in different worktrees.
The operator explicitly selects session execution files as repository context.
[Server-owned placement](../adr/0291-server-owned-session-placement.md) supplies exact
authority; the [source model](../architecture/mecatl.modelith.md#harnesscontext)
keeps acquisition distinct from source selection.

**Acceptance:**
- AC6.1: Two dynamically allocated execution namespaces with conflicting AGENTS.md and same-name commands remain distinct in palette listing, expansion, and model requests for same-owner/profile sessions. Source binding receives authoritative session identity, not just an owner/profile pair or a pre-captured common workspace.
  - verify: `TestADR_0359_HarnessContext_Scenario6_DynamicSessionFiles`
- AC6.2: After a two-Build restart, discovery before the first Run acquires each session's exact authorized selected source. A healthy independent-source control performs zero execution acquisitions while the execution backend is unavailable. Changed/revoked authorization fails without host, default-placement, or other-session fallback.
  - verify: `TestADR_0359_HarnessContext_Scenario6_ExactSourceAfterRestart`
- AC6.3: Creation can bind selected source files before session publication. Binding, engine-construction, and store-publication failures leave no published partial session or reusable poisoned source generation, release every attempt-owned source lease, and do not close an existing execution owner's lease or delete retained files. On reload, injected engine-construction and post-build registration failures abort the prepared generation and leave the prior engine/inventory paired, closing candidate resources exactly once. Delayed failed-attempt teardown and release after a successful retry cannot retire or close the replacement. Stale commit fails without publication; abort after that failure affects only its own candidate. Failed engine construction against an existing source-only binding releases only its borrow and leaves that binding usable.
  - verify: `TestADR_0359_HarnessContext_Scenario6_ProvisionalSourceOwnership`
- AC6.4: Source acquisition is unavailable to process-scoped, disabled, excluded-from-selection, or non-execution-file registrations. A selected source's read-only workspace rejects file/namespace writes, supplies no runner, and does not populate execution read evidence. A no-FS root with a required execution-file source fails rather than fabricating storage; independent no-FS sources still work.
  - verify: `TestADR_0359_HarnessContext_Scenario6_AcquisitionAuthority`
- AC6.5: New root successors and scheduled fires use their own server-authorized placement when current policy explicitly selects execution files, including borrowed and independently owned schedule placement where supported. Neither schedule origin nor execution kind independently selects sources.
  - verify: `TestADR_0359_HarnessContext_Scenario6_SuccessorAndFireSources`
- AC6.6: Cancelling a waiter does not cancel another caller's acquisition. A cancelled acquisition owner cannot publish late, leaks no acquired lease, and leaves later creation retryable. Shutdown also prevents late publication and leaks no acquired lease, but permanently rejects new creation in that Build. A successful binding remains usable after its initiating discovery request ends.
  - verify: `TestADR_0359_HarnessContext_Scenario6_AcquisitionCancellation`

### Scenario 7 - Human inventories match the session's effective context

The [source resolution model](../architecture/mecatl.modelith.md#harnesscontext)
applies to human discovery as well as model consumption. The existing
[session command request](../../contracts/proto/mecatl/v1/harness.proto) supplies
the session-affinity pattern for skill and agent inventories.

**Acceptance:**
- AC7.1: Owner-specific skill and agent definitions appear in that session's gRPC/HTTP inventories and actual model consumption. Adversarial same-name overrides choose identical metadata/body winners; another owner's entries, excluded candidates, and process-level losing definitions are absent.
  - verify: `TestADR_0359_HarnessContext_Scenario7_EffectiveInventories`
- AC7.2: Missing/foreign sessions are indistinguishable and trigger no source bind or list; empty IDs and mismatched affinity fail before resolution. Binding failure is a sanitized error, not an empty inventory. Independent inventories remain available without execution acquisition.
  - verify: `TestADR_0359_HarnessContext_Scenario7_InventoryAuthorization`
- AC7.3: External skill/agent inventory snapshots remain stable for their binding generation and match consumed bodies. Authorized new generations observe changes. Two bindings in the same learned-skill partition with different external-name collisions publish activation/archive changes against their own frozen external winners: external names win locally, while eligible learned definitions remain visible in the noncolliding binding. Inventory and Skill agree without external rediscovery, a partition-wide union veto, or owner/project leakage; stale generation releases cannot affect the replacement.
  - verify: `TestADR_0359_HarnessContext_Scenario7_InventoryLifetimes`
- AC7.4: TUI inventories send the active session ID and affinity across create, load, successor adoption, and session switch; delayed results from another session are rejected. Empty or unavailable views are accurately labeled. Startup inventory flags do not suppress principal-scoped definitions. Admitted metadata does not enable absent Skill/Subagent tools or bypass invocation permissions.
  - verify: `TestADR_0359_HarnessContext_Scenario7_InventoryClientScope`; SDK conformance — generated gRPC/HTTP request and session-affinity tests cover both inventory RPCs with the required session field.
- AC7.5: A restricted child's inventory comes from its actual effective binding, not a union of root or specialist catalogs. Missing reconstructable child authority yields FailedPrecondition, never a broader fallback. Named specialist preload restrictions and all existing tool ceilings remain enforced on consumption.
  - verify: `TestADR_0359_HarnessContext_Scenario7_ChildInventoryAttenuation`

### Scenario 8 - Execution forks preserve inherited source authority

[Delegation](../architecture/subagents-and-teams.md) and the
[durable relationship model](../../engine/session/kind.go) distinguish the child
execution namespace from its parent. The cross-parent resume assertion below is
part of the proposed rule awaiting the explicit Human decision, not an already
approved restriction.

**Acceptance:**
- AC8.1: Isolated Subagent, direct-write Subagent, Parallel branch, and parented Team member execution preserve the parent's admitted bound source while enforcing each child's restrictions. Change the underlying skill/agent/rule source after parent binding but before child creation: the child retains the parent's external snapshot and consumes those same winning bodies without rebinding. Live commands/per-Run instructions still observe the inherited source. Conflicting child-worktree files do not replace it.
  - verify: `TestADR_0359_HarnessContext_Scenario8_ChildSourceInheritance`
- AC8.2: A child independently holds the inherited generation and exposes its exact attenuated inventory until its context use ends. After parent context and execution-owner teardown, a fresh child command read succeeds against a reference backend that becomes unusable on final close. Final release closes it exactly once; failed child publication releases its attempt. Delayed parent teardown and stale child close/release callbacks cannot retire or close a newly activated parent generation or unregister a replacement child binding; the replacements remain usable with their exact inventories.
  - verify: `TestADR_0359_HarnessContext_Scenario8_ChildSourceLifetime`
- AC8.3: Under the proposed original-parent rule, matching-parent resume after restart uses current policy/authorization for the inherited source and preserves attenuation. A same-owner different parent, replaced parent incarnation, missing lineage authority, or revoked source fails before source acquisition, workspace forking, or mutation; the saved transcript remains intact.
  - verify: `TestADR_0359_HarnessContext_Scenario8_ResumeSourceAnchor`
- AC8.4: Reconstructing unavailable inherited context does not select the child's files, a global catalog, or an unrelated source. An independent-source/no-FS child control retains admitted context without execution tools or unrelated attachment.
  - verify: `TestADR_0359_HarnessContext_Scenario8_NoInheritedAuthorityFallback`

## Dependency stack

1. **This amendment PR:** review the shared binding and inventory interfaces,
   domain scenarios, and the explicit unresolved resume decision. Merge is the
   amendment's approval event; it does not approve implementation.
2. **Shared implementation #1875:** rebase on the approved amendment, implement
   source-bound APIs and the shared policy/binding/inventory paths, and retain all
   existing reference proofs while adding Scenarios 6-8. Update generated contracts,
   API snapshots, client migrations, and owning user guides. Renumber only
   HarnessContext ADR/test references. No backend-specific qualification is claimed.
3. **Sibling backend integrations:** MicroVM #580, Redis #1811's implementation PR,
   and native Kubernetes #1614 consume the same shared implementation. Native plan
   #1579 must first amend its source-policy and command-discovery criteria.

The siblings can proceed independently after the shared implementation. Their
handoffs must state the exact approved contract and implementation baselines.
Before approval, any downstream draft link is explicitly proposed. Each integration
qualifies conflicting unselected files, selected sources, discovery/consumption
agreement, freshness, owner isolation, no-FS, errors without fallback, source reads
without edit evidence, and current-policy restart rebinding through its real backend.

| Integration | Additional required proof and boundary |
|---|---|
| MicroVM #580 | Host/API context stays independent of guest tools. Explicit guest-file context acquires the exact session worktree; listing and expansion agree. Isolated and direct-write children preserve inherited source authority. Keep VM/worktree lifecycle and independently rooted governance separate from prompt sources; remove implicit `CompositionRoot` prompt selection, not legitimate permission policy. |
| Redis #1811 | An independent positive source wins over conflicting real Redis instructions/commands; an explicitly selected Redis source consumes those files through the actual adapter. Never reopen `/workspace` as host storage. Retain exact Redis file tools, independent session ledgers, and no Shell. Use real-adapter freshness, owner-isolation, and two-Build restart proofs. |
| Native #1579 / #1614 | Replace execution-kind suppression with explicit admitted source policy. The first slice can select deployment/operator/API sources and leave PVC context unselected. Independent discovery works during provider failure; no-FS retains context. Preserve exact placement, operator permission rules, and existing unsupported delegation/schedule capabilities. PVC-backed context requires the exact-source proof before it can be advertised. |

YAML selects registered sources; it does not create adapters. A deployment handoff
must name its available registrations. Instructions/rules served by services need
explicit composition registrations; the compatibility `driver` ID does not imply
that every content kind has a built-in transport. Do not create backend-specific
resolution algorithms, public source selectors, or a durable context identity.

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Runtime implementation in this PR | Shared implementation PR | Model and contract only. |
| Concrete MicroVM integration and live journey | PR #580 | Dependent on the shared contract. |
| Concrete Redis correction | Issue #1811 | Separate sibling implementation. |
| Concrete native Kubernetes integration | Plan #1579 and implementation #1614 | Explicit source-policy migration and backend qualification after the shared layer. |
| New deployment-wide inventory API | None | Session-effective inventories replace the ambiguous global views; no independent admin requirement is established. |
| Generic registry or one new transport per source noun | Future demonstrated need | Existing ports and APIs already support independent sources. |
| Ancestor-directory traversal within a file source or changed caps | Separate decision | Preserve root-only discovery within each file source; composing multiple configured sources is in scope. |
| Automatic deletion or rejection of legacy state | None | Current-policy rebinding requires no destructive migration. |

## Definition of done

This amendment remains draft while the cross-parent resume decision is unresolved.
Run the acceptance-plan checker and its fixtures, `task docs:model`, and `task docs`;
render model Markdown from YAML. The review PR reports the open decision and the
exact proposed interfaces. No implementation dispatch starts from a draft contract.

After human contract merge, #1875 must pass `task test`, `task lint`,
`task test:race`, `task api:check`, `task generate` freshness, `task docs`,
`task site:build`, strict AC tracing, the offline demo, and independent panel review.
The implementation updates the owning settings, API/SDK, inventory, and delegation
guides in place; this plan does not present proposed behavior as shipped. Tests
exercise production composition and transport seams with isolated offline fixtures.
The implementation PR records the approved amendment commit. Humans alone merge.
