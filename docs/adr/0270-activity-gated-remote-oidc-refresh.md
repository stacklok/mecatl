# ADR 0270 — Activity-gated remote OIDC refresh

- Status: Accepted
- Date: 2026-08-26
- Scope: `mecatui` remote OIDC credential refresh
- Supersedes: None
- Superseded by: ADR 0277

## Context

A remote access token can expire while an operator is still using a session. A
refresh token can also be rejected after the identity provider's SSO idle
lifetime has elapsed. Refreshing on a wall-clock timer would prevent that idle
lifetime from expiring even while the operator is away: every refresh resets
the provider's idle clock and silently changes the deployment's session policy.

## Decision

Use activity-gated proactive refresh in `clientauth.RefreshSource`. A successful
public `Token` call is activity, including one that returns an already-valid
access token. The background refresher is not activity and can refresh only
when activity has occurred since the last refresh and the stored access token
is within the existing 30-second refresh-ahead window. A bounded poller checks
this condition; it is not a timer that refreshes unconditionally.

Serialize public and proactive work, including credential loading, token
validation, provider exchange, and CAS persistence, with the source mutex. The
source owns and joins the poller during `Close`, before closing its validator;
it does not own the credential store. Rotated access and refresh tokens remain
subject to the existing versioned CAS save. If a proactive refresh receives `invalid_grant`,
its safe `SessionExpired` cause is retained for the next public token request even
though the rejected credential is deleted. Credential deletion errors other than an
already-absent record are reported as a safe operational cleanup failure instead of
claiming that login is required.

Keep `ErrLoginRequired` as the compatibility sentinel and attach a safe typed
cause: `NotEnrolled` for an absent credential, `CredentialUnusable` for a
corrupt or malformed credential, and `SessionExpired` for a missing refresh
token or an `invalid_grant` refresh rejection. Store and CAS failures remain
operational errors. A server's `Unauthenticated` response is a transport-layer
`Rejected` diagnosis, not a `clientauth` cause, because the refresh source
cannot observe the server verdict.

Never include access tokens, refresh tokens, authorization codes, or provider
response bodies in errors, diagnostics, or UI state. The behavior is covered by
offline `httptest` fixtures; live Kind qualification is separate.

## Consequences

An actively used remote session can renew before its access token expires and
persists rotation safely, PROVIDED the provider's access-token lifetime is shorter
than its SSO idle lifetime. The refresher acts inside the existing 30-second
refresh-ahead window, so it contacts the provider roughly once per access-token
lifetime; that is what keeps the idle clock from lapsing. Where a deployment
configures a token lifetime LONGER than its idle timeout, the idle session expires
before the first refresh is due and an active session still ends in
`SessionExpired`. The client cannot detect this: the idle lifetime is not published
in discovery metadata. Deriving the lead time from a configured hint, rather than
from token expiry alone, is the remedy if such a deployment appears. An idle source does not keep the identity provider's
SSO session alive, so deployment idle policy remains effective. The source
contains a small polling goroutine and must be closed; callers retain that
lifecycle responsibility. Local credential failures are more diagnosable while
existing sentinel-only callers continue to work.

## See also

- [ADR 0273 — Remote mecatui OIDC client login](./0273-remote-mecatui-oidc-login.md)
- [ADR 0218 — Internal encrypted credential-store substrate](./0218-credential-store.md)
- [ADR 0235 — Scoped private HTTPS OIDC transport](./0235-scoped-private-https-oidc-transport.md)
- [ADR 0236 — Private HTTPS OIDC issuer](./0236-private-https-oidc-issuer.md)
- [ADR 0206 — Ship reusable OIDC caller identity](./0206-oidc-authn-module.md)
- [Architecture guide](../architecture.md)
- [Historical readiness tracker](https://github.com/stacklok/mecatl/blob/33a3747d9008691d4d51a872c9e82c050c43fafa/docs/design/PRODUCTION-READINESS.md)

---
