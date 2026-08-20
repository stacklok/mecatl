# Authority evaluator port — acceptance plan

**Phase:** capability — in-process delegated authority, decision behind a port
**Status:** landed, 2026-08-19 (defect-repair pass applied; see *Resolved defects*). Successor to the unmerged `review/authority-attenuation-reconciliation` branch.
**Issue:** [stacklok/mecatl#371](https://github.com/stacklok/mecatl/issues/371) — *An agent cannot widen its own authority.*
**ADR:** ADR-0234 — derived capability sets, decision behind a swappable evaluator port.
**Salvage source:** branch `review/authority-attenuation-reconciliation` (commits `d8966606..5dbef5f6`), worktree `.worktrees/sensitivity`. **Not merged, and not to be merged.**
**Branch:** `feat/authority-evaluator-port`, off `origin/main`.

## What this builds, and how it differs from the salvage source

The salvage branch built a bespoke capability language: a hand-rolled JSON wire
format with its own parser (twice — once in `engine/governance`, once again in
`engine/session`), a hand-rolled evaluator, and enforcement wired into four
separate points in the loop.

This plan keeps the branch's *judgment* and replaces its *mechanism*.

Kept: capability sets narrow by intersection and never widen; derivation happens
at every delegation seam **before** any runtime resource is acquired; the derived
set travels with the run and persists; a resumed child can never widen a parent
that has since narrowed; only an operator-managed definition tier may supply a
ceiling.

Replaced: the wire format becomes a plain struct with no bespoke parser; the four
enforcement points become one, at the single `execute` chokepoint; and the
decision itself moves behind a port with swappable adapters, so a deployment
chooses no enforcement (local/demo), an in-process set check (default), or Cedar
(where operators need rules a capability set cannot express).

```text
derivation (Go, pure)                    decision (port, swappable)
──────────────────────                   ──────────────────────────
child set = parent set                   AuthorityEvaluator.AuthorizeTool
          ∩ definition ceiling             ├─ noop            (no identities)
          ∩ call tightening                ├─ localauthority  (default)
          then one delegation hop          └─ cedarauthority  (opt-in)
```

The definition ceiling needs no new authoring surface: it is the existing
`AgentDef.Tools` allowlist minus `AgentDef.DisallowedTools`, plus the tool names
of the definition's resolved `mcpServers:`, all of which an operator already
writes in `.claude/agents` frontmatter. The salvage branch's parallel
`AuthorityCeiling` JSON field is removed.

The set is a flat list of tool names, and the check at `execute` is a name
comparison. MCP tools carry no special type: composition expands each resolved
server into its `mcp__<server>__<tool>` names at derivation time and the core
compares strings, exactly as `agent.MemberBuild.MCPToolNames` already has
composition name MCP tools for the team supervisor to compare. The one seam that
needs more than a name comparison is the `CallMcpWithQuery` meta-tool, which
addresses a remote tool by `{server, tool}` argument rather than by its
namespaced name; a decorator at the dispatch boundary reconstructs the name and
applies the same predicate.

## Salvage map

Port deliberately, file by file. **Do not cherry-pick** — the salvage commits
couple the surviving logic to the wire format being deleted.

| From the salvage branch | Action |
|---|---|
| `governance.Intersect` / `Descend` / `intersectNames` / `minInt` | port as `governance.Narrow` |
| `governance.Contains` / `namesContain` / `profileContains` | port as the containment helper the spawn seam and the local adapter share |
| `governance.AllowsTool` | drop — becomes the local adapter's body |
| `governance.ParseAuthority` / `Canonical` / `validProfile` / the three kinds | **drop** |
| `governance.AuthorityProfile.Isolated` | **drop** — one consumer, no config surface, and its containment ordering is inverted (see *Resolved defects*) |
| `session.canonicalAuthorityBound` and its private parser | **drop** — the second parser is the defect |
| `agent.deriveChildAuthority` / `childAuthorityRequest` / `parallelChildAuthority` | port; the posture request loses its isolation axis |
| `agent.authorityToolSearch` | port as **request-shaping**, not enforcement — ToolSearch is a catalog query and its result is a tool listing |
| `agent.authoritySpecs` / `authorizedExtraTools` | port as **request-shaping** — filtering `LLMRequest.Tools` builds a correct request; it is never relied on for enforcement |
| the `lookupToolContext` gate | **drop** — the one genuinely redundant enforcement site |
| — (new) | a `CallMcpWithQuery` decorator in `engine/agent`, and per-server resource reach derived from the carried names |
| `agent.authorityDecision` at `execute` | port; body becomes the port call |
| `Deps.AuthorityRevoker` | **drop** — the port replaces it |
| `session.BindAuthority` / `AuthorityBound` / `RestoreAuthorityBound` | port; payload becomes a plain struct |
| `sessnap` + `eventsource` authority fields | port; same fields, new payload |
| `DefinitionIdentity` (`explicit:<name>`) | port — it becomes the policy principal |
| `internal/app/root_authority.go` | port, **correct at mint** (see Scenario 6) |
| `bindRootAuthority`, `copyAuthorityProvenance`, `team.go` direct-team authority | port as-is |
| `AgentDef.AuthorityCeiling` + driver proto `authority_ceiling` | **drop**; the ceiling is `Tools` − `DisallowedTools` |
| `docs/adr/0226-authority-attenuation-on-current-main.md` | **rewrite as ADR-0234** (0226 is taken on `main`) |
| `docs/acceptance/authority-attenuation-reconciliation.md` | superseded by this plan |
| Algebra tests | port; wire-format and catalog-filter tests are dropped with their subjects |

## Why these scope cuts

- **ADR-0234 (this plan writes it)** — authority is local runtime attenuation of
  a derived capability set, distinct from owner identity, credentials, and
  external authorization. The decision is a port; Cedar is one adapter.
- [ADR-0214](../adr/0214-environment-persistence.md) — durable `EnvironmentRef`
  selects an execution environment. Authority constrains compatible execution
  posture before acquisition; it does not authenticate a filesystem path.
- [ADR-0038](../adr/0038-event-sourced-rehydration.md) — an event fold needs
  explicit creation metadata for facts events do not carry.
- [ADR-0204](../adr/0204-caller-identity-threading.md) — the session owner is a
  separate axis from the capability set. Both are stamped at the same seam and
  neither substitutes for the other.

## In scope — 7 scenarios, in implementation order

Each scenario is independently green. Scenarios 1–6 satisfy #371; Scenario 7 is
the new capability and may land separately.

### Scenario 1 — narrowing is pure, total, and monotone

`governance.Narrow` computes a child's capability set from a parent's set and a
definition ceiling. It performs no I/O, touches no session, and has no
serialization. There is no operation anywhere in the package that widens a set.

**Acceptance:**

- AC1.1: `Narrow` returns the set intersection of tool names, the lower of the two delegation depths, and the conjunction of each execution-posture flag; the result is never a superset of either input on any axis.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario1_NarrowIsIntersectionOnEveryAxis`
- AC1.2: Every operation on a capability set is monotone downward — for any two valid inputs, each input contains the result. No union, widening, or additive operation exists.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario1_OperationsAreMonotone`, `FuzzAuthorityOperationsAreMonotone`
- AC1.3: Consuming a delegation hop at remaining depth zero is an error, not a silent pass or a clamp.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario1_DepthExhaustionIsAnError`
- AC1.4: A capability set has exactly one in-tree representation and exactly one place that serializes it; no second parser, canonical form, or field-count check exists in any package.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario1_SingleRepresentationAndSerializer`

On AC1.2's two proofs: the pinned `Test…` name is the resolvable one — it runs
the fuzz target's seed corpus under plain `go test`. The `Fuzz…` target is the
same property under a real fuzzer, and it needs a line added to `task fuzz`,
which enumerates targets explicitly rather than discovering them; without that
line it never runs in CI.

---

### Scenario 2 — the derived set persists and round-trips without a bespoke format

A session carries its derived set durably. The payload is a plain struct
marshalled by `encoding/json`; adding a field later is additive and needs no
version negotiation, because nothing hand-parses it.

**Acceptance:**

- AC2.1: A bound session persists its capability set, its provenance, and any resolved definition identity before its first runnable state, and restores them byte-equivalently through the snapshot and the event fold.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_SetRoundTripsThroughSnapshotAndFold`
- AC2.2: The persisted payload contains no path, credential, token, header, catalog pointer, runner, or raw identity claim.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_PayloadExcludesSensitiveRuntimeData`
- AC2.3: A record that claims a capability set but cannot be decoded fails closed before a run starts, with a diagnostic naming the failure; a genuinely pre-feature record with no set is classified legacy and behaves as documented.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_UndecodableSetFailsClosedLoudly`
- AC2.4: A decode failure is never silently swallowed into a bound-but-empty state; no code path sets "this run is bound" while discarding the error that produced an empty set.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_NoSilentBoundButEmptyState`
- AC2.5: Completed, cancelled, and failed bound sessions retain the same persisted set through Reopen, Interrupt, Recover, and Abandon.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario2_TerminalRecoveryPreservesSet`

---

### Scenario 3 — one evaluator port, one chokepoint, three adapters

The decision moves behind `port.AuthorityEvaluator`. It is consulted at
`Engine.execute` — the single function every dispatch path funnels through — and
nowhere else. The request carries the derived set, so the evaluator checks a
carried value rather than looking one up.

Disclosure and ToolSearch filter to the carried capability set as request construction,
but `execute` remains the enforcement boundary: an omitted or stale tool call is
still independently refused. Sending the model a capability that will be refused
is a wrong request, not a safe one.

One tool addresses its target by argument instead of by name. `CallMcpWithQuery`
takes `{server, tool}` and dispatches to any connected server, so a name
comparison on the meta-tool authorizes the whole manager. A decorator at the
dispatch boundary reconstructs the namespaced name and re-checks it.

**Acceptance:**

- AC3.1: Every tool execution passes the evaluator exactly once, from every dispatch path: sequential, read-parallel batch, awaiting-approval resume, cross-process resume, and guardrail approve-once.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_EveryDispatchPathConsultsTheEvaluatorOnce`
- AC3.2: The request carries the derived set, the tool name, the delegation depth, and a principal comprising definition, instance, and owner; it carries no raw tool arguments, credentials, or Cedar-specific types.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_RequestShapeIsNeutralAndCarriesTheSet`
- AC3.3: A denial and an evaluator failure are distinguishable at the call site and produce different model-visible messages; an evaluator failure fails closed and emits an operator diagnostic.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_UnavailableEvaluatorIsDistinctFromDenial`
- AC3.4: An absent evaluator is a deliberate deployment mode selected by an explicit flag and reported in the build-once posture line; it is never a silent default.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_AbsentEvaluatorIsExplicitAndAnnounced`
- AC3.5: The three adapters — noop, local, and Cedar — satisfy one shared conformance suite, including identical fail-closed behaviour on a malformed request.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_AdaptersSatisfyConformanceSuite`
- AC3.6: Capability filtering at disclosure and at ToolSearch shapes the request only and is never relied on for enforcement. Dispatch refuses independently: a tool that is disclosed but absent from the derived set is still refused at `execute`, and no enforcement site survives between lookup and dispatch.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_DisclosureIsNotLoadBearing`
- AC3.7: A call to `CallMcpWithQuery` is authorized against the remote tool it targets, not against the meta-tool's own name: the decorator reconstructs `mcp__<server>__<tool>` from the call arguments and applies the same predicate as `execute`, refusing with a message naming the reconstructed target. The meta-tool is a transport helper, not a second grant, and is disclosed only when a reachable target exists.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_MetaToolIsAuthorizedAgainstItsTarget`
- AC3.8: A bound run reaches MCP resources through a derived per-server resource capability carried in its set. Resource-only servers are reachable when their own capability is present; aggregate resource operations require a concrete server. The evaluator receives that capability separately from the resource operation action, and no separately authored grant exists.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario3_ResourceReachDerivesFromToolNames`

---

### Scenario 4 — every delegation seam derives before acquiring anything

Subagent (every variant), Parallel branches, the Team tool, and server-created
team members all derive their child's set before a worktree, engine,
environment, or runner is created. A refusal costs nothing. Root creation is a
prior seam: it mints and stamps a complete carried set before this child
derivation begins. For this plan, the operator-managed definition tier is the
existing `AgentOriginExplicit` tier only; `driver` remains excluded even though
it is operator-configured, because a remote driver cannot establish a local
capability ceiling.

**Acceptance:**

- AC4.1: A parent spawning a child that asks for more than the parent holds yields the intersection, on every seam and every Subagent variant including background, fork, structured-output, and per-call model override.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_ChildGetsIntersectionOnEverySeam`
- AC4.2: Derivation completes before any runtime resource is acquired; a refused delegation creates no worktree, engine, environment, runner, or child session.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_RefusalAcquiresNoRuntimeResource`
- AC4.3: The definition ceiling is the resolved `Tools` allowlist minus `DisallowedTools`, plus the expanded tool names of the definition's resolved `mcpServers:`, from an operator-managed definition tier only; a project-, user-, or driver-tier definition cannot establish a ceiling, and a lower-tier definition cannot occupy a higher-tier name.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_OnlyManagedTierSuppliesACeiling`
- AC4.4: A per-call request may only tighten; a call asking for a capability, delegate, or execution posture outside the derived set is refused with a reason naming which check refused it.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_CallTighteningCannotWiden`
- AC4.5: The child's owner and its capability set are stamped at the same seam and neither is inferred from the other.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario4_OwnerAndSetAreIndependentlyStamped`

---

### Scenario 5 — resume never widens

A resumed child re-derives from persisted state and is checked against the
caller's *current* set. A hop already spent is not spent again.

**Acceptance:**

- AC5.1: A resumed child whose persisted set is not contained by the caller's current set is refused; a child persisted before this feature is refused rather than upgraded.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario5_ResumedChildCannotExceedCurrentParent`
- AC5.2: A resume consumes no additional delegation hop and does not re-derive against the definition ceiling.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario5_ResumeSpendsNoAdditionalHop`
- AC5.3: The property holds across a process restart, on both the snapshot path and the event-fold path.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario5_NoWideningAcrossRestart`

---

### Scenario 6 — the root maximum is complete at mint

A root's set is minted from the composed catalog and is *usable*: it permits the
delegation the deployment actually supports. The salvage branch's mint omitted
delegation depth and the isolation posture, which refused every delegation from
every bound root — verifiably, in four `internal/app` team tests that fail on
that branch and pass on `main`.

Half of that failure is closed structurally: the isolation axis is deleted, so
there is no longer a field whose omission denies. The other half is closed by
value and by proof: the mint sets depth explicitly, and AC6.1 requires the
minted root to actually delegate on every seam rather than merely to look
well-formed.

**Acceptance:**

- AC6.1: A session created through the ordinary composition path can spawn a default read-only subagent, a named managed specialist, a Parallel branch, and a Team, and each child receives a non-empty derived set.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_ComposedRootCanDelegateOnEverySeam`
- AC6.2: Every field of a minted root set is populated explicitly at the mint site, and the minted root can consume one delegation hop.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_MintPopulatesEveryFieldExplicitly`, `TestADR_0233_AuthorityEvaluator_Scenario6_MintedRootCanDescend`
- AC6.3: A server-created team, a peer fork, and a scheduled fire each receive a set whose provenance is recorded and whose derivation point is documented; a fork copies its source's set and safe provenance without re-deriving.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_NonSpawnDerivationPointsAreExplicit`
- AC6.4: The composition posture line reports which evaluator adapter is active and whether enforcement is on.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario6_PostureLineReportsEvaluator`

---

### Scenario 7 — Cedar expresses what a capability set cannot

The Cedar adapter adds operator-editable rules — most importantly path scoping,
which a tool-name set cannot express. Policies are static and only the request
data is per-call. For resource-sensitive rules, the execution boundary supplies
a normalized, non-secret resource descriptor for recognized calls; it never
forwards raw tool arguments to the evaluator.

**Acceptance:**

- AC7.1: The shipped policy set is static and contains no generated text; per-call variation rides request-scoped entity attributes derived from the carried set, and nothing is registered or removed per subagent.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_PolicyIsStaticAndDataIsPerRequest`
- AC7.2: An operator rule can deny a capability the carried set permits — including confining a definition to a path subtree — and cannot grant one the carried set omits.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_OperatorRuleTightensButCannotGrant`
- AC7.3: An entity hierarchy linking an instance to its definition is used only for tightening; a policy granting a capability to a definition group is rejected by a shipped lint or guarded test, because Cedar membership widens.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_DefinitionGroupGrantIsRejected`
- AC7.4: The Cedar dependency appears only in the adapter under `internal/`; the engine module's dependency closure is unchanged and its standalone build still passes.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_CedarStaysOutOfTheEngineModule`
- AC7.5: The adapter is off by default and selected by an explicit flag; a policy set that fails to load is a startup failure, not a silent fallback to permit.
  - verify: `TestADR_0233_AuthorityEvaluator_Scenario7_PolicyLoadFailureIsFatal`

---

## The final vertical test

One offline test that exercises the whole stack in one run, with reference
adapters and no network. It is the plan's single end-to-end proof and it must
fail if any layer regresses.

`TestADR_0233_AuthorityEvaluator_VerticalSlice`

1. **Compose** a session through the ordinary `app.Build` path with a managed
   `.claude/agents` definition `code-reviewer` whose frontmatter declares
   `tools: [Read, Grep]`.
2. **Assert the root is usable** — its minted set is non-empty on every axis and
   permits delegation.
3. **Spawn** `Subagent{agent:"code-reviewer", mode:"read-only"}`. Assert the
   child's derived set is exactly `{Grep, Read}` with one hop spent, and that the
   child's owner matches the parent's.
4. **Deny at dispatch / stale disclosure** — the specialist's inline MCP catalog
   exposes `mcp__slack__post_message`, although the composed parent never held that
   inline-server capability. Assert refusal at `execute` with the authority reason,
   that the remote body never runs, and that the refusal is not reported as an unknown
   tool.
5. **Allow at dispatch** — the child calls `Read`. Assert it executes.
6. **Deny through the meta-tool** — in a second ordinary `app.Build` run, narrow a
   persisted root snapshot so `mcp__slack__post_message` is absent, then call
   `CallMcpWithQuery{server:"slack", tool:"post_message"}`. Assert refusal names
   the reconstructed target and that the remote call never left the process.
7. **Restart** — persist, drop the process state, reload from the snapshot. Assert
   the child's set survives byte-equivalently.
8. **Resume under a narrowed parent** — write a new current parent snapshot with
   `Grep` removed and resume the child. Assert the resume is refused because
   the persisted child set is no longer contained. There is deliberately no broad
   production evaluator or authority-mutation injection seam: the snapshot models an
   externally persisted narrowing, while evaluator-outage proof remains at the engine
   adapter seam (`TestADR_0233_AuthorityEvaluator_Scenario3_UnavailableEvaluatorIsDistinctFromDenial`).
9. **Swap the adapter** — rerun the composed policy case against Cedar in
   `TestADR_0233_AuthorityEvaluator_VerticalSlice_Cedar`.
10. **Operator rule** — add an operator policy confining `code-reviewer` away from
    `/workspace/vendor`. Assert a `Read` the carried set permits is now refused, and
    that no policy can grant `Write` back.

Steps 9 and 10 need the Cedar adapter, which Scenario 7 may land separately, so
the policy proof is a **second test** — `TestADR_0233_AuthorityEvaluator_VerticalSlice_Cedar`.
The evaluator-outage proof remains an engine-adapter test because `app.Build` selects
only configured production adapters; it intentionally exposes no evaluator-injection
seam solely for a vertical test. The two tests are:

- `TestADR_0233_AuthorityEvaluator_VerticalSlice` — steps 1–8. Proves the ordinary
  `app.Build` composition, execution, stale disclosure, meta-target, persistence, and
  narrowed-resume paths with no Cedar dependency.
- `TestADR_0233_AuthorityEvaluator_VerticalSlice_Cedar` — steps 9 and 10, run
  against the same composed session. Gates Scenario 7.

Keeping them one test would make the #371 proof depend on an optional scenario.

## Out of scope

| Item | Defer-to | Rationale |
|---|---|---|
| External credentials, token exchange, gateway-visible agent identity | [#372](https://github.com/stacklok/mecatl/issues/372) | outbound boundary, not local attenuation — see *The outbound credential* below |
| Embedding ToolHive vMCP as the downstream authorization boundary | [#372](https://github.com/stacklok/mecatl/issues/372) | its two benefits both sit behind the same blocker — see *Embedding ToolHive vMCP* below |
| Argument- and resource-level capability constraints | future authority vocabulary | the set is tool-name granular by decision; raised by PR 617 and deliberately deferred |
| A live read of the parent's current authority on every operation | — | deferred: the check is against a value carried on the run, per [#371](https://github.com/stacklok/mecatl/issues/371) AC1. Nothing narrows a live parent today; revisit when a revocable credential exists |
| MCP prompts (`ListPrompts` / `GetPrompt`) | — | not a tool surface: their consumers are composition-time slash-command construction, a gRPC client API, and the prompt expander. There is no `Execute` to gate |
| Minting signed instance credentials | [#478](https://github.com/stacklok/mecatl/issues/478) | issuer infrastructure is its own slice |
| A containment predicate over resolved MCP targets | [#377](https://github.com/stacklok/mecatl/issues/377) deferral | needed when resource-level authority arrives, not before |
| Capability scopes below exact tool names (Bash subcommands, action grammars) | future authority vocabulary | the set is tool-name granular by decision |
| Whether a scheduled fire derives from its creator | [#373](https://github.com/stacklok/mecatl/issues/373) | a design decision, not a fix; Scenario 6 only requires the derivation point be explicit |
| CAS, storage MACs, protection against direct snapshot mutation | [#374](https://github.com/stacklok/mecatl/issues/374), [#385](https://github.com/stacklok/mecatl/issues/385) | store integrity is a separate threat |
| Tenant isolation and cross-caller access | [#368](https://github.com/stacklok/mecatl/issues/368) | ownership is a different question |

## The outbound credential

This plan is the local half of a two-half design, and the halves are not in
tension. What this plan builds is the in-process check: derive a set in Go, carry
it on the run, refuse at `execute`. What it does not build is the credential an
MCP call presents when it leaves the process.

Today, that credential is a static operator-configured header per server
(`mcp.ServerConfig.Headers`) — the same value for parent and child. The later
design ([`docs/agent-identity-outbound.md`](../agent-identity-outbound.md),
Hop 3) replaces it with a delegated token: `sub` the user, `act` the agent
definition, `aud` the gateway, `cnf` bound to a key the broker holds at a
different uid. There is no parent bearer token copied into a child; parent and
child tokens are siblings, and the narrowing is enforced by the authorization
server at mint time — mecatl asks and the server refuses a widening ask.

So the credential is not a gap in this plan's machinery. It is a different hop,
owned by the authorization server and the broker, tracked as
[#372](https://github.com/stacklok/mecatl/issues/372), and blocked upstream in
ToolHive (no `client_credentials` grant, no provisionable confidential client,
`actor_token.sub` bound to `client_id`). The outbound design's own note says
authority must exist as a runtime type first, because it gates everything the
gateway could later enforce against.

The constraint this places on the present work: where a choice would foreclose
the delegated-token shape, take the option that keeps it open. Concretely, a
definition's inline MCP headers are treated as an opaque per-definition value, so
a minted token can replace them without changing the derivation or the check. Do
not model per-instance credentials — [#377](https://github.com/stacklok/mecatl/issues/377)
decision 2 keys external authorization on the definition, and reserves the
instance for auditing.

## Embedding ToolHive vMCP

PR [617](https://github.com/stacklok/mecatl/pull/617) (`docs/agent-identity-mcp-policy-attenuation.md`
on its branch — a research note, not a design record, and unmerged) proposes routing an attenuated child's MCP
traffic through an embedded ToolHive vMCP, so the downstream authorization
decision happens outside mecatl. Deliberately not done here. Its two benefits
both sit behind something already deferred: a boundary downstream of mecatl needs
the child to present a *distinct* identity, which is the Hop 3 credential blocked
on #372; and Cedar-expressible argument and resource constraints are the
tool-name-granularity deferral in the table above. Embedding now would buy a
second copy of the name check this plan already performs.

Three facts to carry, so the option stays cheap later rather than needing
rediscovery:

- **The attach point already exists and this plan does not threaten it.**
  `internal/app/agentdefs.go` (`defMCPTools`) already builds a scoped manager for
  an inline `mcpServers:` entry with its own URL and headers, and the client is
  Streamable-HTTP-only (`internal/adapter/mcp/mcp.go`, `Server.dial`). Pointing
  that manager at an in-process handler is configuration, not architecture. The
  decision to treat a definition's inline headers as an opaque per-definition
  value keeps it open.
- **The brood-box precedent needs re-proving.** It embeds vMCP against ToolHive
  `v0.30.0`; mecatl pins `v0.40.0`, and authorization is exactly what moved
  between them — ToolHive's own `vmcp/server` package records that authz moved to
  the core
  admission seam, `Config.AuthzMiddleware` is vestigial on the Serve path, and
  `Config.Authz` is the supported input. So brood-box proves vMCP works as a
  library with a caller-owned handler; it does not demonstrate the v0.40.0
  authorization surface.
- **Global logger mutation is a prerequisite, not an implementation detail.**
  brood-box redirects ToolHive's *global* zap logger because the library mutates
  it. This repo routes diagnostics through the injected `port.Diagnostics`, bans
  package-level slog in `engine/` and `internal/` via `forbidigo`, and has
  mecatui redirect the slog default so ambient third-party logging cannot corrupt
  the alt-screen. Resolve upstream before embedding.

## Resolved defects carried from the salvage branch

Seven defects in the salvage design were resolved before this plan was
implementable. Recorded here because several resolutions are deletions, and a
deletion leaves no code to explain itself.

**Where to read the cited code.** Paths in this section point at the **salvage
branch**, not at this tree: `review/authority-attenuation-reconciliation`, checked
out at `.worktrees/sensitivity`. Read them there. Two consequences for an
implementer:

- Nothing here fails `docs/lint`'s citation guard, because its corpus is
  `docs/design/*.md`, `docs/adr/*.md`, and the architecture guide — acceptance
  plans are outside it.
- **ADR-0234 is inside that corpus.** When you write it, do not copy these
  citations unqualified: either re-point them at the paths the port actually
  creates, drop to bare symbol names (a slash-free span is treated as a prose
  back-reference and skipped), or mark the span `lint:not-a-citation`. A
  salvage-branch path in an ADR is a dead citation on `main`.

1. **The ceiling is a flat name set.** MCP tools were absent from the salvage
   ceiling entirely (`internal/app/agentdefs.go:292` — a server's tools do not
   count toward the `tools:` filters), so a definition with `mcpServers:` had a
   ceiling that omitted most of what it could call. Composition expands the
   resolved server names; the core compares strings. No new type in `engine/tool`;
   the precedent is `agent.MemberBuild.MCPToolNames`.
2. **`CallMcpWithQuery` is authorized against its target.** A decorator in
   `engine/agent`, alongside the request-shaping `authorityToolSearch`. The cost,
   accepted: the core learns one tool's argument shape.
3. **The root mint sets depth.** One line in `internal/app/root_authority.go`,
   plus AC6.2's `Descend` proof. The residual is recorded below.
4. **`AuthorityProfile.Isolated` is deleted.** It had one consumer
   (`engine/agent/subagent.go:255`), no configuration surface — it was computed
   from runtime facts as `!writable && hasForker` — and its containment ordering
   was inverted: `engine/governance/authority.go:346` demanded the container hold
   `Isolated`, so the *safer* posture required the *stronger* grant, and an
   unflagged root could not spawn an isolated child. Deleting it makes both
   posture probes agree on FileSystem + DirectWrite and removes half the
   root-mint failure rather than papering it.
5. **Resource reach derives from tool names.** A name set cannot express
   "`github` resources but not `slack`" — resources have no tool names, only
   `{server, uri}`. Deriving reach from the names already carried avoids a second
   grant to keep consistent. Resource tools are main-catalog-only today
   (`internal/app/catalog.go:274`, one caller at `:213`), so this is a rule for a
   surface no child currently reaches; it exists because the dead-end argument at
   `internal/app/catalog.go:598-604` that put `CallMcpWithQuery` in the no-FS
   child catalog applies verbatim to resources.
6. **Disclosure is request construction, not enforcement.** See AC3.6.
7. **AC1.2 asserts a property, not a naming taboo.** Its former structural clause
   could only have been a name denylist — Go reflection cannot enumerate
   package-level functions, and an AST walk sees signatures, not semantics — so
   `func Combine(a, b Authority) Authority` would have passed it. A monotonicity
   fuzz over the four real operations proves what the clause gestured at.

## Documentation work this plan owns

1. **ADR-0234 records this decision.** It is not a port of the salvage branch's
   ADR: `0226` is taken on `main` by the dream-consolidation ADR, and the salvage
   branch's `0226` never merged, so there was nothing to supersede.
2. **Do not restate the salvage ADR's decision 4.** It required enforcement at
   advertised specs, lookup, hydration, and dispatch, and called that defence in
   depth. It was not: all four invoked the same predicate, so it was one layer
   with four call sites. ADR-0234 names `execute` as the single enforcement
   boundary, and separately records that filtering advertised specs and ToolSearch
   survives as *request construction* — which is why the plan drops only
   `lookupToolContext`. Record the salvage filter's actual failure too: on an
   evaluator error `authoritySpecs` returned nil, yielding a tool-less request, an
   empty turn, and a no-progress nudge. That is an argument for the filter failing
   loudly, not for deleting it.
3. **Amend #371 AC1.** It requires authority to be "a value carried on the run,
   not a lookup against a mutable source at point of use." Record that a
   tighten-only check against a carried value satisfies the criterion's purpose,
   that a Cedar policy file *is* such a mutable source, and that this is why
   Cedar is opt-in while the default adapter looks nothing up.
4. **Cite the independent convergence on the meta-tool decorator.** ToolHive
   v0.40.0 reaches AC3.7's design from the other direction, twice. Its
   `vmcp/server` package refuses to combine `Authz` with `OptimizerConfig`,
   because the optimizer rewrites calls into virtual `find_tool`/`call_tool` tools
   that would pass a name-keyed gate; and it *permits* `CodeModeConfig` with
   `Authz` precisely because a script's inner calls are re-authorized by real name
   through the core admission seam. Same failure mode, same fix, arrived at
   separately. Worth recording in ADR-0234 — it makes the decorator a converged
   design rather than a local invention.
5. Update `docs/architecture.md` and `docs/design/IMPLEMENTATION-NOTES.md` for
   the port and the derivation seams.
6. Update `user-docs/` for the evaluator flag and the operator policy file.

## Definition of done

1. `task lint` and `task test` pass.
2. `task docs` regenerates `llms.txt` and passes the strict documentation gate.
3. `task api:check` passes; any intentional engine public API change has updated
   `engine/api/*.txt` and an `engine/CHANGELOG.md` note classified per
   `engine/COMPATIBILITY.md`.
4. `task test:engine-standalone` passes — the Cedar dependency has not leaked
   into the engine module.
5. `task ac-trace-strict` resolves every acceptance proof in this plan once it is
   landed.
6. The named scenario tests and the vertical slice pass offline with reference
   adapters; no test calls a live provider or network service.
7. `go run ./cmd/mecademo` still prints a complete offline session.
8. ADR-0234 exists, and the #371 AC1 amendment is recorded on the issue.

## Deferred decisions and known risks

- **Delegation depth keeps two readings, deliberately.** In the held position
  (`Descend`) zero means *exhausted*; in the requirement position (`Contains`,
  `a.depth >= other.depth`) zero means *no requirement*. Of six construction
  sites, four legitimately want zero, which is why rejecting zero in the
  constructor is wrong and why splitting the positions into an `Authority` and a
  `Requirement` type was considered and declined as more machinery than the
  problem. The accepted residual: a future held-authority construction site that
  omits depth is silently unable to delegate, and only AC6.2's `Descend` proof
  catches it — at the root, not at the new site.
- **Depth is not load-bearing today.** No harness path can reach depth two: a
  subagent child's catalog has no `Subagent`, a team member's has no `Team`, a
  Parallel branch is one hop, and `internal/adapter/server/team.go:157`'s own
  comment says "one structural child hop." No-nesting is enforced by catalog
  composition. Deleting the counter was considered on exactly that ground and
  declined to keep the axis available for remote execution, where onward
  delegation becomes reachable. Revisit if it stays unused.
- **Two decisions want re-checking against PR 617.** Keeping depth (above) and
  deleting `Isolated` both assume 617 does not key authorization on nesting depth
  or execution posture. Check before implementing either.
- **Operator error is a new exposure.** A capability set cannot be widened by
  any code path, but a Cedar policy can be authored to grant too much. Accepted
  deliberately: it is the price of rules being expressible, and the reason the
  Cedar adapter is opt-in.
- **Path-prefix arithmetic cannot live in Cedar.** `like` is string-to-pattern;
  there is no set-level prefix containment. Any prefix reduction happens in Go
  and Cedar checks the result.
- **The store remains a root of trust** for a resumed set, as recorded on #371's
  hazard note. Unchanged by this plan.
- **The salvage branch is red.** `TestMaxTeamTokensPropagates` and three sibling
  team tests fail in `internal/app` on it and pass on `main`. Do not use it as a
  reference for expected behaviour without checking against `main` first.

## Exit criteria

When every point under *Definition of done* holds on this branch, this plan is
satisfied and #371 can be closed with its AC1 amendment recorded.
