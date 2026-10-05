# Session-scoped agent identity — acceptance plan

**Contract:** human-reviewed/v2
**Work classification:** Architectural — extends a security/trust boundary (session tool
ceiling, guardrail wiring, minted Authority) and a public gRPC/protobuf compatibility
surface (`CreateSessionRequest`/`CreateSessionResponse`).
**Decision record:** [ADR 0353](../adr/0353-session-scoped-agent-identity.md)
**Phase:** capability
**Status:** proposed, 2026-09-23. Authored via `/to-acceptance-plan` after ADR 0353 merged
(`862bb4632`); all Human decisions resolved by the directing human on 2026-09-23.
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
every engine-rebuild path, and no silent widening through any of the extra grant channels
that ADR 0353's own text does not name: [`Config.RootAuthority`](../../internal/app/root_authority.go),
[`Config.MCPBroker`](../../internal/adapter/server/service.go),
[`GrantToolAuthority`](../../engine/session/principal.go), and
[`CompleteWorkspaceEnrollment`](../../engine/session/workspace_enrollment.go).

## Human decisions

- [x] Should the `PermissionMode` tighten-only clamp (`plan < default < acceptEdits`) use a small local ordinal table now, or block on PR #1730's still-unmerged permission-mode vocabulary (now `docs/adr/0375-one-permission-mode-vocabulary.md`, having renumbered itself twice after colliding with unrelated ADRs) landing first? — Decision: define a small local ordinal table now; reconcile later if #1730 lands a different shape.
- [x] Should `AgentDefSessionEngineFactory` (the new per-session engine factory this plan introduces) ever accept a tool-widening input parameter, for symmetry with the existing `SessionEngineWithToolsFactory`? — Decision: no, never — the security property "the catalog is built exclusively from the def's tools" must be structurally impossible to violate, not merely a documented convention.
- [x] Is failing closed (refusing to resume) an acceptable v1 cost for restart/MCP-resume on an agent-bound session, or does the motivating mecak8s Slack-bot deployment need session continuity across a pod restart badly enough to pull the larger persist-authority-and-rebuild-through-it fix (issue #1796) into this plan? — Decision: fail closed now for simplicity, matching ADR 0353's own stated v1 scope; keep #1796 as a separate follow-up.
- [x] `memory: project` for an agent-bound session: should `resolveAgentMemoryHead` be extended to thread the session's own resolved placement (`EnvironmentRef`) into its "project" root, or should it keep reading the single process-wide `cfg.Workspace` unmodified? — Decision: implement the placement-aware version. This is real new work (today's code, including the exploratory Slice A branch, does not do this — `resolveAgentMemoryHead` is called unchanged), but it closes a real cross-placement leak risk and is what ADR 0353's own text already claims is true.
- [x] `CreateSessionRequest.Limits` fields are plain `int32`. The `.proto` doc comment ("a zero value in any field disables that particular limit") is itself stale against the server's actual behavior: `createSession` (`internal/adapter/server/service.go`, ~2265-2266) already states "a zero field means 'unset', not 'explicitly unlimited'" and fills it via `limits.WithDefaults(s.cfg.DefaultLimits)`. For an agent-bound session, does zero mean "not supplied, inherit the def's cap," or "explicitly unlimited"? — Decision: for an agent-bound session, a request `Limits` zero value means "not supplied, inherit the def's cap" — a caller cannot use this field to request unlimited on an agent-bound session. This is now recognized as consistent with, not a carve-out from, the server's real existing zero-means-unset behavior — the stale "0 = unlimited" wire *documentation* is a separate, pre-existing inaccuracy this plan does not fix.
- [x] AC1.7/AC1.8 originally required fixing `internal/adapter/modelhook.Runner.check` to inspect `innerOut.Mutated` when present, instead of the pre-mutation `HookEvent` it read at draft time — a real, code-verified gap affecting BOTH governance phases. `modelhook.Runner` no longer exists on `main` (ADR 0363 replaced hook-decorator guardrails with dispatch-level contextual review, `reviewAction`/`prepareInboundAssessment` in `engine/agent/dispatch.go`), re-verified to already inspect the effective/post-mutation payload for both phases — is a fix still needed, and if not, what should AC1.7/AC1.8 become? — Decision: retired. No fix needed; AC1.7/AC1.8 are now pinning regression tests against the real current mechanism instead of a fix against removed code.
- [x] ADR 0353's Decision text says a def author who wants more than the bounded base set "lists them explicitly" (WebSearch, memory tools, …) — but naming one today silently drops it as an "unknown tool," it does not grant it. Should `base` be widened for the root-shaped path so naming works as the ADR describes, or should the ADR text be corrected instead? — Decision: correct the ADR text for v1 (simpler, and consistent with reusing the same bounded base set a Subagent child gets); open a follow-up issue if a concrete use case ever needs a wider explicit-opt-in set. Already done in this PR's own diff (`docs/adr/0353-session-scoped-agent-identity.md`) — not deferred.

## Interface contract

- **gRPC / protobuf:** `CreateSessionRequest.agent_definition_name` — new optional
  `string`, field `12` (`contracts/proto/mecatl/v1/harness.proto`; fields 1 and 8 reserved,
  11 is the last currently used). `CreateSessionResponse.resolved_agent_definition_name` —
  new optional `string`, field `6` (5 is the last currently used). Both additive;
  `task generate` regenerates `contracts/gen/**`.
- **Exported Go APIs / interfaces:** `engine/session.Session.AgentDefinitionName string` —
  new, write-once creation label alongside `Profile`/`ProviderID` (additive).
  `engine/session.Authority.Ceiling` — new, write-once, OPTIONAL capability-set field set
  once alongside `BindAuthority` for an agent-bound session (nil/absent — unrestricted —
  for an ordinary session, additive). `engine/session.Session.GrantToolAuthority` and
  `engine/session.Session.CompleteWorkspaceEnrollment` both gain a Ceiling check (Changed,
  not Added — see below; `CompleteWorkspaceEnrollment`'s own internals are also under
  separate, active redesign in the now-approved PR #1741/ADR 0358, implementation still
  open in #1809 — this plan's requirement is a contract-level property of that method,
  not a specific internal mechanism, so it composes with #1809 landing before, after, or
  concurrently with this plan).
  `engine/adapter/sessnap.Snapshot.AgentDefinitionName string` — new, `json:"agent_definition_name,omitempty"`
  (additive). `internal/adapter/server.AgentDefSessionEngineFactory` — new factory type on
  `server.Config`, mirroring the existing `DebugSessionEngineFactory`. `internal/app` gains
  unexported `resolvedAgentDefCatalog`, `buildAgentDefRootEngine` (both already landed,
  unused, on the exploratory branch — see `.scratch/orchestrate/session-scoped-agent-identity/run.md`),
  `resolveAgentDefRootProviderModel`, and `tightenLimits`; none of these cross the engine
  module boundary. The `engine/api/*.txt` gate tracks only the eight core packages in
  `engine/arch.CorePackages` (`session`, `governance`, `learning`, `tool`, `prompt`,
  `port`, `team`, `agent`) — `engine/adapter/sessnap` is a reference adapter and is NOT in
  that list. So `engine/session.Session.AgentDefinitionName` (Added) and `Authority.Ceiling`
  + `GrantToolAuthority`/`CompleteWorkspaceEnrollment`'s behavior changes (Added/Changed
  respectively) need `task api:update` + classified `engine/CHANGELOG.md` entries per
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
  become a strict, non-widenable ceiling on the session's catalog (ADR 0353), and — the
  load-bearing guarantee — on what the session can actually EXECUTE: `session.Authority`
  gains a write-once `Ceiling` set once at bind time from the def's resolved catalog, and
  `authorizeExecution` (`engine/agent/dispatch.go`, already documented as "the single
  authority-enforcement boundary") enforces it independent of what the catalog offers. The
  TWO existing aggregate methods that can widen a bound session's tools post-bind —
  `GrantToolAuthority` (ADR 0355's direct/global MCP-refresh path, today unions names into
  `CapabilitySet.Tools` unconditionally) and `CompleteWorkspaceEnrollment` (the
  broker-enrollment path, today replaces `CapabilitySet.Tools` wholesale) — no ceiling
  check in either today; both are changed to reject any grant that would exceed the
  Ceiling. This is deliberately a `Session`-aggregate invariant, not an adapter-layer
  special case: enumerating adapter call sites is fragile (plan review already found two
  real widening paths — `rebuildGrantedAuthorizationEngine`'s lazy broker-OAuth grant and
  `ConnectWorkspaceServices`'s workspace enrollment — beyond the six originally named:
  `SetMode`, `LoadSessionWithMCP`, `StartRunContent`, `RetryFailedRun`, `CompactSession`,
  and `resumeFromAwaiting`), and any future call site this plan doesn't enumerate is still bound by the same
  aggregate check. The session's minted `session.Authority` (`mintRootAuthority`) is
  derived from the def's own resolved catalog and resource-capability list at bind time —
  not the deployment's build-time default catalog the ordinary `Config.RootAuthority`
  closure produces — so a def's own inline-MCP tools are never silently dropped from the
  model-offered spec list by the authority filter. Adapter-layer guards remain as
  catalog-consistency and defense-in-depth (an agent-bound session's catalog should never
  OFFER a tool its Ceiling would refuse to execute, even though the Ceiling alone makes
  execution safe): `Config.MCPBroker` attachment, the lazy broker-OAuth grant flow, and
  workspace enrollment are all skipped for an agent-bound session at every reachable call
  site (create, Fork/Clear successor, `rebuildGrantedAuthorizationEngine`,
  `ConnectWorkspaceServices`) — mecak8s is ADR 0353's own named deployment target, so its
  external-MCP-granting mechanisms must not be unaddressed widening channels alongside
  `mcp_servers`/`debug_mcp_servers` (both already rejected by the ADR). Guardrails,
  `AudienceMain` governance, and the normal awaiting/approve ask-flow are unchanged from any
  other main session. Every existing engine-rebuild trigger that doesn't know about
  `agent_definition_name` today ALSO fails closed at the catalog layer: `SetMode` (its own
  direct guard, AC3.3); `LoadSessionWithMCP` (its own direct guard, AC3.5, since it calls
  `s.cfg.SessionEngine` directly and never passes through the choke point below); and —
  sharing ONE common choke point — `StartRunContent`/run-entry, `RetryFailedRun`,
  `CompactSession`, and `resumeFromAwaiting`. The shared choke point for the latter four
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
  both reach — gains a dispatch arm for `AgentDefSessionEngineFactory`.
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
through the request's own `mcp_servers`, the deployment's default catalog via
`Config.RootAuthority`, or `Config.MCPBroker` (this scenario's own ACs; Scenario 3 covers
the remaining widening channels — `GrantToolAuthority`, `CompleteWorkspaceEnrollment`, the
lazy broker-OAuth grant flow, and workspace enrollment). See
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
- AC1.7: An agent-bound session's contextual guardrail review (`reviewAction` for
  `PreToolUse`, `prepareInboundAssessment`/`assessInbound` for `PostToolUse` —
  `engine/agent/dispatch.go`, ADR 0363) inspects the EFFECTIVE payload for both phases —
  `pre.effective` (post-hook-mutation args) and the post-`postHook` result — exactly as
  any other main session's review does. This is a pinning regression test, not a fix: the
  mutation-inspection gap this AC originally targeted (a def hook's mutated payload
  skipping guardrail inspection) was a real, code-verified defect against
  `internal/adapter/modelhook.Runner`, but that type no longer exists on `main` — ADR 0363
  replaced hook-decorator guardrails with this dispatch-level review, which already
  operates on the effective/post-mutation payload for both phases. Because an
  agent-bound session's engine bottoms out in ordinary main-session `Deps` (AC1.6,
  including `Deps.ToolReviewer` and friends), it inherits this review unchanged — no new
  mechanism, just confirmed inheritance, guarded so a future change can't quietly exclude
  agent-bound sessions from it.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_ContextualGuardrailInspectsEffectivePayload`
- AC1.8: A def hook that mutates either phase's payload is not exempt from that review —
  fixture defs whose `PreToolUse` hook rewrites a benign call, and whose `PostToolUse`
  hook rewrites a benign result, into ones a configured guardrail rule would flag are, in
  fact, flagged (the same negative-test intent as before, now proven against the real
  current mechanism instead of the removed one).
  - verify: `TestSessionScopedAgentIdentity_Scenario1_HookMutationCannotBypassGuardrail`
- AC1.9: The session's minted `session.Authority` is derived from the def's own resolved
  catalog and resource-capability list, not the deployment's build-time default catalog —
  a def's inline-MCP tools are genuinely offered to the model, never silently dropped by
  the authority filter (`authoritySpecs`).
  - verify: `TestSessionScopedAgentIdentity_Scenario1_AuthorityMintedFromDefCatalog`
- AC1.10: `Config.MCPBroker` attachment is skipped entirely for an agent-bound session at
  session-creation time — broker-granted tools never widen the def's ceiling.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_MCPBrokerAttachmentSkipped`
- AC1.11: Mutation (Edit/Write/Shell) is available only if the def's `tools:` allows
  it — never forced read-only the way a Subagent delegate is. The def allowing mutation
  is necessary but not sufficient: `profile: "no-fs"` (AC1.15) still removes these tools
  even when the def's `tools:` lists them, since profile/deployment authority narrow the
  AVAILABLE set before the def's allowlist is ever consulted.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_MutationFollowsDefTools`
- AC1.12: Request `Limits.max_turns`/`max_tool_calls` may only lower the def's configured
  values, never raise them; an unset (zero-valued) request field means "not supplied,
  inherit the def's cap," never "explicitly unlimited," per the corresponding Human
  decision.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_LimitsTightenOnly`
- AC1.13: A request `mode` looser than the def's configured `permissionMode` is silently
  clamped to the def's value (`plan` < `default` < `acceptEdits`) — never rejected, never
  raised.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_PermissionModeClampedNotRaised`
- AC1.14: `provider_id`/`model_id` resolve as a single pair, covering all 5 cases of ADR
  0353's algorithm: (1) no request override resolves the def's own base pair (its
  `Model`/`Provider` over the deployment default); (2) a request `model_id` alone applies
  to the base pair's PROVIDER (never switches provider); (3) a request `provider_id` alone
  switches provider and re-derives THAT provider's own default model, rather than carrying
  over a model the def pinned for a different provider; (4) both fields set uses the
  explicit pair verbatim; (5) an unavailable definition-pinned provider (case 1's base
  pair) fails session creation rather than silently instantiating the def on another
  provider.
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
  cover. ADR 0353's text is already corrected in this PR's own diff to match this
  always-bounded behavior, rather than the code being widened to match the ADR's
  original wording.)
  - verify: `TestSessionScopedAgentIdentity_Scenario1_ExplicitlyListedToolOutsideBaseSetStillDropped`
- AC1.17: An `agent_definition_name` resolves through the existing `AgentDefSource` port
  exactly like the three existing call sites (startup Subagent construction, the
  `agent`+`model` override, the `agent`+`read-write` override) — no special-casing by
  discovery mechanism, whether the def comes from `--agents-dir` filesystem discovery or
  the remote `--agent-source-url` driver.
  - verify: `TestSessionScopedAgentIdentity_Scenario1_ResolvesViaExistingAgentDefSource`

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
  later fails closed on rehydration (AC3.4): the label itself always restores onto the
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
  memory file. The placement-aware extension uses the ROOT-AWARE trust admission check
  (`projectIngestionAdmittedForRoot(cfg, sessionRoot)`), never the bare
  `projectIngestionAdmitted(cfg)` global-flag check `resolveAgentMemoryHead` calls today —
  a session forked onto a placement the operator never specifically vetted must not read
  or write project memory there merely because the deployment's global trust flag happens
  to be true. (Resolved per the corresponding Human decision above: the placement-aware
  version is required, not today's unmodified process-wide `resolveAgentMemoryHead`.)
  - verify: `TestSessionScopedAgentIdentity_Scenario2_ProjectMemoryScopedToOwnPlacement`
- AC2.6: Under `profile: "no-fs"`, `memory: project` is inert (a silent no-op) for an
  agent-bound session, never an error — there is no workspace to bind a project-memory file
  to, and the ceiling never signals an unrelated failure for it.
  - verify: `TestSessionScopedAgentIdentity_Scenario2_ProjectMemoryInertUnderNoFS`

### Scenario 3 — The tool ceiling is a session-aggregate invariant, not an enumerated adapter-layer guard list

Enumerating adapter-layer rebuild call sites (the original framing of this scenario) is
fragile: plan/interface review already found two more real widening paths beyond the six
originally named (`SetMode`, `LoadSessionWithMCP`, `StartRunContent`, `RetryFailedRun`,
`CompactSession`, and `resumeFromAwaiting`) — `rebuildGrantedAuthorizationEngine`
(`internal/adapter/server/mcp_authorization.go`, the lazy per-tool broker-OAuth grant
flow) and `ConnectWorkspaceServices`/`connectWorkspaceServicesLocked`
(`internal/adapter/server/workspace_enrollment.go`, the client-callable workspace/bundle
enrollment RPC) — neither reachable through `needsRehydration()`/`engineAndEnvironmentFor`
at all, so neither of Scenario 3's two proposed guard locations would have caught them.

Rather than keep enumerating adapter call sites, the actual security-relevant boundary
belongs one layer down, at the session aggregate: `authorizeExecution`
(`engine/agent/dispatch.go`) is already documented as "the single authority-enforcement
boundary," gated on `sess.BoundAuthority()` — what a session can ACTUALLY execute is
governed by `session.Authority.CapabilitySet`, independent of what its catalog happens to
offer. `session.Authority` gains a write-once `Ceiling` (set once, alongside
`BindAuthority`, from the def's resolved catalog for an agent-bound session; absent for an
ordinary session — additive, no behavior change there). The two aggregate methods that can
widen a bound session's tools after bind — `GrantToolAuthority` and
`CompleteWorkspaceEnrollment`, exact mechanisms named in the Security/authority section of
the Interface contract above, not restated here — are both changed to reject any grant
that would exceed the Ceiling. This makes the ceiling a `Session`-aggregate invariant
(matching [AGENTS.md](../../AGENTS.md)'s "mutate `Session` through aggregate methods"
rule) instead of an adapter-layer special case a future call site can bypass simply by
not knowing about it — which is exactly how the two adapter sites above got missed here.

Coordination note: `CompleteWorkspaceEnrollment`'s internals are themselves under active,
separate redesign — [PR #1741](https://github.com/stacklok/mecatl/pull/1741) / ADR 0358
("workspace enrollment preserves session authority") merged as the approved Plan/Interface
contract; its implementation (#1809) is still open. That approved contract changes
`exactTools`'s semantics from a full-catalog replacement to a broker-registration-key
delta against a persisted ledger, keeping the same method signature — CONFIRMED still
unimplemented as of this writing (`CompleteWorkspaceEnrollment`'s body on `main` is still
the naive full-replace this plan's AC3.2 describes). This plan's Ceiling requirement is
written as a property of `CompleteWorkspaceEnrollment`'s CONTRACT — whatever tool set it
computes, for an agent-bound session it must never exceed the Ceiling — not a specific
internal implementation, so it composes with #1809 landing before, after, or concurrently
with this plan. Do not re-design the ledger/delta mechanics here; that is #1741/#1809's
scope.

The originally-scoped adapter-layer guards (`SetMode`; the
`needsRehydration()`/`engineAndEnvironmentFor` choke point covering `StartRunContent`,
`RetryFailedRun`, `CompactSession`, and `resumeFromAwaiting`; `LoadSessionWithMCP`) still
matter and still fail closed — but now as catalog-consistency and defense-in-depth, not as
the sole enforcement mechanism: an agent-bound session's catalog should never OFFER a tool
its Authority Ceiling would refuse to execute, even though the Ceiling itself makes actual
execution safe regardless. The two newly-found call sites join this list. Per
[ADR 0353](../adr/0353-session-scoped-agent-identity.md)'s fail-closed decision.

**Acceptance:**
- AC3.1: An agent-bound session's bound `session.Authority` carries a write-once `Ceiling`
  — the def's resolved tool/resource-capability set — set once at bind time
  (`BindAuthority`/`RestoreLabels`), never re-derived. An ordinary (non-agent-bound)
  session's Authority carries no Ceiling (unrestricted) — additive, not a behavior change
  for any existing session.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_AuthorityCeilingBoundOnce`
- AC3.2: The two verified aggregate methods that can widen a bound session's tools
  post-bind — `GrantToolAuthority` and `CompleteWorkspaceEnrollment` — both reject any
  grant that would set a tool outside an agent-bound session's Ceiling; fixtures that
  attempt to grant a direct-MCP tool (`GrantToolAuthority`) or a broker tool
  (`CompleteWorkspaceEnrollment`) the def's ceiling doesn't include both fail, rather than
  silently widening `CapabilitySet.Tools`. This is the structural guarantee: any FUTURE
  authority-widening aggregate method, or an adapter call site this plan doesn't
  enumerate, is bound by the same check by construction, not by remembering to add it to
  a list.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_GrantToolAuthorityRespectsCeiling`, `TestSessionScopedAgentIdentity_Scenario3_CompleteWorkspaceEnrollmentRespectsCeiling`
- AC3.3: `SetMode` is rejected with `InvalidArgument` outright for an agent-bound session —
  the mode is fixed for the session's entire lifetime.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_SetModeRejected`
- AC3.4: An agent-bound session with an EMPTY provider/model selector (the def resolves
  its own provider/model — the common case), default (non-no-fs) profile, and a deployment
  with no `Config.MCPBroker`/`Config.LearnedSkills` configured — the "boring" configuration
  under which `needsRehydration()`'s OTHER clauses are all false, so a fix that only
  touches `rehydrateSession` would be a silent no-op — fails closed at
  `engineAndEnvironmentFor` (a clear error, never the deployment's default engine) whenever
  its in-memory per-session engine registration is lost (simulated process restart, or any
  other eviction of the live registration), reached via EACH of `StartRunContent`,
  `RetryFailedRun`, `CompactSession`, and `resumeFromAwaiting`.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_RestartFailsClosedOnBoringConfig`
- AC3.5: `LoadSessionWithMCP` is rejected with `InvalidArgument` for an agent-bound session
  whenever the caller supplies client MCP servers on resume.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_LoadSessionWithMCPRejected`
- AC3.6: The lazy per-tool broker-OAuth grant flow (`rebuildGrantedAuthorizationEngine`)
  and the workspace/bundle enrollment RPC (`ConnectWorkspaceServices`) are both
  skipped/rejected for an agent-bound session, so its catalog is never widened by either —
  reinforcing AC3.2, since a session whose catalog can't be widened this way never even
  offers a tool its Ceiling would refuse to execute.
  - verify: `TestSessionScopedAgentIdentity_Scenario3_BrokerAuthorizationAndWorkspaceEnrollmentSkipped`

Implementation note, not a separate AC (pinning it would test an internal code path, not
observable behavior): `engineAndEnvironmentFor`'s ordinary mode-switch/promotion rebuild
branches (CASE 1 and CASE 2) need no direct edit — AC3.3 makes a mode change on an
agent-bound session impossible, and AC3.4 makes every entry into `engineAndEnvironmentFor`
fail closed before either branch could fire, so both are closed transitively. A future
refactor of those branches should not need to touch this feature's guards at all; if it
does, that is a sign the transitive closure broke and AC3.3/AC3.4 should catch it directly.


## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Agent-as-session-root delegation (`Subagent`/`Parallel`/`Team` from an agent-bound session) | a later ADR/issue if a concrete use case appears | ADR 0353 named non-goal |
| Per-caller authorization on which `agent_definition_name` values a caller may request — including the def's own `hooks:` local-command-execution surface this grants, not just its tool selection | deployment-level access control (separate mecak8s deployments) | ADR 0353 named non-goal |
| Persisting the def's authority so restart/MCP-resume can rebuild the SAME restricted catalog instead of failing closed | [issue #1796](https://github.com/stacklok/mecatl/issues/1796) | ADR 0353 Consequences |
| Session-configurable operator posture (strict/trusted/auto/yolo) | [issue #1784](https://github.com/stacklok/mecatl/issues/1784) | ADR 0353 named non-goal |
| PR #1730's own permission-mode vocabulary (ADR 0375) | PR #1730 (independent, non-conflicting work) | not this plan's concern |

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
  `Authority.Ceiling` mechanism and its enforcement in `GrantToolAuthority`/
  `CompleteWorkspaceEnrollment` (AC3.1/AC3.2 — discovered in the latest review round,
  the biggest of these), and the `hooks:`-execution-surface and discovery-tier-blindness
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
