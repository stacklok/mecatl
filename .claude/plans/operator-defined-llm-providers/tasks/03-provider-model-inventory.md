---
id: 03-provider-model-inventory
title: Conservative custom-provider live model inventory
blocked_by: [02-provider-registry]
status: done
branch: plan-operator-defined-llm-providers/03-provider-model-inventory
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/operator-defined-llm-providers
---

# Task brief

Make a custom provider's configured default model a synchronous composition-owned model
inventory floor. Add flavor-appropriate live listing only through safe bounded,
redirect-refusing requests with the resolved auth behavior. Listing success may expand
inventory; every failure path preserves the configured default and unknown metadata must
remain conservative. Do not introduce a static custom-model declaration or new core API.

## Acceptance criteria

- AC4.1: a custom Responses or Chat-Completions provider can use the compatible `/models`
  lister at its configured base URL, and a custom Anthropic provider can use the native
  lister at its configured base URL; each request is authenticated as configured,
  redirect-refusing, and bounded before successful results expand inventory.
  - verify: `TestOperatorDefinedLLMProviders_Scenario4_LiveListing`
- AC4.2: the configured default model is the synchronous registry inventory floor for
  empty-model selection, deployment-default validation, live-list fallback, and
  child/provider reminting; it is usable even without a static custom model catalog.
  - verify: `TestInvariant_custom_provider_default_model_inventory_floor`
- AC4.3: a failed, timed-out, malformed, or empty live listing preserves that default-model
  floor and does not discard a valid provider registration.
  - verify: `TestOperatorDefinedLLMProviders_Scenario4_ListingFallback`
- AC4.4: unknown live model metadata never overclaims image, audio, reasoning, output, or
  context-window capabilities.
  - verify: `TestInvariant_custom_provider_live_metadata_conservative`
