---
id: 02-constrained-issuer
title: Constrained typed issuer and governance containment
blocked_by: [01-logical-identity-value]
status: done
branch: "plan-logical-agent-identity-projection/02-constrained-issuer"
worktree: ""
issue: "371"
retries: 0
last_error: ""
accumulator: acc/logical-agent-identity-projection
---

# Task brief

Add constrained typed logical-agent issuance inside `internal/identityissuer`, sharing I1 key custody and envelope policy. Add the minimal host-level typed mint access needed by future composition, but no listener, RPC, agent wiring, arbitrary signing handle, or persistence. Validate/canonicalize the requested tool-only value, then call the existing `governance.CapabilitySet.Contains` for the sole no-widening decision. Preserve exact names and reject duplicates before containment. Generate a fresh CSPRNG 16-byte unpadded-base64url `jti`; own all JOSE/registered claims through I1 policy. Add structural regression coverage proving the production issuance path calls the governance predicate rather than a copied subset implementation.

## Acceptance criteria

- AC2.1: A requested tool set equal to or narrower than the source succeeds, including an empty request meaning no projected tools; reordered equivalent requests produce the same canonical sorted claim.
  - verify: `TestADR_0252_ContainedToolProjectionSucceeds`
- AC2.2: A request containing one exact tool absent from the source fails before signing and emits no compact token, while the same request succeeds when that tool is added to the positive-control source.
  - verify: `TestADR_0252_LogicalAgentProjectionNeverWidens`
- AC2.3: Tool names remain byte-exact: case, whitespace, Unicode, and MCP-shaped variants are neither trimmed, normalized, aliased, prefix-matched, nor granted through disclosure-only exceptions.
  - verify: `TestLogicalAgentIdentityProjection_Scenario2_ToolNamesStayExact`
- AC2.4: For every schema-valid unique requested set, the issuance decision matches `source.Contains(governance.CapabilitySet{Tools: requested})` across a deterministic table and fuzz-seed corpus covering empty, reordered, Unicode, whitespace, case, and MCP-shaped values.
  - verify: `TestADR_0252_ContainmentOracleCorpus`
- AC2.5: The production issuance path invokes `CapabilitySet.Contains` as its authority decision and contains no inlined or helper-local second subset policy; a structural sentinel fails if that call or dependency direction disappears.
  - verify: `TestADR_0252_IssuerUsesGovernanceContainment`
- AC3.1: A valid typed request produces one ES256 compact JWT-SVID with the configured local issuer, canonical subject, exact singleton I3 audience, fixed bounded TTL, current RFC-7638-derived `kid`, and the one v1 public claim.
  - verify: `TestLogicalAgentIdentityProjection_Scenario3_TypedIssue`
- AC3.2: Every mint receives exactly 16 cryptographically random bytes encoded as an unpadded base64url `jti`; repeated mints of the same identity differ in `jti`, times/signature as applicable, while deterministic injected-randomness tests remain offline.
  - verify: `TestADR_0252_FreshRandomJWTID`
- AC3.3: Randomness failure, claim validation failure, containment failure, or a post-sign token above 16 KiB returns no token and never falls back to a generated key, unsigned value, anonymous identity, or partial claim.
  - verify: `TestADR_0252_IssueFailsBeforeReturningCredential`
- AC3.4: The typed host operation exposes no caller control over algorithm, `kid`, issuer, audience, TTL, timestamps, `jti`, generic claim maps, signing bytes, or private key, and adds no listener or production mint RPC.
  - verify: `TestADR_0252_LogicalAgentIssuerIsNotArbitrarySigner`
