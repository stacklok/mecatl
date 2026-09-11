# Agent identity: mecatl as its own SPIFFE trust domain

*Status: strawman / working draft. Speculative scoping, not a design record under
[ADR 0002](adr/0002-documentation-lifecycle.md) (no frozen decision here). Same
tier as [`docs/scoped-resource-grants.md`](scoped-resource-grants.md). If this
direction is ever committed, it becomes one or more ADRs and this doc gets
superseded.*

This doc proposes how mecatl identifies **who is acting** in a multi-user,
multi-session, autoscaled deployment: a user spawns an agent, the agent spawns
subagents, every hop holds a strict subset of its parent's authority, and the
whole delegation tree leaves a durable, verifiable audit trail. It is written
to be concrete enough to argue with. Nothing here is implemented.

**mecatl becomes its own SPIFFE trust domain issuer.** Not a consumer of SPIRE
per-workload identities, not a client of the (experimental) SPIFFE Broker API,
but an issuer in its own right, with its own signing keys, its own bundle endpoint,
and a claim vocabulary the SPIFFE JWT-SVID spec explicitly leaves open to us.
SPIRE (or cloud IAM) only ever attests the *infrastructure*: the mecatl pods
themselves. Everything above the pod (users, agent definitions, sessions,
subagents) is mecatl's own identity domain.

## Why this matters

A cloud-native agent harness is a **multi-tenant delegation machine**, and
today it runs on trust-me semantics. One mecatl process serves many users'
sessions at once; each session's agent runs tools, calls forges and MCP
servers, and spawns subagents on the user's behalf. Nothing cryptographic
distinguishes any of them. Concretely, what that means today:

- **The audit trail is the harness's word.** The event log records what
  happened, but it is self-attested. An operator, auditor, or downstream
  service cannot *verify* that a given action traces back through a specific
  subagent, agent, and user. The chain exists only as the harness's own
  say-so.
- **Outbound calls are unattributable.** When a subagent's work results in
  an API call to a forge or an MCP server, the callee sees the harness (or
  worse, the user's ambient credentials), not *which* agent, acting for
  *which* user, with *what* delegated authority. There is no way to answer
  "who actually did this?" from the outside.
- **Delegation has no teeth.** mecatl already narrows what subagents may do
  (its permission evaluator, its posture ladder), but the narrowing is
  internal convention. Nothing makes "a subagent holds a strict subset of
  its parent's authority" a property a third party can check. Nor does
  anything force the system itself to uphold it at the boundary.
- **Multi-user is a promise, not a proof.** Sessions are isolated by
  software discipline. There is no per-user, per-agent identity a reviewer
  can point to and say "this action belonged to that delegation."

An identity layer changes each of these: actions become **attributable**
(the delegation chain is signed, not asserted), delegation becomes
**enforceable** (attenuation is an issuer invariant, not a convention),
outbound calls become **verifiable offline** (any party with the bundle can
check who acted, for whom, with what scope), and multi-tenancy becomes
**provable** (each user/agent/subagent is a distinct principal in the chain).
One scoping note on that last word: "distinct principal" holds at the
*internal* tiers that mint identities. At the outbound hop the credential is
definition-scoped and shared across sibling subagents, so subagents are
distinct in the signed chain but not each a distinct cryptographic principal
at the gateway; the gateway enforces per-definition. This is the difference
between "trust our logs" and "verify the chain yourself."

## What breaks today (and why that is the point)

The design is not abstract: mecatl's current multi-user posture already
leaks, and the identity layer is what closes it. Honest inventory:

- **`ListSessions` returns every stored session with a title derived from
  that session's first user prompt** (clamped to 120 runes), inventory and
  prompt content, no id needed. On Redis it takes the slow path (loads every
  session per request).
- **`GET /v1/sessions/{id}/events` relays `EvUserPrompt` and
  `EvCompactionArchive` verbatim**: every prompt plus entire pre-compaction
  conversations. Right for a single-user harness; exactly the assumption
  that breaks on a shared store.
- **Fair framing:** today mecatl has no tenants (one shared operator token
  means one principal), so this is cross-*session* context isolation, not a
  cross-tenant vulnerability, and a missing session owner is the
  harness-tier norm (Claude Code, Codex, LangGraph all list without a
  principal). But ADR 0048's shared Redis moved mecatl into the
  multi-tenant-server category while the code stayed single-user. Google
  ADK shows the fix: its primary key is `(app_name, user_id, session_id)`
  and `list_sessions` can't be called without a `user_id`: **put the
  principal in the signature**, so enumeration is scoped by construction.
- **A scheduled fire has no user by construction.** `makeFireFunc` creates a
  session in-process from a leader-elected goroutine. It never crosses any
  edge interceptor, so an edge-only principal binding misses it. The owner
  record must be primary and the edge one writer of it; a cron fire's owner
  is `client_credentials` (the OAuth grant: "the schedule did this"), not a
  fake user.

Worth stealing from Entra: it hard-blocks specific permissions from ever
being granted to an agent identity, even by an admin who wants to. That is a
ceiling independent of any issuer's correctness, stronger than narrowing at
the issuer and trusting the issuer.

## Why SPIRE alone is not enough

SPIRE is excellent at what it is for, and this design **keeps it** for
infrastructure. The decisive reason it cannot do the job above is about
*what can be attested*, not about features:

- **The attestation unit is a process; our principals are goroutines.**
  SPIRE establishes workload identity by introspecting the *calling process*:
  the Workload Endpoint identifies callers via kernel socket state (§5),
  and workload attestors select on process attributes (uid/gid/path from
  `/proc/<pid>`). A mecatl pod is *one* process serving many users,
  sessions, and subagents. No SPIFFE mechanism, and no SPIRE attestor, can
  attest an *in-process* principal. Even the Broker API's custom reference
  types must resolve to something the server can independently verify through
  `/proc` or the container runtime, which a goroutine is not. **Only the
  harness itself can attest its own sub-principals.** That is the portable,
  spec-level reason mecatl must be its own issuer.
- **The on-behalf-of that exists doesn't reach us.** SPIFFE *does* have a
  delegation mechanism: the Broker API lets a trusted component act "on
  behalf of" workloads it references (PID or Kubernetes object). But it
  vends *identity-only* SVIDs (`sub`/`aud`/`exp` and nothing else: no
  chain, no user principal, no attenuation), it is `Incubating`, and its
  references resolve to processes and k8s objects, not goroutines. It
  answers "give this referenced workload an identity," not "prove this
  chain of narrowing delegation happened."
- **The implementation levers are admin-gated and non-portable.** SPIRE
  *can* mint arbitrary SPIFFE IDs (`MintJWTSVID`, no registration-entry
  lookup) and *can* inject private claims (`CredentialComposer` server
  plugins). Both are SPIRE implementation details, not SPIFFE spec. Both
  are gated by an `allow_admin` boolean over the *entire* trust domain, not
  scoped to a path prefix. Building on them means asking operators for a
  credential strictly more powerful than the one we are trying to build.
  And it works only in SPIRE's world, not with cert-manager's
  csi-driver-spiffe, Istio's istiod, or a managed SPIFFE platform, none of
  which share that plugin surface.
- **The conclusion is portability, not capability.** We adopt the SPIFFE
  *envelope* wholesale (the ID format, the JWT-SVID shape, bundle
  distribution, federation), all spec-level surfaces every implementation
  speaks. We build the delegation layer (chain, attenuation, user
  principal, parkable lifecycle) ourselves, in our own trust domain,
  because no spec carries it and no issuer can attest our principals for
  us. SPIRE stays for exactly one thing: attesting the pod.

## Prior art, honestly assessed

A research pass over the standards landscape (2024–2026) established three
things. First, **the primitives are mature**: SPIFFE/SPIRE is CNCF-graduated,
RFC 8693 (OAuth token exchange, `act`/`may_act` delegation chains) is a
standard, and the IETF WIMSE working group is actively standardizing workload
identity for exactly this deployment shape. Second, **the industry converges on
disaggregating durable identity from runtime state** (a definition/instance
distinction): Microsoft's Entra Agent ID (blueprint / agent identity),
OpenAI's Assistants API (assistant / thread / run), A2A (AgentCard / task),
and WIMSE (workload / workload instance) all separate the durable "what it is"
from the ephemeral "it is running." The run/transaction tier is this doc's own
elaboration, matching OpenAI's run but not present in every analog.
Third, **the attenuation picture is more nuanced than "nobody has solved
it."** No *ratified* standard mandates attenuation: RFC 8693 only *suggests*
scope narrowing as an abuse mitigation (§5), `draft-ietf-oauth-identity-chaining`
expects non-escalation in non-normative prose and leaves claim representation
undefined, and ID-JAG (the Identity Assertion JWT Authorization Grant
draft) makes narrowing a policy MAY (§4.3.3). But several
individual drafts *do* mandate it, and they are worth citing rather than
pretending the space is empty:

- **`draft-mcguinness-oauth-actor-profile-00`** (the closest overlap; an
  OAuth "actor-profile" for delegated agents) mandates
  non-escalation on every path, specifies a chain construction and validation
  algorithm with append-only as a MUST, enforces a depth limit, and has
  `sub_profile: "ai_agent"` with a "user → orchestrator → agent → tool"
  reference architecture. Its §3.2 also states the invariant RFC 8693 never
  does: "`sub` is the authorizing principal."
- **`draft-mcguinness-oauth-ai-agent-instance-00`** (same author) makes this
  doc's definition/instance split in near-identical words: "A platform
  registers a single `client_id` and then runs many concurrent agent
  instances under it," with a REQUIRED `agent_instance_id` and provenance
  claims. This is the closest analog for the tier model itself.
- **`draft-liu-agent-operation-authorization-02`** and
  **`draft-liu-oauth-chain-delegation-00`** both name a `delegation_chain`
  claim and enforce strict-narrower server-side, the second with a depth cap
  of 5. That is prior art for this doc's chain name and `max_depth`.
- **Macaroons** (Birgisson et al., 2014) and **Biscuit** are the actual
  origin of *holder-side* attenuation: the holder appends a narrowing block
  offline and the verifier rejects any block that widens. Biscuit gives a
  cryptographic guarantee rather than a runtime policy. This doc's model is
  the inverse (issuer-side enforcement at the mint seam), which is the
  correct call for a harness that IS the issuer (a compromised child runtime
  must not be able to decline to attenuate); the comparison is drawn in
  "Build vs buy."

All four are individual submissions with Standards-Track intent, none
WG-adopted; only `draft-ietf-oauth-identity-chaining` and ID-JAG carry that.
**What survives as genuinely unspecified everywhere:** every attenuation MUST
in actor-profile cites "[RFC8693], Section 4" for the *method*, and §4 is
the claims registry, where §4.2 defines `scope` as a space-separated string.
No reduction algorithm anywhere. So even the draft with the hardest MUSTs
mandates the *requirement* and points at a section with no *method*.
Containment over a structured authority model (tool names, a posture ladder,
delegation rights, workspace resources), and who is obliged to refuse a
widening, is the genuinely unclaimed ground, and it is where this doc's
`authorization_details` subset computation sits.

WIMSE's architecture draft describes user→AI-agent→AI-agent→workload chains
(§3.4.11, "AI and ML-Based Intermediaries") as *requirements*: no protocol,
no claims, no implementation. RFC 8693 gives the `act` chain as an audit
artifact but normatively declares nested actors "informational only". There
is no standard agent capability vocabulary and no credential lifecycle for an
interruptible, human-in-the-loop workload. Those gaps are called out
per-section below as **innovation ground**.

The five questions the rest of this doc adjudicates, with their verdicts:

- **Q1**: Is issuer HA solved for this deployment shape? **Converged**:
  yes, stateless replicas, KMS-backed signing (see "The issuer in a
  Kubernetes deployment").
- **Q2**: How does the credential lifecycle work when sessions park for
  hours and resume on different pods? **Innovation ground** (see "The
  parkable credential lifecycle").
- **Q3**: Is the SVID the *source* of authority or a *projection* of it?
  **Converged**: projection (see "Projection, not authority").
- **Q4**: What scope vocabulary should an agent capability credential
  carry? **Innovation ground** (see "The scope vocabulary").
- **Q5**: How should a multi-hop agent run be correlated for audit?
  **Converged**: dual-ID, one immutable tree ID plus per-hop parent links
  (see "Audit").

## The three-tier model

Identity is segmented exactly where the industry draws the lines, and where
mecatl's existing types already draw them. The durable specialist definition
is the `AgentDef` value object (`engine/tool/agentsource.go`), the stateful
conversation is the `Session` aggregate (`engine/session/session.go`), and
the ephemeral turn loop is `Engine.Run`:

```
TIER 1  DEFINITION   durable identity: what the agent IS
                     (mecatl's AgentDef; the generic main engine is the
                      built-in "main" definition)
TIER 2  INSTANCE     one living session of a definition: the credential tier
                     (mecatl's Session aggregate; short-TTL SVIDs minted here)
TIER 3  RUN          one execution: the transaction tier
                     (mecatl's Engine.Run; per-run txn correlation)
```

SPIFFE IDs in mecatl's own trust domain (name is a config knob):

```
spiffe://<td>/user/<uid>
spiffe://<td>/agent/<def>
spiffe://<td>/agent/<def>/inst/<sessionID>
spiffe://<td>/agent/<def>/inst/<sessionID>/child/<childSessionID>
```

**The path names a principal; the delegation topology lives in the claims,
not the path.** SPIFFE-ID §2.2 leaves path semantics to the administrator and
explicitly sanctions opaque paths ("the most general case"); §4.1.1 warns
that only assertions stable for the SVID's lifetime belong in the credential,
and delegation topology is runtime-emergent (children park, resume, and
outlive runs). mecatl already carries the authoritative topology twice, in the
signed `delegation_chain` claim and the event log's `ParentCallID`, so
embedding it again in the path is speculative generality. Child identities
therefore embed the **verbatim runtime child session id** under one flat
`child/` segment: `subagent-<callID>`, `parallel-<callID>-<i>`,
`team-<teamID>-<member>` (minted by `engine/agent/teamsupervisor.go`
(`MemberSessionID`)). The identity graph and the runtime delegation graph are
the same string, so they cannot drift, and audit correlation needs no scheme
knowledge. Family and team structure stay claims-and-log data. An issuer-side
invariant keeps team and call ids dash-free hex (already true), so the packed
`team-<teamID>-<member>` form stays prefix-queryable for audit. Grandchildren
recurse the same `child/<id>` append, bounded by `max_depth` and the
2048-byte SPIFFE-ID §2.3 ceiling (~40 bytes/level, so tens of levels, a
non-issue, stated so the budget is explicit). Depth is enforced by the issuer
at every hop.

The one place structure *is* enforced is the durable, user-controlled,
policy-targeted **definition name**: it is admitted only if it already
conforms to the §2.2 segment charset `[a-zA-Z0-9.-_]` (round-trip reject at
def load). Accepting a lossy sanitize would let two distinct definitions
collapse to one SPIFFE id, silently breaking attribution, the property the
whole model exists to provide. (Session ids are already hex; provider call
ids are issuer-minted and stay inside the family prefixes by construction.)

- **Definition identities** (`agent/<def>`) are the durable "who", the anchor
  for operator policy ("what may a `tdd-worker` ever do?"), exactly the
  ServiceAccount/blueprint role in the platform analogs. They are never
  minted as credentials; they exist as path structure and as policy targets.
  Honest gap: today `CreateSessionRequest` has no agent field, so `<def>`
  would resolve to a constant for every default session. The fix is an
  optional `agent` field plus a synthetic default-definition id (never the
  telemetry `roleFamily` bucket: that label is deliberately lossy and would
  collapse every definition into six buckets, destroying the policy anchor).
  Second gap: `AgentDef.Origin` carries a trust tier (project/user), but it
  is carried, not enforced into identity. A project-tier def read from a
  mutable workspace can take the name of an operator-managed one (project
  precedence over user), so two legitimately-valid same-name definitions
  from different-trust sources collide. That is a definition-spoofing path
  through repository content, separate from the sanitizer collision, and it
  needs a trust-tier distinction in the path or in policy.
- **Instance identities** (`inst/<sessionID>`) are where SVIDs live. A session
  (including a reopened, recovered, or pod-migrated one) is one instance;
  rehydration re-mints the *same* instance identity. N concurrent sessions of
  one definition are N instance SVIDs under one definition identity: audit
  can group by definition ("what did code-reviewers do this week?") or by
  instance ("what did *this* delegation do?").
- **Child identities** extend the parent's *instance* path with
  `child/<childSessionID>` (the verbatim runtime id, per the block above).
  Within one instance the identity graph and the
  runtime delegation graph cannot drift; across instances the child id is
  not globally unique (provider call ids are per-conversation), which is
  exactly why the instance tier must qualify them.
- **Runs are not identities.** A run carries the instance credential plus a
  per-run `txn` claim; resumption re-proves the instance, it does not create
  one.

Why not session-as-identity (an earlier draft's mistake): a session is
*state*, not agency. Identity anchored to it inherits the wrong lifetime
(sessions reopen across runs and migrate across pods) and the wrong semantics
(users grant authority to *kinds* of agents, not to conversations). The
definition/instance/run split fixes both while mapping onto types mecatl
already has.

## The credential: a JWT-SVID with a delegation vocabulary

Instance SVIDs are SPIFFE JWT-SVIDs with private claims, spec-shaped (`sub`,
`aud`, `exp`, bundle-published signing keys). JWT-SVID §3 permits this in so
many words ("Registered claims not described in this document, in addition to
private claims, MAY be used as implementers see fit"), then warns that
reliance on them may impact interoperability. Accepted; this vocabulary is
ours to carry. Private claims use collision-resistant names
(`https://mecatl.dev/claims/…`, the JWT private-claim convention); the table
uses short names for readability:

| Claim | Meaning |
|---|---|
| `sub` | the instance SPIFFE ID (spec) |
| `aud` | intended verifier(s) (spec; single-audience strongly recommended per JWT-SVID §7.2, to bound replay) |
| `exp`/`iat`/`jti` | short TTL, unique per mint (spec) |
| `delegation_chain` | ordered `{id, def, authorization_details}` entries, user → … → parent. Append-only, signed at mint. 8693 `act`-nesting semantics (below), explicit and carrying a per-hop scope snapshot. The name mirrors `draft-liu-agent-operation-authorization` / `draft-liu-oauth-chain-delegation` |
| `authorization_details` | the effective authority of this instance, RFC 9396's registered claim for structured authorization (see vocabulary below). Issuer-enforced invariant: **strict subset of the parent's** |
| `depth` / `max_depth` | how much further this identity may delegate (the RFC 3820 `pCPathLenConstraint` idea, in JWT form; absorbed into `authorization_details.constraints` if 9396 is adopted wholesale) |
| `txn` | immutable correlation ID for this run, **registered in RFC 8417 §2.2** and borrowed by the Txn-Token draft; parent's `txn` is recorded in the chain, preserving the tree without one giant ID |
| `cnf` | proof-of-possession binding for outbound presentation: RFC 9449 `cnf` with a `jkt` thumbprint, not a new claim (Phase 3) |

### The delegation semantics (8693-derived, stated in our vocabulary)

The semantics are RFC 8693's, but the **evaluation rule must be stated in
this token's own terms**: quoting 8693 §4.1 verbatim would be wrong here,
because this token's shape differs from 8693's in the one place that rule
keys on. In canonical 8693, `sub` is the *delegator* and the outermost
`act.sub` is the *current actor*; §4.1 keys access control to "the token's
top-level claims and the party identified as the current actor," with prior
(nested) actors "informational only." This token has no top-level `act`, and
its `sub` *is* the current actor (JWT-SVID §3.1 requires `sub` = the holder's
own SPIFFE ID). So the rule, restated for this shape:

1. **Authorization keys off `sub` + `authorization_details`.** `sub` is the
   acting instance (the credential holder); `authorization_details` is the
   authority the issuer bound to *this* instance at mint. A verifier grants
   only what the issuer bound, never a right re-derived from the chain.
2. **The chain is signed provenance, never an authz input.** Prior actors in
   `delegation_chain` are audit only, the same posture 8693 §4.1 and
   actor-profile §3.2 both take toward nested actors. The issuer's mint-time
   subset invariant is what makes `authorization_details` trustworthy; the
   chain is the verifiable *proof that the narrowing happened*, not a source
   of rights.
3. **The current actor IS an authority input, and here it is `sub`.**
   actor-profile-00 §14.5 warns that evaluating *only* the subject when a
   delegation is present is a confused-deputy risk, and §3.2 makes the
   current actor part of the decision. This token honors that: the acting
   instance (`sub`) plus its bound authority (`authorization_details`) *are*
   the evaluated pair. The confused-deputy reading doesn't bite because the
   verifier trusts the issuer's narrowing, not the bare identity.
4. **Delegation, never impersonation.** 8693 §1.1 separates the two:
   impersonation makes the issued token's `sub` the delegator (the actor
   vanishes); delegation names the actor. The model is strictly delegation:
   the user is always at the root of `delegation_chain`, the acting instance
   always named. (The industry counterexample is GitHub Copilot's coding
   agent: commits attributed to the user, no visible actor, impersonation
   with no audit trail. We refuse that shape.)

**Why a private claim instead of RFC 8693's `act` itself.** The two specs
assign `sub` incompatible meanings: JWT-SVID §3.1 requires `sub` to be the
credential holder's own SPIFFE ID (the acting instance), while 8693
delegation (§4.1, Appendix A.2.5) puts the *delegator* in `sub` with the
actor in `act`. (8693 states this as "typically" plus an illustrative
appendix rather than a single normative MUST, but §4.1 does define `act` as
the party *to whom* authority was delegated and keys access control to it,
which is the load-bearing text.) A single token cannot be simultaneously a
conformant JWT-SVID and a conformant 8693 delegation token. So the chain
rides a private claim with 8693-derived semantics; `act` is honored as the
semantic contract rather than reused as the on-the-wire name. A secondary
reason: 8693 §4.1 confines `act`-subobject claims to identity ("claims
within the act claim pertain only to the identity of the actor"), while our
chain entries carry a per-hop **scope snapshot** (the basis of attenuation
audit and of the resume invariant), which could not ride `act` conformantly
either. Note also that 8693's nested `act` already *is* a history trail in
the token; the honest difference here is not "explicit vs emergent" but that
`delegation_chain` is **issuer-enforced** (not AS-discretionary) and carries
the per-hop scope snapshot `act` forbids.

**Revisit triggers**: conditions under which this choice flips to a
standard-`act` shape, recorded so a future reader knows when to reopen it:
(a) WIMSE ratifies an agent credential profile whose `act` semantics are
compatible with JWT-SVID's `sub` = holder convention; (b) `go-spiffe/v2`
grows an alternative identity field (e.g. an actor from `act.sub`), making
library identity correct regardless of `sub` semantics; (c) a widely
deployed JWT audit tool emerges that deep-walks multi-hop `act` chains
(canonical semantics would then be the documented expectation); (d) the
Phase-3 external verifier ecosystem (`scoped-resource-grants.md`
consumers, federated domains) demands `act`-named claims for interop;
(e) SPIFFE issues guidance letting delegation-carrying tokens set `sub`
to the delegator. One is already live: actor-profile-00 §3.2 ratifies
"`sub` is the authorizing principal", the 8693 side, so if that profile
is adopted, the collision resolves *against* this doc's current `sub` =
acting-instance choice; flag it, don't hide it. Until one fires,
`delegation_chain` stands: an 8693-trained reader misreads an inverted
`act` *confidently*, and confident misreading in an audit context is worse
than a private claim.

**`may_act` (8693 §4.4)**, the claim that pre-authorizes which actors may
**act for** a subject (it covers impersonation as well as delegation, and it
is near-dead in deployment: Keycloak gates it behind an experimental flag
and doesn't support `actor_token` at all), is played by **definition-tier
policy**, not a token claim: "user U may spawn definition D", "D may spawn
children of definitions {…}". Those are the three nested envelopes (user
grant ⊇ definition policy ⊇ instance scope ⊇ child scope) from the tier
model. Same consent-hook semantics as `may_act`, expressed as issuer policy
because our exchange is in-process (below) rather than an STS the user
consents through, and because `may_act` itself is barely deployed.

**Where 8693 ends and the novel part begins.** 8693 *permits* scope
narrowing at exchange; nothing structurally enforces it (a misconfigured
STS can mint wider). Our issuer **refuses** to mint a child whose
`authorization_details` is not a strict subset: attenuation as an
invariant, not a policy option. And the exchange itself is in-process: a
child mint happens at the delegation seams (`buildChildSession`, the team
member factory), so 8693's wire protocol (`grant_type=token-exchange`,
`subject_token` / `actor_token`) never runs. It only enters if the issuer
is later exposed as a network STS for external consumers, at which point
these claims slot into 8693's envelope unchanged (an instance JWT-SVID is
already a valid `actor_token` of type
`urn:ietf:params:oauth:token-type:jwt`). `depth`/`max_depth` has no 8693
analog; it comes from RFC 3820. A JWT is immutable once signed, so a
holder cannot attenuate its own token. Narrowing always means the issuer
mints a new one; that is cheap here because minting is a local signing
call (see the lifecycle section), not a network round-trip.

### The parkable credential lifecycle (innovation ground: Q2)

No standard or published deployment models a credential lifecycle for a
workload that parks mid-operation for an indeterminate human-in-the-loop
wait and resumes on a different host. The converged TTL floor: SPIRE
defaults (X.509 1h, JWT 5m; JWT-SVIDs are minted fresh per request,
so there is no rotation schedule to inherit, and mint-per-request *is* the
"credential ephemeral" half of the contract below), and WIMSE WIT "hours,
PoP minutes, never bearer". A mecatl session can **park for hours awaiting a
human approval and resume on a different pod**, and no standard models
that. WIMSE's practices draft says tokens "SHOULD be invalidated when the
workload *pauses*" and explicitly leaves the mechanism out of scope. Two
live IETF schools are adjacent: vault/broker re-attestation, and Zhu's
async-delegated refresh tokens (short TTL + re-derive from durable state,
monotonic scope, absolute max lifetime). This design **converges with** Zhu
on the re-derivation; what is genuinely new is the
parked-and-resumed-on-another-pod lifecycle, not the re-derivation itself.

The design: **chain durable, credential ephemeral.**

- Instance SVID TTL is minutes; the harness re-mints on demand (it is the
  issuer, and minting is a local signing call over the in-memory intermediate
  key, not a network dependency; see the issuer section).
- A parked session's SVID simply expires. Nothing is revoked because nothing
  needs to be: TTL ≪ any useful attack window. TTL-only is a *considered and
  rejected* alternative to a revocation list. Sweeney's delegation draft has
  the only fully worked revocation design in this space, and it is heavier
  than the risk window a minutes-long TTL leaves.
- Resume (the existing `rehydrateSession` seam) re-mints the **same instance
  identity with the same `delegation_chain` and an
  `authorization_details` no wider than pre-park**.
  This enforces monotonic attenuation across resume, issuer-side, against the
  persisted chain. Two refinements, from review: (a) where a *live caller*
  exists (an approval click, a parent turn resuming a child), authority is
  derived from the live caller and the persisted chain is only the *bound*
  it is checked against. The record is a cache; the live binding is the
  authority. (b) Where the chain itself must be authoritative (the
  scheduled fire, the one case with no live caller), the issuer signs the
  chain at mint and verifies it before re-mint. That way the row proves
  its own integrity rather than being trusted for being in the database.
  This state machine (active/parked, re-attest-on-resume, attenuation
  preserved across pod boundaries) is genuinely novel; the doc treats it
  as a named contribution, not an implementation detail.
- Session GC (PrunableStore) plus short TTL bounds the
  deleted-session-but-live-token window without a revocation list. Internal
  verifiers may additionally check `jti`↔session-liveness against Redis
  (cheap in-cluster: a liveness hint, not revocation, since the harness
  re-mints on demand); external verifiers stay offline/TTL-only.

### The scope vocabulary (innovation ground: Q4)

Nobody has standardized what an agent may *do* in a token: MCP scopes are
server-defined strings, A2A says "agent-defined", Entra reuses Graph resource
scopes, Cedar/OPA are app-defined. The claim itself is **not** a private
invention: RFC 8693 §4.2 registers `scope` as a space-separated *string*
(RFC 6749 §3.3), which cannot carry structured authority. So the structured
payload rides **RFC 9396 `authorization_details`**, the registered,
production-proven (FAPI) claim for exactly this. A v1 entry:

```json
{"type": "mecatl_agent",
 "operations": ["Read", "Grep", "Shell"],
 "resources": ["/workspace/repo"],
 "constraints": {"posture_ceiling": "auto", "max_depth": 3}}
```

- **`operations`**: the catalog tool names the permission evaluator already
  resolves (`engine/governance`, the deny-dominant, scope-ordered rule
  evaluator; its `Audience` enum already distinguishes main- vs
  subagent-bound rules, the closest existing thing to an attenuation
  channel);
- **`resources`**: the workspace roots this instance may touch, with prefix
  containment after canonicalisation. This axis is *required*, not optional:
  a model-created session (a scheduled fire) can name an arbitrary workspace
  root today (`validateScheduleSpec` checks non-emptiness only), so a child
  narrowed on tools and posture could otherwise read a strictly *larger*
  filesystem than its parent. Construction is exactly what a scoped
  principal must not be able to widen.
- **`constraints`**: the posture ceiling (the `strict < trusted < auto <
  yolo` ladder from `internal/app/posture.go`; a child's ceiling ≤ its
  parent's) and delegation rights (`max_depth`, which definition identities
  it may spawn).

Providers/models and egress are candidates for a later vocabulary revision.
The subset computation over this structure, and who is obliged to refuse a
widening, is the genuinely unspecified part (see "Prior art"): the issuer
computes containment over `operations` (set), `resources` (canonicalised
prefix), and `constraints` (posture rank, depth integer) and refuses to mint
on any widening.

### Projection, not authority (converged pattern: Q3)

The SVID is a **projection** of the session's authority for outbound calls,
never the source of it. Internally, authority remains exactly where it is
today: the session aggregate plus the deny-dominant governance evaluator
described above. This mirrors the SPIFFE Workload Endpoint model (the local
agent is the authority; the SVID exists to leave the node) and WIMSE's
Identity Proxy / egress generalization pattern (token exchange at the
boundary, internal context stays local). The in-process generalization (the
*session aggregate* minting projection tokens for its children rather than
delegating to an external STS) is a pattern many systems use implicitly.
Nobody has articulated it as a named primitive. It is called out here because
the definition/instance/run mapping makes it explicit and testable.

Two projection rules follow. **Single-audience is required, not merely
preferred.** A downstream that re-presents a child SVID to a sibling backend
is a confused deputy; requiring single-audience (JWT-SVID §7.2 already
strongly recommends it, for replay) closes that. And **the SVID's scope
claims must only ever narrow on re-mint** (the projection must never be the
path by which authority grows).

A consequence that simplifies everything: **subagents are not network
entities**. A subagent is a goroutine, not a pod; it never presents its own
SVID. When the harness makes an outbound call attributable to a subagent
(a forge API call, an MCP call), the *harness* presents the subagent's SVID
plus its own proof-of-possession. Only the pod tier holds keys; everything
below is signed claims. No per-subagent key material, no PoP problem below
the pod.

## Threat model and trust boundaries

Before the worked examples, the boundaries this design draws and what it
does and does not defend. The point of the identity layer is not to make the
harness trustworthy. It is to make its delegations *verifiable* and its
attenuation *enforceable* by parties who do not have to take its word.

### Trust boundaries

```
                        ┌─────────────────────────────────────────────┐
                        │              UNTRUSTED / PUBLIC             │
                        │   (the internet, a forge's other tenants,   │
                        │    a downstream service's other callers)    │
                        └───────────────▲─────────────────────────────┘
                                        │ outbound call + SVID + PoP
        ═══════════════  BOUNDARY 3: the trust-domain edge  ═══════════════
                        │               (verifiable by bundle)        │
┌───────────────────────┴───────────────────────────────────────────┐
│  TRUST DOMAIN: the mecatl deployment (one issuer, one bundle)     │
│                                                                   │
│   ┌──────────────┐   gRPC/HTTP + OIDC bearer   ┌───────────────┐  │
│   │   the user   │ ──────────────────────────► │  edge auth    │  │
│   │  (browser /  │   BOUNDARY 1: the user edge │  interceptor  │  │
│   │   CLI client)│                             └───────┬───────┘  │
│   └──────────────┘                                     │ binds principal
│                                                        ▼          │
│   ┌──────────────────────────────────────────────────────────┐   │
│   │  a mecatl pod (any autoscaled replica)                   │   │
│   │                                                          │   │
│   │   issuer (KMS root ──► in-memory intermediate)           │   │
│   │     │ mints                                              │   │
│   │     ▼                                                    │   │
│   │   session instance SVID ──► subagent SVID ──► grandchild │   │
│   │   (attenuation enforced HERE, at the mint seam)          │   │
│   │                                                          │   │
│   │   session aggregate + governance evaluator = authority   │   │
│   └──────────────┬───────────────────────────────┬───────────┘   │
│                  │ BOUNDARY 2a: state            │ BOUNDARY 2b:   │
│                  ▼                               ▼ downstream     │
│        ┌───────────────────┐          ┌──────────────────────┐    │
│        │  Redis (sessions, │          │  ToolHive vMCP / a   │    │
│        │  event log) — the │          │  backend MCP server  │    │
│        │  root of trust on │          │  (separate trust     │    │
│        │  resume           │          │  domain, verifies    │    │
│        └───────────────────┘          │  the SVID)           │    │
│                                       └──────────────────────┘    │
└───────────────────────────────────────────────────────────────────┘
```

- **Boundary 1: the user edge.** The user authenticates to the edge
  interceptor (OIDC bearer / mTLS); the edge binds the session to
  `user/<uid>`. Everything downstream trusts that binding. This is the one
  place a human's identity enters, so it must be right: a mis-issued
  principal here poisons every chain rooted at it. (Scheduled fires bypass
  it by construction, see "What breaks today", so the owner record, not
  the edge, is primary.)
- **Boundary 2a: state.** Redis holds sessions and the event log; on
  resume *all* authority is re-derived from it. It is the root of trust for
  the whole model, which is why its current unauthenticated, unencrypted,
  un-MAC'd state is a prerequisite fix (B5), and why the chain carries
  issuer-signed integrity rather than trusting the row (the resume
  invariant).
- **Boundary 2b: downstream.** A backend service (ToolHive vMCP, a forge,
  a memory/filesystem service) is a *separate* trust domain. It does not
  trust the harness's word; it verifies the presented SVID against the
  bundle, offline. This is the boundary the whole design exists to serve.
- **Boundary 3: the trust-domain edge.** The line between "inside, where
  the issuer is believed" and "outside, where only the signature speaks."
  Inside, the session aggregate is the authority; outside, the SVID is a
  verifiable projection of it.

### What the adversary can and cannot do

The adversary model has three actors the design speaks to directly, and
several more it must name honestly. First the three:

- **A compromised subagent runtime (prompt injection reaching a child).**
  Cannot mint a wider credential: it holds no key, only the harness does,
  and the issuer refuses to mint outside the strict-subset invariant. It
  *can* abuse the authority its own SVID already carries, which is exactly
  why that authority is a narrow subset, and why the ceiling is Entra-style
  hard-blockable independent of the issuer. Contained by attenuation, not
  by trusting the child. **Honesty note:** "the issuer refuses to mint
  wider" is the Phase-2 mechanism. Today nothing is minted; a child's
  authority is scoped by the catalog composition builds for it plus the
  audience-pinned, deny-dominant evaluator (`engine/governance`), real and
  tested, but a convention, not a cryptographic containment. The
  containment claim is only as strong as that distinction, and the doc's
  phasing carries it.
- **A compromised pod (can request signatures, can write Redis).** This is
  the honest worst case, and the design does not pretend otherwise: **the
  pod that can sign is the pod that can impersonate any session** (see
  "Honest costs and risks"). Mitigations are about blast radius and
  detection (KMS-rooted short-lived intermediates, KMS-layer signing
  audit, short TTLs, Redis auth/TLS), not about making the pod trustworthy.
- **An external party holding a leaked SVID.** Bounded by TTL (minutes), and
  a parked session's token expires before it can be replayed. Until Phase 3
  adds the PoP binding (`cnf`/`jkt`), every instance SVID is a *bearer*
  token, so "TTL only, until Phase 3" is the accurate statement; PoP is
  future protection, not present. Cross-checking the SVID against the forge's or KMS's own audit
  log is what turns "trust our logs" into "verify the chain."

Five more the model must name, or it is incomplete for a multi-tenant AI
harness:

- **Prompt injection at the *main* agent (P1).** Strictly more dangerous
  than at a child: the main agent holds the full tool set, spawns children,
  sets their mode and prompts. Cryptographic identity is *mostly* not the
  defense here; the attacker is driving the harness's own prompt, inside the
  pod, so the containment is the harness's existing defenses (guardrails, the
  deny-dominant fold, the plan-mode gate), which live at this boundary. The
  one identity-carried lever is `constraints.posture_ceiling`: a ceiling on
  what an injected main agent can grant a child, which the Entra hard-block
  pattern (a ceiling independent of issuer correctness) makes stronger than
  trusting the issuer. Note the defenses P1 relies on are themselves
  posture-conditional (guardrails drop to advisory at the top of the
  ladder).
- **A malicious tenant (P2).** A user with legitimate authority is the
  relevant multi-tenant threat: valid credentials, can create sessions and
  run agents. The identity layer makes their actions *attributable* but
  does not *confine* them. Tenant isolation (User A reading User B's
  session) needs tenant-scoped `ListSessions`, tenant-scoped event-log
  reads, and a tenant-aware edge, i.e. policy, not identity. "What breaks
  today" describes the symptom; the adversary is named here.
- **A mis-binding edge (P3).** A compromised or misconfigured edge
  interceptor could bind a session to `user/alice` when the OIDC token was
  Bob's, and every SVID minted under the wrong principal inherits the
  error, undetectably downstream. The naive mitigation (the edge writes a
  signed binding, the store records it, audit compares the two) is
  **circular**: a compromised edge holds the signing key, writes Bob-as-Alice
  in both places, and the comparison agrees with itself. What actually works
  is anchoring to something the edge cannot forge: keep the issuer and
  identifier of the *original* assertion from the identity provider, so an
  auditor re-checks the binding against the provider, not against the
  component under suspicion.
- **A compromised downstream credential store (P4).** Scenario B has
  ToolHive storing Alice's upstream credentials. A compromised ToolHive
  leaks the credential itself. The identity layer limits *attribution* (you
  learn which delegation used it) but does not protect the credential.
  The store is its own trust domain, and the boundary must be named so
  operators know where credential protection ends.
- **Event-log replay / exfiltration (P5).** The log persists unauthenticated
  JSON (prompts, pre-compaction conversations, verdicts). An attacker with
  Redis read access reconstructs every delegation tree. The log carries no
  MAC; Rekor-style transparency anchoring (Phase 3) is a future integrity
  measure, not a current defense. Two refinements: the anchoring must be
  over *digests only* (anchoring prompt content to a public log is an
  exfiltration channel, not a defense), and Redis auth/TLS covers the
  transport-confidentiality half.

Three more the architecture implies, which the first pass missed:

- **A delegated token that carries the credential-store reference.** A
  narrowed child then reaches the user's *whole* stored credential set at
  the gateway, so attenuation over tools and resources is bypassed one layer
  down. This is not P4 (the store being compromised); it is ordinary
  delegation reaching too far by construction. Closing it is the user-keyed,
  target-scoped credential read (a refused call reads no credential) from
  the scenarios.
- **A store writer who cannot sign.** A leaked database credential against
  an unauthenticated store is likelier than pod compromise, and this
  adversary *cannot* forge a signed chain, which is exactly what
  chain-integrity (issuer signs at mint, verifies before re-mint) defeats.
  Naming it separately from "the pod that can sign" keeps that mitigation
  from looking less valuable than it is.
- **A substituted trust bundle.** The whole value of offline verification
  collapses if the JWKS endpoint serves a poisoned bundle: every external
  verifier then accepts forged chains. The costs section raises that
  endpoint's *availability*; its *integrity* (authenticated, tamper-evident
  bundle distribution) is the sharper requirement and is named here.

What the design does **not** defend: a malicious or confused *user* acting
within legitimately granted authority (that is policy, not identity); the
harness lying about what a user asked (the signature adds tamper-evidence,
not truth; the user's consent is out-of-band); and anything below the pod
it cannot attest (the goroutine boundary is why it is its own issuer, not a
defense).

## Two end-to-end scenarios (ToolHive as the testing ground)

These walk the model through a real downstream: **ToolHive's vMCP**, which
has already implemented most of the delegation/identity constructs this
design must interface with: an embedded authorization server that mints
nested-`act` delegation tokens, an outgoing-strategy registry, an
XAA/ID-JAG strategy, and a Cedar authorization layer that evaluates the
actor claim (`context.claim_act.sub`, today as a `like`-glob with the user
as principal). ToolHive is the example **not** because it is the
only way. The same shape applies to any downstream that supports these
constructs, such as a memory service or the filesystem abstraction from
`docs/scoped-resource-grants.md`. It is the example because it is a live,
code-complete testing ground where the integration points already exist.

**A note on what is fixed and what is ours to change.** The scenarios below
reference current ToolHive behavior: vMCP validates inbound tokens with a
self-issued validator only (the multi-issuer validator is unwired), the
credential lookup is keyed on a `tsid` login-session claim, and Cedar
matches the actor as a string glob. **These are not constraints to design
around.** The mecatl team owns ToolHive and its Cedar layer, so each is an
enhancement with a tracker, not a limitation: the multi-issuer validator
(#5989), `cnf`-bound agent tokens (#5815), a provisionable confidential
client (#6082), user-keyed credential reads with an ownership recheck, and
structured SPIFFE-aware Cedar evaluation. The scenarios describe the model
as it should work once those land; where current code falls short, the text
says so rather than pretending otherwise.

The cast for both: **Alice** (user) → **her mecatl agent** (session
instance `agent/main/inst/<sid>`) → **a code-reviewer subagent**
(`.../child/subagent-<cid>`) → **ToolHive vMCP** → a backend MCP server.
The two scenarios use different backends on purpose, because they exercise
different grants: Scenario A needs an authorization server that accepts
ID-JAG (an *enterprise* service federated with the org's IdP; public SaaS
like GitHub does not), Scenario B uses the GitHub MCP server (which the
plain credential-store path genuinely serves).

### Scenario A: the XAA / ID-JAG path

Here the backend's authorization server does not trust mecatl's trust
domain directly, so the delegation crosses domains via the Identity
Assertion JWT Authorization Grant. mecatl's chain rides the token the
whole way. **The backend must be an enterprise service whose AS implements
the ID-JAG target grant**: the canonical case is an internal corporate API
or an Okta-federated enterprise app (ID-JAG,
`draft-ietf-oauth-identity-assertion-authz-grant`, is an Okta-authored
enterprise-IdP→enterprise-app grant). A public SaaS is *not* a valid
example: GitHub's authorization server supports only `authorization_code`
and `device_code`, accepting no token-exchange and no ID-JAG, so this
flow would stop at step 5b. That is why Scenario A uses a corporate
code-review service, and GitHub appears only in Scenario B.

**How mecatl's agent identity reaches the gateway is the part current code
does not yet do, and it is the trust-placement fork this doc leaves open.**
vMCP today validates inbound tokens with a self-issued validator and derives
the actor from the authenticated client, so a mecatl-self-signed SVID is not
accepted as an outbound actor. Two ways through, both ours to build: (a)
**two-leg** (the shape jhrozek's outbound doc, `agent-identity-outbound.md`,
proposes): the pod `client_credentials`-authenticates with its SVID for an
AS-minted agent token, then RFC 8693 exchanges Alice's token with that agent
token as actor, so the gateway's AS asserts the agent and mecatl signs
nothing that crosses the boundary; or (b) **single-mint**: teach vMCP to
validate mecatl's SVID directly (the multi-issuer validator, #5989, plus a
`cnf` binding, #5815), so mecatl asserts the agent as its own issuer. The
diagram below shows the two-leg shape because it works against the AS vMCP
already has; the fork between them is a real decision (who asserts the
agent), not a settled one.

```
 Alice                mecatl (issuer)         ToolHive vMCP       enterprise AS + MCP
  │                        │                       │                      │
  │ 1. OIDC login          │                       │                      │
  │───────────────────────>│                       │                      │
  │                        │ 2. mint instance SVID │                      │
  │                        │    sub=agent/main/inst/<sid>                 │
  │                        │    delegation_chain=[{user/alice}]           │
  │                        │ 3. spawn subagent, mint child SVID           │
  │                        │    authorization_details ⊂ parent's          │
  │                        │    delegation_chain=[alice, agent]           │
  │                        │                       │                      │
  │                        │ 4. subagent's work needs a code-review call  │
  │                        │──────────────────────>│                      │
  │                        │   child SVID + harness PoP (cnf/jkt)         │
  │                        │                       │                      │
  │                        │                       │ 5. XAA strategy:     │
  │                        │                       │  a) IdP exchange     │
  │                        │                       │     (RFC 8693):      │
  │                        │                       │     identity.        │
  │                        │                       │     UpstreamIDTokens │
  │                        │                       │     [alice] ────────>│
  │                        │                       │     → ID-JAG (aud=   │
  │                        │                       │       backend AS)    │
  │                        │                       │  b) target grant     │
  │                        │                       │     (RFC 7523):      │
  │                        │                       │     ID-JAG ─────────>│
  │                        │                       │     → backend-scoped │
  │                        │                       │       access token   │
  │                        │                       │                      │
  │                        │                       │ 6. call backend MCP  │
  │                        │                       │─────────────────────>│
  │                        │                       │  Authorization:      │
  │                        │                       │  Bearer <token, sub= │
  │                        │                       │   alice, act=...>    │
```

What to notice:

- **The user never disappears.** Alice authenticates once at the mecatl
  edge; her identity is the `sub` of every downstream token, and the
  mecatl chain (`delegation_chain=[alice, agent]`) is the verifiable record
  of who narrowed what on the way. The backend sees `sub=alice` with the
  agent in the actor position: delegation, never impersonation.
- **Crossing domains is XAA's job, not mecatl's.** mecatl does not ask the
  backend's AS to trust its trust domain; the ID-JAG is the standard
  bridge. mecatl's SVID is what the vMCP validates to know *which* agent
  instance is calling; the XAA strategy then does what it already does for
  any caller.
- **The attenuation is verifiable by the backend; the credential scope is
  enforced at the gateway.** The child SVID's `authorization_details` is a
  strict subset of the parent's, signed at the mint seam, and RFC 8693 §4.1
  carries the `act` chain *in* the output token, so any consumer (gateway or
  backend) can verify the delegation. But the *credential* the backend acts
  on is enforced at the gateway: the backend receives only the provider
  credential the user granted at connect time, so the only hop that can
  refuse a call against the actual authority is the gateway. The backend
  verifies the chain; the gateway enforces the scope. (Cedar evaluates the
  agent's SPIFFE ID as a `like`-glob on `context.claim_act.sub` with the
  user as principal, not as trust-domain-aware matching; see the intro note
  and the work list.)

### Scenario B: ToolHive with a credential store keyed off the user

Here there is no cross-domain grant; the backend accepts the user's own
upstream credential, which ToolHive stores keyed off the originating user.
mecatl's job is to make sure the *right* user's credential is used, and
that the delegation that led there is auditable. **GitHub is a realistic
backend here** precisely because it needs no special grant: the user does a
standard 3LO OAuth consent, ToolHive stores the resulting token, and the
GitHub MCP server accepts an ordinary Bearer access token, the path XAA
cannot take.

```
 Alice                mecatl (issuer)         ToolHive vMCP          upstream IdP + MCP
  │                        │                       │                      │
  │ 1. OIDC login          │                       │                      │
  │───────────────────────>│                       │                      │
  │                        │ 2. mint SVIDs as in A (attenuated child)     │
  │                        │                       │                      │
  │  (earlier: Alice did a 3LO consent; ToolHive stored her upstream     │
  │   credential under her token-session id `tsid`)                      │
  │                        │                       │                      │
  │                        │ 3. subagent needs a GitHub MCP call          │
  │                        │──────────────────────>│                      │
  │                        │   child SVID + harness PoP                   │
  │                        │                       │                      │
  │                        │                       │ 4. validate inbound  │
  │                        │                       │    token, extract    │
  │                        │                       │    `tsid` claim      │
  │                        │                       │ 5. loadUpstreamTokens│
  │                        │                       │  GetAllUpstreamCreds │
  │                        │                       │  (tsid) ────────────>│
  │                        │                       │  → identity.         │
  │                        │                       │    UpstreamTokens    │
  │                        │                       │    ["github"]        │
  │                        │                       │                      │
  │                        │                       │ 6. upstream_inject   │
  │                        │                       │    strategy reads    │
  │                        │                       │    UpstreamTokens    │
  │                        │                       │    ["github"]        │
  │                        │                       │                      │
  │                        │                       │ 7. call backend MCP  │
  │                        │                       │─────────────────────>│
  │                        │                       │  Authorization:      │
  │                        │                       │  Bearer <alice's     │
  │                        │                       │   github token>      │
```

What to notice:

- **The credential is keyed off the originating user, not the agent, and not
  a login-session claim.** The mechanism matters here: today ToolHive keys
  the read on a `tsid` claim minted only in a browser authorization-code
  flow, which an agent never walks, and the token-exchange handler drops any
  inherited one. So a delegated (agent) token carries no `tsid`, and the
  `tsid`-keyed lookup returns an empty map with no error. **That is the gap,
  and it is ours to close** (it is the current implementation, not a
  protocol law): the read should key on the *user* (`sub`/`UserID`), the
  primitive for which already exists (`GetLatestUpstreamTokensForUser`), and
  re-check the inbound `sub` against the stored `UserID` at the read seam
  (`ErrInvalidBinding` is declared but never returned on this path). The
  diagram shows the target shape; the `tsid`-keyed load is what exists
  today.
- **Under this design's own tier model the inbound `sub` is the acting
  instance, not Alice** (she rides `delegation_chain`). So "the right user's
  credential" rests on the read resolving Alice from the chain's root, not
  from `sub`. That is exactly the user-keyed read above: the gateway reads
  Alice's stored credential because the delegation is rooted at her, not
  because a login session says so. The agent never holds Alice's GitHub
  token; it only triggers its use, against a lookup keyed on the user the
  chain proves.
- **The audit trail closes the loop.** mecatl's event log records the
  chain (Alice → agent → subagent) with `txn`; ToolHive's audit captures
  the delegation chain from the inbound token. The `txn` correlation id is
  the join key that lets an auditor answer "which delegation used Alice's
  GitHub credential, and was it narrowed all the way down?"

### Why ToolHive and not only ToolHive

Both scenarios lean on constructs ToolHive already ships: the embedded AS's
nested-`act` delegation minting, the outgoing-strategy registry (XAA,
upstream_inject, token_exchange), the user-keyed upstream credential store,
and Cedar's SPIFFE-aware actor evaluation. That makes it the shortest path
to a working proof. But nothing in the design is ToolHive-specific: any
downstream that (a) validates a JWT-SVID against the bundle, (b) carries a
delegation chain, and (c) enforces an authorization decision from the actor
plus a structured scope can play the same role: a memory service, the
filesystem grant substrate, or another gateway. The identity model is the
portable part; ToolHive is just where we plug it in first.

## Build vs buy

So how much of an issuer do we actually have to write? Less than it sounds:
becoming an issuer does not mean writing subtle security code from scratch.
The principle: build on proven libraries for everything cryptographic;
build only the parts that are genuinely ours (the claim vocabulary, the
attenuation invariant, the lifecycle). The stack:

- **go-spiffe/v2**: SPIFFE ID and trust-domain parsing, bundle sources,
  federation, the Workload API client, and JWT-SVID *verification*. Note:
  its `jwtsvid` package is parse-and-validate only. There is no minting
  helper, so the issuing side is genuinely ours to write (a small signer).
- **go-jose/v4**: signing and JWKS serialization. This is what SPIRE
  itself uses, and it is already in mecatl's module graph (indirect today;
  make it direct).
- **sigstore/sigstore `pkg/signature/kms`**: key custody. Its
  `SignerVerifier` exposes a `crypto.Signer` over AWS, Azure, GCP, and
  Vault behind go-cloud-style URIs, so KMS choice becomes config rather
  than four integrations. Apache-2.0, OpenSSF, and cosign runs on it.
  (Net-new dependency, as is `go-spiffe/v2`; only `go-jose` is already in
  the graph.)
- **Housekeeping**: mecatl already pulls several JWT/JOSE libraries
  transitively (go-jose v3+v4, golang-jwt v5, lestrrat-go/jwx v3,
  cristalhq/jwt v4). Pick one, make it direct, drop the rest.

The one area with real existing art worth a spike before building:
**Biscuit** (public-key, offline, holder-side attenuation via Datalog
blocks). Its model is the inverse of ours: the *holder* appends narrowing
blocks, whereas our invariant is enforced *issuer-side* at the mint seam.
Issuer-side is the correct model for a harness that IS the issuer (a
compromised subagent runtime must not be able to decline to attenuate, and
only the harness holds the signing key). But the Go implementation is small
(~89 stars): a timeboxed spike to read its subset-checking before
hand-rolling our own, not a dependency decision.

## The issuer in a Kubernetes deployment

mecak8s ([ADR 0048](adr/0048-mecak8s.md), the storage-free, Redis-backed,
k8s-native agent deployment) already fixed the topology the issuer must fit:
storage-free agent pods, autoscaled, state in Redis, single-writer via k8s
leases, any pod rehydrates any session. The industry pattern for issuer HA in
exactly this shape is **converged** (Q1): stateless replicas, signing keys
that never leave a signing service.

- **Key custody**: one logical issuer. The root key lives in a KMS/Vault
  Transit-style signing service. **Each pod signs locally with a
  short-lived intermediate key the KMS signs at startup and rotation**,
  the SPIRE upstream-authority pattern (KMS as root, in-memory intermediate
  for workload signing). This resolves an apparent contradiction:
  KMS-per-mint would make every delegation a 10–50ms network call, and bare
  software keys would put the root in pod memory. The intermediate gives
  KMS-rooted trust with local-signing performance, and its blast radius is
  bounded by the rotation window. (SPIRE's KMS-backed signing lives in its
  *key manager* plugins, `aws_kms`/`azure_key_vault`/`gcp_kms`/
  `hashicorp_vault`, with the upstream-authority plugins as the CA side;
  cert-manager's external-issuer pattern is the same shape.) **Honesty note
  on reachability:** local-signing buys audit/integrity and availability,
  not process isolation. The Shell tool runs in the same filesystem namespace
  as the process holding the intermediate key, with no OS-level isolation,
  and `envscrub` is irrelevant to an in-memory `crypto.Signer` (it scrubs
  secret-shaped env *names*, not a resident key). So a prompt-injected agent
  could read a key on disk or drive signing through the parent. SPIRE avoids
  this by splitting agent and server so the signer is never co-located with
  the attested workload, but that split does not map onto goroutines (SPIRE
  attests a process; our subagents are not processes). The honest statement:
  local-signing defends the *internal* chain's integrity; defense against the
  agent process abusing signing comes from the external-AS model on the
  outbound hop, or from a policy-enforcing signing sidecar (a separate
  process holding the key, `SO_PEERCRED` over a unix socket, enforcing its
  own policy), named here as a later-phase option rather than a v1 claim.
- **Residual risk, named**: a compromised pod cannot exfiltrate the root
  key but can request signatures while it runs, and can *write* sessions
  too. There is no "the identity must reference an existing session"
  backstop: the session store is attacker-writable in the same compromise.
  The honest statement is the one in the risks section: **the pod that can
  sign is the pod that can impersonate any session.** Defenses: KMS-layer
  audit logging of every sign request, short pod lifetimes, short
  intermediate TTLs, and Redis auth/TLS (below, a prerequisite, since on
  resume all authority comes out of Redis, making it the root of trust for
  the model).
- **Bundle distribution**: the public JWKS is served by any pod (they are
  stateless) and cached in Redis / a ConfigMap; in-cluster verifiers fetch
  once. Cross-cluster is SPIFFE federation, and it is **not free**. A bundle
  endpoint needs either the `https_web` profile (a public-CA cert) or
  `https_spiffe` (the endpoint presents its own X.509-SVID, a real addition
  in a JWT-only design, and this doc picks no profile yet). Federation is
  bilateral registration of each domain's bundle endpoint, not discovery.
  The JWKS endpoint's own SLO is an open operator question, named here
  rather than deferred.
- **Composition**: the issuer is a sibling of the existing stores in
  `internal/app` (`Build`). Child minting happens in
  **composition-supplied factory closures** on the delegation seams, the
  same idiom as `WithSubagentEngineFactory` and the team member factory,
  with `engine/agent` carrying only an opaque identity string on
  `parentCaps` (the `forkHistory` precedent). The engine loop stays
  identity-agnostic; the EventLog analogy (loop emits, relay persists) does
  *not* hold for spawn, because the child is constructed and driven inside
  dispatch, and there is no downstream seam. The closer precedent for the
  issuer's *lifetime* is `Deps.ChildAskReviewer`: a Build-scoped optional
  interface declared in `engine/agent`, implemented in composition, consumed
  per-run, and needing no `engine/port` type (which is the first place a
  reader greps after "sibling of `port.EventLog`"). Session gains an inert
  `Principal` label, same pattern as the existing
  `Profile`/`ProviderID`/`ModelID` snapshot labels
  (`engine/session/session.go`), persisted so rehydration re-derives the
  same identity, and propagated to children (reject-empty: an empty
  principal must not silently mean "single-user deployment").
  `port.SessionLease.Owner` is *not* overloaded with the issuer identity:
  exclusion needs per-Build distinctness while attribution needs sharing,
  so an additive field carries the principal instead.

## The edge: where users come from

The one piece with no existing seam. mecated/mecak8s have
TLS/auth/rate-limit but no *user* concept. The design needs an **edge auth
interceptor** (gRPC/HTTP): the caller authenticates with an OIDC bearer from
the corporate IdP (or mTLS client cert), and `CreateSession` binds the
session to that user principal, the `user/<uid>` at the root of every
`delegation_chain`. That requires a `principal` field on `CreateSessionRequest`
(the one wire-level addition; the session-store driver RPCs already carry
`session_id`, and a principal on the *snapshot* needs no proto change since
`sessnap` is additive opaque JSON; the event-log `principal` annotation is
the other additive change). But the edge is **one writer, not the only
source**: the durable owner record on the session is primary, and work that
starts itself (a scheduled fire) writes it directly rather than finding
nothing (see "What breaks today").

## Audit: the delegation tree, durably

Converged pattern (Q5): **dual-ID**, one immutable tree ID for audit (the
`txn` claim; Txn-Token and W3C trace_id both won this argument) plus per-hop
IDs with parent links for topology. mecatl already has this shape: the
session ID is the root, `ParentCallID` on the delegation event families
(`subagent.*`/`team.*`/`parallel.*`) is the topology, and the durable
Redis-backed `port.EventLog` is the substrate. The deltas:

- events gain a **principal annotation** (metadata-only: the existing
  contract that no child-authored content ever crosses into a parent event
  stream is preserved, identity, never child content);
- `txn` correlates a run across the delegation tree; the tree is
  reconstructable offline from Redis alone. Note `txn` is mecatl's *internal*
  join key: the credential that crosses to vMCP is minted by the gateway, so
  nothing mecatl-issued can be inside it, and a separate outbound correlation
  value joins mecatl's log to the gateway's. Two join keys, two boundaries;
  neither spans both.
- optional Rekor-style transparency anchoring of EventLog commits is a later
  consumer, not a dependency.

Three audit problems are beyond the current frontier, flagged rather than
solved: **cross-trust-domain correlation** (Txn-Tokens stop at the domain
boundary), **child→parent result attestation** (every standard flows
parent→child; nothing lets a child return signed proof of what it did), and
**background children that outlive the parent run** (OTel assumes children
end first; mecatl's background subagents don't).

## Phasing

Each step is independently useful; nothing is big-bang. Phase 1 is
deliberately **unsigned-and-honest**: it ships the audit trail as a plain
`principal` annotation on the event log, a durable record of which
principal did what, on a seam (`Service.appendEvent`) that already exists,
with no signature inviting trust it hasn't earned. The alternative
(signed-and-enforcing immediately) would sign claims like
"`authorization_details` is a strict subset" while nothing enforces them:
a signed claim that isn't true yet, which is worse than an unsigned honest
one. The one-way commitments (the KMS dependency, the JWKS endpoint's SLO,
the trust-domain name) move behind the first enforcing phase, so they land
after something has been learned about whether the novel parts work.

1. **Audit trail (unsigned)**: `Principal` on the aggregate, propagated to
   children (reject-empty); the `principal` event annotation; the edge
   interceptor binding users on `CreateSession`; the owner record on
   `ScheduleSpec`. Delivers durable multi-user attribution.
2. **Issuer + attenuation engine (signed, enforcing)**: trust domain config,
   KMS-backed intermediates, JWKS endpoint, per-session instance SVIDs with
   `delegation_chain`/`authorization_details`/`txn`; the subset invariant
   enforced at the child-minting seams; `depth` limits;
   attenuation-preserved re-mint across resume. Delivers the delegation
   model, the first phase whose signatures mean something.
3. **External surface**: outbound presentation (subagent-attributed SVID +
   harness PoP via `cnf`/`jkt`, DPoP/WPT-shaped), verification middleware
   recipe for downstream consumers (the `scoped-resource-grants.md`
   integration point), optional Rekor anchoring.

## Honest costs and risks

- **We become an issuer operator**: key rotation, bundle lifecycle, KMS
  dependency, signing-request audit. This is the price of the model; the
  research says it is a well-trodden price (SPIRE/cert-manager/step-ca all
  pay it the same way). Operational specifics this doc does not yet answer:
  whether session creation degrades gracefully when the KMS is unreachable
  (likely yes, since minting rides the session-open/delegation path, not the
  per-turn path, so a brief outage delays new sessions without killing
  running ones); whether the JWKS endpoint needs its own SLO; and whether
  trust-domain renaming has a migration story or is a one-time choice best
  locked early. A rename orphans every SPIFFE ID in the historical audit log.
  Standard issuer-operator concerns; none novel.
- **Claim-name drift**: `delegation_chain`/`depth` are pre-standard (URI-named
  to mark that); `txn` is *not*, it is registered in RFC 8417 §2.2.
  `delegation_chain` is structurally RFC 8693 `act`-shaped by design and the
  scope/audience semantics are 8693's, so ratification of Txn-Token / WIMSE
  agent work, or interop with a plain 8693 STS, is a rename + envelope
  adapter, not a rebuild.
- **Multi-tenant trust reduces to "the issuer doesn't lie."** This is the
  same trust a token-exchange STS already carries, but it must be stated:
  the pod that can sign is the pod that can impersonate any session. The
  signature adds tamper-evidence and offline verifiability, not truth; a
  compromised issuer can sign anything. What it buys is that a forge's or
  KMS's audit log and mecatl's event log can be cross-checked, and an
  auditor with the bundle can verify a chain without trusting the harness's
  word.
- **Redis is the root of trust on resume.** All authority comes out of the
  session store at rehydration; the store today dials with no auth, no TLS,
  no keyspace scoping, and `sessnap` (the session snapshot format) is plain
  JSON with no MAC. Redis auth/TLS is a prerequisite for any of this
  meaning anything, and the chain-integrity mitigation (issuer signs the
  chain at mint, verifies before re-mint) is what stops "monotonic
  attenuation across resume is one `HSET`" (one Redis write command).
- **Deployment scale honesty.** This layer is over-engineered for a
  single-user or single-org deployment and table stakes for a multi-org
  platform. It should be **optional** (`--identity-domain`, empty = off),
  like session leasing, so the operational cost is paid only by deployments
  that need verifiable identity.
- **JWT size budget.** `delegation_chain` grows one entry per hop, and JWTs
  ride HTTP headers (commonly capped at 4–8KB). Deep delegation trees could
  exceed this. Mitigation: bounded `max_depth`, scope snapshots kept small,
  and, if needed, truncating the chain to the last N hops with earlier
  hops referenced by `txn`. State the budget explicitly when the vocabulary
  lands.
- **Key-compromise recovery** is unwritten today: rotate the root, publish a
  new JWKS, let old SVIDs expire within their (minutes-long) TTL, re-mint on
  next session operation. The procedure exists but must be documented and
  rehearsed.
- **Definition drift across resume.** A project-tier `AgentDef` is read from
  a mutable workspace, and instance identity is deliberately stable across
  reopen and migration, so a resumed instance can run a different catalog
  than its credential was minted for. `draft-goswami-agentic-jwt-01`
  derives identity from a hash of prompt+tools+config and forces
  re-registration on change (caveats: TOCTOU under template substitution;
  patent-pending). Deferred, named.
- **The scope vocabulary is ours to get right.** It is the highest-leverage
  design surface in the doc and the least guided by prior art. v1 is
  minimal for that reason.
- **Entra/Okta/A2A/MCP could converge on a vocabulary that isn't ours.**
  Mitigation: the claims are private by design; interop is a mapping layer
  later, and the doc avoids claiming our vocabulary as a standard.

## References

- SPIFFE specs: `github.com/spiffe/spiffe` standards: `SPIFFE-ID.md` (§2.2
  path semantics + segment charset, §2.3 length, §4.1.1 temporal accuracy),
  `JWT-SVID.md` (§3.1 `sub` = holder, §3 private-claims MAY, §7.2
  single-audience), `X509-SVID.md`, `SPIFFE_Workload_Endpoint.md` (§5: no
  client auth; local agent is the authority), `SPIFFE_Broker_API.md`
  (on-behalf-of for referenced workloads)
- RFC 8693 (OAuth 2.0 Token Exchange: §1.1 delegation vs impersonation, §4.1
  `act` + nested-actor informational-only rule, §4.4 `may_act`, §5 scope
  suggestion, A.2.5 delegation example)
- RFC 8417 (Secure Event Token: `txn` claim, §2.2), RFC 9396 (Rich
  Authorization Requests: `authorization_details`), RFC 9449 (DPoP: `cnf` +
  `jkt`), RFC 3820 (X.509 proxy certificates: `pCPathLenConstraint` §3.8.1 +
  rights-intersection §3.8.2, prior art for `depth`/`max_depth` and for the
  attenuation itself)
- IETF drafts: `draft-ietf-wimse-arch` (§3.4.11 AI intermediaries; Identity
  Proxy; egress generalization), `draft-ietf-wimse-wpt`,
  `draft-ietf-oauth-transaction-tokens` (`txn`, MUST-narrow replacement
  §13.14 in -09), `draft-ietf-wimse-workload-identity-practices` (§5.4–5.5
  pause language), `draft-ietf-oauth-identity-chaining` (non-escalation
  expectation), `draft-ietf-oauth-identity-assertion-authz-grant` (ID-JAG,
  narrowing as policy MAY §4.3.3), `draft-zhu-oauth-async-delegation`
  (monotonic scope across rotation), `draft-hartman-credential-broker-4-agents`
  (CB4A, the Credential Broker for Agents draft: per-session agent SVIDs,
  non-renewable re-attestation)
- Individual drafts (attenuation prior art):
  `draft-mcguinness-oauth-actor-profile-00` (non-escalation MUSTs,
  append-only chain, `sub` = authorizing principal §3.2, confused-deputy
  §14.5), `draft-mcguinness-oauth-ai-agent-instance-00` (definition/instance
  split, `agent_instance_id`), `draft-liu-agent-operation-authorization-02` +
  `draft-liu-oauth-chain-delegation-00` (`delegation_chain`, depth cap 5),
  `draft-sweeney-wimse-credential-delegation-00` (revocation design),
  `draft-goswami-agentic-jwt-01` (def-drift hash)
- Holder-side attenuation: Macaroons (Birgisson et al., 2014), Biscuit
  (`eclipse-biscuit/biscuit`)
- Microsoft Entra Agent ID docs (blueprint / identity / user account);
  OpenAI Assistants API (assistant/thread/run); A2A spec (AgentCard/task)
- SPIRE docs: key-manager plugins (`aws_kms`/`azure_key_vault`/`gcp_kms`/
  `hashicorp_vault`) + upstream-authority plugins, HA guide, defaults
  (`default_jwt_svid_ttl=5m`, JWT-SVIDs minted fresh per request, so no
  rotation schedule), `MintJWTSVID` + `CredentialComposer` (implementation
  details, `allow_admin`-gated)
- Repo: [ADR 0027](adr/0027-cloud-native.md) (disposable-process arc, event
  log, leasing), [ADR 0048](adr/0048-mecak8s.md) (k8s-native storage-free
  topology), [`docs/scoped-resource-grants.md`](scoped-resource-grants.md)
  (the downstream grant-consumer strawman this feeds)