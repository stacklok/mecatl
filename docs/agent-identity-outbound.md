# Agent identity, part 2: the outbound hop

*Status: strawman / working draft. Speculative scoping, not a design record under
[ADR 0002](adr/0002-documentation-lifecycle.md). Companion to
[`docs/agent-identity-model.md`](agent-identity-model.md), which this takes as its premise
and does not restate. Same tier as
[`docs/scoped-resource-grants.md`](scoped-resource-grants.md).*

## What this decides

The companion doc models identity from the user down to the subagent and stops at the
boundary. This covers the boundary: how the harness reaches a tool, what the gateway
decides, and how a user's stored third-party credential gets used without handing an agent
more than it was given.

The gateway must authorize the use of a **concrete credential** for a **concrete resolved
target**. mecatl presents a short-lived delegated access token in which the user is the
subject, the agent is the actor, and the pod is the authenticated holder. The gateway
verifies it, resolves the call once, decides over that resolution, and fetches only the
selected credential after allow.

**The decisions:**

1. Four questions get four separate answers, and one answer never stands in for another:
   whose authority is being spent, which agent is spending it, which process holds the key,
   and who is accountable for the stored session or schedule.
2. A subagent can do less than the agent that spawned it, and never more.
3. What a subagent may reach has to be legible outside mecatl. How many turns it may take
   does not, and stays inside the process.
4. The gateway works out what a call resolves to once. The policy decision and the
   credential lookup both use that result, and neither works it out again.
5. No credential is read out of storage until the call has been allowed.
6. The correlation value is written to mecatl's log and the gateway's, and is read by
   nothing that makes a decision.
7. GitHub receives an ordinary GitHub token. It is never asked to understand agents,
   delegation, or anything else specific to mecatl.
8. A resumed session and a scheduled run each get authority no wider than what was granted
   before.

**Non-goals:** giving in-process children their own workload keys; making a provider verify
mecatl's delegation chain; carrying runtime budgets in access tokens; specifying OAuth
behaviour beyond the mecatl/vMCP profile.

**How to read the status notes.** Quoted blocks say what exists today. They are cost
signals rather than constraints. These codebases are built by one team and everything in them is
changeable. They stay inline rather than moving to an implementation doc because they are
what makes this checkable: two review rounds found false claims in them, and both times the
claim sounded like plumbing and was a prerequisite.

---

## Identity and token profiles

| Artifact | Subject | Actor | Holder | Verified by | Purpose |
|---|---|---|---|---|---|
| Agent token (leg 1) | agent definition | — | mecatl pod | vMCP authorization server, which also mints it | Prove which agent is acting |
| Outbound access token (leg 2) | the user | agent definition | mecatl pod | vMCP gateway | Authorize the gateway call |
| Provider credential | provider account | — | vMCP | the provider | Execute the backend operation |
| Schedule grant | the user | schedule + definition | credential store | mecatl and the AS | Permit unattended re-derivation |
| Correlation value | — | instance label | — | nobody | Join two audit records |

Terms whose outbound meaning differs from the companion model:

**Owner** — who is accountable for a persisted session or schedule, and the identity every
operation on that object is authorized against. A distinct axis from subject: owner is about
the stored object, subject is about whose authority a call spends.

**Definition and instance** — a definition is a kind of agent, named in configuration and
stable enough for policy to reference. An instance is one running occurrence, ephemeral and
unnamed in advance. Policy keys on definitions; audit records instances.

**Resolved target** — what routing determines a call to be: tool, operation, canonical
resource, and enough to select exactly one credential.

Authority, attenuation, limits and the internal tier model are defined in the companion doc.

### Named invariants

Referenced by name below rather than re-argued.

**Target binding.** Routing produces one resolution. Admission decides about that
resolution and credential selection consumes it. Nothing re-resolves in between.

**Constrain, never grant.** A binding may not let a holder reach past the authority the
credential already states. A session pointer fails this; a holder key satisfies it.

**No key below the pod.** The pod is the workload and the key holder. A subagent gets an
identity from mecatl, real inside mecatl's trust domain, and no private key, because
siblings share a process and cannot hold one separately.

**Refuse on a missing input.** When something a decision needs is absent, whether that is a policy, a
resolved target, or an annotation saying whether a tool writes, deny rather than guess. Absent
metadata is treated as the dangerous case.

**Correlation is not authority.** Correlation values are read by logging and nothing else.

---

## The flow

Alice asks her agent to review a pull request. It spawns a code-reviewer subagent, which
needs the diff from GitHub. GitHub sits behind vMCP, which holds Alice's GitHub credential.

```mermaid
sequenceDiagram
    autonumber
    actor Alice
    participant M as mecatl pod
    participant S as code-reviewer subagent
    participant AS as vMCP authorization server
    participant G as vMCP gateway
    participant V as credential store
    participant B as GitHub

    Note over M: at startup, fetches an X.509-SVID from the Workload API.<br/>The subagent cannot reach that socket.

    Alice->>M: OIDC access token, and "review PR 42"
    Note over M: store the owner on the session

    M->>S: spawn. fewer tools, one repository
    Note over S: a goroutine in this pod. no key of its own.

    rect rgba(128,128,128,0.07)
    Note over M,AS: leg 1. cached per pod and definition
    M->>AS: POST /token, grant_type = client_credentials<br/>client authenticates with the pod's SVID<br/>scope = agent:code-reviewer
    Note over AS: is that definition registered?
    AS-->>M: agent token. sub = the definition, cnf bound to the pod.<br/>audience is the AS, so it opens nothing on its own.
    end

    S-->>M: needs the diff

    rect rgba(128,128,128,0.07)
    Note over M,AS: leg 2. per user and definition
    M->>AS: POST /token, same client cert<br/>subject_token = Alice's access token<br/>actor_token = the agent token above
    AS-->>M: access token. sub = Alice, act = the definition,<br/>cnf bound to the pod
    end

    M->>G: tools/call for github.read_file
    Note right of M: the access token above, plus proof the pod holds the bound key<br/>X-Correlation-Id names this subagent and this call

    Note over G: verify token and certificate binding<br/>resolve the call to one target
    alt denied, or any decision input missing
        G--xM: refuse. no credential is read.
    end

    G->>V: fetch, keyed on Alice and that target
    V-->>G: one credential
    G->>B: GET the diff, using Alice's GitHub token
    B-->>G: the diff
    G-->>M: tool result
    M-->>S: the diff
```

| Hop | Input | Action | Output | Invariant |
|---|---|---|---|---|
| 1 Bind | authenticated caller | resolve to an immutable principal | owned session | owner persisted and enforced |
| 2 Narrow | parent authority | compute a subset | child authority + limits | only authority travels |
| 3 Exchange | pod SVID, then subject token + agent token | two legs: mint the agent token, then exchange | sender-bound access token | three identities, each authenticated |
| 4 Call | access token, holder proof over the same connection | send the tool call | gateway request | correlation is not authority |
| 5 Decide | verified claims, resolved target | admission | allow or deny | refuse on a missing input |
| 6 Fetch | subject, credential selector | read one credential | provider credential | target binding |
| 7 Serve | provider credential | call the backend | result | chain stops at the gateway |
| 8 Resume | live principal or offline grant | re-derive | access token | authority never widens |

Hops 1, 2 and 4 are fully stated by that table, with one exception each recorded below.
The rest need detail.

> **Hop 1 today.** Session creation has no owner field, and a scheduled fire constructs its
> session in-process from a leader-elected goroutine, so it never reaches anything that
> could assign one. Session listing takes no principal at all.
>
> **Hop 2 today.** There is no per-spawn narrowing at all. A
> child's tool set is resolved *statically, per definition, at build time* against the
> shared catalog; nothing reads the parent's current authority because no such value exists.
> The per-call knobs are limits and a selector for which pre-built tier to use. The audience
> tag on permission rules is a config-parse-time label on *rules*, identical for every child.
> So `Narrow` is not wiring an existing computation outward — **authority has to be invented
> as a runtime type first**, and that gates everything the gateway could enforce against.
>
> **Hop 4 today.** Correct already: the inbound path reads only protocol headers, and the
> identity struct has no header-populated field. Worth keeping when the outbound path stops
> baking a static header map into a client at dial time.

---

### Hop 3 — the exchange, in two legs

Two calls to the authorization server, not one. The pod proves itself once and receives a
token naming the agent; that token is then the actor input to the exchange that brings in the
user. Splitting them is what puts the authorization server, rather than mecatl, in charge of
which agents may exist.

**Leg 1 — the pod asks for an agent token.**

This is `client_credentials` — a client asking for a token for itself, narrowed by scope to
one agent. The SVID is how the client authenticates, and
`draft-ietf-oauth-spiffe-client-auth` §3.1.2 is this exact request:

```http
POST /token HTTP/1.1
Host: as.vmcp.example.com
Content-Type: application/x-www-form-urlencoded

grant_type=client_credentials
&client_id=spiffe://mecatl.example.com/pod/mecatl-7f4c
&client_assertion_type=urn:ietf:params:oauth:client-assertion-type:jwt-spiffe
&client_assertion=<the pod's JWT-SVID, audienced to this server>
&scope=agent:code-reviewer
```

With the X.509 method (§3.2.1) the two assertion parameters disappear and the same request
goes over mTLS carrying the SVID as the client certificate. Nothing else changes, which is the
point of the draft: the method is swappable and the grant is unaffected.

```jsonc
{
  "sub": "spiffe://mecatl.example.com/agent/code-reviewer",
  "aud": "https://as.vmcp.example.com",  // input to leg 2, not a gateway credential
  "cnf": { "x5t#S256": "..." },          // this pod holds it, and only this pod
  "exp": 1785298000
}
```

Cached per pod and definition. It names no user and is useless alone — presented to the
gateway it gets nothing, because its audience is the authorization server.

**Whose identity is `sub` here** is a genuine question, not a detail. RFC 9068 §2.2 says a
client-credentials token SHOULD carry the client in `sub`, which would make it the pod, with
the agent named in a separate claim. Putting the definition there instead is what makes
leg 2's `act.sub` the thing policy names, and it reads as the server asserting an identity
the pod is registered to run. The SHOULD is not a MUST and the second reading is more useful,
but it is a deviation and should be argued rather than assumed.

**Leg 2 — the user's authority is added.**

```http
POST /token HTTP/1.1
Host: as.vmcp.example.com

grant_type=urn:ietf:params:oauth:grant-type:token-exchange
&client_id=spiffe://mecatl.example.com/pod/mecatl-7f4c
                    # client authenticates exactly as in leg 1 — the method is
                    # a property of the client, not of the grant
&subject_token=<Alice's access token>
&actor_token=<the agent token from leg 1>
&actor_token_type=urn:ietf:params:oauth:token-type:access_token
&resource=https://vmcp.example.com
&scope=repo:read
```

```jsonc
{
  "sub": "u_01HQ8Z...",                       // Alice, immutable internal id
  "act": { "sub": "spiffe://mecatl.example.com/agent/code-reviewer" },
  "scope": "repo:read",                       // intersected down, never widened
  "aud": "https://vmcp.example.com",
  "cnf": { "x5t#S256": "..." },               // bound to the pod certificate
  "exp": 1785312000
}
```

**The authorization server decides which agents may exist.** In a single call mecatl asserts
its own actor claim and the server believes it — which makes mecatl both the asserter and the
attack surface, because definitions come from configuration a repository can write. Leg 1
moves minting to the server, which issues an agent token only for a definition it was
configured to know. A definition invented in a pull request cannot be named, because nothing
registered it.

That settles a question a single call leaves open: only registered definitions are nameable in
policy. Per-project specialists still run. They run *under* a registered definition rather
than *as* one, and lose only the ability to be named in a rule.

What remains is that mecatl still chooses which registered definition to request per spawn, so
a prompt-injected parent picks among the operator's own agents. That is the ordinary
attenuation problem the rest of this design handles, not a forgery.

**mecatl signs nothing.** The server verifies the SVID, which client authentication requires
of it anyway, and signs what it issues. No keys of mecatl's cross the boundary, and the
deployment loses a trust relationship rather than gaining one.

**Two tokens, two lifetimes.** Leg 1 is per pod and definition, leg 2 per user and definition.
The workload proof happens rarely, and off the user's path.

**This is delegation, not impersonation.** Leg 2 carries both parties — Alice in `sub`, the
agent in `act`. RFC 8693 §1.1 draws that line, and it is the premise of everything here: an
impersonation token would spend Alice's authority while leaving nothing to say what was
acting, so the gateway would have nothing to constrain and the log nothing to record.

**Three identities, each proved by its own input.** The SVID proves the pod, at both legs.
Leg 1 turns that into proof of which agent is acting — client authentication cannot do that
by itself, because the client is the pod and one pod runs many agents. Leg 2 adds the user. A
server accepting an unauthenticated actor would let a caller request the policy identity of a
more privileged agent.

**`sub` is the user**, by immutable internal identifier rather than an email, which is mutable
and not unique across issuers. The JWT-SVID rule forcing `sub` to be the holder governs
credentials *mecatl* issues; both tokens here are minted by the gateway's authorization
server, so the standard shape applies and nothing traverses a chain to find the user.

**`act` is the definition, not the instance** — what policy names, and what lets concurrent
siblings share one credential. The pod is not in `act`: it is the holder, and `cnf` already
says so.

**Attenuation is by scope, and the exchange enforces it downward.** The issued scope set is
the intersection of what the client is registered for and what the subject token was granted,
so no client can request more than the user authorized, and a subject token with no scope
claim grants none. `repo:read` denies writes.

Scope cannot name a repository. So a reviewer confined to one pull request can read any
repository the credential reaches — a real residual, bounded by the credential's own scope,
accepted here and closed in [later phases](#later-phases).

**`cnf` binds each token to a key the pod holds.** Authenticating the client and constraining
the holder are separate decisions over separate connections, and conflating them is easy:

| Decision | Options | Connection |
|---|---|---|
| Authenticate the client | JWT-SVID assertion, X.509-SVID over mTLS, or WIT-SVID | to the token endpoint |
| Constrain the holder | certificate binding (`x5t#S256`) or DPoP (`jkt`) | to the gateway |

`draft-ietf-oauth-spiffe-client-auth` §4 requires an authorization server to support one of
the three, so mTLS is a choice rather than an obligation — and a JWT-SVID assertion, being a
form parameter, survives an L7 ingress that would strip a client certificate. Either holder
binding works; without one, a token copied from memory or a log replays until it expires.

**One leg-2 credential covers many calls.** It is obtained once per user, definition, authority
and audience, and reused. Minting per call would put a network round trip in front of every
tool use.

**Reuse means a narrowing can take effect late.** If authority is reduced mid-session, a
credential minted before that still carries the wider authority until it expires. The
bound is the TTL. This doc argues elsewhere that a stored record is never the authority; for
one TTL, a cached credential is exactly that for outbound calls. Shorter TTLs trade it for
round trips, revocation lists for a distributed dependency — neither clearly beats a bounded
window named out loud.

> **Today.** Leg 1's grant does not exist. The server composes authorization-code, refresh
> and PKCE handlers plus the token-exchange factory — no `client_credentials`. Adding it is
> the smaller half of leg 1; the registration that says which agents a client may ask for is
> the larger half, and nothing like it exists either.
>
> **And no client that may use either grant can be provisioned:** registration hardcodes
> clients public and permits only `authorization_code` and `refresh_token`, and discovery
> advertises neither the grant nor secret-based client authentication, so even a
> hand-provisioned client is invisible to any library that reads metadata. **This blocks both
> legs.** Both halves are [#6082](https://github.com/stacklok/toolhive/issues/6082), which
> also carries the non-secret option: authenticate the client from a verified X.509-SVID and
> auto-register it with no secret, making the client id the SPIFFE ID. A shared secret would
> also work and is rejected — it ships the credential-in-the-environment problem this design
> removes.
>
> That option has a weakness which lands on the scope paragraph above: every auto-registered
> client receives **all** supported scopes and audiences. That makes the client half of the
> intersection vacuous and leaves attenuation resting entirely on what the subject token was
> granted. Phase 1 needs per-identity client scopes to mean anything.
>
> **Leg 1 needs one change on [#5815](https://github.com/stacklok/toolhive/issues/5815).**
> That issue requires an actor token to be self-issued — which two legs satisfies, since the
> server mints it — and additionally requires `actor_token.sub` to equal the authenticated
> `client_id`. Here `sub` is the definition and the client is the pod, so the check still
> rejects. The replay attack it defends against is real: a leaked actor token must not be
> usable by a different client. But `cnf` defends it better. Leg 1's token is already bound
> to the pod's key, so checking the binding against the presenting client is both stronger
> than subject equality — a leaked token is useless without the key, not merely
> attributable — and frees `sub` to name the agent. That is the ask: keep self-issued, bind
> by `cnf` rather than by `sub`.

---

### Hop 5 — the gate

```cedar
permit ( principal, action == Action::"call_tool", resource )
when {
    context.claim_act.sub == "spiffe://mecatl.example.com/agent/code-reviewer" &&
    resource.readOnlyHint == true &&
    context.claimset_scope.contains("repo:read")
};
```

**The rule names no backend.** Policy naming backends couples it to deployment topology and
works against the gateway presenting as an opaque toolset. The resolved target carries a
credential selector, but as something the fetch consumes rather than something policy reasons
over. A deployment wanting backend-level rules can have them; the design does not depend on
it.

**One gate rather than one per outbound path.** A check that lives in each path can be left
out of one of them, and nothing reveals the omission until someone looks. A single gate can
be wrong, but it cannot be missing from a path that has only it.

**Set membership, not substring.** Claims reach Cedar twice over: `claim_scope` is a
space-delimited string, so `like "*repo:read*"` would also match `repo:readwrite`, while
`claimset_scope` is a set with exact-element matching. Use the set. It requires naming `scope`
in the authorizer's multi-valued claims, and a rule referencing a set that was never built
errors and denies — refuse-on-a-missing-input, working as designed.

**The scope test is the only comparison policy makes** — one axis, not a subset algorithm
over a schema, because definition and operation are named directly. In [later
phases](#later-phases) this becomes containment over the resolved target, which is what lets
the rule constrain *which* repository.

> **Today.** Admission is the single gate and runs before routing; the HTTP authz middleware
> is vestigial. `act` reaches policy as a nested claim.
>
> Three gaps. **The backend reaches the admission seam and is dropped before policy** — the
> tool carries it, the seam receives it, and only the name goes onward; a call to an
> unadvertised name gets a synthesised tool with no backend at all. **The read/write hint is
> absent by default** with no classifier to derive one, so a rule omitting `== true` silently
> permits unannotated tools — though per-tool operator overrides already exist in config,
> which is a cheaper lever than a new policy default. And **the resolved target does not
> exist as a value** for policy to compare against.
>
> One thing that is not simply a gap. When a primary upstream provider is pinned, the issued
> token's claims are deliberately not a claim source: in a multi-upstream chain the presented
> token's name and email belong to the first configured upstream, and using them would
> attribute one provider's identity to another. So `act` vanishes there by design. Restoring
> it reverses a provenance decision rather than fixing a bug, and needs arguing on those
> terms. An opaque upstream token falls back to request claims with a warning, so `act` does
> survive on some providers.

---

### Hop 6 — credential resolution

This doc does not say how vMCP stores credentials. It requires two things of the read.

**Read one credential, and read it only after the gate has allowed the call.** The gateway
allowed a specific call against a specific target. The credential it reads is the one that
target needs. If the target were worked out a second time after the decision, the credential
could belong to a backend the gate never saw.

This is where the scope limit either applies or does not. Leg 2 issued a token confined to
`repo:read`. If the gateway loads every credential the user has and sends GitHub a full-scope
token, that limit changed nothing about what GitHub was asked to do.

**The key is the user, not the token session id.** vMCP keys stored credentials on a `tsid`
claim — a login-session pointer. An agent can never carry one, for two independent reasons,
either of which alone is fatal: the claim is minted at the start of an authorization-code
flow, so a browser login produces one and an agent never walks that path; and the
token-exchange handler passes an empty session link, so any inherited one is dropped.

That gives a choice with no third option:

| | `tsid` | actor | credential lookup |
|---|---|---|---|
| Forward the user's token unchanged | present | **lost** | works |
| Exchange it for a delegated token | **dropped** | present | **dead** |

You get the actor or the `tsid` lookup, never both. This design needs the actor, so the read
keys on the user. The enterprise deployment already keys on the user, for its own reasons.

That is a consequence, not a choice. `tsid`-keyed credential injection cannot serve agents in
OSS vMCP at all, and [#5194](https://github.com/stacklok/toolhive/issues/5194) ends it for
everyone as a side effect of adding the actor.

> **Today.** Authentication middleware validates the token, takes the session pointer out of
> it, and loads **every** credential stored under that pointer into a map — before the
> JSON-RPC body is parsed, so nothing there knows what is being called. The code says as
> much: a per-backend check would need routing context this layer does not have. Afterwards
> the outbound strategy indexes that map by a provider name from static configuration.
>
> So a subagent narrowed to one repository can trigger a call to another service and nothing
> in the credential path objects, because by then that credential is already loaded.
>
> And when the `tsid` claim is absent the loader returns no credentials and **no error**. An
> agent presenting a delegated token therefore gets an empty map rather than a refusal, and
> what happens next is whatever the outbound strategy does with one — which differs per
> strategy and is nowhere stated as a contract.
>
> **Change.** Move the read to where the target is known, and key it on the user. What that
> takes is in [the work](#the-work).

**Other systems read credentials against a target they already know.** RFC 8693 requires an
exchange to carry `resource` or `audience`, so the request cannot be built before the target
is known. Envoy runs external authorization after route matching, and treats a later filter
changing the route as a privilege-escalation bug, because the decision then applied to a
different destination than the one served. Loading everything up front and picking later is
the same mistake: the component that picks is not the component that decided.

---

### Hop 7 — the backend

The backend receives a credential it already understands, for a call already authorized, and
learns nothing about agents. That is a goal: a provider has no policy about mecatl's
subagents it could apply.

It is also where the narrowing stops. The credential is whatever the user granted at connect
time, so GitHub applies its own limits and none of ours. Every constraint this design adds
sits upstream of the only party that could enforce one against the actual API call — see
[later phases](#later-phases).

**Where the chain stops.** RFC 8693 §2.1 is explicit that an exchange "is a one-time event
and does not create a tight linkage between the input and output tokens", so a backend cannot
walk it back. Verifiability is scoped to **the gateway** — the only place where the claimed
authority and the credential are both visible, and the only hop where refusing prevents
anything.

> **Today, with one strategy that breaks the rule.** Four of five outbound strategies derive
> a credential without reading the inbound claims. **The AWS STS strategy reads them, and the
> read is authority-bearing**: the inbound token's claims select which IAM role the outbound
> credential assumes, and it hard-fails when claims are absent. Two strategies also forward
> the raw inbound token as the subject token when no provider is pinned.
>
> So this is not uniformly a boundary where nothing of ours crosses, and an earlier version
> of this document asserting "no change needed" here was wrong. On that path a discarded or
> forged actor claim changes the outbound authority directly, which ties it straight to the
> claims-provenance behaviour in hop 5. Any design for `act` has to account for a consumer
> that already authorizes on claims.

---

### Hop 8 — resume

The credential expires; the authority does not. On resume it is re-derived, never wider.

**Prefer the live caller.** Someone who just authenticated is a stronger statement than a
stored row. That covers more paths than it seems: approve-after-restart arrives on an inbound
request, a resumed subagent runs under a live parent turn, a background child is run-scoped.
The only case with nobody present is a scheduled fire, which has its own grant.

**Authority must not come from a row anything can write.** If the stored record is the
authority, whatever writes the store grants authority.

> **Today.** Sessions park and resume on another pod when a shared store is configured —
> flag-gated, with the default falling back to in-memory even in the Kubernetes binary.
> Persisted state is plain JSON with no signature or MAC.
>
> The posture ladder is **not** session state and is not restored; it is applied at build
> time. What is persisted and restored verbatim is the session mode, one value of which
> relaxes edit prompting. So a permission-relevant label is trusted verbatim — the posture
> ladder is simply not that label.

---

## Scheduled execution

Alice says "check CI at 3am and fix what is broken." Two kinds of schedule, separate types,
because the failure mode is one silently becoming the other.

| | User-delegated | Service-owned |
|---|---|---|
| Owner | Alice | a service or admin principal |
| Subject | Alice | none |
| Actor | schedule + definition | schedule + definition |
| Holder | firing pod | firing pod |
| Grant | offline grant scoped to this schedule | client credentials |

**A user-delegated schedule runs as Alice.** The work is hers. A run acting as itself loses
her, which is one of the two bad options the delegation epic exists to avoid.

**That requires capturing offline access when the schedule is created.** Nothing can mint a
credential naming Alice from nothing at 3am, and no specification offers a way — every shipped system either
replays something captured at consent time or degrades to a service identity. The agent still
holds nothing of hers: the refresh token lives in the credential store, scoped to one user and one
provider, revocable. That is *safer* than the alternative that avoids storage, since a system
able to mint a user's credential at will is more dangerous than one holding a token that can
be taken away.

**Consent must happen outside anything the model wrote.** Scheduling is model-facing, so a
prompt-injected agent can create recurring work, and "captured with consent" means nothing if
the model can cause the consent. Creation produces a pending authorization; a separate
interaction the model cannot author confirms account, resources, cadence and an absolute
expiry.

**The schedule stores a signed envelope, not a token.** Verified before every fire, so
mutating the row without re-signing invalidates it, and widening needs fresh consent.

**Refresh is bounded by the schedule, not the token.** Refresh-on-use lets an unsupervised
agent extend a credential forever; refresh-on-login stops a working schedule for invisible
reasons. A schedule has an expiry its owner set, and its grant refreshes only while it lives.

**When the grant is gone, fail and surface reauthorization.** Never fall back to a service
identity — that converts Alice's job into somebody else's and makes the audit record false.

---

## What an adversary gets

| Adversary | Can | Cannot | Caught at |
|---|---|---|---|
| Prompt-injected subagent | choose tool and arguments within the credential's authority, which for now is everything the user granted the provider | change whose authority is presented, which definition is named, or what it permits — fixed before it ran | the gate |
| Prompt-injected parent | choose a child's authority up to its own | exceed its own; a credential-carried ceiling bounds what it hands out | mint |
| Stolen access token | attempt replay | use it without the pod's certificate | the gateway |
| Store writer, no signing key | rewrite mutable rows | forge a schedule envelope, or obtain an agent token — that needs the pod's key and a registration | verification |
| Compromised gateway | use stored credentials; alter its own audit | be distinguished from Alice by the backend | reconciliation with mecatl's log |
| Compromised pod | anything the pod may do; lie about which goroutine acted | — | accepted boundary |

**What this does not protect against.** Providers cannot verify the agent chain in the stored-credential
path. Correlation gives operational attribution, not cryptographic proof. A compromised
gateway can both abuse credentials and rewrite its own record of doing so. A compromised pod
sits inside the accepted workload boundary.

One rule adopted verbatim from the credential-broker draft: *the PDP must not evaluate
justification text for approval decisions.* Agent-authored prose must never influence a
decision — in an agent deployment that is the whole injection surface.

---

## Interfaces

Every output is the next input. Where it is not, that is a finding.

```
Hop 1   BindPrincipal(inbound)                     -> PrincipalID
        OwnerOf(schedule)                          -> PrincipalID
        AuthorizeObject(principal, object, action) -> Decision
        RootAuthority(principal)                   -> Authority

Hop 2   Narrow(parent Authority, spawn)            -> (Authority, Limits)

Hop 3   AgentToken(clientSVID, definition)         -> ActorToken   // leg 1, AS-issued
        Exchange(subjectToken, actorToken,
                 clientSVID, authority, resource)  -> AccessToken  // leg 2
        VerifyScheduleGrant(schedule)              -> OfflineGrant

Hop 4/5 Verify(accessToken, holderProof)           -> Claims
        Resolve(call)                              -> Target
        Decide(claims, target)                     -> Decision

Hop 6   Fetch(subject, target.selector)            -> StoredCredential

Hop 8   Rederive(prior Authority,
                 livePrincipal | offlineGrant)     -> AccessToken
```

`Fetch` takes no decision: the call only arrives if the gate allowed it, and target binding
means it allowed *this* target. `Narrow` returns two values because only the first travels.
`Verify` is a step of its own because issuer, audience, expiry, algorithm and holder binding
must all be checked before any claim reaches policy. `Correlation` appears in no signature —
logged, never read by a decision, which is the point.

---

## The work

Status verified against code except where marked unknown.

| Capability | Owner | State | Blocks |
|---|---|---|---|
| Provisionable confidential client | ToolHive | **missing** — registration hardcodes public clients, and discovery advertises neither the grant nor secret auth ([#6082](https://github.com/stacklok/toolhive/issues/6082)) | everything |
| External-issuer subject tokens | ToolHive | partial — validator landed ([#5814](https://github.com/stacklok/toolhive/issues/5814)) but unwired; consent model open ([#5989](https://github.com/stacklok/toolhive/issues/5989)) | any real IdP |
| Authority as a runtime value | mecatl | **missing** — tool sets are static per definition at build time; nothing reads a parent's current authority | narrowing, and anything the gate can enforce |
| Canonical user principal | mecatl | missing | multi-user anything |
| Owner enforcement on every object operation | mecatl | missing — listing takes no principal | shared deployment |
| Agent token (leg 1) | ToolHive | proposed on [#5815](https://github.com/stacklok/toolhive/issues/5815), which binds `actor_token.sub` to `client_id` and so rejects a token naming the definition. Needs `cnf` binding instead. mecatl side is a call, not a signing key | a trustworthy actor claim |
| `client_credentials` grant | ToolHive | missing — the server composes authorization-code, refresh, PKCE and token-exchange only | leg 1 |
| Definition registration at the AS | ToolHive + operator | missing — nothing registers which definitions a client may be issued a token for | leg 1 meaning anything |
| Client authentication without a secret | ToolHive + deployment | prior art exists, unmerged; carried as an option on [#6082](https://github.com/stacklok/toolhive/issues/6082). Which of the three SPIFFE methods is undecided | the exchange |
| Sender-bound tokens | ToolHive | missing, **no tracker**; method undecided (mTLS binding or DPoP) | replay resistance |
| Resolved target as a value | ToolHive | partial — backend reaches the admission seam, dropped before policy | target binding |
| Post-admission credential fetch | ToolHive | **missing** — the load happens in auth middleware | least privilege |
| User-keyed credential read | ToolHive + enterprise | partial — see below | agents using stored credentials |
| Credential-ownership recheck | ToolHive | missing — the error is declared and returned by no implementation | multi-user safety |
| Upstream subject on the identity | ToolHive | missing ([#6053](https://github.com/stacklok/toolhive/issues/6053)) — so an OAuth-only upstream cannot produce a correct Cedar principal | policy naming the user |
| Signed schedule grant | undecided | unknown | unattended work |
| Token cache | ToolHive | exists, unwired — zero importers | fan-out cost |
| Resource-level authority | ToolHive | missing, **no tracker** — `authorization_details` appears nowhere. Later phase | constraining a call to one repository |

### Order

**Agree the shared contract first** — principal, agent token, authority, target,
selector, trust relationships. Neither repository should invent these separately.

**Then the two blocking gaps, in parallel.** ToolHive: a provisionable confidential client.
mecatl: authority as a runtime value, which gates everything on that side because there is
nothing to narrow or carry until it exists.

**Then local correctness, still in parallel.** mecatl adds canonical ownership and object
authorization. vMCP resolves one target, passes it to policy, preserves verified claims, and
treats absent metadata as mutating.

**Then the exchange, leg 1 before leg 2.** Client authentication from the SVID, a
registration saying which definitions a client may be issued a token for, and agent tokens
bound by `cnf`. Leg 2 then adds subject validation and the scope intersection. Leg 1 is
testable on its own — an unregistered definition must be refused — and leg 2 cannot be built
before it, since the agent token is its actor input.

**Then move the credential read behind the gate**, rechecking ownership at the read. This is
two changes and an interface addition, not a plugin — see below.

**Then prove one slice:** Alice, one code-reviewer, one GitHub read tool. Complete when a
*write* call is denied before any credential is read — not when the exchange succeeds.
Denying a call to a different repository is the phase-2 proof; phase 1 cannot express it,
which is the honest cost of deferring resource-level authority.

### What has to be true of the credential read

Six statements about the resulting logic in vMCP. They are what hop 6 requires; how they are
built is the next section.

1. The read takes a user and a resolved target, and returns at most one credential.
2. It runs after admission allowed the call, and uses the same target admission decided on.
3. The user comes from the subject of the verified token, not from a session pointer and not
   from a header.
4. A credential stored for one user is never returned for another. Checked at the read, not
   assumed from the key.
5. A refused call reads no credential at all.
6. A missing credential fails the call. The backend is never called without one.

Today none of the six holds. The read takes a session id and returns everything under it, it
runs in auth middleware before the body is parsed, the ownership check exists as a declared
error that no implementation returns, and an absent session id yields an empty map and no
error, so what reaches the backend depends on which outbound strategy runs.

### Getting there

**Statements 1 and 3 are an interface change.** `upstreamtoken.TokenReader` has one method: it
takes a session id and returns every credential for that session. Nothing takes a user and a
target. That method has to exist before anything else can be arranged around it.

**Statement 4 is the storage layer**, and it is the piece the enterprise deployment has
already built — a decorator that keys tokens by user. Reusable as-is for what it does. Its
authors' warning applies: how a session maps to a user has to be settled first, or re-keying
can reintroduce a cross-user path.

**Statement 2 is the one that needs a decision.** Moving the call from auth middleware to
after admission satisfies it, and leaves nothing preventing a second call site from being
added later that reads early again. That is how the current behaviour arose — the read was put
where the token was validated, which was reasonable in isolation.

The cheap way to make the ordering hold is to make the resolved target a value that only
admission can produce. If the read's signature demands one, a caller in auth middleware has
nothing to pass and the early read stops being expressible. No new machinery, and it converts
target binding from a rule someone has to remember into one the compiler applies.

**Statements 5 and 6 follow from where the read sits**, once it sits there. A refused call
returns before reaching it, and a read that returns nothing has no map to fall through — the
call fails instead of continuing with whatever the strategy makes of an empty result.

### Later phases

Each entry says what would un-defer it. A deferral with no stated trigger is a decision nobody
revisits.

**Resource-level authority.** RFC 9396 `authorization_details` carries the structured fields
that name a resource, so hop 5 tests containment over the resolved target rather than a scope
string, and the residual stated in hop 3 closes. Deferred because nothing in vMCP implements
it, because §6.1 leaves the comparison of two authorization-detail requests unspecified so the
narrowing rule would be ours to define rather than adopt, and because phase 1 is provable
without it.

*Un-defers when* a deployment needs one agent confined to a subset of a backend that another
agent may reach in full. Scope cannot express that.

**Per-agent credential isolation.** Three ways to stop agents sharing one credential. They are
alternatives, not steps — a deployment picks one — and they differ in who decides.

| | Who decides | Where the agent binds | Have it |
|---|---|---|---|
| Gate a shared credential | the operator, in policy | `act.sub`, tested at hop 5 | **yes** |
| Narrow what the provider sees | the operator, per backend | the credential handed to GitHub | no |
| One credential per agent | the user, at connect time | the stored credential | no |

The first is already here. An operator can say which agents may call which tools against a
credential all of them share.

The second is the gap worth naming plainly: **this design narrows every token except the one
that reaches the provider.** Leg 2 narrows what mecatl presents to vMCP, and hop 5 decides what
mecatl may ask for — but GitHub receives whatever Alice granted at connect time. Every
constraint sits upstream of the only party that could enforce one.

The third is where AWS and Azure both are. Bedrock AgentCore stores vault entries under the
agent identity and the user together, so a token is scoped to that pair. Entra Agent ID has no
shared vault: the agent identity is a service principal with its own delegated permissions,
consented to per agent. Atrium reaches the same place and calls the consent record a
`ConnectorBinding`. One credential per user is where we stop, not a property of the model.

*Un-defers when* two conditions hold together: a second agent that needs different authority
from its siblings, and a record of the user consenting to a named agent. The second is the
hard one. Without it there is nothing to check, and a per-agent storage key would partition
credentials by an agent nobody authorized.

**Schedules, caching and stronger attribution.** *Un-defers when* unattended work is a
requirement, when fan-out makes the per-call exchange measurable, and when an auditor needs to
attribute a call to an instance rather than a definition.

---

## Deployment requirements

What this needs from how the system is run rather than from its code. A runbook item is
weaker than a product guarantee, and it is worth being explicit about which these are.

| Requirement | Provider | Consumer | Failure |
|---|---|---|---|
| Workload API reachable at startup | SPIFFE deployment | mecatl | no credential for any session |
| A registration naming every definition that may be acted as | operator | vMCP AS | leg 1 refuses, and the agent cannot act at all |
| AS issuer metadata and keys | vMCP AS | vMCP gateway | every call fails verification |
| A client authentication method the deployment can carry — mTLS needs TLS terminating at the AS or the ingress forwarding the certificate; a JWT-SVID assertion needs neither | deployment | AS | client authentication impossible; reads as configuration, is topology |
| An mTLS path to the gateway, if holder binding is by certificate | deployment | vMCP gateway | `cnf` cannot be checked, and the token is a bearer token in practice |
| An authorization policy wherever credentials are configured | operator | vMCP admission | every authenticated caller gets every credential |
| `scope` listed in the authorizer's multi-valued claims | operator | vMCP admission | the set form is absent, and a rule naming it denies every call |
| One credential per resolved target | configuration | vMCP | an allow permits a class and the fetch picks a member, silently |
| Authenticated, encrypted state transport | deployment | mecatl | signed objects still required; state alone grants nothing |

**The gateway needs the authorization server's keys.** Both tokens are minted by that
server, and with two legs mecatl signs nothing, so there is no second trust relationship to
conflate with the first. An earlier version of this document had mecatl self-signing the actor
assertion and therefore owed the server a trust bundle; splitting the exchange removed it.

---

## Open questions

1. Which SPIFFE client authentication method: JWT-SVID assertion, X.509-SVID over mTLS, or
   WIT-SVID. The deployment cost differs; the standard requires only one of them.
2. Whether the holder is bound by certificate thumbprint or DPoP, which is a separate choice
   from the one above and lands on a different connection.
3. Whether [#5815](https://github.com/stacklok/toolhive/issues/5815) will bind the agent token
   by `cnf` rather than by subject equality. Two legs depends on it, and nothing else in this
   design substitutes.
4. Whether leg 1's `sub` is the agent or the pod. RFC 9068 §2.2 says a client-credentials
   token SHOULD carry the client; naming the agent is what makes `act` legible to policy.
5. Definition-based or instance-based external authorization.
6. Whether per-agent consent is a product requirement, and if so who holds the record. Until
   one exists, per-agent credentials cannot be built and the user has no say in which agents
   use her connections.
7. The exact credential selector, and how uniqueness is enforced.
8. Whether the access token is a profiled JWT or opaque plus introspection.
9. Whether signed per-call instance attribution is a product requirement.
10. How ownerless legacy sessions and schedules are handled.
11. Who owns the schedule grant broker.

---

## References

- **RFC 8693** — §1.1 delegation versus impersonation; §2.1 an exchange creates no linkage
  between input and output tokens; §4.1 the closed set of top-level claims plus the current
  actor, and `act` contents restricted to identity.
- **RFC 9396** — §2.2 the field model, and that fields within one object combine as a
  product; §6.1 no standardized way to compare two authorization detail requests. Relevant to
  later phases, not phase 1.
- **RFC 8707** the `resource` parameter, which binds the issued token to one resource server.
- **RFC 8705** §3 certificate-bound tokens. **RFC 9449** DPoP, the alternative holder binding
  where mTLS is not available.
- **`draft-ietf-oauth-spiffe-client-auth`** — §3.1 JWT-SVID as a client assertion, §3.2
  X.509-SVID over mTLS, §3.3 WIT-SVID; §4 an authorization server must support at least one,
  which is why mTLS is a deployment choice rather than a requirement. Leg 1 is §3.1.2's
  example verbatim; the mTLS variant is §3.2.1.
- **RFC 6749** §4.4 the client-credentials grant. **RFC 9068** §2.2 on what `sub` carries in
  a token issued by it.
- **`draft-ietf-wimse-arch`** §2 a workload is independently addressable and executable, which
  is why a goroutine is not one; §4.5 avoid treating authentication as implicit authorization.
- **`draft-hartman-credential-broker-4-agents`** §4.2 the justification-text rule, adopted
  verbatim.

Material read but not relied on is recorded in the review notes rather than here.
