---
id: 03-broker-auth-callback-service
title: Authenticated TLS broker service and ToolHive callback lifecycle
blocked_by: [01-remote-contract]
status: done
attempt: 1
branch: plan-initial-production-mcp-broker/03-broker-auth-callback-service-attempt-1
worktree: .scratch/worker-initial-production-mcp-broker-03-broker-auth-callback-service-attempt-1
issue: ""
retries: 0
last_error: ""
accumulator: acc/initial-production-mcp-broker
---

# Task brief

Build the broker-side authenticated transport and service boundary, retaining ToolHive ownership exclusively in `mecabroker`. Reuse the OIDC validator with strict HTTPS issuer, audience, nonzero bounded JWKS staleness, verified TLS, closed-operation diagnostics, and admission before state touch. Mount ToolHive's entire fixed route bundle plus final callback through a shared lifecycle. Callback OAuth is state-bound and unauthenticated; it must not become a client authority channel. Pin confidential broker-client handling under ADR 0302. Add local `httptest` issuer/upstream coverage; no external network.

## Acceptance criteria

- AC2.1: A valid configured workload token over verified TLS can invoke every broker RPC, and the same calls without a bearer are rejected as unauthenticated before broker state is read.
  - verify: `TestInitialProductionMCPBroker_Scenario2_AuthenticatedTLS`
- AC2.2: Wrong issuer, wrong audience, expired token, invalid signature, malformed bearer, an untrusted or wrong-name server certificate, and plaintext non-loopback configuration fail closed; production exposes no insecure-TLS escape hatch.
  - verify: `TestInitialProductionMCPBroker_Scenario2_RejectsInvalidIdentity`
- AC2.3: Identity, bearer values, tool arguments, callback state, presentation URLs, and credentials never appear in diagnostics or metric labels; bounded diagnostics use closed operation/outcome labels.
  - verify: `TestInvariant_initial_broker_observability_is_bounded_and_secret_free`
- AC2.4: Browser callback fields and model/client input cannot select a mecatl session, owner, backend, route, or authenticated principal; canonical authority comes from broker-created state and operator configuration.
  - verify: `TestInvariant_initial_broker_callback_cannot_supply_authority`
- AC2.5: Production OIDC verification uses HTTPS with explicit trust, exact issuer and audience, and a non-zero bounded JWKS-staleness policy; once that bound is exceeded, key-source outage returns unavailable and never authenticates from indefinitely stale keys or downgrades to anonymous access.
  - verify: `TestInitialProductionMCPBroker_Scenario2_JWKSFailureIsBounded`
- AC2.6: The embedded ToolHive client remains confidential: registration persists only its hash with `client_secret_basic`; code exchange and refresh use HTTP Basic and omit `client_secret` from form bodies; the raw secret appears in no gRPC message/metadata, snapshot, event, diagnostic, metric, profile, or upstream request and is intentionally lost on broker restart.
  - verify: `TestADR_0302_RemoteBrokerPreservesConfidentialClient`
