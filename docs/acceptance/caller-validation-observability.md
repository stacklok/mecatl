# Caller-validation observability — acceptance plan

**Status:** landed, 2026-09-08
**Contract:** human-reviewed/v2
**Work classification:** Bounded — adds closed, credential-free diagnostics at an existing authentication edge without changing a durable API, authority, persistence, or deployment decision.
**Decision record:** None — the implementation applies existing diagnostics and authentication-response contracts.
**Delivery:** Combined
**Expected tasks:** 1
**Combined rationale:** The bounded authentication-edge observability change has one compact implementation task and no new interface, authority, or persistence decision; a separate plan review would add no value.
**Issue:** [stacklok/mecatl#450](https://github.com/stacklok/mecatl/issues/450)
**Context:** [caller identity](caller-identity.md) and [architecture](../architecture.md#caller-identity).

### Scenario 1 — auth-edge outcomes are actionable without exposing identity data

Following the existing [caller-identity contract](caller-identity.md) and [architecture](../architecture.md#caller-identity), authentication enabled at the HTTP or gRPC edge emits actionable operator diagnostics without exposing identity data.

- AC1.1: diagnostics contain only the closed `outcome`, `category`, `transport`, and `status` attributes; they never contain credentials, headers, JWTs, validator error text, issuer, subject, claims, KID, or other external values.
  - verify: `TestAuthenticationRejectionDiagnosticsAreCategorizedAndSafe`, `TestHTTPMalformedAndStaticBearerRejectionsAreObservedSafely`
- AC1.2: accepted authentication is INFO and is emitted once per closed successful category/transport pair; rejection and unavailability are WARN and remain per-request. Auth-disabled requests emit no authentication diagnostic.
  - verify: `TestAcceptedAuthenticationDiagnosticsAreBoundedPerCategoryAndTransport`, `TestRejectedAndUnavailableAuthenticationDiagnosticsRemainPerRequest`, `TestAuthenticationDiagnosticsCoverEnabledSuccessAndAvailability`, `TestGRPCAuthenticationDiagnosticsCoverSuccessAndUnavailable`
- AC1.3: HTTP and gRPC retain their existing generic 401/`Unauthenticated` and 503/`Unavailable` responses and do not expose diagnostic detail to callers.
  - verify: `TestHTTPAuthenticationRejectionDiagnosticPreservesGenericClientResult`, `TestAuthenticationDiagnosticsCoverEnabledSuccessAndAvailability`, `TestGRPCAuthenticationDiagnosticsCoverSuccessAndUnavailable`
- AC1.4: ToolHive OIDC reasons map deterministically to the edge's closed categories; unknown reasons fail closed to `invalid_token`, and JWKS-unavailable reasons remain unavailable even if an upstream error code is inconsistent.
  - verify: `TestMapError`

## Human decisions

None — existing authentication response semantics and the `port.Diagnostics` pattern determine the bounded diagnostic taxonomy.

## Interface contract

- **gRPC / protobuf:** None — authentication response codes and messages are unchanged.
- **Exported Go APIs / interfaces:** None — the implementation uses the existing `SecurityConfig.Diagnostics` seam.
- **Tool schemas:** None — no tool surface changes.
- **CLI / config:** None — no configuration surface changes.
- **Events / persistence:** None — diagnostics are transient operator signals and add no event or persisted state.
- **Security / authority:** None — existing authority remains unchanged; closed diagnostic labels stay credential-free and callers continue to receive generic 401/`Unauthenticated` and 503/`Unavailable` results.
- **Compatibility / migration:** None — deployments retain their existing authentication behavior without migration.

## Definition of done

Focused auth-edge and OIDC tests, `task docs`, `task lint`, `task test`, the public-site build, and the offline demo pass without changing authentication response semantics.

## Out of scope

- Changing authentication protocols, client-facing response semantics, or the diagnostics port.
