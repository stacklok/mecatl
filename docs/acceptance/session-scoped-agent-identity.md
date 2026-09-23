# Session-scoped agent identity — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — extends a security/trust boundary (session tool
ceiling, guardrail wiring, minted Authority) and a public gRPC/protobuf compatibility
surface (`CreateSessionRequest`/`CreateSessionResponse`).
**Decision record:** [ADR 0353](../adr/0353-session-scoped-agent-identity.md)
**Phase:** capability
**Status:** draft, 2026-09-23. Authored via `/to-acceptance-plan` after ADR 0353 merged
(`862bb4632`).
**Delivery:** Split. Interface-bearing (proto field, exported Go types, a security/authority
boundary) — the default two-PR path applies.
**Expected tasks:** deferred to orchestration
**Issue:** [stacklok/mecatl#1053](https://github.com/stacklok/mecatl/issues/1053).
**Plan PR:** <added when opened>
**Approved baseline:** <merged plan commit; absent until approved>

Make a `CreateSessionRequest.agent_definition_name` binding demonstrable end-to-end: a
session whose root engine is built exclusively from a named `AgentDef`'s own tools,
provider/model, limits, permission mode, hooks, and memory — with ordinary main-session
guardrails and ask-flow, a strictly non-widenable tool ceiling that survives fork/clear and
every engine-rebuild path, and no silent widening through either of the two extra grant
channels ([`Config.RootAuthority`](../../internal/app/root_authority.go) and
[`Config.MCPBroker`](../../internal/adapter/server/service.go)) that ADR 0353's own text
does not name.

## Human decisions

- [ ] Should the `PermissionMode` tighten-only clamp (`plan < default < acceptEdits`) use a small local ordinal table now, or block on PR #1730's still-unmerged (and, since it collided with an unrelated Studio ADR, still-unnumbered) permission-mode vocabulary landing first? Recommended: define a local ordinal table now, since Scenario 1 only needs the three `session.PermissionMode` values already in the codebase, and reconcile later if #1730 lands a different shape.
- [ ] Should `AgentDefSessionEngineFactory` (the new per-session engine factory this plan introduces) ever accept a tool-widening input parameter, for symmetry with the existing `SessionEngineWithToolsFactory`? Recommended: no, never, since the security property "the catalog is built exclusively from the def's tools" should be structurally impossible to violate from a future caller, not merely a documented convention.
- [ ] Is failing closed (refusing to resume) an acceptable v1 cost for restart/MCP-resume on an agent-bound session, or does the motivating mecak8s Slack-bot deployment need session continuity across a pod restart badly enough to pull the larger persist-authority-and-rebuild-through-it fix (issue #1796) into this plan rather than leaving it a separate follow-up? Recommended: fail closed now, matching ADR 0353's own stated v1 scope, and keep #1796 separate.
- [ ] `memory: project` for an agent-bound session: should `resolveAgentMemoryHead` be extended to thread the session's own resolved placement (`EnvironmentRef`) into its "project" root, or should it keep reading the single process-wide `cfg.Workspace` unmodified? Today's code only supports the latter (`cfg.Workspace`, fixed once at `Build()` time, identical for every session in the process), but ADR 0353's own Decision text already claims the former ("resolves against this session's own bound EnvironmentRef"). A session ROOT (unlike a child, which always inherits its parent's single placement) can genuinely be forked/cleared onto a distinct alternate-worktree placement (ADR 0291), so leaving this unresolved risks two differently-placed sessions bound to the SAME def sharing or leaking the same "project" memory file. Recommended: implement the placement-aware version — it is real new work (resolveAgentMemoryHead's signature must grow a placement parameter, threaded through both its call sites), but it is also what ADR 0353 already claims is true, so implementing it faithfully closes a correctness gap rather than leaving the ADR's own description wrong.
- [ ] `CreateSessionRequest.Limits` fields are plain `int32` with an existing, already-documented wire meaning ("a zero value in any field disables that particular limit"). For an agent-bound session, does a request's zero value mean "caller did not supply this field, inherit the def's cap" (required for tighten-only to hold), or does it keep its documented "caller explicitly wants unlimited" meaning (which would be a WIDENING the whole plan exists to forbid)? The existing Subagent per-call analogue sidesteps this with a `*int` override (nil vs. pointer-to-zero), a shape `CreateSessionRequest.Limits`'s plain `int32` fields don't have. Recommended: for an agent-bound session specifically, a request `Limits` zero value means "not supplied, inherit the def's cap" — a caller cannot use this field to request unlimited on an agent-bound session; only a positive value strictly below the def's cap may tighten it.
- [ ] AC1.7/AC1.8 (guardrail-inspects-effective-payload) require fixing `internal/adapter/modelhook.Runner.check` to inspect a `PreToolUse` hook's `Mutated` content when present, instead of the pre-mutation `HookEvent` it reads today — a real, code-verified gap (`Runner.Run` calls `r.check` against the original event; `mergeOutcomes` combines outcomes, it never re-runs the checker over the inner's rewritten payload). This is pre-existing behavior for every main session with BOTH operator hooks and guardrails configured, not something this plan introduces — but this plan is the first to state "a definition hook can never bypass a guardrail verdict" as a tested claim, and the first to route a caller-selected (not necessarily operator-authored) hook source through it with no prior trusted-model decision gating it. Should fixing `Runner.check` be in THIS plan's scope (closing the gap for every main session, not just agent-bound ones), or should AC1.7/AC1.8 instead be narrowed to the current, weaker, honest property and the bypass filed as a separate, higher-priority security fix this plan blocks on? Recommended: fix it here — shipping a plan that asserts a false security guarantee is worse than a slightly wider diff, and the fix is narrowly scoped (one function, `contentUnderReview`'s input).
- [ ] ADR 0353's Decision text says a def author who wants more than the bounded base set "lists them explicitly" (WebSearch, memory tools, …) — but `scopedToolNamesMode` resolves BOTH the omitted-tools default AND an explicit `tools:` entry against the identical `baseSubagentTools(cfg)` availability map, so naming `WebSearch` or a memory tool in `tools:` today silently drops it as an "unknown tool," not grants it. Should `base` be widened for the root-shaped path specifically so a def CAN opt into a larger, still-bounded set by naming tools explicitly (matching the ADR's stated intent — this needs its own explicit, security-reviewed decision about exactly which additional tools are eligible, not "whatever the shared default catalog has"), or should the ADR text instead be corrected to say the ceiling is always `baseSubagentTools(cfg)` regardless of naming? Recommended: correct the ADR text for v1 (simpler, and consistent with "reuse the same bounded base set a Subagent child gets" being this plan's whole design principle) and open a follow-up issue if a concrete use case ever needs a wider explicit-opt-in set.

## Interface contract

- **gRPC / protobuf:** `CreateSessionRequest.agent_definition_name` — new optional
  `string`, field `12` (`contracts/proto/mecatl/v1/harness.proto`; fields 1 and 8 reserved,
  11 is the last currently used). `CreateSessionResponse.resolved_agent_definition_name` —
  new optional `string`, field `6` (5 is the last currently used). Both additive;
  `task generate` regenerates `contracts/gen/**`.
- **Exported Go APIs / interfaces:** `engine/session.Session.AgentDefinitionName string` —
  new, write-once creation label alongside `Profile`/`ProviderID` (additive).
  `engine/adapter/sessnap.Snapshot.AgentDefinitionName string` — new, `json:"agent_definition_name,omitempty"`
  (additive). `internal/adapter/server.AgentDefSessionEngineFactory` — new factory type on
  `server.Config`, mirroring the existing `DebugSessionEngineFactory`. `internal/app` gains
  unexported `resolvedAgentDefCatalog`, `buildAgentDefRootEngine` (both already landed,
  unused, on the exploratory branch — see `.scratch/orchestrate/session-scoped-agent-identity/run.md`),
  `resolveAgentDefRootProviderModel`, and `tightenLimits`; none of these cross the engine
  module boundary. The `engine/api/*.txt` gate tracks only the eight core packages in
  `engine/arch.CorePackages` (`session`, `governance`, `learning`, `tool`, `prompt`,
  `port`, `team`, `agent`) — `engine/adapter/sessnap` is a reference adapter and is NOT in
  that list. So only `engine/session.Session.AgentDefinitionName` needs `task api:update` +
  an `engine/CHANGELOG.md` Added entry per
  [ADR 0037](../adr/0037-engine-stability-contract.md); the `sessnap.Snapshot` field is
  additive but produces no `engine/api/*.txt` diff.
- **Tool schemas:** None — no tool's own input/output schema changes; the change is which
  tools get REGISTERED into a session's catalog, never a tool's schema.
- **CLI / config:** None — no new flag or config key. `agent_definition_name` is
  caller-supplied per request; definition discovery reuses the existing
  `--agents-dir`/`--agent-source-url` machinery unchanged.
- **Events / persistence:** `sessnap.Snapshot.AgentDefinitionName` (additive, `omitempty`,
  same round-trip discipline as `Profile`/`ProviderID`/`ModelID`). No new `session.Event` —
  the binding is a durable session label, not an event.
- **Security / authority:** a resolved `AgentDef`'s `tools`/`disallowedTools`/`mcpServers:`
  become a strict, non-widenable ceiling on the session's catalog (ADR 0353). The session's
  minted `session.Authority` (`mintRootAuthority`) is derived from that SAME resolved
  catalog and resource-capability list — not the deployment's build-time default catalog
  the ordinary `Config.RootAuthority` closure produces — so a def's own inline-MCP tools
  are never silently dropped from the model-offered spec list by the authority filter.
  `Config.MCPBroker` attachment is skipped entirely, at both the create and the
  Fork/Clear-successor engine-build call sites, for an agent-bound session — mecak8s is
  ADR 0353's own named deployment target, so its external-MCP-granting mechanism must not
  be a fourth, unaddressed widening channel alongside `mcp_servers`/`debug_mcp_servers`
  (both already rejected by the ADR) and the ordinary default catalog. Guardrails,
  `AudienceMain` governance, and the normal awaiting/approve ask-flow are unchanged from any
  other main session. Every existing engine-rebuild trigger that doesn't know about
  `agent_definition_name` today fails closed for an agent-bound session rather than
  silently rebuilding the wider default catalog: `SetMode` (its own direct guard, AC3.1);
  `LoadSessionWithMCP` (its own direct guard, AC3.3, since it calls `s.cfg.SessionEngine`
  directly and never passes through the choke point below); and — sharing ONE common choke
  point — `StartRunContent`/run-entry, `RetryFailedRun`, `CompactSession`, and
  `resumeFromAwaiting`. The actual shared choke point for the latter four
  is `needsRehydration()`/`engineAndEnvironmentFor` (`internal/adapter/server/service.go`)
  — NOT specifically `rehydrateSession`, which only fires when `needsRehydration()` already
  returns true; a fix that only touches `rehydrateSession` is a no-op for an agent-bound
  session whose provider/model selector is empty, profile is default, and the deployment
  has no `MCPBroker`/`LearnedSkills` configured (plausibly the exact shape of the
  motivating mecak8s/Slack-bot deployment), because `needsRehydration()` returns false and
  execution never reaches `rehydrateSession` at all. The guard belongs in
  `needsRehydration()` itself (or an unconditional check at the very top of
  `engineAndEnvironmentFor`, ahead of every branch), so it structurally covers all four
  callers and any future one. `buildAndRegisterSessionEngineWithBrokerTools` — the ONE
  function the guarded rehydration path and the must-succeed Fork/Clear-successor path
  both reach — gains a dispatch arm for `AgentDefSessionEngineFactory`; the invariant this
  whole set of guards protects is that an agent-bound session may reach that factory ONLY
  from session-creation and the Fork/Clear successor path, never from a rebuild trigger.
  Definition hooks: resolving an `agent_definition_name` grants unconditional local
  command execution via the def's `hooks:` (one shell command per governance phase,
  running on every matching tool call with no model decision, no permission ask, and no
  guardrail veto for a `PostToolUse` hook) directly to any caller who can call
  `CreateSession` — this is a strictly broader blast radius than "tool selection," and the
  deferred "no per-caller authorization" decision below is evaluated against it
  explicitly, not against a narrower tool-catalog framing. Discovery-tier blindness: per
  ADR 0353, `agent_definition_name` resolution does not consult the `Managed`/explicit-
  origin trust tier the existing Subagent-delegation path's `managedDefinitionAuthority`
  check enforces before recording a def's authority ceiling. This is intentional, not an
  oversight: that check exists to stop a lower-tier def from claiming an authority ceiling
  by colliding on the SAME NAME as a higher-tier one inside a shared, name-keyed map at
  delegation time, when a running model picks the name at runtime. A session-root binding
  has no such ambiguity — it resolves exactly one def, by its exact name, once, at
  `CreateSession` time, with no shared map and no runtime name choice — so the
  collision class that check defends against structurally cannot occur here.
- **Compatibility / migration:** fully additive, backward-compatible. A request that never
  sets `agent_definition_name` is byte-identical to current behavior (ADR 0353: empty
  reproduces today's default-explorer session). No migration for existing sessions — one
  created before this change simply carries an empty `AgentDefinitionName` label forever.

## In scope — 3 scenarios, in implementation order

### Scenario 1 — A session bound to a named AgentDef gets a strict, non-widenable tool ceiling

An operator or SDK caller (the motivating case: a Slack bot, issue #1053) creates a session
naming an `AgentDef`. The resulting session behaves as an ordinary main session in every
respect except its tool catalog, which is exactly the def's own resolved scope — never wider
through any of the three channels this exploration found (the request's own `mcp_servers`,
the deployment's default catalog via `Config.RootAuthority`, or `Config.MCPBroker`). See
[ADR 0353](../adr/0353-session-scoped-agent-identity.md)'s Decision section for the
authoritative rules this scenario proves.

**Acceptance:**
- AC1.1: `CreateSession` with `agent_definition_name` set builds the session's catalog
  **exclusively** from the resolved `AgentDef`'s `tools`/`disallowedTools` (core) and
  `mcpServers:` (MCP) — never the deployment's default explorer catalog.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_CatalogExactlyMatchesDef`
- AC1.2: `CreateSessionRequest.mcp_servers`/`debug_mcp_servers` are rejected as
  `InvalidArgument` whenever `agent_definition_name` is set.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_ClientMCPRejected`
- AC1.3: Omitting `tools:` in the `AgentDef` resolves against the SAME bounded base set a
  Subagent child already gets (`baseSubagentTools`) — never the ordinary root's full
  default catalog.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_OmittedToolsUsesChildBaseSet`
- AC1.4: An unresolvable `agent_definition_name` is a loud `InvalidArgument`, mirroring how
  an unknown `provider_id` is handled today.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_UnknownDefRejected`
- AC1.5: `agent_definition_name` combined with `debug_target_session_id` is
  `InvalidArgument`.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_DebugTargetMutualExclusionRejected`
- AC1.6: Guardrails, governance audience (`AudienceMain`), and the ask-flow (a permission
  ask pauses the session to `awaiting` and resolves via the normal approve path) are the
  SAME as any other main session — never the child-shaped headless auto-deny or optional
  ask-reviewer.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_OrdinaryMainSessionBehavior`
- AC1.7: The def's own `hooks:` compose as the `inner` `HookRunner` under the existing
  operator-guardrails decorator (`modelhook.Runner`); the checker inspects the EFFECTIVE
  payload — when a `PreToolUse` hook mutates the tool-call args, the guardrail checker is
  extended to inspect that mutated content, not the stale pre-mutation input
  `modelhook.Runner.check` reads today. This closes a real, code-verified gap:
  `Runner.Run` currently calls `r.check` against the original `HookEvent`, never
  `innerOut.Mutated`, so a hook that rewrites benign args into a dangerous call runs with
  zero guardrail inspection.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_GuardrailInspectsHookMutatedPayload`
- AC1.8: A def hook that mutates a `PreToolUse` call's args is not exempt from guardrail
  inspection — a fixture def whose hook rewrites a benign call into one a configured
  guardrail rule would block is, in fact, blocked (negative test for AC1.7; today's code
  would let it through).
  - verify: `TestSessionScopedAgentIdentity_Scenario1_HookMutationCannotBypassGuardrail`
- AC1.9: The session's minted `session.Authority` is derived from the def's own resolved
  catalog and resource-capability list, not the deployment's build-time default catalog —
  a def's inline-MCP tools are genuinely offered to the model, never silently dropped by
  the authority filter (`authoritySpecs`).
  - verify: `TestSessionScopedAgentIdentity_Scenario1_AuthorityMintedFromDefCatalog`
- AC1.10: `Config.MCPBroker` attachment is skipped entirely for an agent-bound session at
  session-creation time — broker-granted tools never widen the def's ceiling.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_MCPBrokerAttachmentSkipped`
- AC1.11: Mutation (Edit/Write/Shell) is available if and only if the def's `tools:` allows
  it — never forced read-only the way a Subagent delegate is.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_MutationFollowsDefTools`
- AC1.12: Request `Limits.max_turns`/`max_tool_calls` may only lower the def's configured
  values, never raise them.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_LimitsTightenOnly`
- AC1.13: A request `mode` looser than the def's configured `permissionMode` is silently
  clamped to the def's value (`plan` < `default` < `acceptEdits`) — never rejected, never
  raised.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_PermissionModeClampedNotRaised`
- AC1.14: `provider_id`/`model_id` resolve as a single pair: an explicit pair on the request
  wins; a lone `provider_id` override re-derives THAT provider's own default model, rather
  than carrying over a model the def pinned for a different provider; an unavailable
  definition-pinned provider fails session creation rather than silently instantiating the
  def on another provider.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_ProviderModelPairResolution`
- AC1.15: Under `profile: "no-fs"`, filesystem tools and Shell stay absent from an
  agent-bound session's catalog even when the def's `tools:` lists them — the profile and
  the def's ceiling compose by intersection, and neither one can restore what the other
  removes.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_NoFSComposesWithDefCeiling`
- AC1.16: A def's `tools:` naming any tool outside `baseSubagentTools(cfg)` — `WebSearch`,
  any of the six memory tools, `Skill`/`SkillDraft`, `Schedule`, `Team`/`InspectMember`, or
  any other tool the ordinary default catalog has but a Subagent child does not — never
  causes that tool to appear in an agent-bound session's catalog, whether `tools:` is
  omitted (AC1.3) or explicitly names it. (This is the negative case AC1.3 alone doesn't
  cover, and it means ADR 0353's own "a def author who wants more (WebSearch, memory
  tools, …) lists them explicitly" claim does not hold against today's code — flagged as a
  Human decision below, not silently resolved either way.)
  - verify: `TestSessionScopedAgentIdentity_Scenario1_ExplicitlyListedToolOutsideBaseSetStillDropped`

### Scenario 2 — The identity is durable: it persists, echoes, and survives fork/clear

The binding is a session-lifetime label, not a per-request hint. It rides the session
snapshot, is echoed to the caller, and both `ForkSession`/`ClearSession` continue the same
identity onto their successor rather than dropping or re-selecting it — mirroring how
`Profile` already behaves under
[ADR 0291](../adr/0291-server-owned-session-placement.md)'s server-owned placement rules,
per [ADR 0353](../adr/0353-session-scoped-agent-identity.md)'s "fixed for the session's
lifetime" decision.

**Acceptance:**
- AC2.1: `agent_definition_name` is stamped onto the session at creation, persists on the
  session snapshot, and survives a save/restore round-trip — including for a session that
  later fails closed on rehydration (AC3.2): the label itself always restores onto the
  session object; only the per-session ENGINE build refuses.
  - verify: `TestSessionScopedAgentIdentity_Scenario2_PersistsAcrossSnapshotRoundTrip`
- AC2.2: `CreateSessionResponse.resolved_agent_definition_name` echoes the bound name
  verbatim, mirroring the existing `resolved_model` echo.
  - verify: `TestSessionScopedAgentIdentity_Scenario2_ResponseEchoesBoundName`
- AC2.3: `ForkSession` and `ClearSession` always inherit the source session's
  `agent_definition_name` unconditionally (no request-side override field — Profile-style,
  not `ProviderID`/`ModelID`-style) and rebuild the SAME restricted catalog for the
  successor, never the default catalog.
  - verify: `TestSessionScopedAgentIdentity_Scenario2_ForkClearPreserveBindingAndCatalog`
- AC2.4: A forked/cleared successor's engine build also skips `Config.MCPBroker`
  attachment, mirroring AC1.10 for the fresh-successor construction path (`placement_successor.go`
  builds a new engine directly, not via the create-time path AC1.10 covers).
  - verify: `TestSessionScopedAgentIdentity_Scenario2_ForkClearSkipMCPBrokerAttachment`
- AC2.5: `memory: project` for an agent-bound session resolves against THIS session's own
  bound placement (`EnvironmentRef`), not the deployment's single process-wide daemon
  workspace — two agent-bound sessions on the SAME def but DIFFERENT placements (e.g. one
  forked onto an alternate worktree per ADR 0291) never share or leak the same project
  memory file. (Depends on the corresponding Human decision above; if that decision instead
  keeps today's unmodified process-wide `resolveAgentMemoryHead`, this AC is superseded by
  one documenting that cross-placement limitation instead.)
  - verify: `TestSessionScopedAgentIdentity_Scenario2_ProjectMemoryScopedToOwnPlacement`
- AC2.6: Under `profile: "no-fs"`, `memory: project` is inert (a silent no-op) for an
  agent-bound session, never an error — there is no workspace to bind a project-memory file
  to, and the ceiling never signals an unrelated failure for it.
  - verify: `TestSessionScopedAgentIdentity_Scenario2_ProjectMemoryInertUnderNoFS`

### Scenario 3 — The binding fails closed, never silently widens, across every engine-rebuild path

A session's per-session engine gets rebuilt at points that have nothing to do with
`agent_definition_name` today: a `SetMode` switch, a client-MCP resume
(`LoadSessionWithMCP`, which calls the generic session-engine factory directly), and —
sharing one common choke point, `needsRehydration()`/`engineAndEnvironmentFor`
(`internal/adapter/server/service.go`) — every run-entry path that can reach a session
with no live per-session engine registered: `StartRunContent`, `RetryFailedRun`,
`CompactSession`, and `resumeFromAwaiting`. None of those paths read the new binding —
they all fall through to the deployment's shared default engine (`engineAndEnvironmentFor`
initializes `engine := s.cfg.Engine` and only ever overwrites it if a rebuild branch
fires). This scenario proves each one is closed, per
[ADR 0353](../adr/0353-session-scoped-agent-identity.md)'s fail-closed decision — and,
specifically, that the fix lives in the SHARED choke point
(`needsRehydration()`, or an unconditional check at the top of `engineAndEnvironmentFor`
itself), not only in `rehydrateSession`, which is unreachable in exactly the case that
matters most (see AC3.2).

**Acceptance:**
- AC3.1: `SetMode` is rejected with `InvalidArgument` outright for an agent-bound session —
  the mode is fixed for the session's entire lifetime.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_SetModeRejected`
- AC3.2: An agent-bound session with an EMPTY provider/model selector (the def resolves
  its own provider/model — the common case), default (non-no-fs) profile, and a deployment
  with no `Config.MCPBroker`/`Config.LearnedSkills` configured — the "boring" configuration
  under which `needsRehydration()`'s OTHER clauses are all false, so a fix that only
  touches `rehydrateSession` would be a silent no-op — fails closed at
  `engineAndEnvironmentFor` (a clear error, never the deployment's default engine) whenever
  its in-memory per-session engine registration is lost (simulated process restart, or any
  other eviction of the live registration), reached via EACH of `StartRunContent`,
  `RetryFailedRun`, `CompactSession`, and `resumeFromAwaiting`.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_RestartFailsClosedOnBoringConfig`
- AC3.3: `LoadSessionWithMCP` is rejected with `InvalidArgument` for an agent-bound session
  whenever the caller supplies client MCP servers on resume.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_LoadSessionWithMCPRejected`

Implementation note, not a separate AC (pinning it would test an internal code path, not
observable behavior): `engineAndEnvironmentFor`'s ordinary mode-switch/promotion rebuild
branches (CASE 1 and CASE 2) need no direct edit — AC3.1 makes a mode change on an
agent-bound session impossible, and AC3.2 makes every entry into `engineAndEnvironmentFor`
fail closed before either branch could fire, so both are closed transitively. A future
refactor of those branches should not need to touch this feature's guards at all; if it
does, that is a sign the transitive closure broke and AC3.1/AC3.2 should catch it directly.


## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Agent-as-session-root delegation (`Subagent`/`Parallel`/`Team` from an agent-bound session) | a later ADR/issue if a concrete use case appears | ADR 0353 named non-goal |
| Per-caller authorization on which `agent_definition_name` values a caller may request — including the def's own `hooks:` local-command-execution surface this grants, not just its tool selection | deployment-level access control (separate mecak8s deployments) | ADR 0353 named non-goal |
| Persisting the def's authority so restart/MCP-resume can rebuild the SAME restricted catalog instead of failing closed | [issue #1796](https://github.com/stacklok/mecatl/issues/1796) | ADR 0353 Consequences |
| Session-configurable operator posture (strict/trusted/auto/yolo) | [issue #1784](https://github.com/stacklok/mecatl/issues/1784) | ADR 0353 named non-goal |
| PR #1730's own permission-mode vocabulary and its pending renumbering | PR #1730 (independent, non-conflicting work) | not this plan's concern |

## Definition of done

1. Applicable `task lint`, `task test`, `task docs`, and `task api:check` gates pass on the
   final candidate.
2. `task ac-trace-strict` resolves every named proof when the plan becomes `landed`.
3. `go run ./cmd/mecademo` remains green.
4. The implementation PR links this Plan / Interface PR and its approved merge commit, and
   reports interface conformance against the contract above.
5. `/panel-review` reports no ship blockers or unwaived reviewer failures.

## Deferred decisions and known risks

- AC1.9/AC1.10 and AC2.4 (the `RootAuthority`-minting and `MCPBroker`-skip fixes), the
  guardrail-mutation-inspection fix (AC1.7/AC1.8, if the corresponding Human decision keeps
  it in scope), and the `hooks:`-execution-surface and discovery-tier-blindness
  reconciliations in the Security/authority section above are all consistent
  implementations or clarifications of ADR 0353's stated ceiling guarantee, not deviations
  from it — but ADR 0353's own Decision text does not currently name any of them. Once this
  plan lands, ADR 0353 should get a short follow-up documentation pass naming all of them,
  so a future reader of the ADR alone (without this plan) sees the complete threat model.
  Non-blocking for this plan.
- The exploratory branch `implement-session-scoped-agent-identity` (draft PR #1803) already
  contains a working split of `buildAgentDefEngine` into a shared catalog-resolution core
  plus child-shaped and root-shaped Deps wrappers, built under an explicit human-authorized
  spine waiver before the ADR merged. `/plan-orchestrate` may reuse it as a starting point
  where it matches this plan's Interface contract, or implement fresh via TDD — this plan
  does not mandate either.
- Implementation guidance, not a material decision: `buildAgentDefEngine` and
  `buildAgentDefRootEngine` share a large (15-argument) positional prefix that must stay
  byte-identical between them; bundling it into one small struct (e.g.
  `agentDefCatalogInputs`) removes the transposition risk a future edit could otherwise
  introduce, especially once issue #1796's authority-persistence work threads yet another
  field through both. Likewise, `resolveAgentDefRootProviderModel` should delegate to the
  existing `resolveProviderModel` for its base-pair step (case 1 of the 5-case algorithm)
  rather than reimplementing provider/alias lookup — the two functions have real,
  ADR-documented precedence and failure-mode differences that justify forking, but that
  base-pair computation itself is identical and already tested.
