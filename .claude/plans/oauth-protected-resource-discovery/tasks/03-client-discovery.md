---
id: 03-client-discovery
title: Hardened resource metadata discovery
blocked_by: [02-server-metadata]
status: done
branch: plan-oauth-protected-resource-discovery/03-client-discovery
worktree: ""
issue: "1033"
retries: 0
last_error: ""
accumulator: acc/oauth-protected-resource-discovery
---
# Task brief
Adapt ToolHive `FetchResourceMetadata` and ToolHive-Core networking/challenge parsing as appropriate. Implement bare-host and HTTPS URL grammar, exact RFC 9728 resource/issuer validation, anonymous public bootstrap transport, bounds, and safe errors. Do not add RFC 8707 in V1.

## Acceptance criteria
- AC3.1: Bare hostnames and explicit HTTPS URLs produce deterministic resource and gRPC identities; HTTP, userinfo, query, fragment, malformed escapes, and ambiguous authorities fail before I/O.
  - verify: `TestADR_0290_ResourceInputGrammar`
- AC3.2: RFC 9728 path insertion and exact resource matching reject origin-only, prefix, trailing-slash, port, and escaped-path near matches.
  - verify: `TestADR_0290_ExactResourceBinding`
- AC3.3: Fetching is anonymous, redirect-free, TLS-verified, public-address-only, DNS-rebinding resistant, timeout-bounded, and response-size bounded.
  - verify: `TestADR_0290_MetadataFetchSecurity`
- AC3.4: Over-limit and trailing data are rejected and JSON media types are parsed correctly.
  - verify: `TestADR_0290_MetadataBodyBounds`
- AC3.5: Zero/multiple authorization servers, unsafe profile values, and missing required profile extensions fail closed.
  - verify: `TestADR_0290_ProfileDocumentValidation`
- AC3.6: OIDC discovery `issuer` exactly matches the selected authorization server and uses the same anonymous public-bootstrap transport, with no private-address or custom-CA exception.
  - verify: `TestADR_0290_IssuerBinding`
- AC3.7: Metadata fetch or issuer discovery failure, timeout, mismatch, or malformed response performs no browser launch, keyring initialization, credential write, or registry write.
  - verify: `TestADR_0290_DiscoveryFailureLeavesNoState`
- AC3.8: Duplicate security-relevant JSON members are rejected, and profile scopes are never silently expanded by a later metadata refresh; unknown non-profile members remain ignorable.
  - verify: `TestADR_0290_MetadataDuplicateFields`
- AC3.9: No bearer, cookie, client certificate, credential-store material, response body, or token appears in discovery requests/errors/diagnostics.
  - verify: `TestInvariant_resource_discovery_is_anonymous`
