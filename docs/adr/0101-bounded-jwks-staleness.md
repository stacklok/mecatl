# ADR 0101 — Bound cached JWKS staleness

- Status: Accepted
- Date: 2026-08-11
- Scope: OIDC caller-identity validation and its cached JWKS availability policy
- Supersedes: —
- Superseded by: —

## Context

OIDC validation depends on signing keys fetched from the identity provider. Keeping the
last successful JWKS during a temporary outage improves availability, but trusting that
cache forever means a revoked signing key remains valid for the full outage. Rejecting
all tokens as soon as refresh fails has the opposite failure mode: a brief identity
provider or network interruption takes every authenticated mecatl request down.

The shared `toolhive-core/authn` validator can bound cached-key staleness and attempts a
refresh before rejecting keys that cross the bound. mecatl must choose the deployment
policy explicitly rather than inherit an unbounded library zero value.

## Decision

Add `--oidc-max-jwks-staleness` to the shared OIDC configuration used by both server
mains and pass it to `authn.Config.MaxJWKSStaleness`.

Default the flag to **1 hour**. An explicit **0** disables the upper bound for operators
who knowingly prefer availability during an extended identity-provider outage. Reject
negative durations as a startup/configuration error.

Once cached keys are older than the configured bound, validation first attempts to
refresh them. If refresh cannot succeed, reject validation with the existing
unavailable classification, which the mecatl edge maps to **HTTP 503**, not 401. Keep
issuer equality, required audience, HTTPS/private-address protections, error mapping,
and optional validator teardown unchanged.

Do not persist the JWKS cache. A restarted process reconstructs the validator and
fetches current key material from the identity provider; signing keys remain a
derivation of the IdP's live JWKS, not mecatl-owned durable state.

## Consequences

The default limits the revoked-key exposure window during an IdP outage to one hour,
while tolerating ordinary short outages. After that window, authenticated requests fail
closed with a retryable service-unavailable response until keys can refresh. Operators
can restore the previous unbounded-availability posture with an explicit zero, accepting
the corresponding revocation risk. Restarting while the IdP is unavailable cannot rely
on an old on-disk key cache because none exists.

## See also

- [ADR 0100](./0100-caller-identity-threading.md) — caller identity and the shared OIDC validator
- [ADR 0027](./0027-cloud-native.md) — process-owned JWKS cache lifecycle and restart policy
