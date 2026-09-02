---
id: 02-server-metadata
title: ToolHive-derived metadata endpoint and challenge
blocked_by: [01-profile-adr]
status: done
branch: plan-oauth-protected-resource-discovery/02-server-metadata
worktree: ""
issue: "1033"
retries: 0
last_error: ""
accumulator: acc/oauth-protected-resource-discovery
---
# Task brief
Adapt the minimum Apache-2.0 ToolHive v0.40.0 metadata/routing/challenge code and use ToolHive-Core v0.0.41 primitives where suitable. Wire both mecated and mecak8s public HTTP listeners outside auth/admin. Correct upstream gaps and preserve attribution.

## Acceptance criteria
- AC2.1: Both binaries return the configured metadata with `200` and `application/json` outside authentication and metrics/admin listeners.
  - verify: `TestADR_0290_ProtectedResourceMetadata`
- AC2.2: Metadata includes standard fields, both mecatl extensions, and optional scopes without secrets, CA paths, or internal listener data.
  - verify: `TestADR_0290_MetadataFields`
- AC2.3: Only the exact well-known path or valid RFC path-specific form is routed; unsupported methods are rejected deliberately.
  - verify: `TestADR_0290_WellKnownRouting`
- AC2.4: One path helper drives metadata routing and challenge URL generation, including path resources.
  - verify: `TestADR_0290_WellKnownPathDerivation`
- AC2.5: Disabled profiles return 404 and static-token-only deployments do not advertise OAuth.
  - verify: `TestADR_0290_MetadataDisabledCompatibility`
- AC2.6: 401 responses carry one safe `resource_metadata` challenge derived only from operator configuration; unrelated routes retain existing behavior.
  - verify: `TestADR_0290_ChallengeMatrix`
- AC2.7: Metadata includes the standard resource and authorization-server fields; ToolHive fixture parity remains unproven by this local serialization test.
  - verify: `TestADR_0290_MetadataStandardFields`
