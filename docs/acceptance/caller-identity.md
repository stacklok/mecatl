# Caller identity — completed acceptance record

**Status:** landed, 2026-08-11
**Issue:** [stacklok/mecatl#367](https://github.com/stacklok/mecatl/issues/367)
**Rationale:** [ADR 0100](../adr/0100-caller-identity-threading.md); cached-JWKS availability policy: [ADR 0101](../adr/0101-bounded-jwks-staleness.md).

Caller identity is optional OIDC authentication and durable attribution. A verified
principal is carried in the request context, sessions and schedules retain their
owner, and durable events record the caller that acted. Current behavior and operator
configuration belong in the [architecture guide](../architecture.md) and
[`mecated` usage guide](../usage/mecated.md).

## Acceptance criteria and verification mapping

### Edge and context

- **AC1.1:** When OIDC is configured, a valid token from the configured issuer and
  audience yields its `(issuer, subject)` principal on the handler context.
  - verify: `TestCallerIdentity_Scenario1_ValidTokenYieldsPrincipal`
- **AC1.2:** Invalid, malformed, or unusable principals are rejected before the
  handler runs; an unavailable JWKS is distinguishable as a 503-class condition.
  - verify: `TestCallerIdentity_Scenario1_BadTokensRejected`,
    `TestCallerIdentityEdgeRejectsMalformedPrincipal`,
    `TestCallerIdentity_Scenario1_JWKSDownIsTransientNotUnauthorized`
- **AC1.3:** Without OIDC configuration, requests remain unauthenticated with a nil
  principal; no anonymous principal is fabricated.
  - verify: `TestCallerIdentity_Scenario1_NoAuthByteIdentical`,
    `TestInvariant_no_fabricated_principal`
- **AC1.4:** OIDC enablement is independent of static-token authentication, and rate
  limiting under OIDC uses the validated `(issuer, subject)`, not the raw token.
  - verify: `TestCallerIdentity_Scenario1_IdentityPredicateIndependent`,
    `TestCallerIdentity_Scenario1_RateLimitKeyedOnPrincipal`
- **AC2.1:** The principal rides the context rather than widening port interfaces.
  Internal work that crosses a port boundary runs with an explicit system principal.
  - verify: `TestCallerIdentity_Scenario2_InternalGoroutinesRunAsSystem`

### Durable ownership and attribution

- **AC3.1:** `CreateSession` records the verified principal as the write-once session
  owner. The owner round-trips snapshots, restart/rehydration, and list rows,
  including both list implementation paths.
  - verify: `TestCallerIdentity_Scenario0_OwnerSnapshotRoundTrip`,
    `TestCallerIdentity_Scenario0_PreShipSnapshotRestores`,
    `TestCallerIdentity_Scenario0_APICompatAdditive`,
    `TestCallerIdentity_Scenario3_OwnerRecordedAndListed`,
    `TestCallerIdentity_Scenario3_OwnerSurvivesReopenRestart`
- **AC3.2:** Child sessions and forks retain the source owner. Pre-identity sessions
  remain ownerless and are never adopted on a later access.
  - verify: `TestCallerIdentity_Scenario3_ForkInheritsSourceOwner`,
    `TestCallerIdentity_Scenario3_PreShipSessionNeverBackfilled`
- **AC3.3:** Session ownership is display-only: it does not filter listings or deny
  access.
  - verify: `TestCallerIdentity_Scenario3_ListRowOwnerIsDisplayOnly`
- **AC4.1:** Durable events record the context principal that acted, not the session
  owner. The annotation is log-only and does not reach client event relays or alter
  event-sourced rehydration.
  - verify: `TestCallerIdentity_Scenario4_EventActorStampedAtAppendOnly`,
    `TestCallerIdentity_Scenario4_EventActorLogOnly`,
    `TestCallerIdentity_Scenario4_OwnerlessEventActorAbsent`
- **AC4.2:** A schedule captures its owner at creation. Its fire session retains that
  owner with `client_credentials`; the fire's events name the scheduler system
  principal.
  - verify: `TestCallerIdentity_Scenario4_ScheduleOwnerCapturedAtCreate`,
    `TestCallerIdentity_Scenario4_OutOfBandScheduleOwnerFromContext`,
    `TestCallerIdentity_Scenario4_FireRunsAsOwnerClientCredentials`

### Deployment boundary

- **AC5.1:** A configured validator fails closed at startup when initial key material
  cannot be obtained. Cached JWKS behavior follows ADR 0101: after the configured
  staleness bound, an unavailable refresh is 503-class rather than an authentication
  success or 401.
  - verify: `TestCallerIdentity_Scenario1_MisconfiguredOIDCFailsToStart`,
    `TestCallerIdentityE2E_Scenario2_RealTokenYieldsPrincipal`,
    `TestCallerIdentityE2E_Scenario2_WrongAudienceRejected`,
    `TestCallerIdentityE2E_Scenario2_StaticJWKSSkipsDiscovery`
- **AC5.2:** The published deployment configuration leaves identity off unless OIDC
  is explicitly configured and does not enable the private-issuer relaxation.
  - verify: `TestCallerIdentityE2E_Scenario3_PrivateIssuerRefusedByDefault`,
    `TestCallerIdentityE2E_Scenario3_InsecureIssuerFlagWarns`

## Non-goals

- Authorization, ownership enforcement, list filtering, and workspace isolation.
- Per-user keying of memory, soul, user-tier skills, agent definitions, commands, or
  rules.
- Per-token revocation, multi-tenant quotas, or authentication audit metrics.
- Redis authentication/TLS/keyspace scoping, snapshot MACs, event-log integrity,
  sensitivity labels, and deliberate sharing.

Caller identity is attribution, not a tenancy boundary. A valid token under a
currently trusted signing key remains valid until normal expiry; the JWKS staleness
bound limits cached signing-key trust during an identity-provider outage only.
