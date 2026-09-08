# Caller-validation observability — acceptance plan

**Status:** landed, 2026-09-08
**Issue:** [stacklok/mecatl#450](https://github.com/stacklok/mecatl/issues/450)
**Context:** [caller identity](caller-identity.md), [architecture](../architecture.md#caller-identity),
and [`mecated` authentication](../usage/mecated.md#security--transport-auth-tls-rate-limiting).

## Scenario — auth-edge outcomes are actionable without exposing identity data

With authentication enabled, rejected credentials or malformed authorization input,
and validator/IdP unavailability emit one structured diagnostic at the HTTP or gRPC
edge per request. Successful validation emits one INFO diagnostic per closed
category/transport pair for the process, rather than per request.

- AC1: diagnostics contain only the closed `outcome`, `category`, `transport`, and
  `status` attributes; they never contain credentials, headers, JWTs, validator error
  text, issuer, subject, claims, KID, or other external values.
- AC2: accepted authentication is INFO and is emitted once per closed successful
  category/transport pair; rejection and unavailability are WARN and remain
  per-request. Auth-disabled requests emit no authentication diagnostic.
- AC3: HTTP and gRPC retain their existing generic 401/`Unauthenticated` and
  503/`Unavailable` responses and do not expose diagnostic detail to callers.
- AC4: ToolHive OIDC reasons map deterministically to the edge's closed categories;
  unknown reasons fail closed to `invalid_token`.

Verification: `TestAuthenticationRejectionDiagnosticsAreCategorizedAndSafe`,
`TestHTTPAuthenticationRejectionDiagnosticPreservesGenericClientResult`,
`TestAuthenticationDiagnosticsCoverEnabledSuccessAndAvailability`,
`TestAcceptedAuthenticationDiagnosticsAreBoundedPerCategoryAndTransport`,
`TestRejectedAndUnavailableAuthenticationDiagnosticsRemainPerRequest`,
`TestGRPCAuthenticationDiagnosticsCoverSuccessAndUnavailable`, and `TestMapError`.

## Definition of done

Focused auth-edge and OIDC tests, `task docs`, `task lint`, `task test`, the public-site
build, and the offline demo pass without changing authentication response semantics.
