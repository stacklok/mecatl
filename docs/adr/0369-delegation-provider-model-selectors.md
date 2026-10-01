# ADR 0369 — Model aliases and delegation selectors use provider/model pairs

- Status: Accepted in this implementation candidate; authoritative on merge
- Date: 2026-09-26; amended 2026-09-27 and 2026-10-01
- Scope: composition-owned model aliases and project model configuration; delegated child-engine selection in `Subagent`, `Parallel`, and `Team`; the agent-facing `DiscoverModels` inventory; delegation observability; and model-visible discovery guidance.
- Supersedes: ADR 0030 for model-only alias targets and trusted-project `models:` bindings, and ADR 0031 and ADR 0034 for the assumption that an explicit delegation selector and router target are same-provider model strings. Their fixed-provider session replay, slot taxonomy, classifier, breaker, automatic-routing evidence, and fail-soft automatic-routing rules otherwise remain unchanged.
- Extends: ADR 0083 with explicit router-category provenance that is neither an automatic-router hit nor miss.
- Superseded by: none

## Context

Model IDs are opaque and provider-specific. Mecatl's current aliases map a semantic name to only a model string, so each consumer supplies an assumed provider. That assumption prevents one alias from naming the same concrete target everywhere and makes cross-provider delegation depend on a separate provider field. The existing CLI `--model-alias name=model-id` has the same limitation, but no delimiter can safely split an opaque model ID into provider and model components.

The earlier project-model policy also permits a trusted repository to rebind `models.default`, `models.slots`, and `models.aliases` within an operator allowlist. In practice, provider-specific IDs make a model-only cap ambiguous, and repository control over model/provider selection is an unnecessary project authority. Trusting project instructions or tools should not let a repository choose inference infrastructure.

Delegation has two further incompatible selection paths. A `Subagent` call can pin an opaque model string under the parent provider, while the semantic router can choose an operator-defined category but the parent model cannot request that category explicitly. `Parallel` and `Team` have automatic routing but no per-call selector. Meanwhile, `DiscoverModels` exposes exact provider/model pairs although no delegation surface accepts that pair.

A router category is not an inference provider: it has no credentials or endpoint. Adding router rows to `ListModels` would therefore be wrong because that API inventories root-session inference targets, while a category cannot create a root session without a delegated task.

## Decision

### Model aliases are universal targets

An alias has one of two operator-controlled forms:

```yaml
models:
  aliases:
    fast: gpt-5-mini
    strong:
      provider: anthropic
      model: claude-opus-4-1
```

The scalar form binds to the effective operator default provider when `models.default_provider` or `--default-provider` is set, before a provider-aware `models.default` changes the main session provider. Without a configured default provider, scalar aliases remain contextual. CLI scalar aliases remain contextual; the strict object form requires exactly non-empty `provider` and `model` fields and always resolves to that atomic pair. `model-router` is not a valid concrete alias provider.

Every alias consumer preserves the resulting target. This includes deployment/session defaults, plan-mode selection, compaction and auxiliary call slots, the global subagent default, agent definitions, automatic router-category targets, and explicit Subagent, Parallel, and Team selectors. Pair-aware consumers re-derive provider/model-dependent engines, adapters, token counters, compactors, prompt configuration, and context-window policy through composition factories; they never clone-and-swap a model or discard the provider. A model literal or scalar alias declared in operator `models.slots`, `models.subagent`, or `models.router.categories` uses the configured default provider when present, regardless of the main session's provider. A bare literal supplied to a Subagent call remains relative to its parent session's persisted provider; a call naming a configured alias uses that alias's bound target. A scalar plan slot targeting a different provider from an existing session still takes the provider-bound fail-soft fallback. A separately supplied provider must match a pair alias for explicit delegation. For deployment defaults, a provider-aware default model alias instead selects the complete provider/model pair, taking precedence over a separately configured default provider; that provider supplies the context for a scalar default model or an absent model. The configured default provider must be available even when a pair selects a different main-session provider, because scalar operator bindings may target it.

The persisted provider remains fixed for the lifetime of an existing session because its conversation may contain provider-private replay state. A plan-slot pair on that provider can rebuild the session engine on its resolved model. A plan-slot pair naming another provider is incompatible and follows the established fail-soft slot posture: emit a bounded warning, retain plan permission mode, and run the turn on the persisted provider and ordinary non-plan model. It makes no request to the alias target, does not rebase the alias model onto the session provider, and reports the actual fallback provider/model. A provider-aware deployment default instead applies when constructing a new session, before provider-private history exists.

Existing consumer failure postures remain deliberate. An alias object naming an unknown or unavailable provider fails during Build because the local registry can decide provider availability without a network probe. Explicit delegation resolution fails before child construction. Auxiliary slots and automatic routing retain their existing fail-soft behavior for runtime resolution failures, while forgiving named-agent definitions retain their current warning/fallback behavior. Alias resolution never probes live model inventory or rejects an opaque model ID merely because inventory cannot prove it.

Keep `--model-alias name=model-id` for scalar CLI aliases and add repeatable `--model-alias-provider name=provider-id`. Alias lookup is single-hop and leaves the target model opaque. A CLI model entry replaces the complete lower-tier target for that name and does not inherit a YAML provider; a provider entry pairs only with a same-tier CLI model entry, independent of argument order, or startup fails. CLI remains the highest-precedence source per alias name. This companion form avoids inventing escaping or a delimiter inside opaque model IDs.

Model aliases and all effective `models:` policy become operator-tier only. After a project settings document is syntactically decoded, its `models:` node is treated as opaque ignored content: nested model keys and values are not schema-validated, merged, resolved, canonicalized, or probed. One source-bounded warning that does not echo values is emitted regardless of project trust or posture, and startup continues. Whole-document syntax errors retain existing settings behavior. The operator `models.allowlist` key remains accepted temporarily so existing settings do not block launch, but it has no effect and emits a bounded deprecation/no-effect warning. A later compatibility cleanup may remove it.

### Delegation accepts explicit targets

Use an explicit `(provider, model)` selector on every model-selectable delegation surface:

- `Subagent` accepts optional `provider` and `model`.
- `Parallel` accepts one optional `provider` and `model` pair applied to every branch.
- Each `Team.members[]` entry accepts optional `provider` and `model`.

The normal default remains omission of both fields. Model-visible guidance tells the agent to leave them unset unless the user requests a model/provider or a concrete task capability justifies an override. Omission preserves inherited/default selection and automatic semantic routing.

A non-empty ordinary provider/model pair mints a def-less child on that exact target. A provider without a model is invalid. A literal model without a provider keeps current same-parent-provider behavior; a configured provider-aware alias carries its pair. For a named specialist, existing read-only Subagent `agent` plus model-only behavior remains: composition rebuilds the specialist on that model while preserving its prompt, catalog, limits, permission/filesystem posture, skills, and MCP configuration. Named Team members gain that same model-only override. A call-level provider-bearing selector, including a pair alias or `model-router`, is rejected for a named specialist; pair aliases declared by the definition's own model field remain valid. Team validates all selectors before adding any member, so failure leaves no partial team, and Parallel validates its shared selector before fan-out. `fork` and `resume` reject selectors because their replay and continuation semantics are provider-bound.

Reserve `provider: "model-router"` on delegation-only surfaces. Its `model` is the exact name of an enabled operator router category. The category's model selector resolves through the universal alias resolver: a configured scalar target uses the operator default provider when set (otherwise the parent provider), while a provider-aware alias carries its explicit pair. Explicit category selection bypasses automatic classification and its breaker, fails with a bounded tool error if unavailable, unknown, disabled, or unresolvable, and never falls back silently.

Extend `DiscoverModels`, but not `ListModels`, with virtual rows for enabled router categories. Each row has `provider_id: "model-router"`, the category name as `model_id`, a bounded operator-authored `description`, and no inferred endpoint, credentials, alias, or concrete target. The virtual rows are the single canonical discovery projection alongside inference rows: descriptions participate in literal search, canonical ordering, cursor digest, pagination, the 32 KiB result limit, and stale-cursor restart. An unfiltered facet includes `model-router` only when it has virtual rows. The tool specification and factory-built system-prompt posture describe the router selector and omission-first delegation policy without embedding categories or aliases. `ListModels`, root-session model picking, and `CreateSession` remain inference-provider-only.

Reserve `model-router` at provider-registry and configuration admission. No built-in or operator-defined inference provider may use that ID.

Delegation start evidence carries the actual resolved provider as well as the existing actual model and routing fields. An explicit router selection carries a dedicated `explicit_router_category` field; it leaves `routed_category`, `routed_model`, `routing_reason`, and classifier-decision evidence empty rather than misusing classifier provenance. Mecatui renders it as `selected: model-router/{category} → {provider}/{model}` on Subagent cards, Parallel branches, and Team rosters; classifier-originated selections retain the existing `routed:` label. Actual model cues across these delegation surfaces display the concrete `provider/model` when provider evidence is present; the session header shows the same identity within its width cap. Older events without a provider retain model-only labels. Candidate routing evidence does not invent a provider.

## Consequences

Aliases become stable deployment targets instead of provider-relative model labels when the operator chooses the object form. This enables cross-provider defaults for new sessions, isolated auxiliary calls, named-agent definition resolution, automatic routing, and explicit def-less delegation without provider inference from model text. Existing sessions remain provider-bound: a cross-provider plan target is rejected into the defined fail-soft fallback rather than replaying provider-private history elsewhere. Composition must replace model-only alias plumbing with a target value and re-derive provider-dependent dependencies at each compatible factory seam.

Repositories lose their former trusted, allowlist-capped ability to influence model configuration. Existing project `models:` blocks stop taking effect but do not prevent launch; warnings make the change visible. The now-inert operator allowlist remains parseable only as migration tolerance.

The agent can choose an operator-approved category without guessing category names and can use a discovered exact provider/model pair for a direct child override. Parallel branches and team members gain the same explicit-selection capability as Subagent. The change widens tool schemas, event/protobuf projections, child-engine composition, configuration parsing, and the agent-facing discovery contract.

Cross-provider construction must preserve fresh-child and fresh-call isolation, re-derive model-dependent dependencies through factories, and never reuse provider-private replay state. The reserved `model-router` value is intentionally not a server provider, cannot be used to create a session or alias target, and is not published by `ListModels`.

No new resource outlives a call. Router-category rows and provider-aware targets are derived from existing Build-owned operator configuration and provider factories. Delegation events retain only bounded resolved metadata; they do not persist alias names, requested selectors, category targets, credentials, or configuration topology.

## See also

- [ADR 0030](./0030-model-selection-heuristics.md), [ADR 0031](./0031-subagent-model-router.md), [ADR 0034](./0034-team-parallel-model-routing.md), and [ADR 0035](./0035-per-delegation-model-surface.md)
- [ADR 0352](./0352-jev-delegated-model-router.md)
- [Provider architecture](../architecture/providers.md)
- [Subagents and teams architecture](../architecture/subagents-and-teams.md)
- [ADR 0027](./0027-cloud-native.md) resource inventories
