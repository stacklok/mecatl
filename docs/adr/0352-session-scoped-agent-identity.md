# ADR 0352 — Session-scoped agent identity

- Status: Proposed
- Date: 2026-09-22
- Scope: `CreateSessionRequest`/`sessions.create()`, composition-layer agent-definition
  engine construction (`internal/app/agentdefs.go`), session persistence
  (`engine/adapter/sessnap`)
- Supersedes: none

## Context

[ADR 0013](./0013-agent-definitions.md) gave mecatl named specialist `AgentDef`s — tool
allowlist, model, run limits, MCP servers, hooks, memory — but only reachable as a
**child** (`Subagent(agent=...)`, a team member). `CreateSessionRequest` has no field to
select one; a session's root engine is always the deployment's default explorer, built
once at `app.Build` time.

The gap is bigger than one missing field: `AgentDef` already models a complete
security-and-behavior configuration. Once a session can bind to one directly, one
instance can run several *different* configurations side by side, each reachable as its
own top-level session — wired up by naming the def at `CreateSessionRequest` time, no
parent session or delegation machinery required. Because the mechanism is reuse
(`buildAgentDefEngine`, below) rather than a parallel path, the same def keeps working
unchanged as a delegate too: one spec, two shapes, no duplication.

Issue [#1053](https://github.com/stacklok/mecatl/issues/1053) is the motivating case: a
Slack bot wants several distinct `@mention`-able identities, each backed by its own
`AgentDef` and restricted to its own tools — the one open item under
[#1395](https://github.com/stacklok/mecatl/issues/1395)'s "Identity and expansion."
mecak8s is the deployment target.

`buildAgentDefEngine` (`internal/app/agentdefs.go`) is the one function that turns an
`AgentDef` into a real engine, already reused by three call sites (startup Subagent
construction, `agent`+`model` override, `agent`+`read-write` override). A session-root
call site is a fourth use of the same step — except `buildAgentDefEngine` bottoms out in
`newChildEngineForProvider`, a **child-shaped** construction (no guardrails, headless
auto-deny, optional ask-reviewer) for reasons specific to delegation that don't apply to
a session that **is** the root. Splitting the reusable catalog/prompt/model/limits
construction from that child-shaped Deps choice is the real work this ADR requires.

This ADR does not touch operator-tier permission posture. PR
[#1730](https://github.com/stacklok/mecatl/pull/1730) (draft, unmerged — its own working number, 0351, collided with an unrelated ADR that landed first)
proposes a `--permission-mode` vocabulary and holds that composition-bearing tiers
(posture, allow-all, substitution loosening) stay operator-only, non-session-selectable,
and that agent-definition frontmatter can't name one. This ADR is consistent with that:
it only ever resolves the existing session-level `PermissionMode` enum (`plan`/
`default`/`acceptEdits`), never posture.
[Issue #1784](https://github.com/stacklok/mecatl/issues/1784) tracks whether posture
itself should ever become session-configurable — an open question this ADR deliberately
leaves unanswered.

A stale, never-landed `add-slack-bot-adr` branch sketches the Slack bot's own approval
UX — a `PendingAsk` mapped to Slack's `suspended` status with Block Kit approve/deny
buttons. It's cited by issue chain below, not ADR number (its reserved slot, 0254, is
now an unrelated document). It matters here only because this ADR's "ordinary
main-session behavior" decision keeps that UX buildable.

Naming note: this repo separately uses "agent identity" for SPIFFE-style workload
identity / MCP credential exchange (`agent-identity-fold`/`-issues` branches) — a
different axis (cryptographic caller identity, not persona/config). To avoid that
collision baking into the protobuf/SDK surface, the new field is named
`agent_definition_name`, not `agent_id` — it names an `AgentDef.Name`, not a security
principal or a workload identity.

## Decision

**Extend `CreateSessionRequest` with an optional `string agent_definition_name` field.**
Empty reproduces today's behavior; an unresolvable name is `InvalidArgument` (mirroring
`provider_id`). A dedicated RPC was rejected — it would duplicate `mode`/`limits`/
`mcp_servers`/`reasoning_effort`/debug fields and create a second call path to keep in
sync, against the existing "one seam" convention (`provider_id`/`model_id`/`profile`
already live on the same message).

**The def's tool scope is a strict, non-widenable ceiling, covering core and MCP tools
uniformly.** When `agent_definition_name` is set, the catalog is built **exclusively**
from the def's `tools`/`disallowedTools` and `mcpServers:` — full replacement, not a
filter. `CreateSessionRequest.mcp_servers`/`debug_mcp_servers` are **rejected** as
`InvalidArgument` when it's set (they'd let a caller add tools the def never declared);
already-configured global MCP servers the def doesn't reference stay unexposed. Goal:
one `AgentDef` file tells you exactly what the session can call.

**Omitted `tools:` resolves against the same bounded base set a Subagent child already
gets, never the ordinary root's full catalog.** `AgentDef.Tools` empty already means
"the call site's default set" ([ADR 0013](./0013-agent-definitions.md)); for the
existing Subagent path that default is `baseSubagentTools` (core FS tools + Shell) —
already excludes global MCP, delegation, scheduling, and memory-management tools by
construction, since they were never in that available set to begin with. A session-root
call site reuses the identical `scopedToolNamesMode` step over that same base set, so
omitting `tools:` on a root-bound def can never implicitly pull in anything wider than a
child def already gets. A def author who wants more (WebSearch, memory tools, …) lists
them explicitly.

**No delegation in v1.** `Subagent`/`Parallel`/`Team` are excluded, mirroring the
existing rule for a def used as a child. Keeps the security story bounded; a delegating
specialist is a separate feature if a use case ever needs it.

**Mutation is allowed iff the def's `tools:` allows it.** Unlike the Subagent-delegate
path (forced read-only for dispatcher race-hazard reasons), a session root has no such
constraint.

**Run limits are tighten-only**: request `Limits.max_turns`/`max_tool_calls` may lower
the def's configured values, never raise them — mirroring per-call Subagent overrides.

**`PermissionMode` is tighten-only against the def's `permissionMode`, silently
clamped**, unlike `provider_id`/`model_id` (explicit-wins, below). The asymmetry:
`PermissionMode` has a real ordering (`plan` < `default` < `acceptEdits`, per PR #1730),
so a def author setting `plan` means a real restriction; models have no such ordering,
so caller-wins is correct there and wrong here. A request mode looser than the def's is
clamped to the def's, exactly like `Limits` — never rejected, and never raised. Combined
with the fail-closed `SetMode` rule below, the effective mode is fixed for the session's
entire lifetime, not just at creation.

**`provider_id`/`model_id` are resolved as a single pair, not two independent fields.**
(1) Resolve a base pair from the def's `Model`/`Provider` over the deployment default.
(2) No request override → use the base pair. (3) Request sets only `model_id` → apply it
to the base pair's provider. (4) Request sets only `provider_id` → switch provider and
use *that* provider's default model — never carry over a model the def pinned for a
different provider. (5) Request sets both → use the explicit pair. This fixes a gap in
independent-field resolution: resolving `model_id` and `provider_id` separately could
pair an overridden provider with a model the def picked for a different one. The
resolved pair is persisted for the session's lifetime. An unavailable
definition-pinned provider **fails session creation** — it never silently falls back to
instantiating the def on a different provider. The existing rule "`model_id` without
`provider_id` is `InvalidArgument`" is scoped to account for `agent_definition_name`
supplying the provider — that combination is no longer ambiguous.

**Everything else is ordinary main-session behavior, not child behavior.** Guardrails
stay wired; governance evaluates under `AudienceMain`; a permission ask pauses to
`awaiting` and resolves via normal approve — never child-style headless auto-deny or the
ask-reviewer. Why: guardrails are a content check independent of the tool ceiling and
Slack input is untrusted, so dropping them would be a real regression; and headless
auto-deny would make the Slack bot's planned approval UX (see Context) impossible. This
means `buildAgentDefEngine`'s catalog/prompt/model/limits construction must be separated
from its child-shaped Deps choice — no v1 toggle exists between the two, since nothing
needs the child-shaped variant as a session root.

**Definition hooks compose with operator guardrails via the existing decorator, never
around it.** A def's `hooks:` are wired as the `inner` `HookRunner` under the SAME
`modelhook.Runner` every other main session already uses — the mechanism that already
runs inner first, guardrails second, and merges block-dominant
(`internal/adapter/modelhook/merge.go`). No new composition mechanism: an
`agent_definition_name` session just supplies a non-nil inner instead of the ordinary
no-op. A definition hook can mutate or block a call, but it runs and is merged *before*
guardrails see the result, so it can never replace or bypass an operator guardrail
verdict.

**Other session-shape combinations:**
- `agent_definition_name` and `debug_target_session_id` are mutually exclusive —
  combining them is `InvalidArgument`. They're unrelated concepts (a named specialist
  root vs. the session-debugger admin transport) and combining them isn't meaningful.
- `profile: "no-fs"` composes as further attenuation: filesystem tools and Shell stay
  absent even if the def lists them. Authority only ever narrows — profile, def, and
  deployment ceiling all combine by intersection, never by any one of them restoring
  what another already removed.

**Project memory binds to the session's own placement, not a deployment default.** For
an `agent_definition_name` session, `memory: project` ([ADR 0013](./0013-agent-definitions.md)
issue #33) resolves against *this session's* bound `EnvironmentRef` (ADR 0291's
server-owned placement) — there's no parent session to inherit a workspace from, the way
a child def gets one. Fork/Clear resolve it from the successor's own placement, matching
ADR 0291's existing rule. Under `profile: "no-fs"` there's no workspace to bind to, so
`memory: project` is simply inert for that session (not an error) — `memory: user`
is operator-level and unaffected either way.

**No per-caller authorization on which `agent_definition_name` values may be requested,
in v1.** Access control stays at the deployment level (e.g. separate mecak8s
deployments per bot). Deferred, not dropped — it needs a caller/principal model this RPC
doesn't have.

**`agent_definition_name` is fixed for the session's lifetime and persists on the
snapshot** (same discipline as `ProviderID`/`ModelID` — an additive
`AgentDefinitionName string` in `sessnap`). `ForkSession`/`ClearSession` preserve it.
`CreateSessionResponse` echoes the resolved value as `resolved_agent_definition_name`,
mirroring the existing `resolved_model` echo.

**No special-casing by discovery mechanism.** The new call site consumes
`AgentDefSource` exactly as the three existing ones do, filesystem or remote driver
alike.

**The tool-scope ceiling must survive every engine-rebuild path, not just the initial
build — enforced in v1 by failing closed, not by re-deriving the def's catalog.** A
per-session engine is rebuilt at several points that have nothing to do with
`agent_definition_name`: a `SetMode` (plan/default/acceptEdits) switch, a process
restart (`rehydrateSession`), and a client MCP resume (`LoadSessionWithMCP`). None of
those rebuild paths read the def binding today — they read only
`ProviderID`/`ModelID`/`Profile` and rebuild through the same generic per-session
catalog assembly every other session uses. Left alone, an `agent_definition_name`-bound
session that survives any of these would silently regain the full default catalog. v1
closes this the cheap way: `SetMode` is rejected outright for such a session
(`InvalidArgument` — also what fixes the `PermissionMode` clamp above for the session's
whole lifetime), and restart rehydration / MCP-resume for such a session **fails
closed** (refuses to resume) instead of rebuilding on the default catalog. This costs
session continuity across a restart — an acceptable v1 trade for a Slack-bot identity,
not a real fix. The real fix (persisting the def's authority so every rebuild path can
only narrow it, never re-derive it wider) is tracked in
[issue #1796](https://github.com/stacklok/mecatl/issues/1796).

## Consequences

- **Closes a real gap, elegantly.** Per-agent tool restriction — what #1053 needs — is
  now reachable as a session root, not just a delegate. One instance can host multiple
  specialists concurrently, each its own top-level session; the same def is untouched as
  a delegate, defined once, used in both shapes.
- **Real implementation cost.** `buildAgentDefEngine`'s construction and its
  child-shaped Deps selection are fused today and must be split — the harder half of the
  work, not "just load the data."
- **Contract surface.** New `agent_definition_name` request field +
  `resolved_agent_definition_name` echo need `task generate`, and an `engine/api/*.txt`
  update if core API changes ([ADR 0037](./0037-engine-stability-contract.md)).
- **Named non-goals (v1)**, mirroring ADR 0013's own scope honesty:
  - No agent-as-session-root delegation.
  - No per-caller authorization on identity selection.
  - No child-shaped-behavior toggle — always ordinary main-session behavior.
  - No change to operator-tier posture or how it's selected.
  - No real authority-persistence across restart/MCP-resume — v1 fails closed on those
    paths instead; the full fix is [issue #1796](https://github.com/stacklok/mecatl/issues/1796).

## See also

- [ADR 0013 — Agent definitions](./0013-agent-definitions.md) — the `AgentDef` port and
  `buildAgentDefEngine`.
- [Issue #1053](https://github.com/stacklok/mecatl/issues/1053) — motivating issue.
- [Issue #1395](https://github.com/stacklok/mecatl/issues/1395) — Slack bot roadmap.
- Issues [#881](https://github.com/stacklok/mecatl/issues/881),
  [#882](https://github.com/stacklok/mecatl/issues/882),
  [#883](https://github.com/stacklok/mecatl/issues/883) — Slack bot v1; unmerged planning
  draft sketches the approval UX this ADR keeps buildable.
- [PR #1730](https://github.com/stacklok/mecatl/pull/1730) (draft, unmerged) —
  operator-tier permission-mode vocabulary; posture stays non-session-selectable.
- [Issue #1784](https://github.com/stacklok/mecatl/issues/1784) — open question: should
  posture itself ever become session-configurable.
- [Issue #1796](https://github.com/stacklok/mecatl/issues/1796) — the deferred follow-up:
  persist the def's authority so every engine-rebuild path (restart, `SetMode`,
  MCP-resume) can only narrow it, never re-derive it wider than v1's fail-closed guard.
- [ADR 0037 — Engine stability contract](./0037-engine-stability-contract.md) — engine
  API stability gate.
