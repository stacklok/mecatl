---
id: 01-profile-adr
title: Shared OIDC profile and ADR
blocked_by: []
status: done
branch: plan-oauth-protected-resource-discovery/01-profile-adr
worktree: ""
issue: "1033"
retries: 0
last_error: ""
accumulator: acc/oauth-protected-resource-discovery
---
# Task brief
Add ADR 0290 and shared `cliconfig.OIDCConfig`/flag/config validation for `--oidc-resource`, `--oidc-client-id`, and optional CSV `--oidc-scopes`. Existing issuer/audience remain the sole validation/projection sources. Use ToolHive provenance as required by ADR 0290.

## Acceptance criteria
- AC1.1: Existing issuer and audience feed both token validation and metadata projection; no duplicate issuer/audience policy exists.
  - verify: `TestADR_0290_ProfileProjection`
- AC1.2: A complete resource/client profile enables metadata under OIDC; absent profile fields preserve existing behavior.
  - verify: `TestADR_0290_ProfileConfigurationMatrix`
- AC1.3: `--oidc-scopes` accepts CSV, trims, rejects empty/unsafe entries, deduplicates, and produces deterministic metadata order; absence omits `scopes_supported`.
  - verify: `TestADR_0290_ScopeCSV`
- AC1.4: Invalid or partial profile configuration fails before serving.
  - verify: `TestADR_0290_ProfileValidation`
