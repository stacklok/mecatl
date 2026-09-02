---
id: 06-helm-docs-e2e
title: Helm, docs, and end-to-end proof
blocked_by: [05-registry-aliases]
status: blocked
branch: plan-oauth-protected-resource-discovery/06-helm-docs-e2e
last_error: "panel gate: AC6.2/AC6.3 proofs are shallow; final panel also found path-bearing issuer discovery, RFC 9728 challenge/resource mismatch, and resource-alias uniqueness/canonicalization blockers"
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
- AC6.2: The hermetic flow proves metadata, confirmation, PKCE, persistence, authenticated gRPC, and bare-host reconnect.
  - verify: `TestOAuthProtectedResource_Scenario6_EndToEnd`
- AC6.3: Both server composition roots exercise the shared contract.
  - verify: `TestADR_0290_ServerCompositionParity`
- AC6.4: Documentation distinguishes RFC fields, mecatl extensions, existing OIDC projections, transport separation, and ToolHive provenance.
  - verify: inspection — documentation includes protocol, extension, provenance, and compatibility sections
- AC6.5: Generated documentation and site build are current.
  - verify: `task docs && task site:build`
