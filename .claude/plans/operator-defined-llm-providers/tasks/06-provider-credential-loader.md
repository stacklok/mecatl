---
id: 06-provider-credential-loader
title: Composition-owned provider credential loader
blocked_by: [04-provider-root-integration]
status: done
branch: plan-operator-defined-llm-providers/06-provider-credential-loader
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/operator-defined-llm-providers
---

# Task brief

Replace command-root provider bootstrap mutation with an injected composition-owned
provider-credential loader analogous to MCPProfileLoader. `app.Build` must resolve operator
provider definitions once, invoke the loader once, apply the immutable credentials before
default selection/registry construction, and own any credential lifecycle the loader returns. The
cliconfig implementation remains the local settings/auth-file adapter and preserves one
strict auth-file snapshot plus CLI endpoint precedence. Do not implement OAuth; make the
seam ready for it. Tests must use a fake loader rather than bypassing the production path.

## Acceptance criteria

- AC6.1: `app.Build` supplies its resolved operator provider definitions to one injected
  provider-credential loader before provider default selection and registry construction.
  - verify: `TestADR_0238_BuildLoadsProviderCredentialLoaderOnce`
- AC6.2: a provider-credential loader error aborts Build, and Build closes an acquired
  provider-credential lifecycle exactly once on every later Build failure or normal close.
  - verify: `TestADR_0238_BuildOwnsProviderCredentialLifecycle`
- AC6.3: API-key provider credentials retain one strict `auth.yaml` snapshot and the existing
  CLI endpoint override precedence; custom-provider auth IDs are validated only after
  definitions resolve.
  - verify: `TestInvariant_provider_credentials_auth_snapshot`
- AC6.4: all four command roots inject the same shared provider-credential resolver, while
  mecatequi/mecak8s policy and mecatui connect-mode no-local-provider behavior remain
  unchanged.
  - verify: `TestOperatorDefinedLLMProviders_Scenario6_CredentialLoaderRoots`
