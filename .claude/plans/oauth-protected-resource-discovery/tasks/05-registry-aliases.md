---
id: 05-registry-aliases
title: Saved resource aliases and reconnect
blocked_by: [04-shorthand-enrollment]
status: pending
branch: ""
worktree: ""
issue: "1033"
retries: 0
last_error: ""
accumulator: acc/oauth-protected-resource-discovery
---
# Task brief
Add optional ResourceURL to public registry metadata while preserving encrypted credential keys. Implement exact resource/target lookup, legacy behavior, metadata drift protection, and lifecycle integration across connect/logout/reauth/recovery.

## Acceptance criteria
- AC5.1: New records store separate resource and gRPC identities without making existing credentials unreachable.
  - verify: `TestADR_0290_AdditiveResourceRegistryIdentity`
- AC5.2: Exact lookup by resource or target succeeds only when unambiguous.
  - verify: `TestADR_0290_RegistryLookupAmbiguity`
- AC5.3: Legacy rows without ResourceURL remain usable by exact target with no eager rewrite.
  - verify: `TestADR_0277_LegacyRegistryCompatibility`
- AC5.4: `mecatui connect RESOURCE` is browser-free and uses only the saved confirmed tuple.
  - verify: `TestOAuthProtectedResource_Scenario5_SavedConnect`
- AC5.5: Metadata drift cannot rewrite saved issuer, audience, client ID, scopes, resource, or target.
  - verify: `TestADR_0290_SavedIdentityDrift`
- AC5.6: Logout, reauthentication, `/connect`, concurrency, and recovery use one alias rule and preserve ADR 0277 transaction/CAS behavior.
  - verify: `TestADR_0290_ResourceAliasLifecycle`
- AC5.7: A legacy record without ResourceURL is not synthesized into a resource alias unless an explicit resource is confirmed; exact target lookup remains the only legacy path.
  - verify: `TestADR_0290_LegacyResourceAliasPolicy`
- AC5.8: Resource-alias and target-alias operations resolve the same record deterministically, and concurrent enrollment/reauthentication cannot mutate a different record.
  - verify: `TestADR_0290_AliasConcurrency`
