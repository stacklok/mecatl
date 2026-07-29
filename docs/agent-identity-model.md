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

The north star is deliberate: **mecatl becomes its own SPIFFE trust domain
issuer**. Not a consumer of SPIRE per-workload identities, not a client of the
(experimental) SPIFFE Broker API — an issuer in its own right, with its own
signing keys, its own bundle endpoint, and a claim vocabulary the SPIFFE
JWT-SVID spec explicitly leaves open to us. SPIRE (or cloud IAM) only ever
attests the *infrastructure* — the mecatl pods themselves. Everything above
the pod — users, agent definitions, sessions, subagents — is mecatl's own
identity domain.

## Prior art, honestly assessed

A research pass over the standards landscape (2024–2026) established three
things. First, **the primitives are mature**: SPIFFE/SPIRE is CNCF-graduated,
RFC 8693 (OAuth token exchange, `act`/`may_act` delegation chains) is a
standard, and the IETF WIMSE working group is actively standardizing workload
identity for exactly this deployment shape. Second, **the industry has
converged on the three-tier identity segmentation** this design uses (see
below) — Microsoft's Entra Agent ID (blueprint / identity / user account),
OpenAI's Assistants API (assistant / thread / run), A2A (AgentCard / task),
and WIMSE (workload / workload instance / service) all draw the same lines.
Third, **nobody has solved multi-tier agent delegation with attenuation**.
The A2A and MCP protocols explicitly punt on it. WIMSE's architecture draft
describes user→AI-agent→AI-agent→workload chains (§3.4.11, "AI and ML-Based
Intermediaries") as *requirements* — no protocol, no claims, no
implementation. RFC 8693 gives the `act` chain as an audit artifact but
normatively declares nested actors "informational only". There is no standard
agent capability vocabulary, no standard attenuation algorithm, and no
credential lifecycle for an interruptible, human-in-the-loop workload. Those
gaps are called out per-section below as **innovation ground**.

## The three-tier model

Identity is segmented exactly where the industry draws the lines — and where
mecatl's existing types already draw them (the durable specialist definition
is `engine/tool/agentsource.go` (`AgentDef`); the stateful conversation is the
`engine/session/session.go` (`Session`) aggregate; the ephemeral turn loop is
`Engine.Run`):

```
TIER 1  DEFINITION   durable identity — what the agent IS
                     (mecatl's AgentDef; the generic main engine is the
                      built-in "main" definition)
TIER 2  INSTANCE     one living session of a definition — the credential tier
                     (mecatl's Session aggregate; short-TTL SVIDs minted here)
TIER 3  RUN          one execution — the transaction tier
                     (mecatl's Engine.Run; per-run txn correlation)
```

SPIFFE IDs in mecatl's own trust domain (name is a config knob):

```
spiffe://<td>/user/<uid>
spiffe://<td>/agent/<def-name>
spiffe://<td>/agent/<def>/inst/<sessionID>
spiffe://<td>/agent/<def>/inst/<sessionID>/child/<childID>
spiffe://<td>/agent/<def>/inst/<sessionID>/child/<childID>/child/<grandchildID>
```

- **Definition identities** (`agent/<def>`) are the durable "who" — the anchor
  for operator policy ("what may a `tdd-worker` ever do?"), exactly the
  ServiceAccount/blueprint role in the platform analogs. They are never
  minted as credentials; they exist as path structure and as policy targets.
- **Instance identities** (`inst/<sessionID>`) are where SVIDs live. A session
  — including a reopened, recovered, or pod-migrated one — is one instance;
  rehydration re-mints the *same* instance identity. N concurrent sessions of
  one definition are N instance SVIDs under one definition identity: audit
  can group by definition ("what did code-reviewers do this week?") or by
  instance ("what did *this* delegation do?").
- **Child identities** extend the parent's *instance* path with `child/<id>`,
  reusing the existing child session ID schemes — `subagent-<callID>`,
  `parallel-<callID>-<i>`, and `engine/agent/teamsupervisor.go`
  (`MemberSessionID`)'s `team-<teamID>-<member>` — so the identity graph and
  the runtime delegation graph cannot drift.
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

Instance SVIDs are SPIFFE JWT-SVIDs — spec-shaped (`sub`, `aud`, `exp`,
bundle-published signing keys) with private claims, which JWT-SVID §3
explicitly permits ("reliance on claims not defined here may impact
interoperability" — accepted; this vocabulary is ours to carry):

| Claim | Meaning |
|---|---|
| `sub` | the instance SPIFFE ID (spec) |
| `aud` | intended verifier(s) (spec; single-audience preferred per JWT-SVID §7.2) |
| `exp`/`iat`/`jti` | short TTL, unique per mint (spec) |
| `dlg` | delegation chain: ordered `{id, def, scope}` entries, user → … → parent. Append-only, signed at mint. **This is RFC 8693 `act` nesting made first-class** — same semantics (see below), explicit instead of emergent |
| `scope` | the effective authority of this instance — see vocabulary below. Issuer-enforced invariant: **strict subset of the parent's** |
| `depth` / `max_depth` | how much further this identity may delegate (the RFC 3820 `pCPathLenConstraint` idea, in JWT form) |
| `txn` | immutable correlation ID for this run (Txn-Token vocabulary); parent's `txn` is recorded in `dlg`, preserving the tree without one giant ID |

### The RFC 8693 contract (`act` / `may_act`)

The delegation semantics are not invented here — they are RFC 8693's,
adopted verbatim, then extended where 8693 is silent. Three normative
commitments:

1. **`dlg` IS `act` nesting.** RFC 8693 §4.1 defines the `act` (actor)
   claim — name the current actor, nest one deeper per delegation hop.
   In 8693 deployments the chain is *emergent*: each token exchange nests
   one level, and the full trail only exists across the sequence of
   exchanges. `dlg` carries the same `{sub, act: {sub, …}}` structure
   **explicitly**, so a verifier reads the whole user→…→parent tree from
   one credential instead of reconstructing it.
2. **The chain is audit, never authority.** 8693 §4.1 is normative:
   consumers "MUST only consider the token's top-level claims and the
   party identified as the current actor… prior actors identified by any
   nested act claims are informational only." We honor exactly that:
   authorization keys off `sub` (the acting instance) + `scope` (its
   effective authority). `dlg` entries are informational — the audit
   trail, not an authz input. This is how every deployed 8693 STS (AWS,
   Azure, GCP SA impersonation) already evaluates tokens, and it is what
   keeps a compromised or confused deep actor from re-asserting rights
   via the chain.
3. **Delegation, never impersonation.** 8693 §1.1 separates the two:
   impersonation makes the issued token's `sub` the delegator (the actor
   vanishes); delegation keeps `sub` = delegator and names the actor.
   The model is strictly delegation — the user is always at the root of
   `dlg`, the acting instance always named. (The industry counterexample
   is GitHub Copilot's coding agent: commits attributed to the user, no
   visible actor — impersonation with no audit trail. We refuse that
   shape.)

**`may_act` (8693 §4.4)** — the claim that pre-authorizes *which* actors
may delegate for a subject — is played by **definition-tier policy**, not
a token claim: "user U may spawn definition D", "D may spawn children of
definitions {…}". Those are the three nested envelopes (user grant ⊇
definition policy ⊇ instance scope ⊇ child scope) from the tier model.
Same consent-hook semantics as `may_act`, expressed as issuer policy
because our exchange is in-process (below) rather than an STS the user
consents through.

**Where 8693 ends and the novel part begins.** 8693 *permits* scope
narrowing at exchange; nothing structurally enforces it (a misconfigured
STS can mint wider). Our issuer **refuses** to mint a child whose `scope`
is not a strict subset — attenuation as an invariant, not a policy
option. And the exchange itself is in-process: a child mint happens at
the delegation seams (`buildChildSession`, the team member factory), so
8693's wire protocol (`grant_type=token-exchange`, `subject_token` /
`actor_token`) never runs — it only enters if the issuer is later exposed
as a network STS for external consumers, at which point these claims slot
into 8693's envelope unchanged (an instance JWT-SVID is already a valid
`actor_token` of type `urn:ietf:params:oauth:token-type:jwt`). `depth`/
`max_depth` and `txn` have no 8693 analog — they come from RFC 3820 and
the Txn-Token draft respectively.

### The scope vocabulary (innovation ground — Q4)

Nobody has standardized what an agent may *do* in a token: MCP scopes are
server-defined strings, A2A says "agent-defined", Entra reuses Graph resource
scopes, Cedar/OPA are app-defined. The v1 vocabulary is deliberately minimal
and maps onto machinery mecatl already enforces:

- **tool names** — the catalog names the governance fold already evaluates
  (`engine/governance` rules; the `Audience` enum already distinguishes
  main- vs subagent-bound rules — the closest existing thing to an
  attenuation channel);
- **a posture ceiling** — the `strict < trusted < auto < yolo` ladder from
  `internal/app/posture.go`; a child's ceiling ≤ its parent's;
- **delegation rights** — whether this instance may mint children at all
  (`max_depth`), and with which definition identities.

Workspace paths are deliberately *not* claims: the workspace is a property of
the session's construction (the osfs root + read-roots), not of its identity.
Providers/models and egress are candidates for a later vocabulary revision.

### Projection, not authority (converged pattern — Q3)

The SVID is a **projection** of the session's authority for outbound calls —
never the source of it. Internally, authority remains exactly where it is
today: the session aggregate plus the deny-dominant governance fold. This
mirrors the SPIFFE Workload Endpoint model (the local agent is the authority;
the SVID exists to leave the node) and WIMSE's Identity Proxy / egress
generalization pattern (token exchange at the boundary, internal context
stays local). The in-process generalization — the *session aggregate* mints
projection tokens for its children — is practiced everywhere, named nowhere;
a mild novelty.

A consequence that simplifies everything: **subagents are not network
entities**. A subagent is a goroutine, not a pod; it never presents its own
SVID. When the harness makes an outbound call attributable to a subagent
(a forge API call, an MCP call), the *harness* presents the subagent's SVID
plus its own proof-of-possession. Only the pod tier holds keys; everything
below is signed claims. No per-subagent key material, no PoP problem below
the pod.

## The issuer in a Kubernetes deployment

mecak8s (ADR 0048) already fixed the topology the issuer must fit:
storage-free agent pods, autoscaled, state in Redis, single-writer via k8s
leases, any pod rehydrates any session. The industry pattern for issuer HA in
exactly this shape is **converged** (Q1): stateless replicas, signing keys
that never leave a signing service.

- **Key custody**: one logical issuer; the signing key lives in a KMS/Vault
  Transit-style signing service (SPIRE's `aws_kms`/`gcp_kms`/`vault` upstream
  authority pattern; cert-manager's external-issuer pattern). Every pod can
  sign; no pod holds key material longer than a signing operation. Per-pod
  intermediate keys are deliberately rejected — the community settled on
  "protect the key in KMS; handle blast radius via identity granularity."
- **Residual risk, named**: a compromised pod cannot exfiltrate the key but
  can request signatures. Defenses: KMS-layer audit logging of every sign
  request, short pod lifetimes, and the fact that minting a *valid-looking*
  but unauthorized identity still fails at the session store (the identity
  references a session that must exist in Redis).
- **Bundle distribution**: the public JWKS is served by any pod (they are
  stateless) and cached in Redis / a ConfigMap; in-cluster verifiers fetch
  once. Cross-cluster = SPIFFE federation (bundle exchange) — free because we
  stay spec-shaped.
- **Composition**: the issuer is a sibling of the existing stores in
  `internal/app` (`Build`), injected as a port; the engine loop stays
  identity-agnostic (the same storage-agnostic discipline as
  `port.EventLog`). Session gains an inert `Principal` label, same pattern
  as the existing `Profile`/`ProviderID`/`ModelID` snapshot labels
  (`engine/session/session.go`), persisted so rehydration re-derives the same
  identity. `port.SessionLease.Owner` — already an opaque identity string —
  becomes the pod's issuer identity, making single-writer exclusion
  attributable. Child minting hooks into the existing delegation seams:
  `engine/agent/subagent.go` (`buildChildSession`) and the team member
  factory, where parent claims flow in, the subset invariant is enforced, and
  the child session carries its identity.

## The edge: where users come from

The one piece with no existing seam. The driver protocol today carries "no
tenant, principal, session, or namespace field anywhere in the service"
(ADR 0027 List 3), and mecated/mecak8s have TLS/auth/rate-limit but no *user*
concept. The design needs an **edge auth interceptor** (gRPC/HTTP): the
caller authenticates with an OIDC bearer from the corporate IdP (or mTLS
client cert), and `CreateSession` binds the session to that user principal —
the `user/<uid>` at the root of every `dlg` chain. The driver protocol gains
an additive `principal` field on store/event-log RPCs — the one proto-level
change.

## The parkable credential lifecycle (innovation ground — Q2)

The converged TTL floor: SPIRE defaults (X.509 1h, JWT 5m, rotate at
half-life), WIMSE WIT "hours, PoP minutes, never bearer". But a mecatl
session can **park for hours awaiting a human approval and resume on a
different pod** — and no standard models that. WIMSE's practices draft says
tokens "SHOULD be invalidated when the workload *pauses*" and explicitly
leaves the mechanism out of scope; the two live IETF schools (vault/broker
re-attestation; Zhu's async-delegated refresh tokens with monotonic scope +
absolute max lifetime) both assume the token holder persists.

The design: **chain durable, credential ephemeral.**

- Instance SVID TTL is minutes; the harness re-mints on demand (it is the
  issuer — minting is a local signing call, not a network dependency).
- A parked session's SVID simply expires. Nothing is revoked because nothing
  needs to be: TTL ≪ any useful attack window.
- Resume (the existing `rehydrateSession` seam) re-mints the **same instance
  identity with the same `dlg` chain and a scope no wider than pre-park** —
  monotonic attenuation across resume, enforced issuer-side against the
  persisted chain. This state machine — active/parked, re-attest-on-resume,
  attenuation preserved across pod boundaries — is genuinely novel; the doc
  treats it as a named contribution, not an implementation detail.
- Session GC (PrunableStore) plus short TTL bounds the
  deleted-session-but-live-token window without a revocation list; internal
  verifiers may additionally check `jti`↔session-liveness against Redis
  (cheap in-cluster), external verifiers stay offline/TTL-only.

## Audit: the delegation tree, durably

Converged pattern (Q5): **dual-ID** — one immutable tree ID for audit (the
`txn` claim; Txn-Token and W3C trace_id both won this argument) plus per-hop
IDs with parent links for topology. mecatl already has this shape: the
session ID is the root, `ParentCallID` on the delegation event families
(`subagent.*`/`team.*`/`parallel.*`) is the topology, and the durable
Redis-backed `port.EventLog` is the substrate. The deltas:

- events gain a **principal annotation** (metadata-only — the gauntlet-#7
  child-isolation contract is preserved: identity, never child content);
- `txn` correlates a run across the delegation tree; the tree is
  reconstructable offline from Redis alone;
- optional Rekor-style transparency anchoring of EventLog commits is a later
  consumer, not a dependency.

Three audit problems are beyond the current frontier, flagged honestly rather
than solved: **cross-trust-domain correlation** (Txn-Tokens stop at the
domain boundary), **child→parent result attestation** (every standard flows
parent→child; nothing lets a child return signed proof of what it did), and
**background children that outlive the parent run** (OTel assumes children
end first; mecatl's background subagents don't).

## Phasing

Each step is independently useful; nothing is big-bang.

1. **Issuer skeleton** — trust domain config, KMS-backed signing, JWKS
   endpoint, per-session instance SVIDs with `dlg`/`txn` (no scope
   enforcement), `Principal` on the aggregate, event annotation, edge
   principal binding. Delivers durable multi-user identity + audit.
2. **Attenuation engine** — the `scope` vocabulary mapped onto governance;
   issuer-side subset enforcement at the child-minting seams; `depth` limits;
   attenuation-preserved re-mint across resume. Delivers the delegation
   model.
3. **External surface** — outbound presentation (subagent-attributed SVID +
   harness PoP, DPoP/WPT-shaped), verification middleware recipe for
   downstream consumers (the `scoped-resource-grants.md` integration point),
   optional Rekor anchoring.

## Honest costs and risks

- **We become an issuer operator**: key rotation, bundle lifecycle, KMS
  dependency, signing-request audit. This is the price of the model; the
  research says it is a well-trodden price (SPIRE/cert-manager/step-ca all
  pay it the same way).
- **The scope vocabulary is ours to get right.** It is the highest-leverage
  design surface in the doc and the least guided by prior art. v1 is
  deliberately minimal for that reason.
- **Claim-name drift**: `dlg`/`txn`/`depth` are pre-standard. `dlg` is
  structurally RFC 8693 `act`-shaped by design and the scope/audience
  semantics are 8693's, so ratification of Txn-Token / WIMSE agent work —
  or interop with a plain 8693 STS — is a rename + envelope adapter, not
  a rebuild.
- **Multi-tenant trust reduces to "the issuer doesn't lie."** This is the
  same trust a token-exchange STS already carries, but it must be stated:
  the pod that can sign is the pod that can impersonate any session.
- **Entra/Okta/A2A/MCP could converge on a vocabulary that isn't ours.**
  Mitigation: the claims are private by design; interop is a mapping layer
  later, and the doc avoids claiming our vocabulary as a standard.

## References

- SPIFFE specs: `github.com/spiffe/spiffe` standards — `SPIFFE-ID.md`,
  `JWT-SVID.md` (§3 private claims, §7.2 single-audience), `X509-SVID.md`,
  `SPIFFE_Workload_Endpoint.md` (§5: no client auth; local agent is the
  authority)
- RFC 8693 (OAuth 2.0 Token Exchange: `act` §4.1, `may_act` §4.4,
  nested-actor informational-only rule)
- RFC 3820 (X.509 proxy certificates: `pCPathLenConstraint` — prior art for
  `depth`/`max_depth`; cite the idea, not the mechanism)
- IETF drafts: `draft-ietf-wimse-arch` (§3.4.11 AI intermediaries; Identity
  Proxy; egress generalization), `draft-ietf-wimse-wpt`,
  `draft-ietf-oauth-transaction-tokens` (`txn`, MUST-narrow replacement
  §14.11.1), `draft-ietf-wimse-workload-identity-practices` (§5.4–5.5 pause
  language), `draft-zhu-oauth-async-delegation` (monotonic scope across
  rotation), `draft-hartman-credential-broker-4-agents` (closest published
  analog: per-session agent SVIDs)
- Microsoft Entra Agent ID docs (blueprint / identity / user account);
  OpenAI Assistants API (assistant/thread/run); A2A spec (AgentCard/task)
- SPIRE docs: upstream authority plugins (`aws_kms`/`gcp_kms`/`vault`), HA
  guide, defaults (`default_jwt_svid_ttl=5m`, rotation at half-life)
- Repo: [ADR 0027](adr/0027-cloud-native.md) (disposable-process arc, event
  log, leasing), [ADR 0048](adr/0048-mecak8s.md) (k8s-native storage-free
  topology), [`docs/scoped-resource-grants.md`](scoped-resource-grants.md)
  (the downstream grant-consumer strawman this feeds)
