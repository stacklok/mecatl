---
id: 01-model-discovery
title: Bounded agent-facing model discovery
blocked_by: []
status: done
attempt: 1
branch: plan-agent-model-discovery/01-model-discovery-attempt-1
worktree: .scratch/worker-agent-model-discovery-01-model-discovery-attempt-1
issue: "1064"
retries: 0
last_error: ""
accumulator: acc/agent-model-discovery
---

# Bounded agent-facing model discovery

Implement the accepted first phase of issue #1064: a model-facing, read-only tool that lets the agent inspect the same resolved model inventory already exposed through `ListModels`.

Keep `provider_id` plus `model_id` as the exact existing configured inference-target handle. Do not redesign provider identity, add a composite/multi-route abstraction, add automatic routing or ranking, or add cross-target Subagent selection. Keep the provider registry/catalog in composition and preserve the provider-neutral `port.LLMRequest` boundary.

Use one composition-owned resolved inventory source shared with `ListModels`; do not add another lister, registry, cache, provider probe, or configuration-read path. The tool must be bounded, deterministic, read-only, and non-disclosing. It must be registered consistently in the default/shared, selector-session, and no-FS catalogs where model selection is available, with a model-visible discoverability instruction tested through the real factory path. Use offline tests and existing reference adapters only.

## Acceptance criteria

- AC1.1: the discovery tool returns only entries from the composition-owned resolved inventory used by `ListModels`; for a stable inventory snapshot, each returned `(provider_id, model_id)` pair and its safe metadata agrees with the corresponding `ListModels` entry.
  - verify: `TestAgentModelDiscovery_Scenario1_SharedResolvedInventory`
- AC1.2: every returned entry identifies its exact configured inference target with both `provider_id` and `model_id`; equal `model_id` values under different provider IDs remain separately addressable and no provider is inferred from a model string.
  - verify: `TestAgentModelDiscovery_Scenario1_ExactProviderModelHandles`
- AC1.3: the tool is read-only and bounded: its response has a deterministic finite maximum independent of inventory size, while preserving enough complete handles for the agent to act through the existing selector.
  - verify: `TestAgentModelDiscovery_Scenario1_ReadOnlyBoundedOutput`
- AC1.4: a model whose tool catalog includes discovery receives a model-visible instruction that the tool exists and that `(provider_id, model_id)` is the exact selection handle; a real composition factory-path system-prompt test proves the instruction lands in its owning layer.
  - verify: `TestAgentModelDiscovery_Scenario1_SystemPromptContainsDiscoveryContract`
- AC2.1: supported filters operate only on safe resolved-inventory fields and do not accept an endpoint, credential, arbitrary query, or route/topology expression.
  - verify: `TestAgentModelDiscovery_Scenario2_SafeFiltersOnly`
- AC2.2: an unknown or unavailable provider filter, malformed filter value, and limit above the documented bound produce an honest bounded result or validation error without falling back to another provider, refreshing inventory, or changing the session selection.
  - verify: `TestAgentModelDiscovery_Scenario2_InvalidFiltersDoNotProbeOrSelect`
- AC2.3: filtered results still show each surviving exact `(provider_id, model_id)` pair and do not merge entries merely because their model IDs or display metadata match.
  - verify: `TestAgentModelDiscovery_Scenario2_FilterPreservesExactHandles`
- AC3.1: discovery catalog registration has parity across the default/shared and eligible per-session catalogs; the existing exact catalog-set parity guard covers the tool without a discovery-specific exception.
  - verify: `TestAgentModelDiscovery_Scenario3_PerSessionCatalogParity`, `TestPerSessionCatalogMatchesSharedCatalog`
- AC3.2: a no-FS session retains the discovery tool when model selection is otherwise available; the tool requires no workspace, shell, or filesystem-derived input.
  - verify: `TestAgentModelDiscovery_Scenario3_NoFSCatalogParity`
- AC3.3: before, during, and after an existing live inventory refresh, the discovery tool uses the same availability/fallback view as `ListModels`: a configured floor or last-known-good inventory remains visible only where the existing inventory permits it, while unavailable or empty inventory is represented honestly without invented models or capabilities.
  - verify: `TestAgentModelDiscovery_Scenario3_RefreshFallbackConsistency`
- AC3.4: discovery output and errors disclose only safe provider/model metadata and bounded status/hints; credentials, endpoint URLs, internal topology, and raw listing-error bodies are absent from tool output, event/diagnostic projections, and any model-visible error.
  - verify: `TestInvariant_agent_model_discovery_non_disclosure`
