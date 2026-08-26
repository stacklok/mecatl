---
id: 01-provider-config-auth
title: Strict operator provider configuration and auth bootstrap
blocked_by: []
status: done
branch: plan-operator-defined-llm-providers/01-provider-config-auth
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/operator-defined-llm-providers
---

# Task brief

Add the operator-tier-only custom-provider and built-in endpoint-override configuration,
with strict validation and the one shared command-side credential bootstrap. Keep provider
definitions and credentials separate: custom API-key entries derive their permitted
`auth.yaml` IDs from validated operator definitions plus the built-in IDs, then every
server-owning root receives the immutable resolved snapshot. Do not add environment
fallbacks, arbitrary headers, OAuth, or project-tier provider ingestion. Preserve
value-free warnings/errors and never accept a credential-bearing URL.

Update the settings/auth configuration reference and operator usage documentation required
by the user-visible settings and `auth.yaml` surface. Do not edit the acceptance plan.

## Acceptance criteria

- AC1.1: a valid operator `providers.<id>` entry with a lower-case stable ID, HTTPS base
  URL, required `default_model`, one supported `api_flavor`, and `none` or `api_key`
  authentication resolves as a custom provider definition.
  - verify: `TestOperatorDefinedLLMProviders_Scenario1_ValidDefinition`
- AC1.2: reserved built-in IDs, duplicate custom IDs, invalid IDs, non-HTTPS URLs,
  URL userinfo/query/fragment, unknown entry fields, unsupported protocol/auth values, and
  absent `default_model` fail configuration validation without printing a secret.
  - verify: `TestOperatorDefinedLLMProviders_Scenario1_InvalidDefinitionsFailClosed`
- AC1.3: all custom definitions and `provider_overrides` from project-tier settings are
  ignored with a value-free warning; an operator definition remains authoritative.
  - verify: `TestInvariant_custom_providers_operator_tier_only`
- AC1.4: `provider_overrides` accepts only `openai`, `openrouter`, `anthropic`, and
  `opencode`; Codex and ToolHive endpoint policies remain unchanged.
  - verify: `TestADR_0238_BuiltinOverrideAllowlist`
- AC2.1: two custom entries may share a URL and flavor while authenticating with distinct
  `auth.yaml` API-key records; neither key appears in diagnostics, errors, model lists,
  or wire responses.
  - verify: `TestOperatorDefinedLLMProviders_Scenario2_SeparateAuthFileKeys`
- AC2.2: an `api_key` custom provider without its matching auth-file record is unavailable,
  while a `none` provider registers without an auth-file record.
  - verify: `TestOperatorDefinedLLMProviders_Scenario2_AvailabilityFollowsAuthMethod`
- AC2.3: every server-owning command root first resolves the operator definition set,
  derives its accepted custom auth-file IDs, then parses `auth.yaml` once into the
  immutable credential snapshot passed to composition; no custom credential has an
  environment fallback.
  - verify: `TestInvariant_custom_provider_auth_bootstrap_single_source`
- AC2.4: unknown auth-file provider IDs and malformed API-key records remain strict,
  value-free failures rather than silently creating an available provider.
  - verify: `TestInvariant_custom_provider_authfile_strict`
