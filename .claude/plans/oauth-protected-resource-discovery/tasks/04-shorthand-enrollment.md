---
id: 04-shorthand-enrollment
title: Shorthand enrollment and confirmed tuple
blocked_by: [03-client-discovery]
status: done
branch: plan-oauth-protected-resource-discovery/04-shorthand-enrollment
worktree: ""
issue: "1033"
retries: 0
last_error: ""
accumulator: acc/oauth-protected-resource-discovery
---
# Task brief
Integrate resource discovery into mecatui login, preserve explicit enrollment, add first-use confirmation using one immutable tuple, derive/default or override the gRPC target, and reuse existing PKCE/credential lifecycle with separate trust roots.

## Acceptance criteria
- AC4.1: Shorthand enrollment completes without explicit issuer, audience, or client-ID flags.
  - verify: `TestOAuthProtectedResource_Scenario4_ShorthandEnrollment`
- AC4.2: `--grpc-target` changes only transport and a resource path never becomes a gRPC path.
  - verify: `TestADR_0290_ResourceTargetSeparation`
- AC4.3: Metadata, issuer, and gRPC TLS/CA policies remain separate.
  - verify: `TestInvariant_oauth_three_transport_trust_split`
- AC4.4: Browser launch and persistence occur only after explicit first-use confirmation; the same immutable confirmed tuple is passed unchanged to authorization and enrollment, and rejection/cancellation/EOF leaves no state.
  - verify: `TestADR_0290_DiscoveredIdentityConfirmation`
- AC4.5: The final requested scope set is the confirmed, validated union of the OIDC baseline and profile scopes, with explicit CLI `--scopes` precedence defined and displayed before confirmation.
  - verify: `TestADR_0290_DiscoveredScopeSelection`
- AC4.6: Legacy explicit enrollment, private issuer mode, explicit scopes, callback, and saved connect remain compatible.
  - verify: `TestADR_0277_ExplicitEnrollmentCompatibility`
- AC4.7: V1 does not add an RFC 8707 request parameter and rejects unsupported opaque-token operation rather than guessing.
  - verify: `TestADR_0290_ProviderCompatibility`
