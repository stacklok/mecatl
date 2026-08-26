---
id: 02-provider-registry
title: Custom registry entries, adapter minting, and overrides
blocked_by: [01-provider-config-auth]
status: done
branch: plan-operator-defined-llm-providers/02-provider-registry
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/operator-defined-llm-providers
---

# Task brief

Consume the validated custom definitions and credential snapshot in composition to create
first-class registry entries. A custom entry owns its configured default-model floor and
mints only the declared existing protocol adapter with the existing resilience/remint
path. Persisted custom selectors fail loudly if their ID is unavailable. Apply built-in
endpoint overrides with the exact matching CLI precedence while preserving each built-in's
current inventory and private-option policy. Keep all work out of engine/domain/port/proto.

## Acceptance criteria

- AC3.1: an `openai-responses` custom provider sends its configured default model to its
  configured base URL through the existing Responses adapter, and an explicit session
  model overrides that default.
  - verify: `TestOperatorDefinedLLMProviders_Scenario3_ResponsesAdapter`
- AC3.2: an `openai-chat-completions` custom provider uses the existing Chat Completions
  adapter, and an `anthropic-messages` provider uses the native Messages adapter with its
  established API-key headers and request requirements.
  - verify: `TestOperatorDefinedLLMProviders_Scenario3_DeclaredFlavorSelectsAdapter`
- AC3.3: custom providers do not inherit OpenRouter routing metadata, canonical-provider
  prompt-cache hints, Codex policy headers, ToolHive discovery, or provider-private
  behavior not declared by their flavor.
  - verify: `TestInvariant_custom_provider_has_no_builtin_private_options`
- AC3.4: no engine/domain/port/proto public API changes are needed for custom provider
  registration.
  - verify: inspection — `task api:check` and the layering gate prove the core boundary.
- AC5.1: `models.default_provider` accepts an available custom ID, a zero-selector session
  uses that provider and its configured default model, and an unavailable or unknown
  custom default fails startup rather than silently falling back.
  - verify: `TestOperatorDefinedLLMProviders_Scenario5_DefaultProvider`
- AC5.2: explicit CLI base-URL flags override matching `provider_overrides` settings,
  settings override built-in defaults, custom provider definitions remain unaffected by
  built-in override settings, and each override retains the matching existing flag's
  inventory and private-option behavior.
  - verify: `TestOperatorDefinedLLMProviders_Scenario5_OverridePrecedence`
- AC5.3: an explicitly selected custom provider/model persists and rehydrates through the
  same ID; a removed or renamed ID fails loudly rather than selecting a different provider.
  - verify: `TestOperatorDefinedLLMProviders_Scenario5_PersistedSelector`
