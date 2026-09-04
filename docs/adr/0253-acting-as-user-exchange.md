# ADR 0253 — Acting-as-user exchange boundary

- Status: Proposed
- Date: 2026-09-03
- Scope: the I3 live acting-as-user token-exchange trust contract and the adapter-neutral I3-C core boundary
- Supersedes: none
- Superseded by: none

## Context

ADR 0204 binds a durable session to a verified `(issuer, subject)` owner pair without retaining an inbound credential. ADR 0234 makes child tool authority a monotone carried capability set. ADR 0251 provides a shell-less broker-host ES256 trust-domain substrate, and ADR 0252 projects one logical agent definition plus its exact current tools as a compact JWT-SVID. None of those facts authorizes an external provider call on behalf of a user.

The external authorization decision has distinct principals and ceilings. A user subject proves whose authority is being spent; the I2 logical-agent token proves which definition acts and its exact tools; the combined broker workload proves which presenter submits the exchange; and a registered vMCP resource/operation identifies the target. Conflating a broker OAuth client identifier with the I2 logical-definition subject would remove the very association the authorization server must check. Conversely, verifying independently valid user and actor JWTs is insufficient: an attacker could recombine them with an unassociated presenter or broader target.

The original bearer used to authenticate at the mecak8s edge is audience-bound to mecak8s, is deliberately not durable, and is not a reusable broker credential. Schedules have no live user and therefore require a separate consent design.

ToolHive current main supports RFC 8693 subject-token exchange and represents a distinct self-issued actor token subject in the output `act` claim, but it validates actor tokens only against its own JWKS and binds their `client_id` to the authenticated client. It cannot yet validate Mecatl I2 evidence or enforce a presenter-to-logical-definition association. A main-only capability is not an I3-T release dependency.

## Decision

### Live exchange profile

A live acting-as-user exchange accepts only:

```text
verified durable owner          = (issuer, subject)
fresh user subject assertion    = configured AS audience and consent profile
I2 actor_token                  = compact logical-agent JWT-SVID
verified broker presenter       = B1-proven workload/client identity
registered request              = one resource, operation, canonical scopes
```

The user assertion is a fresh, short-lived **exchange-subject assertion** minted under one configured issuer profile for the combined broker/AS exchange audience; it is not an ordinary mecak8s login bearer or an ID token. Its verifier pins issuer and key source, allowed algorithms and `typ`, exact singleton audience, required `iss`/`sub`/`exp`/`iat`/`nbf`, bounded age and skew, size, and any issuer-profile authorized-party/client binding. It rejects duplicate security members and all unprofiled claims/tokens, returns only the issuer-qualified owner, a non-secret consent proof, and `NotAfter`, and never exposes a generic claims map. It is sent only to the immediate exchange and never persisted. It must resolve to the durable session owner’s exact `(issuer, subject)` pair. On reconnect, authentication, owner comparison, subject authority, I2 issuance/verification, and exchange all repeat. A Stage 3 authorization ID is correlation only.

The consent proof is independently authenticated and bound to the exact `(owner, authenticated presenter/client, logical definition, registered resource, operation/authorization detail, canonical scopes, validity, policy/consent version)` tuple. It is a prerequisite, not a policy allow: an explicit deny or indeterminate result in any source wins, and consent never overrides target policy. I3-C represents this proof only as a verifier result; choosing the enterprise AS/IdP artifact and revocation mechanism is a deployment/I3-T decision. Its `NotAfter` is mandatory.

Use RFC 8693 parameters with a compact JWT actor token:

```text
grant_type           = urn:ietf:params:oauth:grant-type:token-exchange
subject_token_type   = urn:ietf:params:oauth:token-type:access_token
actor_token_type     = urn:ietf:params:oauth:token-type:jwt
requested_token_type = urn:ietf:params:oauth:token-type:access_token
resource             = exactly one registered RFC 8707 resource
scope                = exact canonical space-delimited requested scopes
authorization_details = exact registered RFC 9396 operation detail, when the
                        resource/scope pair is not itself one operation
```

The registered request is a single canonical tuple `(resource, operation, authorization details, scopes)`. A registry maps it to required I2 tools and permitted scopes; a mechanism cannot replace it with defaults, a different operation, a broader detail, or a cross-resource scope. I3-C accepts only the closed profile above; cross-domain identity chaining is deferred and any issuer/tenant outside the configured bilateral profile is rejected.

The authorization server verifies the I2 token under the same atomically replaced, complete, bounded-freshness Mecatl trust-domain bundle and rotation schedule as ADR 0251; it pins the fixed exchange audience, ES256/profile/time rules, canonical definition subject, and exact tool set. It rejects retired/unknown keys after the bounded freshness window. I2 `instance` and `jti` are audit correlation only; no absent I2 field grants user, resource, consent, presenter, or holder authority.

The AS-authenticated broker client/presenter and the I2 logical definition are separate identities. B1 proves only the workload-to-broker hop; the AS independently authenticates the broker client on the exchange connection (for example through a registered mTLS client) and derives presenter/client identity from that authentication, never from a request field. The authorization server must enforce an operator-owned, deployment-scoped, validity-bounded association selected only from that presenter identity and registered policy. It binds the presenter, logical definition, resource, operation/detail, and canonical requested/granted scope ceiling; absent, ambiguous, stale, or explicit-deny association records refuse. This association is the required authorization fact; equality of `actor_token.sub` and `client_id` is not an alternative.

Permit an exchange only when every independent ceiling permits the exact request:

```text
owner matches verified user subject
AND verified subject authority permits the exact tuple
AND independently verified consent contains the exact tuple
AND AS-authenticated presenter association permits the exact tuple
AND I2 exact tools contain the registered operation requirements
AND registered resource/scope/detail ceiling contains request
AND target policy permits the exact tuple in current context
```

Each source returns permit, explicit deny, or unavailable/indeterminate plus a mandatory `NotAfter` when it permits. Any deny, unknown, malformed, missing, or indeterminate value refuses; unavailable is a distinct retryable failure but grants nothing. The gate computes the output lifetime from the minimum verified subject, actor, consent, association, target-policy, and configured bounds before it invokes issuance.

Unknown, missing, duplicate, noncanonical, or unregistered inputs deny. A logical tool name never implies repository, path, tenant, provider, or write authority. A correlation value is audit-only and never selects identity, actor, target, credential, or policy.

The issued RFC 8693 response must contain `access_token`, `issued_token_type`, `token_type`, `expires_in`, and the effective `scope`; I3 rejects a refresh token or any widened/missing response value. The compact output profile has a collision-resistant, domain-separated encoding of the upstream `(issuer, subject)` pair as `sub`, exactly one registered resource `aud`, canonical granted scopes and authorization details, exact closed `act = {"sub": logical-definition-spiffe-id}`, and one mandatory client-attribution claim derived from AS client authentication. It rejects nested/unknown authority-bearing `act` members, multiple audiences, `cnf`, raw user assertion, I2 token/tool list, provider credential, session ID, and correlation. Its protected header and temporal fields are a closed pinned-algorithm/`typ`/`kid`/`iat`/`nbf`/`exp` profile with bounded skew. Its expiry is no later than the computed minimum bound, and it has no refresh token.

The AS generates a non-secret opaque exchange-decision ID and binds it to its immutable audit record and output/protected introspection record. It can join the broker, vMCP, and provider audit logs but is never a request lookup, cache key, policy input, identity selector, or authorization source. I2 `jti` remains correlation, not replay prevention. The v1 bilateral profile accepts exchange-input replay only until the original minimum input/consent/association deadline; it must not mint a token beyond that bound. A deployment wanting single-use assertions needs a bounded atomic replay ledger, which is deferred from I3-C along with persistent caching.

Until the broker is the data-path proxy and the gateway verifies a real RFC 8705 mTLS confirmation against that connection, the output is a bearer token and its configured short expiry is the complete replay window. `cnf` is forbidden unless enforcement exists. High-risk actions require live policy/introspection or await holder binding. An expiry, denial, or outage never falls back to service, ownerless, broader-actor, or ambient provider authority.

### I3-C core boundary

I3-C introduces a root-internal `internal/actingaccess` gate, not an engine port or a ToolHive adapter. It owns closed, typed non-secret owner/presenter/resource/operation/scope/detail values; profile-specific ephemeral secret wrappers for subject assertion, I2 token, and output token; verification orchestration; the concrete conjunction above; bounded failure kinds; and independent output-profile verification.

The consumer-owned collaborators are narrow: subject assertion verification, I2 verification adapted from `internal/identityissuer.LogicalAgentVerifier`, independently typed consent/association/target decisions, output issuance, and output verification. They return only non-secret facts, permit/deny/unavailable, and validity bounds. The gate verifies the compact I2 token itself and copies its tool set before policy evaluation; it does not accept caller-supplied `identityissuer.VerifiedLogicalAgent` as self-authenticating evidence. Trusted composition constructs registered request values. The mechanism receives an exported root-internal immutable input with unexported fields and read-only accessors: external adapters can implement it but cannot construct an authorized decision. I3-C has deterministic offline fakes and no output-token cache, persistence, ToolHive transport, TokenReview, sidecar RPC, provider credential lookup, target route admission, Stage 3 continuation, scheduled consent, or KMS/federation implementation.

## Consequences

Mecatl gains a testable, provider-neutral authorization boundary before any production exchange integration. The boundary preserves I2’s no-wider exact-tool proof and ADR 0204’s issuer-qualified owner identity while refusing dangerous artifact recombination. It also keeps credentials outside durable/model-visible values.

The design deliberately cannot make a production ToolHive call yet. I3-T requires a selected released ToolHive version plus black-box integration proof for external I2 actor-token trust, explicit presenter-to-definition association, subject consent, scope/resource intersection, target-bound credential lookup, and restart behavior. SPIFFE OAuth client authentication is upstream-gated. I3-S must separately define signed bounded unattended consent.

## See also

- [ADR 0204 — caller identity threading](./0204-caller-identity-threading.md)
- [ADR 0234 — authority evaluator port](./0234-authority-evaluator-port.md)
- [ADR 0251 — identity issuer substrate](./0251-identity-issuer-substrate.md)
- [ADR 0252 — logical-agent identity projection](./0252-logical-agent-identity-projection.md)
- [Agent identity outbound](../agent-identity-outbound.md)
- [RFC 8693](https://www.rfc-editor.org/rfc/rfc8693), [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707), [RFC 8705](https://www.rfc-editor.org/rfc/rfc8705), [RFC 9396](https://www.rfc-editor.org/rfc/rfc9396), and [RFC 9700](https://www.rfc-editor.org/rfc/rfc9700)
