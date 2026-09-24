# Harness context source authority - acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — separates harness source authority from execution across composition, trust, and public engine interfaces.
**Decision record:** [ADR 0359](../adr/0359-harness-context-source-authority.md)
**Phase:** shared source binding and session-effective inventories
**Status:** proposed, 2026-09-24. Amendment to the contract approved in #1814; implementation remains in #1875. The directing user resolved the recovery, authority, and bounded-content choices and authorized the necessary durable contracts. Exact proposed interfaces below still require amendment approval by merge.
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
- [x] Resume and explicit history transfer - Decision: the directing user approved same-parent in-place resume plus explicit cross-parent history transfer as a new child in this amendment. Resume preserves the child ID and requires the original parent ID/incarnation, current authorization, carried-authority containment, and an attenuated binding. History transfer leaves the saved child unchanged and delegates new work under the receiving parent's current context with explicit authority bounds. It is never an automatic fallback. Ship both operations together in #1875; original-source cross-parent reconstruction is out of scope pending demonstrated need. The directing user's subsequent authorization settles the following choices.
- [x] Cold same-parent recovery - Decision: support new default, named-specialist, and fork-created children after restart with a trusted durable selection recipe, existing profile/provider labels, current authorized configuration, and persisted capability bounds. Compacted parent messages and inherited `DefinitionIdentity` labels are not reconstruction authority. Missing/revoked definitions or narrower current parent/eligible managed authority refuse resume without default fallback; eligible saved history remains available for explicit new delegation. Legacy absence is not fabricated. The additive private recipe below is authorized; it is not a durable context reference or frozen prompt snapshot.
- [x] History-transfer authority - Decision: derive new-child authority solely from the receiving parent's current authorized delegation, current specialist, and explicit tightenings. Saved capabilities neither grant nor veto new work. An authorized read-write receiver can explicitly import read-only research history into read-write work. Missing historical authority alone does not prohibit authorized data transfer. Same-ID resume still preserves and checks its saved capability set.
- [x] Transfer content and aggregate bound - Decision: initially support bounded text/structured-text projection with Parts precedence and inert resource links; explicitly reject media and oversize content. The final UTF-8 encoded/fenced seed including notices is limited to 256 KiB, with bounded preflight/projection and separate runtime context admission. No silent truncation, summarization, fetch, or new configuration framework is introduced.
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
  harness-source provider, `session.HarnessContextRef`, or exported engine five-source container.
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
  **Durable Subagent selection API:** add these value types and aggregate methods
  in `engine/session`; the session owns an optional private recipe, not an exported
  mutable pointer:

  ```go
  type SubagentSelection string
  const (
      SubagentSelectionDefault SubagentSelection = "default"
      SubagentSelectionNamed   SubagentSelection = "named"
  )
  type SubagentRecipe struct {
      Version          uint32            `json:"version"`
      Selection        SubagentSelection `json:"selection"`
      AgentName        string            `json:"agent_name,omitempty"`
      ManagedAuthority bool              `json:"managed_authority"`
  }
  func (s *Session) BindSubagentRecipe(SubagentRecipe) error
  func (s *Session) SubagentRecipe() (SubagentRecipe, bool)
  ```

  Version is exactly 1. `named` requires the validated exact logical agent name;
  `default` requires empty `AgentName` and false `ManagedAuthority`. Composition
  stamps `ManagedAuthority` from the selected definition's eligible managed tier
  (`AgentOriginExplicit` / `AgentMeta.Managed`), never from its name or body. It can
  be true only for `named`; the version-1 JSON record requires this boolean even
  when false, so absent classification is not silently adopted. Inherited
  `Authority.DefinitionIdentity` is insufficient evidence: a nonmanaged child can
  inherit its parent's explicit-definition label. Unknown versions/selections,
  noncanonical names, invalid managed classification, and recipes on non-Subagent
  sessions fail validation.
  `BindSubagentRecipe` is write-once while idle before first run/publication;
  restoring an identical value is idempotent, changing it is an error. The getter
  returns a value copy and false for legacy absence. Restoration validates through
  the same method. The constructor/restore path also requires bound authority and
  valid existing session profile/provider labels for a recipe-bearing child.

  `default` selects the normal explorer factory; `named` selects the current exact
  named definition from the original parent's admitted registry. The current Subagent
  `fork: true` path seeds parent history but uses normal no-agent engine selection;
  it records `default`, preserving explicit versus floating provider/model selectors
  in the existing labels.
  No history flag or third engine identity is needed. Composition stamps the
  actual selected path after argument validation and engine selection, including
  model-routed/writable variants, before `BindChildContext`, persistence, or execution.
  `history_from` stamps a fresh recipe from its receiving selection, never copies one.

  Reuse `Session.Profile` for the effective profile and `ProviderID`, `ModelID`, and
  `ReasoningEffort` for their existing selection semantics, not unconditional resolved
  model identity. To carry the selected factory's labels into
  the core creation path, add `agent.SubagentSessionLabels` and
  `agent.Deps.SubagentSessionLabels`:

  ```go
  type SubagentSessionLabels struct {
      Profile         string
      ProviderID      string
      ModelID         string
      ReasoningEffort string
  }
  ```

  Every built-in default/named/model-routed/writable factory supplies these labels
  for its actual selection path. The creator copies them into existing write-once
  fields before recipe binding/publication. Preserve explicitly selected selectors,
  including an accepted router selection, but keep floating defaults empty even when
  the factory knows the resolved model. A named definition's configured default is
  not thereby converted into a per-child explicit override. On cold resume, saved
  explicit selectors win over changed definition/deployment defaults subject to current
  authorization; empty selectors resolve those current authorized defaults. Empty
  reasoning effort remains unset/current default; explicit effort follows existing
  normalization and provider rules. Rebuild model-dependent dependencies through
  factories; neither standalone nor built-in paths may accidentally pin a floating
  default or erase an explicit selector. Historical content supplies no selector.
  `Authority.CapabilitySet` remains the durable execution ceiling and `Limits` the
  existing budget record. Do not duplicate them in the recipe or parse the inherited
  `Authority.DefinitionIdentity` label to infer selection. No prompts, skills/bodies,
  source roots, context references, or credentials enter this recipe.

  Cold recovery also adds `agent.SubagentResumeEngineFactory`:

  ```go
  type SubagentResumeEngineFactory func(
      ctx context.Context, parent, savedChild *session.Session, writable bool,
  ) (*Engine, func(), error)
  ```

  `agent.Deps.RestoreSubagentEngine` and
  `WithSubagentResumeEngineFactory(SubagentResumeEngineFactory)` inject it. Parent
  and saved-child arguments are authoritative read-only metadata under the access
  hold. The callback is bound to that parent's current HarnessContext binding;
  it rebuilds the recipe's engine from current composition without borrowing another
  context generation. `writable` is this call's validated Subagent execution mode
  (`mode: read-write`), not saved `DirectWrite` and not `Session.PermissionMode`.
  Validate parent and eligible managed-definition containment of the full saved set
  before this call's read-only view is applied. A saved writable child can therefore
  resume read-only without failing merely because that view omits direct-write tools.
  The factory then applies the requested mode to the selected named/default engine's
  catalog, runner, workspace access, and effective inventory. Its returned engine is
  authoritative: no later generic-writable-engine swap is permitted. Stored capabilities
  and cumulative usage remain unchanged. The idempotent cleanup owns only newly built
  engine resources; failures clean partial resources.
  Nil refuses durable resume rather than selecting the default engine. Normal
  `BindChildContext` then associates this actual selected engine with the resumed
  child and owns its source/inventory lease. No recipe is accepted from tool arguments.

  **Exclusive session access API:** add the following in `engine/port`, separate
  from refcounted `SessionLiveness` and from the backend `SessionLease`:

  ```go
  type SessionAccess interface {
      Acquire(context.Context, session.SessionID) (SessionAccessHold, error)
  }
  type SessionAccessHold interface {
      Context() context.Context
      Check(context.Context) error
      Close() error
  }
  ```

  Add sentinels `ErrSessionAccessBusy`, `ErrSessionAccessUnsupported`, and
  `ErrSessionAccessLost`. Acquire is exclusive, non-reentrant, and fail-fast on
  contention; cancellation/deadline errors are preserved. It accepts only the trusted
  target session ID, not a public storage/owner selector. A hold is scoped to one
  store authority and ID, including IDs not yet published. Acquire observes its caller
  context until acquisition completes. `Context()` then supplies hold-owned persistence
  ownership with unforgeable in-process identity; caller work cancellation alone does
  not cancel that context or its renewal. Work/capture contexts must observe both
  their caller cancellation and hold loss. `Check(ctx)` respects the supplied operation
  context: cancellation/deadline returns that error without invalidating an otherwise
  valid hold. It separately verifies exact current ownership and backend fencing;
  lease loss or invalidation cancels ownership and forbids writes. Close cancels the
  hold context and stops new IO, retaining backend exclusion through the drain below.
  Renewal/verification cannot accept a replacement token or resurrect lost authority.

  Ordinary run cancellation stops work but retains the exclusive hold/renewal through
  the existing bounded terminal-save and cleanup window. That cleanup derives a fresh
  bounded context from `hold.Context()`, not an uncancelled copy of lost authority.
  A capture cancelled before completion is discarded instead of persisted. `Close`
  first stops new ownership-authorized IO, joins admitted persistence and renewers,
  then releases only its own lease borrow and exclusion. It is idempotent, reports
  cleanup errors, and never closes a session, engine, or workspace. Do not release
  exclusion beneath admitted IO still able to commit, even if its caller timed out.
  No hold survives restart.

  `agent.Deps.SessionAccess` and `server.Config.SessionAccess` receive the same
  `port.SessionAccess`; add `WithSubagentSessionAccess(port.SessionAccess)` for
  standalone Subagent tool construction. `internal/app.Config.SessionAccess` permits
  a trusted injected coordinator for a supplied store; otherwise Build constructs it
  from that store's established local/shared placement and lease configuration.
  All child factories receive it, not a new per-tool mutex. Nil means history transfer
  and durable Subagent resume/start are unsupported in standalone embeddings; ordinary
  nonpersisted new-child embedding remains available. Built-in Build always supplies
  a supported coordinator for its default memory and local-directory stores.

- **Tool schemas:** Add optional `history_from` to `Subagent`, a string saved Subagent
  session ID (the existing `agentId` result value). Omission means no history transfer;
  a supplied empty/whitespace-only value is invalid. Require the existing nonempty
  `prompt` as the new task. `history_from` is mutually exclusive with a nonempty
  `resume` and with `fork: true`; reject conflicts before loading history or allocating
  resources. `fork: false` remains the ordinary default. No source, root, environment,
  or placement selector is added.

  ```json
  {"history_from": "<SAVED_CHILD_ID>", "prompt": "<NEW_TASK>", "mode": "read-only"}
  ```

  History transfer uses ordinary new-delegation `agent`, `model`, `authority`, limits,
  and output options under ordinary receiving-parent delegation authority. Agent lookup is in the
  receiving parent's current admitted registry; omission selects the normal default,
  never the historical specialist label. Preserve existing mode combinations:
  `read-write` rejects `background: true` and `agent` plus `model` together; otherwise
  current deployment/factory availability still governs. Ordinary `resume` continues
  to reject `agent` and `model` overrides and consumes no extra delegation hop.
  A refusal returns an error ToolResult, not a silently fresh child. Missing/foreign
  history IDs have indistinguishable errors; owner-visible eligibility errors can
  explain inspection or explicit history transfer without promising either will pass.

  Update Subagent schema descriptions, injected delegation instructions, recovery
  hints, and no-FS variants together. The parent model must see the distinction between
  same-ID resume, `fork` of its own conversation, and new-ID `history_from`. The child
  receives a harness-authored notice identifying the new child and receiving parent,
  current context, and destination mode (receiving workspace or fresh isolated fork),
  with a requirement to re-read files. Historical labels/paths remain fenced data;
  the notice does not expose a new source selector or claim that old files survived.
  Factory-level model-request tests prove both parent and child instructions arrive.
  `Skill` retains logical bundles and named assets. Execution Read/Write/Shell use
  their existing `Environment` capabilities; harness reads create no read-ledger evidence.
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
- **Events / persistence:** Add one optional private creation record:
  `sessnap.Snapshot.SubagentRecipe *session.SubagentRecipe` with JSON key
  `subagent_recipe,omitempty`; its object uses `version`, `selection`, and
  `agent_name,omitempty`, and required `managed_authority`. Add the same optional field to
  `eventsource.SessionMeta`. Extend snapshot encoding/restoration and event-source
  creation-metadata persistence atomically across all stores and conformance suites.
  The private driver snapshot envelope already carries opaque sessnap JSON, so no
  public session RPC field, new event kind, or driver-envelope protobuf field is
  needed. Event-sourced hosts must retain this creation record independently of
  compactable conversation events; omission restores legacy absence, never a guess.
  Unknown/malformed recipe data fails snapshot validation, not a default-engine fallback.

  Same-session save/load, compaction, and same-ID resume retain the recipe. Main
  Clear/Fork/carryover successors do not copy it; normal new sessions of other kinds
  have none. New Subagent children, including fork/history-transfer children, stamp
  their own selected recipe. It is excluded from public metadata inventories, model
  prompts, diagnostics/audit body projections, and client-editable input. Selection
  labels are private reconstruction metadata, not new authorization claims. Legacy
  children without a recipe remain inspectable and eligible for authorized history
  transfer; cold in-place resume refuses with that explicit recovery option, not an
  automatic migration from parent transcripts or inherited identity labels. A live
  binding may resume only if trusted in-memory creation metadata already supplies the
  same validated recipe; it must not fabricate it from model history.

  This is the authorized amendment to the prior no-new-persistence-field clause.
  No durable `HarnessContextRef`, frozen external snapshot, or durable guard is added.
  Commands remain live per List/Expand;
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

  **In-place resume:** a persisted `SessionKindSubagent` resumes with the same child
  ID only under its original `Relationship.ParentSessionID` and nonempty, matching
  `ParentIncarnation`. Owner-authorization, lineage, and persisted-authority checks
  precede new binding, recovery, rehoming, workspace allocation, or publication.
  The current parent must contain the saved child's complete bound capability set;
  retain that set, including its remaining depth, rather than deriving another hop.
  Requested direct-write mode additionally requires its saved direct-write grant and
  current policy permission. Current source authorization and reconstructable child
  attenuation are required before running; the old child execution ref is not a
  harness source anchor. Exact source-reference mismatch on binding reuse fails
  closed, not as an implicit reload. Resume keeps cumulative usage and applies existing
  tighten-only limits. Its normal terminal-history repair and file-survival rules
  remain separate from the current call's destination mode.

  Cold recovery uses the child's trusted durable recipe and existing labels, never
  the original parent's compactable messages or the inherited `DefinitionIdentity`.
  Rebuild default/named selections through current factories against the
  original parent's current authorized binding. Reapply the stored effective profile
  and saved capability set; derive provider/model dependencies through the normal
  factory using existing stored selectors. Resume still rejects new `agent`/`model`
  arguments. Normalize provider-private replay state under existing provider-change
  rules if current resolution changes provider. Fork-created children use their
  recorded default selection; copied history is not a reason for refusal.

  Current named definitions may legitimately change prompt, skills, and configuration:
  use the current admitted definition, not persisted bodies. The current parent must
  contain the full saved capability set. Apply definition-capability containment only
  for a currently eligible explicit/operator-managed authority definition; project,
  user, and driver definitions scope the engine but establish no authority ceiling.
  Never synthesize a ceiling from an ordinary specialist's smaller catalog: unchanged
  nonmanaged definitions can resume with a broader parent-derived saved capability set
  while their current catalog still limits which tools exist.

  A recipe with `ManagedAuthority: true` requires a currently eligible managed
  definition of that exact name. A same-name lower-tier replacement refuses before
  recovery; name equality or an inherited `explicit:` label does not preserve managed
  provenance. If an originally nonmanaged name is now managed, its eligible current
  ceiling must also contain the saved set. In every case current prompt/profile,
  preloaded skills, disallowed tools, and catalog restrictions are independently
  enforced; ordinary nonmanaged-to-nonmanaged content changes follow current policy.

  Check containment against current authorized configuration before requested-mode
  view attenuation. Where the existing managed ceiling has posture variants, use the
  variant for the saved set's carried direct-write posture for this check, not this
  call's read-only presentation. Then enforce this call's mode; a read-write request
  still needs the saved direct-write grant and current permission. Keep saved rights
  unchanged: current expansion cannot broaden them. A narrowed eligible managed
  ceiling, missing/revoked definition, unsupported saved profile/selector, or revoked
  source refuses with actionable explicit history transfer where eligible. No default
  fallback or later generic writable engine substitution is allowed. Inventories and
  consumption use that same requested-mode specialist engine.

  Missing original parent/incarnation or revoked source authority fails closed
  without deleting transcripts. Missing legacy recipe prevents cold in-place resume,
  but not otherwise authorized data transfer. Legacy children lacking the
  original parent incarnation are never retroactively adopted for in-place resume.
  They remain inspectable under existing authorization and can supply history if
  the separate eligibility checks below pass. Unparented Team sessions use their own
  explicitly composed context; no relationship is invented. Original-source
  cross-parent reconstruction is outside this amendment.

  **Explicit history transfer:** `history_from` reads an owned saved Subagent and
  creates a new Subagent under the receiving parent's current binding and execution
  policy. The source and receiving parents can be different sessions or repositories
  of the same owner. The operation never requires the source parent to exist, its
  execution backend to be available, or its former context grants to remain valid.
  Reading the saved transcript still requires current transcript ownership/access
  authorization, and all receiving-parent grants must be valid. A revoked context
  grant is not a revocation of transcript access; a revoked transcript grant refuses
  transfer. Do not read fresh data from the old source to enrich the history.

  Eligible sources have trusted stored `SessionKindSubagent` metadata, validated
  ownership, and a terminal state of completed, cancelled, or failed. Reject
  unknown/main/scheduled/Parallel/Team/debug kinds and idle or active states; an ID
  prefix or historical text proves none of them. Absence of a saved recipe or bound
  capability set does not by itself disqualify a trusted owned Subagent transcript:
  neither grants nor vetoes new delegation rights. Malformed stored records still fail
  their ordinary validation; no new capability grant is synthesized during loading.
  A missing legacy parent incarnation alone does not disqualify transfer. This does
  not make unknown-kind or idle legacy sessions eligible, so it is not a claim that
  every old recovery workflow is replaced. Preserve ownerless local/system behavior
  only through its existing trusted ownership convention, never as a missing-owner
  bypass in authenticated deployments.

  **Capture/start coordination:** use the `SessionAccess` contract above for the
  complete durable Subagent lifecycle, not just for history reads. A new start acquires
  the reserved child ID before first Create; a resume acquires before authoritative
  load/recovery. Retain its hold through final snapshot/event writes, background
  execution, cancellation drain, and child cleanup. The context supplied by the hold
  is used for that lifecycle's storage operations; detached terminal-save cleanup
  retains the exact hold identity but never resurrects a lost/closed hold. Captures,
  retention/delete, session-ID replacement, and other snapshot writers use the same
  coordinator. Non-Subagent producers sharing these IDs/store must not bypass it.
  An active source therefore refuses capture even when its last stored snapshot is
  terminal. The coordinator replaces Subagent's per-instance in-flight exclusion
  for durable paths; `SessionLiveness` remains retention/liveness accounting, not
  another exclusive gate.

  Build owns one local non-reentrant gate table for its store and injects it into all
  engines and service mutation paths. Acquisition order is local gate, backend-wide
  exclusion/fence, then lease-domain borrow/grant. One exact lease owner/token per
  Build/session is shared with service/liveness management; do not independently
  reacquire and release a refcounted user's lease. Each Build has a distinct lease-owner
  nonce. In-memory stores are private to their owning Build by default; Builds sharing
  an in-memory instance receive the same injected coordinator so its gate spans the
  entire mutation/capture critical section. No process-global registry is introduced.

  **Local commit/takeover fence:** the automatic `StoreDir` coordinator pairs the
  existing flock lease domain with a stable cross-process per-session OS access lock,
  distinct from the lease adapter's short transition lock and generation liveness lock.
  Hold this access lock for the entire SessionAccess lifetime, including every admitted
  snapshot/creation-metadata/event write and IO drain. Every writer, lease takeover
  (including TTL expiry), delete, and ID reuse in that domain must first take this
  same lock. A new generation cannot take over while an old holder still has admitted
  IO capable of committing. Use stable lock-file identity: normal release, GC, and
  session deletion never unlink or replace its inode; offline removal requires the
  whole store domain to be quiescent. Process exit releases the OS hold, not a new
  process interpreting TTL expiry while the old process remains alive. The existing
  TTL flock lease alone is NOT this guarantee. The paired local implementation is
  required in #1875, uses the existing configured/automatic lease directory, and
  keeps ordinary StoreDir deployments usable without a new flag.

  **Expiring remote leases:** qualify a coordinator/store pair only when the backend
  atomically validates the current session owner/epoch/token and lease validity in
  the SAME transaction/script as each mutation commit, including snapshot Create/Save,
  creation metadata, event append, deletion, and replacement. Takeover advances the
  authoritative epoch atomically in that same fencing domain. A stale write admitted
  earlier must be rejected at commit after takeover, even if its context check passed.
  A token query followed by an unfenced write is insufficient. Capture/reload returns
  one atomic snapshot under the current fence and validates owner/incarnation; separate
  field reads or independently fenced metadata that can form a mixed revision do not
  qualify. Split store/event/lease backends must provide the same atomic guarantee;
  mere connectivity to a lease service is not evidence of it.

  Paired adapters can carry fence identity privately in the hold context and enforce
  it at their storage boundary; add no public token, tool argument, recipe field, or
  generic engine fencing framework. A shared pair lacking atomic commit fencing or
  nontransferable exclusion through in-flight IO reports `ErrSessionAccessUnsupported`
  for durable child start/resume/capture. Existing private drivers without a fenced
  mutation protocol do not qualify merely by implementing `SessionLease`; an integration
  must supply and prove that protocol before advertising support. There is no legacy
  unsupported-lease fallback on this path. Fresh nonpersisted embedding is separate.

  Local exact-hold context checks and the existing mutation-capability drain plumbing
  still enforce admission, but are not substitutes for the backend commit fence.
  The selected pair enforces Create/Save/Delete and creation/event writes; an ID-only
  enabled bit cannot authorize them. Close never releases exclusion under admitted
  IO still able to commit. Old writes can neither overwrite nor recreate successor
  state. Pair qualification includes a real paused-write/takeover proof in addition
  to API conformance; raw uncoordinated or older mutation paths cannot share the domain.

  For transfer, a preliminary authorized Load establishes source owner/incarnation;
  it does not authorize capture. Then Acquire fail-fast, reload under the hold context,
  reauthorize owner/kind/state, and compare incarnation and owner to the preliminary
  observation. Replacement refuses the attempt; foreign and absent remain concealed.
  Check the hold before and after bounded projection. Only a successful final Check
  establishes an immutable authorized capture; Close releases its guards/lease borrow
  before destination allocation/run. Cleanup failure refuses publication and is
  diagnosed without leaking source content. Caller cancellation before destination
  publication also discards the capture. Lease loss before capture completion cancels
  and discards it; later source activity cannot alter a completed immutable capture.
  No `IsLive`-then-`Load` test substitutes for exclusion, and Acquire's same-owner lease
  success cannot substitute for the local gate. No source Recover/Rehome/Save/close or
  original backend acquisition occurs.

  Built.Close first stops new admission and cancels work/captures, then drains
  still-authorized bounded terminal persistence while retaining ownership/renewals.
  Only afterward does it Close holds and release their backend exclusion. Actual
  lease loss/invalidation instead cancels ownership immediately and forbids detached
  terminal writes; `WithoutCancel` cannot restore authority. An injected shared
  coordinator remains caller-owned: shutdown drains only this Build's holds.
  In-memory coordinator state starts empty after restart. The implementation updates
  ADR 0027 Lists 1/2 for local gate tables, stable OS-lock handles/files, lease borrows,
  backend epoch records, and renewers: Build/per-hold ownership, join-before-release
  cleanup, stable local lock-file names retained across restart, and existing durable
  backend epochs retained for fencing. Do not remove a stable lock inode during
  session GC. Two receiving parents can create distinct children from completed
  immutable captures; ordinary new-delegation duplicate handling is unchanged.

  Derive ordinary receiving-parent delegation authority with any current eligible
  managed-definition ceiling and explicit tightenings, then consume exactly one delegation hop. An
  explicit `remaining_delegation_depth` is PRE-HOP. Receiver depth 3 with explicit
  depth 1 yields a child at depth 0, regardless of the source child's stored depth.
  Invalid receiving requests fail before resource allocation. There is no intersection
  with source capabilities: saved history grants no rights and imposes no execution
  veto. Read-only research can seed explicitly requested read-write implementation
  when receiving authority, current definition, profile, and permission policy allow it.
  Missing old bound authority is not a new grant; the receiver must independently
  authorize all new work. Same-ID resume still retains its saved set and uses containment
  checks without another hop.

  Source labels, permissions, approvals, credentials, and context access are not
  transferred. The new bound authority has normal delegation provenance and current
  definition identity. The selected engine, tools, and inventory apply that authority
  and current receiving specialist restrictions before child binding. An incompatible
  requested mode is refused, not silently changed.

  The new child has its own ID/incarnation and ordinary relationship to the receiving
  parent/current call. Its history-only seed carries no usage totals, counters, limits,
  pending approvals, read ledger, environment binding, or context snapshot from the
  saved session. New work receives the receiving engine/operator limits plus ordinary
  per-call tightenings. Old spend remains recorded on the source and is neither
  erased nor charged twice. The destination session starts with fresh usage; actual
  provider input (including imported history) and new output count against its limits.
  The receiving parent's own ordinary limits and per-call admission still apply.
  Ordinary Subagent child usage is reported at `subagent.end`; it is not globally
  folded into the parent main session's `MaxRunTokens`. This amendment introduces no
  descendant aggregate budget, reservation, or cross-child charging promise. Existing
  Team/Parallel aggregate rules remain where those paths already enforce them.
  This is new delegation with a new-work budget, not a reset or extension of the saved
  child's exhausted budget. Rights ceilings are distinct from cumulative spending.

  Validate the original call/result structure before fencing. Interior unmatched or
  duplicate results/calls are errors; only unanswered calls in the final assistant
  batch may be omitted from a copy-local projection. Retain completed pairs and their
  associated assistant text. In a final batch containing completed call A and unanswered
  call B, preserve A, its result, and assistant text, omit only B, and add an honest
  omission notice. Never fabricate successful results or repair/persist the source.
  `session.ForkSnapshot` currently drops the entire trailing assistant batch, so it
  is not a mandatory helper for this projection. Reuse existing copy/validation helpers
  where they satisfy this contract, including `session.StripProviderState` even for
  same-provider transfer. The projected transcript must pass pairing validation.

  **Bounded text projection:** support message text and structured call/result
  records only. For a tool result, nonempty `ToolResult.Parts` wins over legacy
  `Content`; preserve part order without appending or falling back to Content.
  Supported `session.Content` kinds are `BlockText`, `BlockStructuredContent`
  (validated JSON text), text-only `BlockEmbeddedResource`, and `BlockResourceLink`.
  Resource-link URI/name/title/description/MIME/size and advisory annotations are
  inert textual data, never typed provider-fetchable blocks or authorization.
  Reject `Message.Parts` media, `BlockImage`, `BlockAudio`, binary embedded resources,
  empty/unknown block kinds (including any future video kind), invalid UTF-8/JSON,
  and unsupported content with a tool error. No silent content drop or asset fetch.

  The data representation is a JSON array of records in original order, each with
  `role`, `text`, optional `tool_calls` (`id`, `name`, `args` as a JSON value), and
  optional `tool_result` (`call_id`, `is_error`, `parts`). Each result part records
  its supported `kind` and text/JSON/inert link metadata; legacy Content becomes
  one text part only when Parts is empty. JSON is encoded, never interpolated.
  Strip provider-private state before projecting and neutralize framing in string
  values with the canonical governance helper before JSON encoding; wrap the
  encoded array with `FenceUntrusted`. Seed it as one user data message via
  `SeedHistory`, with a harness-authored transfer/provenance/omission notice outside
  the historical fence. Roles inside the JSON are labels, not new live messages.
  The new task is supplied separately. No assembled system prompt or live context
  object is copied. Both the projected logical transcript and seeded conversation
  pass pairing validation.

  **Fixed bounds:** final UTF-8 encoded/fenced seed including its notices/framing
  is at most 256 KiB (262144 bytes), checked before destination publication/provider
  use. Before cloning messages, parsing JSON, stripping provider state, or constructing
  the encoded seed, preflight the selected data fields: at most 4096 total message,
  tool-call, and result-part records and at most 1 MiB of raw included string/JSON
  bytes (including IDs, names, and link metadata). Check field lengths and record
  counts before scanning or copying a giant first field. Excluded provider-private
  replay blobs are not copied/scanned into the projection. Count output incrementally
  with a bounded writer; never allocate an unbounded intermediate JSON/fenced body.
  These bounds govern transfer processing after the ordinary store load; they do
  not claim a new cap on `SessionStore.Load` or replace adapter snapshot/transport
  admission. No extra whole-history clone or unbounded model-context allocation is
  allowed before preflight. Per-result validation and runtime model-context admission remain separate and
  include the new task. Exceeding any limit refuses the transfer, not a truncate,
  summarize, raw-history fallback, or new configuration option. Historical tools and
  approvals never execute; new operations require current permissions and fresh
  read-before-edit evidence.

  `mode: read-write` runs serially in the receiving workspace under the direct-write
  barrier; isolated mode uses a new receiving-side fork where supported. No-FS and
  no-fork deployments retain their ordinary capability constraints. Neither mode
  attaches or copies the saved child's files. Tell the child to re-read current files;
  never claim that old isolated work survived or that changing mode recovers it.
  Neither the source child nor its parent is mutated, recovered, reattached, closed,
  or re-persisted, including on copy, creation, cancellation, or publication failure.
  Only destination-attempt resources are cleaned up. Register its actual attenuated
  inventory through `BindChildContext` and the exact-generation lifetime fence above.

  Add no history-transfer persistence field, source selector, or durable context
  reference beyond the separate private Subagent creation recipe specified above. The normal
  parent tool call records `history_from`, the normal result identifies the new child,
  and its seeded conversation records a harness-authored source-history notice with
  the source child ID (never its internal incarnation). These are provenance, never
  authority or a lineage override; store them through existing conversation/event
  paths. The saved child's original relationship and transcript remain unchanged.

  Clear/Fork main-session successors and each scheduled fire are new context
  consumers: when current policy explicitly selects their execution files, bind
  their server-authorized placement. Schedule origin metadata routes results and
  does not silently select context. On restart, existing sessions and schedules
  reauthorize under current policy; rebinding cannot preserve a revoked grant.
- **Compatibility / migration:** The workspace-free signatures are an intentional exported-engine
  break. The shared implementation PR updates `engine/api/*.txt` with `task api:update` and records
  the break in `engine/CHANGELOG.md` under `engine/COMPATIBILITY.md`; callers update atomically, with
  no compatibility-adapter window. The recipe methods, resume factory, and exclusive
  access ports also require API snapshots and classified changelog entries, including
  the strengthened SessionStore reconstruction contract and standalone nil-seam behavior.
  Existing persisted sessions and schedules require no destructive
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
  Original-parent resume enforcement is a behavioral break from the tested same-owner
  cross-parent workflow. Ship `history_from` in the same shared implementation, with
  explicit new-child instructions and updated recovery hints; do not remove that
  workflow first and defer its replacement. Preserve saved sessions and inspectability.
  Rename the implementation's `TestADR_0357_HarnessContext_*` proofs to
  `TestADR_0359_HarnessContext_*`; leave model-stream ADR 0357 proofs unchanged.

## In scope - 9 scenarios, in implementation order

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
execution namespace from its parent. In-place resume preserves that lineage;
Scenario 9 supplies explicit new-child history transfer when continuing under a
different parent or when the saved child's effective binding cannot be restored.

**Acceptance:**
- AC8.1: Isolated Subagent, direct-write Subagent, Parallel branch, and parented Team member execution preserve the parent's admitted bound source while enforcing each child's restrictions. Change the underlying skill/agent/rule source after parent binding but before child creation: the child retains the parent's external snapshot and consumes those same winning bodies without rebinding. Live commands/per-Run instructions still observe the inherited source. Conflicting child-worktree files do not replace it.
  - verify: `TestADR_0359_HarnessContext_Scenario8_ChildSourceInheritance`
- AC8.2: A child independently holds the inherited generation and exposes its exact attenuated inventory until its context use ends. After parent context and execution-owner teardown, a fresh child command read succeeds against a reference backend that becomes unusable on final close. Final release closes it exactly once; failed child publication releases its attempt. Delayed parent teardown and stale child close/release callbacks cannot retire or close a newly activated parent generation or unregister a replacement child binding; the replacements remain usable with their exact inventories.
  - verify: `TestADR_0359_HarnessContext_Scenario8_ChildSourceLifetime`
- AC8.3: Default, named-specialist, and fork-created children with recipes resume after restart despite compacted parent messages. Cold-resume a saved writable named specialist in both requested modes: check saved capability containment before mode attenuation, retain capabilities/usage, and prove RO versus RW engine/catalog/runner/inventory behavior without a generic-engine swap. Current prompt/skill changes apply. Missing/revoked definitions, narrower eligible managed authority, unsupported stored profiles, parent/incarnation mismatch, and revoked source refuse without fallback; legacy absence remains inspectable with explicit history transfer where eligible.
  - verify: `TestADR_0359_HarnessContext_Scenario8_ResumeSourceAnchor`
- AC8.4: Reconstructing unavailable inherited context does not select the child's files, a global catalog, or an unrelated source. An independent-source/no-FS child control retains admitted context without execution tools or unrelated attachment.
  - verify: `TestADR_0359_HarnessContext_Scenario8_NoInheritedAuthorityFallback`

### Scenario 9 - Explicit history transfer creates a receiving-parent child

The recovery requirement from [ADR 0200](../adr/0200-resume-a-failed-subagent.md)
keeps valuable saved work available without retargeting its original child.
[Derived authority](../adr/0234-authority-evaluator-port.md) and
[incarnation-bound lineage](../adr/0258-cryptographic-session-incarnations.md)
constrain the two distinct operations. Use the real Subagent factory and server
binding paths, not an independent transcript-copy implementation in the tests.

**Acceptance:**
- AC9.1: Replace the existing same-owner cross-parent resume journey with an explicit `history_from` call: cross-parent `resume` fails without mutation, then a separately requested transfer creates a new ID/incarnation and receiving-parent relationship with the saved history and new prompt. The source ID, owner, relationship, authority, usage, transcript, and persisted revision remain unchanged. Both operations ship together in #1875.
  - verify: `TestADR_0359_HarnessContext_Scenario9_CrossParentRecovery`
- AC9.2: Tool schema and execution reject blank history IDs, missing prompts, history/resume and history/fork conflicts, unsupported deployment paths, read-write/background, and read-write/agent/model combinations before allocation. Current normal agent/model selection remains available for new transfers; resume rejects their overrides. Production factory model requests expose the distinct operations, conditional recovery hints, and the child's receiving-context/destination notice, including no-FS variants.
  - verify: `TestADR_0359_HarnessContext_Scenario9_ToolAffordance`
- AC9.3: Owned completed, cancelled, and failed trusted Subagent records can seed new children without a saved capability set or resume recipe. Foreign and missing IDs are indistinguishable; unknown/wrong kinds, idle states, active sources, and malformed records fail before destination allocation/disclosure. Missing old parent incarnation or deleted parent prevents in-place resume, not otherwise eligible data transfer. Unavailable/revoked original context triggers zero source-backend acquisitions; revoked transcript access or receiving authorization refuses transfer. Missing historical authority is neither a grant nor a veto.
  - verify: `TestADR_0359_HarnessContext_Scenario9_HistoryEligibility`
- AC9.4: Same-owner parents in different repositories have conflicting same-name agents/skills/instructions. Transfer uses only the receiving binding and current explicit agent selection; omission selects normal new delegation, not the saved specialist label. Inventories and consumed definitions match the actual new engine. Source recipe, profile, limits, and grants are not copied; the destination stamps its own recipe and labels. A current receiving specialist can differ from the source's same-name definition without restoring source rights.
  - verify: `TestADR_0359_HarnessContext_Scenario9_ReceivingSpecialistContext`
- AC9.5: Read-only investigation history can seed explicitly requested read-write work under an independently authorized receiver. Negative controls with receiving direct-write denied, narrower specialist, no-FS, or invalid tightenings fail even when the source held broader grants. Empty/missing source capabilities do not change the result. Transfer consumes one receiving hop: receiver depth 3 / explicit pre-hop depth 1 produces final depth 0 regardless of source depth. Exhausted receiving depth fails. Same-ID resume still requires parent containment of saved authority and consumes no extra hop.
  - verify: `TestADR_0359_HarnessContext_Scenario9_AuthorityIntersection`
- AC9.6: A source that exhausted its cumulative budget remains exhausted and unchanged. The destination starts fresh usage and current operator/per-call limits; imported-history input and new output count against destination limits without copying or double-charging old usage. Receiving-parent own limits and per-call admission remain ordinary; reporting at `subagent.end` does not newly fold child usage into parent main `MaxRunTokens`. Assert no new descendant aggregate/reservation accounting, preserve existing Team/Parallel rules where applicable, and never describe new-child accounting as resetting the resumed source's budget.
  - verify: `TestADR_0359_HarnessContext_Scenario9_NewWorkAccounting`
- AC9.7: For isolated-to-isolated, isolated-to-direct-write, direct-write-to-isolated, and direct-write-to-direct-write transfers, destination placement always follows receiving policy and the requested authorized mode. Direct-write requires current receiving authorization, independent of saved source capabilities. No original files/backend are attached or copied, no old isolated-file survival is claimed, direct-write obeys mutate-serial dispatch, and fresh reads are required before edits. A no-FS control imports history without gaining filesystem tools.
  - verify: `TestADR_0359_HarnessContext_Scenario9_DestinationAndReadEvidence`
- AC9.8: Strip provider-private replay/reasoning for same- and cross-provider copies. A mixed final batch retains completed A/result and assistant text, omits unanswered B with notice, and leaves the source unchanged; interior pairing corruption fails. Parts beats Content without fallback/duplication; supported structured/text resources survive as valid fenced JSON, links stay inert, and media/binary/unknown kinds fail. Canonical framing blocks role/marker injection; historical calls/approvals never execute. Individually valid messages that collectively exceed the 256 KiB encoded/fenced seed limit fail, including notice/escaping overhead; exact-boundary succeeds subject to model admission. Preflight rejects a giant first field or excessive records before cloning/parsing; no unbounded intermediate, old asset fetch, silent truncation, or internal-incarnation exposure occurs.
  - verify: `TestADR_0359_HarnessContext_Scenario9_HistoryIsData`
- AC9.9: Real composition shares SessionAccess across child engines and mutations. Two Builds over StoreDir use paired stable OS exclusion plus the lease domain; shared memory uses one coordinator. Pause an old write after admission/before commit, expire its lease, and attempt takeover/delete/ID reuse: locally the successor cannot take over until old IO completes and exclusion releases; on a qualified remote pair, takeover causes old atomic commits to fail. Stale writes cannot overwrite or recreate successor state; test snapshot, creation metadata, event, and deletion boundaries, stable-inode retention, and an unfenced-driver Unsupported control. Active source with a terminal snapshot, same-owner lease success without exclusion, and guarded owner/incarnation replacement refuse capture. Caller timeout/cancel stops work but permits bounded cancelled-state persistence under the retained hold, followed by cold resume; a cancelled Check context alone leaves ownership valid. Contrast actual lease loss: detached cleanup cannot save, even via WithoutCancel. Shutdown drains authorized persistence before Close. Captures discard on cancellation, release guards before destination run, and never mutate/reattach/close source resources; failure cleanup affects only owned resources.
  - verify: `TestADR_0359_HarnessContext_Scenario9_AtomicCopyAndIsolation`
- AC9.10: Recipe stamping precedes publication for default/named/fork/history-transfer children. Round-trip version/selection/name/managed-authority classification and existing profile/selection labels through sessnap, event-source metadata, stores, and restart. Reject missing/invalid classification, bad versions/combinations/names, non-Subagent recipes, and mutation; getters do not alias. Compaction retains recipes; main successors do not copy them; legacy absence remains absent/inspectable and public/model projections omit recipes. An unchanged nonmanaged named definition resumes even with a broader saved parent-derived set than its catalog; current catalog restrictions still apply. Managed-to-lower-tier same-name replacement refuses, and eligible managed ceiling changes cannot expand saved rights. A saved explicit model/router selection wins over changed defaults, while empty selectors use new authorized definition/deployment defaults; merely resolving a model at creation does not pin it.
  - verify: `TestADR_0359_HarnessContext_Scenario9_DurableResumeRecipe`

## Dependency stack

1. **This amendment PR:** review the shared binding and inventory interfaces,
   domain scenarios, and same-parent resume plus explicit new-child history transfer.
   The directing user's resolved decisions and the exact recipe/access interfaces
   are recorded above. Merge remains the amendment's approval event and does not
   approve implementation.
2. **Shared implementation #1875:** rebase on the approved amendment, implement
   source-bound APIs and the shared policy/binding/inventory paths, and retain all
   existing reference proofs while adding Scenarios 6-9. Deliver resume enforcement
   and `history_from` together, including durable recipes, shared session access,
   real factory instructions, and recovery hints. Update generated contracts,
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
| Original-source cross-parent resume reconstruction | Future demonstrated need | Cross-parent recovery uses an explicitly requested new child with receiving context; no original-anchor lookup is added. Durable same-parent reconstruction is in scope. |
| New deployment-wide inventory API | None | Session-effective inventories replace the ambiguous global views; no independent admin requirement is established. |
| Generic registry or one new transport per source noun | Future demonstrated need | Existing ports and APIs already support independent sources. |
| Ancestor-directory traversal within a file source or changed caps | Separate decision | Preserve root-only discovery within each file source; composing multiple configured sources is in scope. |
| Automatic deletion or rejection of legacy state | None | Current-policy rebinding requires no destructive migration. |

## Definition of done

This amendment is proposed with the directing user's choices recorded; PR #1878
remains draft for independent review until the parent declares readiness. Neither
that policy authorization nor this status is approval by merge. Review the exact
recipe, session-access, and bounded projection contracts together with both recovery
operations. Run the acceptance-plan checker and its fixtures, `task docs:model`,
and `task docs`; render model Markdown from YAML. No implementation dispatch starts
before amendment approval.

After human contract merge, #1875 must pass `task test`, `task lint`,
`task test:race`, `task api:check`, `task generate` freshness, `task docs`,
`task site:build`, strict AC tracing, the offline demo, and independent panel review.
The implementation updates the owning settings, API/SDK, inventory, and delegation
guides in place; this plan does not present proposed behavior as shipped. Tests
exercise production composition and transport seams with isolated offline fixtures.
The implementation PR records the approved amendment commit. Humans alone merge.
