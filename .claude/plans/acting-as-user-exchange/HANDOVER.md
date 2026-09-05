# Handover — I3-D design and I3-C adapter-neutral acting-as-user contract

**Worktree:** `/Users/jakub/devel/mecatl/.worktrees/acting-as-user-exchange`  
**Branch:** `design/acting-as-user-exchange`  
**Base:** `acc/logical-agent-identity-projection` at `e6e5d8740`  
**Scope:** complete I3-D interactively, then—only after explicit operator approval—turn the settled design into a new ADR and scenario-first acceptance plan for I3-C. Do not implement I3-C inline or begin ToolHive production integration automatically.

## Objective

Design and, after approval, prepare the adapter-neutral core for the acting-as-user
exchange:

```text
verified user authority
+ accepted I2 logical-agent token
+ authenticated broker workload
+ explicit vMCP resource/audience and requested scope
→ short-lived outbound token
    sub = user
    act = logical agent definition
    aud = selected vMCP resource
```

I3-D owns the token-flow and trust contract. I3-C owns root-internal typed values,
validation, exchange interfaces, deterministic fakes and offline proof. I3-T later
connects this contract to ToolHive/vMCP. I3-S separately handles scheduled/offline
consent.

## Program position

```text
I0  inbound user authentication and owner binding        complete
I1  ES256 issuer, bundle and verifier                    complete
I2  definition-scoped logical-agent JWT-SVID             accepted base
I3-D acting-as-user design                               THIS SESSION, first
I3-C adapter-neutral exchange contract                   after design approval
I3-T ToolHive/vMCP integration                           later / upstream-gated
I3-S scheduled consent and offline authority             later, issue #373
I4  KMS/Vault hardening                                  later

B1  harness-to-broker workload authentication            sibling track
B4  combined issuer + MCP broker lifecycle integration   consumes I3
```

The Kubernetes projected ServiceAccount token used by the broker-client sidecar
belongs to B1. It authenticates the calling workload to the combined broker. It
is not the human subject token, I2 actor token, or final vMCP access token.

## Immutable inputs

### I1

ADR 0300 and `internal/identityissuer` own:

```text
explicit mecatl trust domain
Secret-backed ES256/P-256 signer
canonical bundle and bounded verifier
issuer-controlled JOSE/time/key fields
shell-less host custody
rotation and overlap
```

Do not weaken I1 into arbitrary `Sign` or generic claim-map APIs.

### I2

ADR 0301 is authoritative for the actor credential:

```text
sub = definition-scoped SPIFFE ID
claim = https://mecatl.dev/claims/logical-agent/v1
fields = definition_tier, definition_name, optional instance, exact tools
aud = fixed configured I3 audience
jti = correlation only
authority = current exact tool set, checked with CapabilitySet.Contains
```

I2 deliberately omits user/groups, resource restrictions, filesystem/direct-write
posture, depth, history, txn, provider credentials and holder binding. I3 must not
infer any of those from absent fields.

### Inbound user bearer

The original enterprise IdP bearer terminates at the mecak8s API authentication
edge:

```text
User -> mecak8s: bearer U, aud=mecak8s
mecak8s validates U
mecak8s persists only Owner{Issuer, Subject}
U is not persisted or reused as a broker credential
```

If the vMCP authorization server needs a subject assertion, I3 must define a
separate broker/AS-audience assertion or a tightly bounded immediate exchange.
The combined broker never treats the mecak8s login bearer as a reusable token.

### Broker workload authentication

B1 uses:

```text
mecatl harness
  -> constrained Unix socket, no broker credential
broker-client sidecar
  -> HTTPS + projected ServiceAccount token, aud=combined-broker
combined broker
  -> TokenReview + exact workload checks
```

That proves which Kubernetes workload calls. It grants no user or tool authority.
I3-C should represent the verified presenter/workload abstractly and must not
implement TokenReview or sidecar transport.

## Required source reading

### Mecatl

Read:

```text
docs/adr/0300-identity-issuer-substrate.md
docs/acceptance/identity-issuer-substrate.md
docs/adr/0301-logical-agent-identity-projection.md
docs/acceptance/logical-agent-identity-projection.md
docs/agent-identity-model.md
docs/agent-identity-outbound.md
docs/scoped-resource-grants.md
docs/adr/0234-authority-evaluator-port.md
internal/identityissuer/*
engine/governance/authority.go
engine/port/authority.go
engine/session/principal.go
```

Read full issue bodies/comments:

```text
stacklok/mecatl#367   owner identity
stacklok/mecatl#371   no-wider authority
stacklok/mecatl#372   harness acts for a user
stacklok/mecatl#373   scheduled consent, deliberately separate
stacklok/mecatl#375   key reachability
stacklok/mecatl#377   identity tracker
stacklok/mecatl#478   issuer substrate
```

### ToolHive source MCP

Use the connected ToolHive source server to inspect the exact current source and
record its revision/version:

```text
pkg/authserver/server/tokenexchange/handler.go
pkg/authserver/server/tokenexchange/*validator*.go
pkg/authserver/server/tokenexchange/factory.go
pkg/authserver/trust_config.go
pkg/authserver/config.go
pkg/authserver/storage/types.go
pkg/authserver/storage/redis.go
pkg/authserver/server/crypto/keys.go
pkg/auth/* identity context/types
pkg/vmcp/core/*check*.go
pkg/vmcp/server/modern_dispatch.go
```

Read ToolHive issues and linked PRs:

```text
#5194 RFC 8693 exchange epic
#5815 explicit actor_token and id_token subject type       closed
#5989 external OIDC subject-token consent                  closed
#6199 SPIFFE client-authentication epic                    open
#6200 SPIFFE client association/configuration              open
#6204 SPIFFE principal OAuth grant integration             open
#6082 provisioned clients/dynamic discovery dependencies
```

A capability on ToolHive main is not available to mecatl until a released version
is selected and tested.

Use OAuth, SPIFFE, agent-identity, security and ToolHive specialists. Verify RFC
claims against RFC 8693, RFC 9396, RFC 8707, RFC 9700 and the current OAuth SPIFFE
client-auth draft rather than relying only on issue summaries.

## Central actor-binding mismatch

ToolHive #5815 currently describes a binding like:

```text
actor_token.sub == authenticated client_id
```

I2 deliberately says:

```text
actor_token.sub = logical agent definition
```

The authenticated presenter is instead the combined broker workload:

```text
authenticated client/workload = combined broker
actor                        = code-reviewer definition
```

These must not be made equal by renaming either identity. I3-D must define the
association the authorization server verifies:

```text
presenter is an admitted combined-broker workload
I2 signature verifies under mecatl trust-domain bundle
I2 audience is the vMCP authorization server
I2 logical definition is allowed for this presenter/deployment
I2 exact tools contain the requested external capability
user subject independently validates and consents to this actor/client
requested resource/scope fits both user and actor authority
```

Determine whether ToolHive #6200/#6204 provide this association or need a precise
upstream change. Do not build a local workaround that skips server enforcement.

## I3-D decisions to produce

### D1 — Subject-token source

Choose the live user input to RFC 8693 exchange:

1. a separate IdP token explicitly audience-bound to the vMCP AS;
2. a short-lived assertion obtained by the mecak8s auth edge for this exchange;
3. immediate forwarding of the original bearer over a protected path, accepted
   only if the AS explicitly validates its issuer/audience/actor consent;
4. another standards-grounded artifact.

The default preference is a separate AS-audience assertion. Never persist the
mecak8s login bearer. Show how this works when the user reconnects.

### D2 — User subject identifier

Define the final outbound `sub`:

```text
raw upstream subject?
issuer-qualified stable subject?
ToolHive internal user ID?
pair represented through iss/sub?
```

It must not collide across issuers. Explain how vMCP resolves it to exactly one
provider credential and how audit maps it back to the session owner without
putting raw credentials in claims.

### D3 — Actor evidence

Define the exact I2 token profile used as `actor_token`:

```text
actor_token_type = urn:ietf:params:oauth:token-type:jwt
actor_token      = compact I2 JWT-SVID
```

Specify audience, TTL, allowed definition tiers, optional instance treatment,
current exact tools and key/bundle verification.

### D4 — Presenter/client association

Define how the AS knows the broker workload may present an I2 agent definition.
The projected Kubernetes token authenticates harness-to-broker, not broker-to-AS.
Possible broker-to-AS methods include confidential-client authentication or
SPIFFE client authentication. Whatever is chosen must bind one deployment's
allowed logical definitions/scopes/resources without granting all supported
values to every workload.

### D5 — Consent

External subject-token exchange requires proof that the user permitted this
client/actor. Evaluate:

```text
may_act
issuer-specific azp/appid/cid allowlist
explicit signed consent evidence
interactive OAuth grant already held by ToolHive
```

State which decision is user consent, which is operator authorization, and which
is merely authentication. The model cannot author consent.

### D6 — Scope/resource intersection

The final request must satisfy all ceilings:

```text
user subject-token authority
broker workload/client association
I2 actor exact tool authority
requested OAuth scope/resource
resolved vMCP target policy
```

Define the intersection/refusal rule. I2 has exact tool names but no general
resource claim; I3 must not infer repository/path scope from a tool name.

### D7 — Output token

Define exact required claims, for example:

```json
{
  "iss": "https://as.vmcp.example",
  "sub": "issuer-qualified-user",
  "act": {
    "sub": "spiffe://agents.example/mecatl/agent-definition/v1/user/code-reviewer--..."
  },
  "aud": "https://vmcp.example",
  "scope": "github.read",
  "exp": 1780000300,
  "iat": 1780000000,
  "jti": "..."
}
```

Decide `client_id`/`azp`, `authorization_details`, resource and instance/audit
metadata. Prior actor chains are audit only and must not grant current rights.

### D8 — Presenter/holder binding and replay

Issue #372 previously deferred `cnf` because it only helps if the broker owns the
data path. The combined broker is now intended to proxy the MCP call, so the
trigger may have fired. Decide:

```text
v1 floor: narrow aud + short exp
optional/later: RFC 8705 mTLS cnf bound to broker connection
```

Never emit `cnf` unless the gateway verifies it against the actual connection.
If deferred, name maximum TTL as the complete replay window.

### D9 — Exchange cache

Define cache keys and bounds for:

```text
root broker/definition association
per-definition actor narrowing
user+definition+resource output token
```

A cache may reuse only decisions whose inputs match exactly. Authority narrowing,
user revocation, key rotation and token expiry must invalidate or bound it. An
unreachable AS must not block local-only child creation.

### D10 — Revocation and expiry

RFC 8693 does not automatically revoke output tokens when the input token is
revoked. State the short-lived residual window and whether high-risk operations
require introspection or live policy checks.

No expiry may fall back to:

```text
service identity
ownerless identity
broader actor
ambient provider credential
```

### D11 — Interactive resume

After a parked Stage 3 authorization:

```text
fresh user request authenticates at mecak8s edge
owner must match durable session owner
broker obtains/validates current subject authority
I2 actor is freshly minted/verified
exchange produces short-lived output token
```

Stage 3 authorization ID is lifecycle correlation, not user authority.

### D12 — Scheduled work boundary

Do not solve scheduled consent inside live I3-C. Produce an explicit handoff to
I3-S/#373:

```text
schedule creation captures signed bounded consent
grant lifetime <= schedule lifetime
fire re-derives short-lived output token
expiry fails closed
no retained login bearer
```

Choose formats only where I3-S needs compatibility; do not implement them in C.

### D13 — Broker-client sidecar boundary

B1 workload authentication precedes I3. Define the minimum credential-free
request context I3-C expects after B1 verification:

```text
VerifiedPresenter
CanonicalSessionReference
VerifiedOwnerReference
I2 actor token
TargetResource
RequestedScopes
```

It must not accept caller-supplied `Authorization`, arbitrary broker method,
issuer, audience, subject, raw backend URL or generic claims.

### D14 — Audit correlation

Define a non-authorizing correlation joining:

```text
mecak8s event log
exchange audit
vMCP decision
provider invocation
```

It cannot be used to select owner, actor, credential or target. Keep internal run
correlation distinct from the outbound request correlation if their trust
boundaries differ.

## I3-C candidate architecture

The core should remain root-internal and provider-neutral. Illustrative values:

```go
type VerifiedUserSubject struct {
    Issuer  string
    Subject string
}

type VerifiedPresenter struct {
    WorkloadID string
    ClientID   string
}

type ExchangeRequest struct {
    User       VerifiedUserSubject
    Actor      identityissuer.VerifiedLogicalAgent
    Presenter  VerifiedPresenter
    Resource   string
    Scopes     []string
    CorrelationID string
}

type ActingAccess struct {
    Token     secretValue
    ExpiresAt time.Time
}

type Exchanger interface {
    Exchange(context.Context, ExchangeRequest) (ActingAccess, error)
}
```

Names and packages are not decisions. Requirements:

- no raw claim map;
- no user bearer in a durable/domain type;
- no provider credential;
- no model arguments;
- no arbitrary URL or audience;
- no ToolHive type in engine packages;
- secret result never formatted/logged;
- copies/zeroization claims remain honest in Go;
- registered resource/scope policy supplied by trusted composition.

Consider separating:

```text
ExchangePlan       non-secret validated decision/input
SubjectCredential  ephemeral secret supplied only at execution
ActingAccess       ephemeral secret output held only by broker
VerifiedActingIdentity non-secret parsed output for tests/audit
```

This prevents a reusable request value from carrying the user's bearer.

## I3-C implementation boundary after approval

I3-C may implement:

1. closed typed request/value validation;
2. I2 actor-token verification and extraction;
3. issuer-qualified user identity handling;
4. exact scope/resource canonicalization and registered allowlists;
5. a provider-neutral exchange interface;
6. deterministic offline fake AS/exchanger;
7. output-token typed verifier/profile;
8. cache-key derivation and TTL rules if approved;
9. secret leakage/error/diagnostic tests;
10. vertical fake proof:

```text
Alice + code-reviewer(read) -> read token accepted
Alice + code-reviewer(read) -> deploy exchange refused
Alice + deployer(write)     -> deploy token accepted positive control
Bob/session mismatch        -> refused
```

I3-C must not implement:

```text
another production OAuth/token-exchange client
ToolHive integration or patches
combined broker RPC
TokenReview/projected token sidecar
provider credential lookup
vMCP route admission
Stage 3 continuation
Redis persistence/fencing
scheduled consent grant
KMS/federation
```

If an adapter-neutral contract would only mirror RFC fields without enforcing a
real invariant, stop rather than add abstraction for its own sake.

## Failure taxonomy to settle

Use typed internal categories without token/claim echo:

```text
invalid subject assertion
subject/owner mismatch
invalid actor token
actor/presenter association denied
actor authority insufficient
resource/scope exceeds ceiling
consent absent or denied
unsupported token profile
exchange temporarily unavailable
output token invalid
```

Distinguish permanent authorization refusal from retryable infrastructure error.
Neither category may trigger service/ownerless fallback.

## Required adversarial proofs

The eventual I3-C acceptance plan should include:

- A valid user token paired with another user's durable owner is refused.
- A valid I2 token for a different deployment/audience is refused.
- A valid broker workload cannot present an unassociated logical definition.
- A reviewer actor requests deploy scope; exchange refuses before returning a
  token, while a legitimate deployer positive control succeeds.
- Actor token signature is valid but exact tools omit the requested operation;
  refusal does not depend only on `act.sub`.
- User subject token has valid signature but wrong audience/client consent;
  refusal occurs.
- Requested resource/scope exceeds user, presenter or actor ceiling by one value;
  each independently refuses.
- Duplicate/unknown claims, multiple audiences, algorithm confusion, oversized
  tokens, stale bundles and expired inputs fail closed.
- Output token has exact user `sub`, agent `act.sub`, vMCP `aud`, bounded scope and
  lifetime; unrelated claims grant nothing.
- Compact subject, actor and output tokens are present at their intended secret
  boundaries and absent from errors, diagnostics, events, snapshots, tool results
  and model-visible values.
- Exchange outage does not prevent a local-only child from running and does not
  turn an external call anonymous.
- Cached exchange output never outlives any input TTL or authority/policy version.
- I3 disabled leaves I1/I2 behavior and stored bytes unchanged.

Every rejection needs a positive-control sibling so “deny everything” cannot
satisfy the proof.

## ToolHive integration handoff

I3-D must finish with an explicit matrix:

```text
required behavior
current ToolHive source symbol
available release/version
works unchanged | needs configuration | needs upstream change
issue/PR and acceptance evidence
```

At minimum cover:

- external issuer validation and consent;
- `actor_token` parsing;
- actor/presenter association;
- `client_credentials`/client authentication;
- requested scope and resource intersection;
- output `sub`/`act` construction;
- discovery metadata;
- JWKS/bundle fetch and staleness;
- Redis restart behavior;
- post-admission credential lookup.

Do not start I3-T until the matrix has no unowned blocker.

## Reviews required

Run a coordinated design review with:

- OAuth/RFC 8693 specialist;
- SPIFFE specialist;
- agent-identity thought-leader specialist;
- ToolHive source expert;
- authorization/Cedar or AuthZEN specialist for target/scopes;
- security reviewer for token confusion, consent and replay;
- privacy reviewer for user/agent audit identifiers;
- software architect for I3-C's value/interface boundary;
- dark-factory critic for proofs that could pass without real intersection.

## Expected design artifacts

Keep design-session work under `.scratch/acting-as-user-exchange/`:

```text
PROGRAM.md          I3-D/C/T/S and B1/B4 boundaries
SUBJECT.md          human bearer termination and exchange subject
ACTOR.md            I2 token profile and presenter association
EXCHANGE.md         RFC 8693 request/output contract
CONSENT.md          user/operator authorization model
AUTHORITY.md        scope/resource intersection
TOKENS.md           output claims, lifetime, cache and revocation
FAILURES.md         permanent/retryable failure taxonomy
TOOLHIVE.md         source/version capability matrix
SECURITY.md         replay, confusion, leakage and adversarial cases
DECISIONS.md        accepted/rejected/deferred choices
RESULT.md           I3-C acceptance-plan handoff
```

Do not modify ADR 0300 or ADR 0301 during exploration. A new ADR records I3.
Do not modify active Stage 3 or B0 handovers/contracts.

## Likely I3-C acceptance scenarios after design approval

1. **Verified user and actor inputs are distinct and typed.**
2. **Presenter association binds an admitted broker workload to an I2 definition.**
3. **Scope/resource request is contained by every authority ceiling.**
4. **Acting-as-user output token has exact `sub`, `act`, `aud` and bounded lifetime.**
5. **Independent verification rejects profile confusion, replay-prone and malformed inputs.**
6. **Secret tokens never persist or leak.**
7. **Cache/expiry/outage behavior fails closed without blocking local-only work.**
8. **Reviewer-read/deployer-write vertical with positive and negative controls.**
9. **ToolHive compatibility matrix identifies every remaining I3-T blocker.**

## Terminal conditions

### I3-D complete

The operator can answer:

```text
what proves the user subject?
what proves the logical actor?
what authenticates the presenter?
what association permits that presenter to use that actor?
which scopes/resources are intersected and where?
what exact token does vMCP receive?
what is the replay/revocation window?
what is cached and under which complete key?
what happens on reconnect and outage?
what remains for schedules, ToolHive and B4?
```

Stop for explicit approval.

### I3-C ready to orchestrate

After approval:

1. write a new ADR;
2. produce a self-contained acceptance plan with numbered criteria and executable
   offline verification;
3. run advisory design/security/ToolHive/dark-factory reviews;
4. hand the approved plan to `/plan-orchestrate` only when the operator asks.

Do not implement I3-T or I3-S as part of I3-C.
