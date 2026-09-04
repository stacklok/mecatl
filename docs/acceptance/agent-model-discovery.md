# Agent model discovery — acceptance plan

**Phase:** bounded agent-facing discovery over the resolved model inventory
**Status:** draft
**Issue:** [#1064](https://github.com/stacklok/mecatl/issues/1064)
**ADR:** [ADR 0016](../adr/0016-multi-provider.md) — composition-owned registry, provider-neutral engine boundary, live inventory, and provider/model selection. [ADR 0238](../adr/0238-operator-defined-llm-providers.md) — stable operator-defined provider IDs and conservative inventory floors.
**Accumulator branch:** `acc/agent-model-discovery` (off `main`).

The first independently valuable outcome from #1064 is a model-facing, read-only way to
inspect the same resolved inventory already exposed through `ListModels`. It lets an agent
choose an exact configured inference target without guessing identifiers or learning from an
inference failure. It does not change provider identity, selection semantics, or routing.

## Scope cuts

- `provider_id` remains the stable configured inference-target ID. The exact current selection
  handle is `(provider_id, model_id)`, matching the existing two-field selector described in
  [architecture: providers](../architecture/providers.md#multi-provider--registry-per-session-routing--model-inventory).
- The source of truth is one composition-owned resolved inventory shared with `ListModels`, not
  a second registry, lister, cache, or discovery path. The engine continues to receive neutral
  injected capabilities, as required by [ADR 0016](../adr/0016-multi-provider.md).
- Discovery is informational. It neither selects a session model nor changes a provider/model
  binding. A discovered handle is only a candidate for the existing selection surface.
- The inventory is public metadata only. It must not reveal credentials, endpoints, network
  topology, raw upstream failures, or configuration details beyond the safe provider status
  already intended for inventory consumers.

## In scope — 3 scenarios

### Scenario 1 — an agent can inspect exact available selection handles

In a session with model selection available, the model can call a bounded, read-only discovery
tool and receive a compact inventory of selectable entries. Each entry presents the exact
`provider_id` and `model_id` together, plus only the established safe descriptive/capability
metadata needed to compare entries. Identical model IDs from different providers remain distinct
entries rather than being merged or guessed. The tool's inventory agrees with `ListModels` for
one snapshot, including provider-status metadata where that surface exposes it.

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

Where the full bounded inventory is still too broad, the tool supports only a small safe
narrowing surface warranted by the resolved metadata, such as provider ID and a caller-supplied
result limit. Invalid filters, unknown provider IDs, or excessive limits fail safely or yield an
honest empty result; they never trigger provider probing, configuration reads, or a model
selection. Filtering and limiting preserve the exact-pair presentation and the response bound.

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

The tool is registered wherever the existing model-facing capability is valid: the default/shared
catalog, eligible per-session catalogs, and the no-FS catalog. It follows the existing live
refresh and fallback semantics rather than promising fresh network state: synchronous floors,
last-known-good data, unavailable/empty inventory, and conservative unknown metadata remain
truthful. It neither exposes sensitive provider configuration nor turns a refresh/listing failure
into raw upstream disclosure.

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

Decide during implementation whether a provider-only filter plus a caller-supplied bounded limit
is sufficient for the first release. Any additional filter must be justified by existing resolved
safe metadata, preserve the shared-inventory invariant, and remain within this plan's disclosure
and boundedness criteria; it must not become a route, endpoint, or provider-group selector.
