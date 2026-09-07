---
id: 03-production-boundary
title: Real readiness and bounded public transport
blocked_by: [01-transport-contract]
status: pending
attempt: 0
branch: ""
worktree: ""
issue: ""
retries: 0
last_error: ""
accumulator: acc/singleton-mcp-broker-review-remediation
---

# Task brief

Make the production factory's authentication, URL validation, verifier health, readiness,
public HTTP limits, and drain lifecycle real and executable. Preserve private configured IdP
support while rejecting malformed/plaintext protected endpoints and production loopback bypass.

## Acceptance criteria

- AC3.1: The real `cmd/mecabroker` factory refuses malformed or plaintext protected upstream, issuer, authorization, token, and callback URLs; one canonical validator enforces HTTPS, forbids userinfo and invalid components, and gives every configured or OIDC-discovered endpoint bounded no-redirect fetching and response sizes without excluding deliberately configured private identity infrastructure.
  - verify: `TestSingletonBrokerRemediation_Scenario3_ProtectedURLsAndOIDCDiscoveryFailClosed`
- AC3.2: Loopback HTTP relaxation is unavailable to production code and remains usable only by test-compiled helpers.
  - verify: `TestInvariant_singleton_broker_loopback_relaxation_is_test_only`
- AC3.3: Production readiness runs a bounded, side-effect-free OIDC/JWKS health check plus profile, ToolHive discovery, and protected-route checks; failure closes readiness without authenticating a user, minting a credential, or executing a tool.
  - verify: `TestSingletonBrokerRemediation_Scenario3_ProductionReadinessUsesRealDependencies`
- AC3.4: A successful readiness probe does not silently extend the verifier's accepted-key staleness window, and stale or unavailable keys fail closed according to the configured bound.
  - verify: `TestSingletonBrokerRemediation_Scenario3_ReadinessDoesNotLaunderStaleKeys`
- AC3.5: The public TLS listener enforces finite header, connection/read/write/idle, and per-route request-body limits. Oversized, incomplete, unsupported-content-type, or over-deadline callback requests are rejected before callback-state consumption, code exchange, ToolHive invocation, or unrelated state mutation, while a valid HTTP/2 gRPC Execute lasting up to its configured deadline still completes.
  - verify: `TestSingletonBrokerRemediation_Scenario3_PublicListenerBoundsRejectBeforeCallbackSideEffects`
- AC3.6: A production-assembled drain rejects new gRPC and callback work, lets admitted work run to the finite deadline, cancels the remainder, joins propagation work, stops listeners, and closes verifier, broker, and ToolHive resources in order.
  - verify: `TestSingletonBrokerRemediation_Scenario3_ProductionDrainAndCleanup`
- AC3.7: Every public broker RPC requires an accepted workload bearer over verified TLS before attachment, receipt, logical-session, or ToolHive state access; absent or malformed bearer, wrong issuer/audience/time/signature, plaintext transport, and wrong or untrusted server identity fail closed with no production anonymous or insecure fallback.
  - verify: `TestSingletonBrokerRemediation_Scenario3_PublicRPCAuthenticationPrecedesBrokerState`
