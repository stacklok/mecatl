# ADR 0083 — Expose the routing miss/gate reason on delegation-start events

- Status: Accepted
- Date: 2026-08-06
- Scope: the engine delegation observability surface (`engine/session` event payloads, `engine/agent` routing gates + dispatch) + the wire (`contracts/proto/mecatl/v1/harness.proto`, `internal/adapter/server` mappers). Additive proto fields only; no `port.LLMRequest`, no config change.
- Supersedes: none (it EXTENDS [ADR 0031](./0031-subagent-model-router.md), [ADR 0034](./0034-team-parallel-model-routing.md), [ADR 0035](./0035-per-delegation-model-surface.md) — ADR 0031 explicitly deferred the wire half of the miss reason as a follow-up)

## Context

[ADR 0031](./0031-subagent-model-router.md) landed the OPT-IN semantic model router and scoped the client-wire projection as a follow-up; [ADR 0034](./0034-team-parallel-model-routing.md) added the routed-*hit* fields (`routed_category`/`routed_model`) to the three delegation-start events; [ADR 0035](./0035-per-delegation-model-surface.md) added the unconditional `model`. That covers only the router-*hit* half. On a miss or a gate, every delegation reads identically on the wire — `routed_category=""`, `routed_model=""`, `model=<id>` — while the *why* (`classifier-error`, `unknown-category`, `breaker-open`, `category-target-unresolvable`, …) existed only in operator diagnostics. A UI could not distinguish: router disabled/absent; an explicitly pinned model; a defined agent that intentionally bypassed routing; an inherited default; a classifier failure; or a breaker-open fallback.

The reason was never collected far from the wire — `Deps.SubagentModelRouter` already returned `missReason`, and issue #287 had plumbed it to the dispatch chokepoint — but the internal `parentCaps.routeTask` closure narrowed the signature to `(category, model, ok)`, and the three `maybeRoute*` gates translated any failure to `("", "")`. The reason was dropped at that seam. The reason strings are already vetted gauntlet-#7-safe metadata (never the task prompt or the classifier's output), so putting them on the wire crosses no new trust boundary.

## Decision

Expose a bounded, additive `routing_reason` on the three delegation-start events, empty on a routed hit:

- **Session payloads** — add `RoutingReason` to `SubagentPayload`, `ParallelPayload`, and `TeamMemberSpec` (`engine/session/event.go`), documented as bare metadata, empty on a hit, clamped at the emit site.
- **Gate constants** — add the closed set of harness-authored reasons to `engine/session` (`RoutingReasonPinnedModel`, `RoutingReasonAgentDefPinned`, `RoutingReasonResume`, `RoutingReasonFork`, `RoutingReasonRouterDisabled`, `RoutingReasonTargetUnavailable`, `RoutingReasonBreakerOpen`, `RoutingReasonAborted`). `RoutingReasonTargetUnavailable` means classification hit but the relevant engine factory declined the target; the routed fields are cleared and `model` names the fallback that actually ran. The classifier-side `RouterMiss*` values, the internal `empty-model` code, and composition's category-mapping codes are NOT duplicated there; the event projection accepts their closed static values alongside the harness constants.
- **Plumbing** — widen only the *internal* `parentCaps.routeTask` closure to `(category, model, reason, ok)`; the exported `Deps.SubagentModelRouter` signature is untouched (it already returns `missReason`). The three `maybeRoute*` gates attribute their own gate (`resume`/`fork`/`pinned-model`/`agent-def-pinned-model`/`router-disabled`); the dispatch closure synthesizes `breaker-open`/`aborted` and carries the classifier `missReason` to diagnostics and the event projection. No `WithRouterConfigured` bit: `Deps.SubagentModelRouter == nil` already IS the honest router-absent signal.
- **Attribution precedence** — the Subagent gate (`maybeRouteModel`) attributes the explicit *choice* gates (`resume` / `fork` / per-call `model` / agent-def pin) BEFORE the router-absent gate, so a delegation that pinned its model is never mislabeled `router-disabled` when no router is wired. Composition passes the pinned names separately via `WithPinnedAgents`; absence from `WithRoutableAgents` is not proof of a pin because provider-switched and inline-MCP defs are also ineligible. Those defs, and a ROUTABLE def that still cannot be routed (writable or agent+model factory unwired), report `router-disabled`.
- **Clamp + event-safe allowlist** — one chokepoint `routingReasonPayload` applied at every emit site (subagent foreground + background, parallel `branch_start` incl. cancelled-before-start, team roster). It whitespace-collapses, caps at 200 runes (mirroring `subagentCausePayload`), AND confines the wire value to a closed allowlist (`routingReasonEventSafe` — the `session.RoutingReason*` gates + the `RouterMiss*` constants + the reference composition's two static `category-*` codes). The missReason channel is OPEN to external engine compositions via the exported `Deps.SubagentModelRouter`; known parenthesised composition detail is reduced to its static code, and any other non-allowlisted reason (a provider error body, classifier output, a task excerpt) is substituted with the generic `routing-miss` label on the wire. The verbatim text stays in the operator-diagnostics channel (`logRouterMissReason`). This enforces gauntlet #7 at the projection rather than trusting every composition to vet its own strings.
- **Wire** — additive proto fields only, next free numbers (Subagent `routing_reason = 18`, TeamMemberSpec `routing_reason = 8`, Parallel `routing_reason = 25`), surfaced via the existing `toProtoSubagent`/`toProtoTeam`/`toProtoParallel` mappers. `Supervisor.MemberRouting` widens 2→3 returns so the Team tool reads the reason back for the roster (the one Changed-signature API change).

Empty-on-hit is the contract: the hit is already fully described by `routed_category`/`routed_model`; a reason is meaningful only on a miss/gate.

## Consequences

- A UI can now distinguish all five cases the issue enumerates (router-absent, pinned-model, agent-def-pinned, inherited-default-via-classifier-miss, classifier-failure/breaker-open) with a structured value, plus the additive `resume`/`fork`/`aborted` gates. mecatui projects it end-to-end (`SubagentMsg`/`ParallelMsg`/`TeamMemberSpec.RoutingReason` → the conversation/fleet/team/parallel block fields → `subagentModelLabel`, rendered as ` · not routed: <reason>`).
- The string channel is OPEN on the miss half only to the *diagnostics* channel; the *wire* is confined to the event-safe allowlist, so an external composition cannot leak arbitrary text onto the client stream. The 200-rune clamp bounds the allowlisted values because consumers are single-line surfaces.
- `engine/api/*.txt` changes: the three payload structs gain a field (Added/minor) and `MemberRouting` changes signature (Changed/breaking pre-v1, CHANGELOG-noted). `Deps.SubagentModelRouter` is unchanged.
- Cost: three `maybeRoute*` gates + the dispatch closure now carry an extra return value threaded to the emit sites; the `routeTaskBody` extraction in `dispatch.go` exists only to keep `parentCaps` under the gocyclo budget after the two added return paths.
- The "loop emits exactly THREE diagnostics lines" invariant is untouched — the reason rides events, not a new diag line.

## See also

- [ADR 0031](./0031-subagent-model-router.md) — the router + the deferred wire follow-up this lands.
- [ADR 0034](./0034-team-parallel-model-routing.md) — the routed-*hit* fields this complements.
- [ADR 0035](./0035-per-delegation-model-surface.md) — the unconditional `model` field.
- [ADR 0079](./0079-delegation-observability-convergence.md) — the bounded-preview delegation-observability contract this extends.
- `docs/design/IMPLEMENTATION-NOTES.md` — the per-subsystem routing description.
