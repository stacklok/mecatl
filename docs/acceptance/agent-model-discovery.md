# Agent model discovery — acceptance plan

**Phase:** bounded agent-facing discovery over the resolved model inventory
**Status:** draft
**Issue:** [#1064](https://github.com/stacklok/mecatl/issues/1064)
**ADR:** [ADR 0016](../adr/0016-multi-provider.md) — composition-owned registry, provider-neutral engine boundary, live inventory, and provider/model selection. [ADR 0238](../adr/0238-operator-defined-llm-providers.md) — stable operator-defined provider IDs and conservative inventory floors.
**Accumulator branch:** `acc/agent-model-discovery` (off `main`).

**Decision:** add a bounded, read-only tool that lets an agent inspect the resolved model
inventory already exposed by `ListModels`. The tool helps an agent identify an existing
configured target; it does not select a target or change routing.

## Why this first

The first useful step is to let an agent inspect the same resolved model inventory that
`ListModels` already exposes. Discovery is read-only: it helps the agent find a valid
configured target instead of guessing a model identifier or learning about availability only
after an inference failure.

The existing identity is sufficient for this work. A configured target continues to be
identified by the exact pair `(provider_id, model_id)`. The same `model_id` under two provider
IDs remains two distinct targets; this plan does not merge them or redesign provider/model
selection. It only makes the existing targets visible to the agent. This preserves the
[ADR 0016](../adr/0016-multi-provider.md) selection contract and the
[architecture: model inventory](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory).

Cross-target child selection is deferred. A child currently uses the parent’s target unless
an existing named-agent configuration provides a separate provider selection. Supporting
general cross-target selection would require new rules for choosing, persisting, replaying,
and restoring a child’s target, and no demonstrated need currently justifies that work. See
[ADR 0016, per-sub-agent provider selection](../adr/0016-multi-provider.md#10-per-sub-agent-provider-selection-shipped--both-halves).

Grouping, multi-route, and composite-provider abstractions are also deferred until a concrete
need is demonstrated. Model identifiers that look similar are not assumed to represent
interchangeable targets. The first release therefore focuses on bounded discovery over the
shared inventory, while preserving the existing `(provider_id, model_id)` contract. The plan
uses repository architecture and ADR evidence without naming a gateway, endpoint, or
confidential deployment.

## Scope cuts

- `provider_id` remains the stable configured inference-target ID. The exact selection handle
  remains `(provider_id, model_id)`, matching the existing two-field selector described in
  [architecture: providers](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory).
- One composition-owned resolved inventory remains the source of truth for both discovery and
  `ListModels`. This plan adds no registry, lister, cache, or discovery path. The engine still
  receives neutral injected capabilities, as required by [ADR 0016](../adr/0016-multi-provider.md).
- Discovery is informational. It does not select a session model or change a provider/model
  binding; a discovered handle is only a candidate for the existing selection surface.
- The inventory is public metadata only. It must not reveal credentials, endpoints, network
  topology, raw upstream failures, or configuration details beyond the safe provider status
  already intended for inventory consumers.

## In scope — 3 scenarios

### Scenario 1 — an agent can inspect exact available selection handles

In a session where model selection is available, the agent can call a bounded, read-only tool
for a compact inventory of selectable entries. Each entry presents `provider_id` and `model_id`
together, with only the established safe descriptive and capability metadata needed for
comparison. Entries with the same model ID from different providers remain separate. For a
single snapshot, the tool agrees with `ListModels`, including provider-status metadata where
that surface exposes it.

**Acceptance:**

- AC1.1: the discovery tool returns only entries from the composition-owned resolved inventory
  used by `ListModels`; for a stable inventory snapshot, each returned `(provider_id, model_id)`
  pair and its safe metadata agrees with the corresponding `ListModels` entry.
  - verify: `TestAgentModelDiscovery_Scenario1_SharedResolvedInventory`
- AC1.2: every returned entry identifies its exact configured inference target with both
  `provider_id` and `model_id`; equal `model_id` values under different provider IDs remain
  separately addressable and no provider is inferred from a model string.
  - verify: `TestAgentModelDiscovery_Scenario1_ExactProviderModelHandles`
- AC1.3: the tool is read-only and bounded: its response has a deterministic finite maximum
  independent of inventory size, while preserving enough complete handles for the agent to act
  through the existing selector.
  - verify: `TestAgentModelDiscovery_Scenario1_ReadOnlyBoundedOutput`
- AC1.4: a model whose tool catalog includes discovery receives a model-visible instruction that
  the tool exists and that `(provider_id, model_id)` is the exact selection handle; a real
  composition factory-path system-prompt test proves the instruction lands in its owning layer.
  - verify: `TestAgentModelDiscovery_Scenario1_SystemPromptContainsDiscoveryContract`

---

### Scenario 2 — narrowing is safe and cannot create a second selection language

When the full bounded inventory is still too broad, the tool may provide a small narrowing
surface based on resolved metadata, such as provider ID and a caller-supplied result limit.
Invalid filters, unknown provider IDs, and excessive limits fail safely or return an honest
empty result. They never probe providers, read configuration, or select a model. Filtering and
limiting preserve the exact-pair presentation and response bound.

**Acceptance:**

- AC2.1: supported filters operate only on safe resolved-inventory fields and do not accept an
  endpoint, credential, arbitrary query, or route/topology expression.
  - verify: `TestAgentModelDiscovery_Scenario2_SafeFiltersOnly`
- AC2.2: an unknown or unavailable provider filter, malformed filter value, and limit above the
  documented bound produce an honest bounded result or validation error without falling back to
  another provider, refreshing inventory, or changing the session selection.
  - verify: `TestAgentModelDiscovery_Scenario2_InvalidFiltersDoNotProbeOrSelect`
- AC2.3: filtered results still show each surviving exact `(provider_id, model_id)` pair and do
  not merge entries merely because their model IDs or display metadata match.
  - verify: `TestAgentModelDiscovery_Scenario2_FilterPreservesExactHandles`

---

### Scenario 3 — discovery remains honest across catalogs and inventory states

The tool is registered wherever the existing model-facing capability is valid: the
default/shared catalog, eligible per-session catalogs, and the no-FS catalog. It follows the
existing live-refresh and fallback behavior rather than promising fresh network state.

Configured floors, last-known-good data, unavailable or empty inventory, and conservative
unknown metadata remain truthful. The tool never exposes sensitive provider configuration or
turns a refresh or listing failure into raw upstream disclosure.

**Acceptance:**

- AC3.1: discovery catalog registration has parity across the default/shared and eligible
  per-session catalogs; the existing exact catalog-set parity guard covers the tool without a
  discovery-specific exception.
  - verify: `TestAgentModelDiscovery_Scenario3_PerSessionCatalogParity`, `TestPerSessionCatalogMatchesSharedCatalog`
- AC3.2: a no-FS session retains the discovery tool when model selection is otherwise available;
  the tool requires no workspace, shell, or filesystem-derived input.
  - verify: `TestAgentModelDiscovery_Scenario3_NoFSCatalogParity`
- AC3.3: before, during, and after an existing live inventory refresh, the discovery tool uses
  the same availability/fallback view as `ListModels`: a configured floor or last-known-good
  inventory remains visible only where the existing inventory permits it, while unavailable or
  empty inventory is represented honestly without invented models or capabilities.
  - verify: `TestAgentModelDiscovery_Scenario3_RefreshFallbackConsistency`
- AC3.4: discovery output and errors disclose only safe provider/model metadata and bounded
  status/hints; credentials, endpoint URLs, internal topology, and raw listing-error bodies are
  absent from tool output, event/diagnostic projections, and any model-visible error.
  - verify: `TestInvariant_agent_model_discovery_non_disclosure`

## Out of scope

| Item | Defer-to | Decision |
|---|---|---|
| Cross-target Subagent selection | demonstrated need and a separate ADR | This plan discovers existing exact handles only; it does not widen Subagent routing or selection. |
| Grouping, multi-route, composite-provider, or protocol-unifying abstraction | demonstrated need and a separate ADR | `provider_id` remains the stable configured inference-target ID; no new public provider identity model is introduced. |
| Automatic model selection, fallback, quality ranking, or routing | future focused plan | Discovery is informational and does not alter the existing selector. |
| Pricing/economics enrichment | [#611](https://github.com/stacklok/mecatl/issues/611) | This plan uses established safe inventory metadata only. |
| New public RPC/API selector or tool-argument contract | implementation design after acceptance | Acceptance constrains observable safety and exact-handle semantics, not a premature generic API. |

## Open decision

During implementation, decide whether a provider-only filter and caller-supplied bounded limit
are sufficient for the first release. Any additional filter must use existing resolved safe
metadata, preserve the shared-inventory invariant, and meet this plan's disclosure and
boundedness criteria. It must not become a route, endpoint, or provider-group selector.
