---
id: 07-provider-credential-cleanup
title: Provider credential and endpoint override cleanup
blocked_by: [06-provider-credential-loader]
status: done
branch: acc/operator-defined-llm-providers
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/operator-defined-llm-providers
---

# Task brief

Narrow the provider composition seam to credentials. Command roots retain non-secret
built-in endpoint flags as endpoint overrides, and `app.Build` merges those overrides over
operator settings before registry construction. Remove bootstrap-only config wrappers while
preserving root-specific policies and mecatui connect mode.

## Acceptance criteria

- AC7.1: `ProviderCredentials` and `ProviderCredentialResolver` carry no built-in endpoint
  fields.
  - verify: `TestInvariant_provider_credentials_auth_snapshot`
- AC7.2: endpoint precedence is CLI > settings > built-in default, without changing custom
  provider URLs or OpenAI/Anthropic SDK defaults.
  - verify: `TestOperatorDefinedLLMProviders_Scenario7_EndpointOverridePrecedence`
- AC7.3: all command roots use named declarative config helpers without unbootstrapped wrappers.
  - verify: `TestOperatorDefinedLLMProviders_Scenario6_ProfileLoaderRoots`
