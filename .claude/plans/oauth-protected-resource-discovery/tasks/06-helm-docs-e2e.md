---
id: 06-helm-docs-e2e
title: Helm, docs, and end-to-end proof
blocked_by: [05-registry-aliases]
status: done
branch: plan-oauth-protected-resource-discovery/06-helm-docs-e2e
worktree: ""
issue: "1033"
retries: 0
last_error: ""
accumulator: acc/oauth-protected-resource-discovery
---
# Task brief
Wire the mecak8s Helm values/schema/template and shared flags, add hermetic end-to-end coverage for both composition roots, and update ADR/architecture/implementation notes/usage/TUI/user-docs plus generated docs. No live IdP.

## Acceptance criteria
- AC6.1: Helm renders `oidc.resource` and `oidc.clientID` as the shared `--oidc-resource` and `--oidc-client-id` flags, joins `oidc.scopes` to the same CSV parser used by the CLI, and rejects partial or malformed values including `oidc.enabled: false` with profile fields set.
  - verify: `TestADR_0290_HelmProtectedResourceProfile`
- AC6.2: Offline coverage proves the real metadata handler and discovery parser plus confirmation and the immutable tuple passed to a test-double login seam. Live browser PKCE, authenticated gRPC, and bare-host reconnect qualification is deferred and outside issue ship criteria.
  - verify: `TestOAuthProtectedResource_Scenario6_EndToEnd`
- AC6.3: Both server composition roots receive the shared OIDC flag/profile projection; the direct metadata-route and subordinate generic-Bearer challenge contract is exercised at the shared server-adapter boundary.
  - verify: `TestADR_0290_ServerCompositionParity`, `TestADR_0290_MetadataEndpointAndSubordinateChallenge`
- AC6.4: Documentation distinguishes RFC fields, mecatl extensions, existing OIDC projections, transport separation, and ToolHive provenance.
  - verify: inspection — documentation includes protocol, extension, provenance, and compatibility sections
- AC6.5: Generated documentation and site build are current.
  - verify: `task docs && task site:build`
