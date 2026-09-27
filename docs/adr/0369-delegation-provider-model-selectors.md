# ADR 0369 — Delegation selectors use provider/model pairs and router categories

- Status: Proposed
- Date: 2026-09-26
- Scope: delegated child-engine selection in `Subagent`, `Parallel`, and `Team`; the agent-facing `DiscoverModels` inventory; delegation observability; and model-visible discovery guidance.
- Supersedes: ADR 0031 and ADR 0034 only for the assumption that an explicit delegation selector is a same-provider model string, and ADR 0083 only for its routing-reason representation of an explicit router-category selection. Their taxonomy, classifier, breaker, automatic-routing evidence, and fail-soft automatic-routing rules remain unchanged.
- Superseded by: none

## Context

Delegation currently has two incompatible model-selection paths. A `Subagent` call can pin an opaque model string, but only under the parent provider. The semantic router can choose an operator-defined category, but the parent model cannot request that category explicitly. `Parallel` and `Team` have automatic routing but no per-call selector. Meanwhile, `DiscoverModels` exposes exact provider/model pairs, although no delegation surface accepts that pair.

A router category is not an inference provider: it has no credentials or endpoint and resolves to a model under the parent session's provider. Treating category names as model-id prefixes would violate the opaque-model-id contract. Adding router rows to `ListModels` would also be wrong: that API inventories root-session inference targets, while a category cannot create a root session without a delegated task.

## Decision

Use an explicit `(provider, model)` selector on every model-selectable delegation surface:

- `Subagent` accepts optional `provider` and `model`.
- `Parallel` accepts one optional `provider` and `model` pair applied to every branch.
- Each `Team.members[]` entry accepts optional `provider` and `model`.

The normal default remains omission of both fields. Model-visible guidance tells the agent to leave them unset unless the user requests a model/provider or a concrete task capability justifies an override. Omission preserves inherited/default selection and automatic semantic routing.

A non-empty ordinary provider/model pair mints the def-less child on that exact provider and model. A provider without a model is invalid. A model without a provider keeps the current same-provider override behavior. For this slice, a call that names `agent` rejects `provider`, including `model-router`; existing named-agent plus bare-model behavior remains unchanged. `fork` and `resume` reject explicit selectors because their replay/continuation semantics are provider-bound.

Reserve `provider: "model-router"` on those delegation-only surfaces. Its `model` is the exact name of an enabled operator router category. It resolves through the existing category mapping for the parent provider, bypasses automatic classification and its breaker, and fails with a bounded tool error if unavailable, unknown, disabled, or unresolvable. It never falls back silently and never changes the parent session provider.

Extend `DiscoverModels`, but not `ListModels`, with virtual rows for enabled router categories. Each row has `provider_id: "model-router"`, the category name as `model_id`, a bounded operator-authored `description`, and no inferred endpoint, credentials, or concrete target model. The virtual rows are the single canonical discovery projection alongside inference rows: descriptions participate in literal search, canonical ordering, the cursor digest, pagination, the 32 KiB result limit, and stale-cursor restart. An unfiltered facet includes `model-router` only when it has virtual rows; that is the narrow exception to the inference-provider-only facet rule. The tool specification and factory-built system-prompt posture describe the router selector and the omission-first delegation policy; neither embeds a category list. `ListModels`, root-session model picking, and `CreateSession` remain inference-provider-only.

Reserve `model-router` at provider-registry/configuration admission. No built-in or operator-defined inference provider may use that ID.

Delegation start evidence carries the actual resolved provider as well as the existing actual model and routing fields. An explicit router selection carries a dedicated `explicit_router_category` field; it leaves `routed_category`, `routed_model`, `routing_reason`, and classifier-decision evidence empty, rather than misusing classifier provenance. Mecatui renders it as `selected: model-router/{category} → {provider}/{model}` on Subagent cards, Parallel branches, and Team rosters; classifier-originated selections retain the existing `routed:` label.

## Consequences

The agent can choose an operator-approved category without guessing category names or encoding policy in opaque model IDs. It can use a `DiscoverModels` exact inference pair for a direct child override. Parallel branches and team members gain the same explicit-selection capability as Subagent.

This widens tool schemas, event/protobuf projections, child-engine composition, and the agent-facing discovery contract. Cross-provider child construction must preserve fresh-child isolation, re-derive model-dependent dependencies through factories, and never reuse provider-private replay state. The reserved `model-router` value is intentionally not a server provider, cannot be used to create a session, and is not published by `ListModels`.

No new resource outlives a call: router-category rows are derived from the existing Build-owned operator configuration and existing per-session router construction. No category, selector, or target is persisted as session configuration; delegation events retain only bounded resolved metadata already in the event lifecycle.

## See also

- [ADR 0031](./0031-subagent-model-router.md), [ADR 0034](./0034-team-parallel-model-routing.md), and [ADR 0035](./0035-per-delegation-model-surface.md)
- [ADR 0352](./0352-jev-delegated-model-router.md)
- [Provider architecture](../architecture/providers.md)
- [Subagents and teams architecture](../architecture/subagents-and-teams.md)
- [ADR 0027](./0027-cloud-native.md) resource inventories
