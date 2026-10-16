# Credentials and lifecycle

*This chapter describes the intended architecture, not current implementation status.*

An agent instance can participate in multiple runs. Its instance credential identifies it to the broker; presenting that credential does not by itself authorize an MCP tool call. The harness manages run execution. The broker checks the session and execution context. Once the [authority work](README.md#order-of-work) is in place, it also checks the acting instance's effective server/tool ceiling, its definition ceiling narrowed by its parent's, with Cedar. User, resource, argument, and dynamic delegation policies are deferred. Renewing the instance credential does not itself extend or widen permission to call tools.

The human enters the flow once, when Alice signs in through AS-1. AS-1 runs that sign-in itself, so every later statement about the human traces back to a login AS-1 witnessed, not to a report from the harness.

## What the user sees

- **Sign in** once per login session, through AS-1 to the company IdP; usually silent with single sign-on.
- **Connect a provider** once, through the existing `mcp_authorization_pending` path. In external mode, the same path first shows the gateway's "Allow Mecatl" step.
- **Everyday use** needs nothing more until the broker refresh token's hard expiry or a revocation. Scheduled runs draw on their own [authority](#scheduled-runs).
- **Sign out** ends one client's tokens; **sign out everywhere** stops every agent and schedule acting for the user. See [sign-out and revocation](#sign-out-and-revocation).

## Credential roles

| Credential | What it establishes | Issuer → holder → verifier |
| --- | --- | --- |
| **AS-1 access token** | Alice's identity for requests to the Mecatl API. | AS-1 → Alice's client (TUI or Studio) → mecak8s. Passed to the broker once, at session open. |
| **Client refresh token** | The client's ability to renew its own AS-1 access token. | AS-1 → client only. Never leaves the client. |
| **Workload credential** | Which Mecatl service contacts the broker and agent-instance issuer. | Platform → mecak8s → broker and issuer. It identifies neither Alice nor an agent instance. |
| **Agent-instance JWT-SVID** | Which registered agent instance is acting. It can span multiple runs. | Agent-instance issuer → harness → broker, which forwards it to AS-1 as `actor_token`. |
| **Broker refresh token** | Alice's delegated authority for one broker session, usable without her present. | AS-1 → broker → AS-1. Bound to client `mecatl`, the broker session, and the user. Accepted only as the subject of an ID-JAG exchange. |
| **Broker client credential** | That the caller at AS-1 and AS-2 is the broker, OAuth client `mecatl`. | Broker's private key; each request carries a short-lived `private_key_jwt` assertion verified by AS-1 and AS-2. |
| **ID-JAG** | Alice and the acting agent, for one redemption at AS-2. | AS-1 → broker → AS-2. Short-lived, single-use. |
| **AS-2 access token (`V`)** | AS-2's native Alice, the acting agent, and client `mecatl`, for the vMCP resource. | AS-2 → broker → vMCP. Cached per session, instance, gateway, and user. |
| **Upstream provider credential** | Access to Alice's GitHub account. | GitHub → gateway credential store → GitHub. Never leaves the gateway. |
| **Signing keys** | Authority to issue credentials. | AS-1, AS-2, and the agent-instance issuer each keep their own key. Separate keys are not process isolation when components share a process. |

### Example nomenclature

| Kind | Name | Meaning |
| --- | --- | --- |
| Identity | Company Alice `(https://login.example.com, alice-204)` | Alice at the company IdP. AS-1 represents her as subject `u-alice-91`; AS-2 as native user `usr-alice-7c1d`. |
| Identity | `mecatl` | The broker's OAuth client at AS-1 and AS-2. Not a user or an agent. |
| State | `hostS42`, `A42` | Host session ID, and prose alias for the existing opaque `BrokerSessionRef`. Neither is a credential. |
| State | `main`, `reviewer`, `R41`, `R42`, `D42` | Registered agent instances, their runs, and the delegation from main to reviewer. |
| Reference | `op-list-prs-001`, `op-pr42-001` | Correlation IDs for two tool operations. They identify operations, not people, and authorize nothing. |
| Credential | `mainSVID`, `reviewerSVID` | Registered agent-instance credentials. |
| Credential | `RT-A42` | The broker refresh token for session `A42`. |
| Credential | `J-main`, `J-reviewer` | ID-JAGs naming Alice and each acting agent. |
| Credential | `V-main`, `V-reviewer` | AS-2 access tokens naming native Alice and each acting agent. |

No credential value belongs in the model's context.

## Interactive execution

Alice owns harness session `hostS42`. She asks Mecatl to list pull requests in `acme/payments` and review PR 42. Main run `R41` calls `pull_request_list`. After the list returns, main delegates the review through `D42` to reviewer run `R42`, which calls `pull_request_read`. Tool names and arguments are illustrative. The reviewer uses definition `project/code-reviewer`; main both creates it and delegates the task to it, but creation and delegation remain separate facts. In the diagram, `main` and `reviewer` abbreviate the SPIFFE subjects in [example instance identities](#example-instance-identities).

The gateway in this example is external, at `https://gateway.example.com`. Embedded mode follows the same steps with AS-2 and vMCP inside the Mecatl deployment.

```mermaid
sequenceDiagram
    actor U as Alice (TUI / Studio)
    participant H as mecak8s
    box Broker
        participant L as Definition loader
        participant I as Agent-instance issuer
        participant B as Broker core
        participant A1 as AS-1
    end
    participant IdP as Company IdP
    box Gateway
        participant A2 as AS-2
        participant V as vMCP
    end
    participant G as GitHub

    Note over L,B: (0) Broker startup
    L->>L: Load global definitions from configured sources
    L->>B: Definition records, sources, revisions, root eligibility<br/>(ceilings with the authority work)

    Note over U,IdP: (1) Sign-in, once per login session
    U->>A1: Authorization code + PKCE<br/>(issuer from mecak8s protected-resource metadata)
    A1->>IdP: Redirect to company sign-in<br/>(AS-1 is an ordinary OIDC client there)
    IdP-->>A1: Alice authenticated
    A1-->>U: AS-1 access token (aud = Mecatl API) + client refresh token

    Note over U,A1: (2)(3) Session open, on the session's first request
    U->>H: PR review request + AS-1 access token
    H->>H: Validate token, check ownership of hostS42
    H->>B: OpenSession: workload JWT + AS-1 access token<br/>+ owner of hostS42 (once)
    B->>A1: RFC 8693: subject_token = AS-1 access token<br/>requested_token_type = refresh_token<br/>authorization_details: session A42, owner<br/>(5) client mecatl, private_key_jwt
    A1->>A1: Token issued by AS-1 for the Mecatl API, active, jti not yet converted<br/>sub_id = reported owner, A42 unowned or owned by the same user
    A1-->>B: RT-A42 (bound to mecatl, A42, Alice, hard expiry, no rotation)
    B-->>H: SessionSnapshot (BrokerSessionRef A42)

    rect rgb(245, 245, 245)
        Note over H,I: (4) Main bootstrap, before R41
        H->>I: registerInstance(main, definition reference), workload JWT
        I->>I: Definition loaded, no parent, so it must be root-eligible
        I-->>H: mainRef + canonical main subject
        H->>I: requestSVID(mainRef), workload JWT
        I-->>H: mainSVID
    end

    Note over H,G: (6) Main call: pull_request_list
    H->>B: InvokeTool: workload JWT, A42, run R41, run owner Alice,<br/>op-list-prs-001, agentJwtSvid = mainSVID
    B->>B: Check session, workload, actor, run owner = grant subject, route<br/>(ceiling with the authority work)
    opt No cached V for (A42, main, gateway, Alice)
        B->>A1: (7) RFC 8693: subject_token = RT-A42<br/>actor_token = mainSVID<br/>requested_token_type = id-jag, audience = AS-2
        A1->>A1: RT-A42 bound to mecatl, session open, hard expiry not passed<br/>upstream IdP re-checked if due<br/>main registered to A42, build act (iss = AS-1)
        A1-->>B: J-main
        B->>A2: (8) RFC 7523: assertion = J-main, client mecatl
        A2->>A2: Map AS-1 subject to native user<br/>External mode: require consent link
        A2-->>B: V-main (sub usr-alice-7c1d, act main, aud vMCP)
    end
    B->>V: (9) tools/call pull_request_list + Bearer V-main
    V->>V: Validate V-main<br/>Load credential (gateway, usr-alice-7c1d, github)
    V->>G: List PRs with Alice's GitHub token
    G-->>V: Result
    V-->>B: Result
    B-->>H: Sanitized outcome

    rect rgb(245, 245, 245)
        Note over H,I: Reviewer bootstrap, after the list succeeds
        H->>H: Delegate review: D42 / R42
        H->>I: registerInstance(reviewer, definition reference project/code-reviewer,<br/>parentRef = mainRef), workload JWT
        I-->>H: reviewerRef
        H->>B: Report delegation D42: R41 to R42, run owner Alice
        B->>B: Child run owner = parent run owner, parent in the same session tree
        H->>I: requestSVID(reviewerRef), workload JWT
        I-->>H: reviewerSVID
    end

    H->>B: InvokeTool: A42, run R42, run owner Alice, op-pr42-001,<br/>agentJwtSvid = reviewerSVID
    Note over B,G: Same checks and cache-miss path with actor_token = reviewerSVID<br/>J-reviewer and V-reviewer: act = reviewer
    B-->>H: Sanitized outcome
    H-->>U: Review outcome
```

The diagram follows the successful path. Other outcomes:

- **No upstream credential.** vMCP finds no stored GitHub credential for Alice and answers "needs authorization". The broker returns the existing `mcp_authorization_pending` outcome; the TUI or Studio opens the authorization URL and rechecks, and the parked run resumes.
- **No consent link (external mode).** AS-2 refuses to redeem the ID-JAG. The broker returns the same pending outcome, and the URL first leads through AS-2's "Allow Mecatl" step.
- **Denial or failure.** Any denial before dispatch prevents the provider call. Retrying requires authorization again.
- **Unknown outcome.** If dispatch may have happened but no completion was observed, the outcome is unknown and is not replayed automatically.

Every later tool call repeats the broker checks, including calls served from the `V` cache.

### (1) AS-1 access token

AS-1 is Mecatl's login authorization server. mecak8s's protected-resource metadata names AS-1, and the TUI and Studio already discover their issuer there, so neither client changes. AS-1 does not hold passwords: it redirects Alice to the company IdP as an ordinary OIDC client. The company IdP administrator registers one web application, AS-1, with AS-1's callback as redirect URI.

The client receives an access token for the Mecatl API and keeps its own refresh token: the TUI in the OS keyring, Studio in sealed server-side cookies. A fictional decoded access token:

```json
{
  "typ": "at+jwt",
  "iss": "https://broker.example.com/as",
  "sub": "u-alice-91",
  "sub_id": {"format": "iss_sub", "iss": "https://login.example.com", "sub": "alice-204"},
  "aud": "https://mecatl.example.com/api",
  "client_id": "mecatui",
  "iat": 1799999900,
  "exp": 1800000800,
  "jti": "at-alice-7731"
}
```

mecak8s validates it as a JWT access token (RFC 9068): `typ` is `at+jwt`, `iss` is AS-1, the signature verifies against AS-1's keys with an allowed algorithm, `exp` has not passed, `aud` is the Mecatl API, and `client_id` is the TUI or Studio. A failure returns 401 `invalid_token` (RFC 6750). It then checks that the company identity in `sub_id` owns `hostS42`; a failure there returns 403. An `id_token` is not accepted here: its audience is the client, not the API.

Session ownership is keyed by the company identity `(https://login.example.com, alice-204)` that AS-1 carries in `sub_id`, not by AS-1's own `(iss, sub)`. Existing owners keep matching without migration, and the owner stays Alice's stable human identity. AS-1 must always set `sub_id`; mecak8s rejects an AS-1 token without it. The company IdP sees only AS-1 as the application: per-application IdP policies can no longer distinguish the TUI from Studio.

**Evidence:** mecak8s accepted request `req-pr42-001` from owner `(https://login.example.com, alice-204)`, authenticated by AS-1 as `u-alice-91`, for `hostS42`. This records API authentication and ownership only.

### (2) Workload credential

Kubernetes supplies mecak8s with a projected ServiceAccount token. mecak8s presents it to the broker and agent-instance issuer over TLS, outside model context:

```json
{
  "iss": "https://kubernetes.example.com",
  "sub": "system:serviceaccount:mecatl:mecak8s",
  "aud": ["https://broker.example.com"],
  "iat": 1799999900,
  "exp": 1800003500,
  "kubernetes.io": {
    "namespace": "mecatl",
    "serviceaccount": {"name": "mecak8s", "uid": "sa-example-42"},
    "pod": {"name": "mecak8s-worker-7", "uid": "pod-example-7"}
  }
}
```

The token is short-lived and has a broker-specific audience; tokens with the Kubernetes API server's audience are rejected. The broker validates it offline against the cluster's published keys: signature, issuer, allowed algorithm, broker audience, and expiry, then checks that the ServiceAccount may contact it. This proves the token was issued for that ServiceAccount, not that the presenter is that pod, and offline validation does not notice a deleted pod before `exp`; short lifetimes bound that. It identifies the mecak8s service, not Alice or an agent instance. [SPIRE alternatives](#workload-authentication) replace this credential without changing its role.

### (3) Session open and the broker refresh token

At `OpenSession`, mecak8s passes Alice's current AS-1 access token to the broker once, alongside workload authentication. This replaces the harness reporting `human {issuer, subject}` as plain data: the broker now holds a token AS-1 signed for Alice's client, not mecak8s's statement about her.

The broker exchanges that token at AS-1 with an RFC 8693 token exchange, authenticating as client `mecatl`, and asks for a refresh token. The request names the broker session `A42` and the owner mecak8s holds for `hostS42` in an RFC 9396 `authorization_details` entry of type `mecatl_session`, because RFC 8693 has no standard parameter for either. AS-1 accepts only a token it issued for the Mecatl API that is still active, presented by `mecatl`, converted at most once, and asking for no wider scope than the original grant. One conversion per `jti` stops a leaked access token from opening a second session. A retry of the same open, with the same `jti`, client, and broker session, returns the existing refresh token, so a lost response does not leave the session unopenable; clients refresh their access token before each new session open. The [implementation companion](implementation.md#as-1-grant-and-as-2-user-resolution) lists the exact checks.

AS-1 then issues `RT-A42`. The refresh token is opaque to the broker; its meaning lives in an AS-1 record that binds it to client `mecatl`, to Alice as `u-alice-91` and her company identity as the session owner, and to session `A42`, with a hard expiry. Every ID-JAG issued from it names that subject. AS-1 accepts `RT-A42` only as the subject token of an ID-JAG exchange; presented at the refresh-token grant, it returns `invalid_grant`, so the broker cannot mint Mecatl API tokens as Alice. `RT-A42` does not rotate: client authentication already binds it to the broker (RFC 9700 §4.14.2, RFC 9449 §5), and rotation would make concurrent exchanges from several broker replicas look like token reuse.

The broker supplies `A42` when the record is created, so the session binding is broker-asserted. Only mecak8s checks that Alice owns `hostS42`, so a mecak8s bug could pair her token with another user's session. Two checks catch that kind of bug:

- **Owner match.** With the token, mecak8s sends the owner it holds for `hostS42`. AS-1 converts the token only if the company identity in its `sub_id` equals that owner.
- **Owner fixed at first open.** AS-1 records the owner when `A42` is first opened and refuses any later open of `A42` for a different user. Session IDs are never reused, so a stale pairing cannot reach a new session.

A mismatch refuses the open and is recorded. Both checks compare facts inside Mecatl, so they catch mistakes, not a compromised mecak8s, which could report a consistent lie.

**Later: a token bound to the session.** When a session is created, the user's client could silently refresh its AS-1 token with RFC 9396 `authorization_details` naming that session, and use that token for it. AS-1 would then convert only a token bound to the session being opened. The client, not only mecak8s, would witness the pairing, and a stolen access token would work for one session instead of the whole API. It needs RFC 9396 support at AS-1, a session-bound token per session in the TUI and Studio, and a two-step session open. Neither check changes the ID-JAG or anything downstream of `RT-A42`.

**Later: run grants.** The [authority work](README.md#order-of-work) replaces the session's standing refresh token with a grant for each run, minted when the run starts from a fresh access token that arrives with the user's request. Sessions of users who are not present then carry no authority. Schedules keep their own authority. A compromised mecak8s could still turn any fresh token the user sends into a run of its choosing, one run per token; only a key held by the client and signing each run's start would prevent that.

The refresh token is not tied to Alice's AS-1 login session, so signing out of one client does not end it. Signing out everywhere, closing the session, and the hard expiry end it, and AS-1's upstream re-check ends it if the company IdP disables her account; see [sign-out and revocation](#sign-out-and-revocation).

The existing broker session record under `A42` keeps the workload and host-session binding. The human authority now lives in `RT-A42`, not in a broker-local human record.

**Alternative:** AS-1 could return a signed session assertion JWT instead of a refresh token. It needs less authorization-server work but still requires the AS-1 record for revocation and one-conversion checks, and is an extension to the ID-JAG profile.

**Evidence:** AS-1 records the conversion: client `mecatl`, user `u-alice-91`, owner, source token `jti`, session `A42`, and the hard expiry, and records any refused open with its reason.

### (4) Agent-instance JWT-SVID

For the diagram's main registration, mecak8s supplies the persisted main agent-instance incarnation and selected definition, with no parent. For the reviewer, it supplies the child's persisted incarnation, the already registered reviewer definition, and the registered main-instance reference, not the main's token. The agent-instance issuer constructs each canonical subject under the [registration checks](agent-definitions.md#registering-instances). A registration with no parent, including after a fork or clear, succeeds only for a root-eligible definition. Definition names that would need encoding in a SPIFFE path are rejected, and nesting depth is capped so a subject stays under the 2048-byte limit of SPIFFE-ID §2.3; registration past the cap is refused. Registration records identity, not permission to spawn, run, or call tools.

The broker's agent issuer signs each registered instance's JWT-SVID. This payload shows the reviewer's credential for the second call. Mecatl's harness holds it outside model history and presents it to the broker for invocation. The broker forwards it to AS-1 as `actor_token`; AS-2 receives only the actor claim in the ID-JAG. The agent issuer retains its private signing key. This fictional decoded example deliberately uses only required JWT-SVID claims:

```json
{
  "sub": "spiffe://agents.example.com/mecatl/agent/system/main/inst/inc_cejrv7re4uy3ubxyzj2gfxfp2u/child/project/code-reviewer/inst/inc_gh56irfcpnupjqgfdw7dhzzw4m",
  "aud": ["https://broker.example.com"],
  "exp": 1800000600
}
```

Before issuance, the issuer checks the workload and reviewer instance against the registration; a definition's existence alone is insufficient. This is an assertion, not attestation: the issuer signs on mecak8s's workload-authenticated registration, and no evidence about the agent itself is produced or appraised. mecak8s checks that the returned subject is the registered reviewer instance. The SVID carries a single audience, the broker's, as JWT-SVID §7.2 recommends; AS-1 is configured to accept that value as its own SVID audience, because the broker presents the same SVID to both. The broker and AS-1 each validate the SVID against the configured agent trust bundle. JWT-SVID requires `sub`, `aud`, and `exp`, but not `iss` or `jti`. The agent-instance issuer and trust domain are distinct from OAuth AS-1 and AS-2; AS-1 uses the validated registered identity to form the ID-JAG's `act` claim.

See [credential lifetimes and renewal](#credential-lifetimes-and-renewal) for renewal across runs.

The agent-instance JWT-SVID stays the actor input because it gives agent identity its own SPIFFE trust domain name and signing key, so verifiers can tell it apart from the OAuth issuers, and leaves a path to attestation and to policy shaped by RFC 9396 `authorization_details`. While the broker runs inside mecak8s, the issuer shares a process, and therefore its security controls, with AS-1 and the broker. While agents share one harness process, the SVID asserts the agent rather than authenticating it: the issuer is in the broker, and the broker presents the SVID. AS-1 checks the broker's registry that the instance belongs to the session; that catches pairing bugs in the broker core, not a compromised broker or mecak8s, which write the registry. Two strengthenings are possible but not selected:

- **Session claim.** The SVID names the session it was issued for, so AS-1 can check that the SVID and the subject token belong to the same session without relying on the broker's pairing. It is still issued on mecak8s's word.
- **Key binding.** The SVID carries a `cnf` key, and the presenter proves possession, so a stolen SVID is useless without the private key. Inside one process, the key proves the process, not the agent.

Attesting each agent's identity needs agents in their own pod, sandbox, or Kubernetes object, for example attested through the [SPIFFE Broker API](https://github.com/spiffe/spiffe/pull/340). That is out of scope for this design.

### (5) Broker client authentication

The subject tokens the broker presents are bearer tokens: they prove whose authority is used, not who presents them. The broker therefore authenticates as OAuth client `mecatl` on every token-endpoint request, at AS-1 and at AS-2, with `private_key_jwt` (OIDC Core §9, RFC 7523 client assertion):

```json
{"alg": "ES256", "kid": "broker-2026-10"}
{
  "iss": "mecatl", "sub": "mecatl",
  "aud": "https://broker.example.com/as",
  "jti": "ca-7f3a", "iat": 1800000000, "exp": 1800000060
}
```

`aud` is the authorization server's issuer identifier as its only value, not its token-endpoint URL, as [draft-ietf-oauth-rfc7523bis](https://datatracker.ietf.org/doc/draft-ietf-oauth-rfc7523bis/) requires. The private key never leaves the broker and can live in a KMS or HSM. In external mode the gateway administrator registers the broker's `jwks_uri` once.

This makes "accept only from the broker" enforceable. A leaked `RT-A42` is useless without the key: a refresh token issued to an authenticated confidential client is already sender-constrained (RFC 9449 §5). A leaked AS-1 access token cannot be converted directly either, but mecak8s converts the token it receives at session open, so a thief who presents a stolen token to the Mecatl API can open a session with it, once per token. A `client_secret` is rejected: it is shared, replayable once leaked, and rotated by hand. mTLS client authentication (RFC 8705) is an equivalent alternative.

One client ID, `mecatl`, is used at both servers, because the ID-JAG's `client_id` must match the client that redeems it at AS-2.

### (6) Per-operation authorization

Local permissions and hooks run before each call. For the reviewer's call, trusted harness client code sends `InvokeTool` with workload authorization metadata, `A42`, `call.id=op-pr42-001`, run `R42` with its [run owner](run-owner.md) Alice, and `agentJwtSvid=reviewerSVID`. The model supplies only the tool content:

```json
{
  "tool": "pull_request_read",
  "arguments": {"repository": "acme/payments", "number": 42}
}
```

Before any exchange or dispatch, the broker checks:

- the session `A42` is open and bound to the authenticated workload and `hostS42`;
- the SVID is unexpired and names an instance registered to `A42`;
- the reported run owner equals the subject of `RT-A42`'s record; a mismatch denies the call and is recorded, and the run owner never selects the grant;
- the route to the gateway;
- once the authority work is in place, the instance's stored effective ceiling allows this server and tool, with no Cedar evaluation errors.

A cache hit skips the token exchanges, never these checks. User, run, and delegation are not inputs to the ceiling check; the reviewer ceiling gives the same result for PR 43. See [two requests, two authorization decisions](mcp-authorization.md#two-requests-two-authorization-decisions).

The broker does not check provider connections. It authorizes the session, workload, actor, run owner, route, and, with the authority work, the ceiling. AS-2 authorizes the user: the mapping, and in external mode the consent link. vMCP authorizes the connection and dispatches. The provider enforces its own permissions on the request.

**Evidence:** `op-pr42-001` passed the session, workload, actor, and run-owner checks, and, once the authority work is in place, the effective-ceiling check. This decision proves neither dispatch nor success. The [audit chapter](audit.md#auditevent-profile) owns the full evidence shape.

### (7) ID-JAG from AS-1

On a cache miss, the broker asks AS-1 for an ID-JAG ([draft-ietf-oauth-identity-assertion-authz-grant-04](https://datatracker.ietf.org/doc/draft-ietf-oauth-identity-assertion-authz-grant/)) with an RFC 8693 token exchange: `RT-A42` is the subject token, `reviewerSVID` the actor token, and AS-2 the audience. The broker authenticates as `mecatl`.

AS-1 resolves `RT-A42` to its record and checks that the session is open, the hard expiry has not passed, and the token is bound to `mecatl`. If the re-check interval has passed, it re-checks Alice's company-IdP grant first ([upstream re-check](#sign-out-and-revocation)). It validates the SVID and checks that the reviewer is registered to `A42`. ID-JAG leaves actor-token processing undefined and warns that a client could pair a valid subject token with an unrelated actor (§9.7); this registration check is that missing binding. AS-1 authorizes the combination of client, subject, actor, and target AS-2, then issues:

```json
{"typ": "oauth-id-jag+jwt", "alg": "ES256", "kid": "as1-key-1"}
{
  "iss": "https://broker.example.com/as",
  "sub": "u-alice-91",
  "aud": "https://gateway.example.com",
  "client_id": "mecatl",
  "act": {"iss": "https://broker.example.com/as", "sub": "agent:7hq2x9"},
  "jti": "jag-pr42-001",
  "iat": 1800000001,
  "exp": 1800000121
}
```

- `act` names only the acting instance, with an opaque, stable, per-instance `agent:` identifier: no SPIFFE path and no definition name. `agent:` is a private URI scheme. AS-1 generates a random identifier for each instance and stores the mapping from SPIFFE ID; stability lets the vMCP bind a session to the instance and audit correlate its calls. `act.iss` names AS-1, which minted it, so the identifier stays attributable to AS-1 inside `V`, whose own `iss` is AS-2. The full SVID stops at AS-1, so internal structure does not cross into the gateway's domain. `act` has one level. That main delegated `D42` to the reviewer is recorded in Mecatl's harness and broker records, not in the token. A nested `act` naming main comes later, from a [chained exchange](mcp-authorization.md#chained-exchange) at delegation time.
- `aud_sub` and `email`, recommended by the draft, are deliberately omitted: AS-1 does not know AS-2's subject, and an email address is not an identity link.
- AS-1 and AS-2 are separate issuers with separate keys, also in embedded mode. The draft's §9.3 scopes the profile to parties in different trust domains; external mode fits it, and embedded mode reuses the format for one protocol path. See [embedded mode](mcp-gateway-modes.md#embedded-mode).

ID-JAG is a profile of [identity chaining across trust domains](https://www.ietf.org/archive/id/draft-ietf-oauth-identity-chaining-17.html). It adds the `typ`, required `client_id` and `jti`, client-binding redemption checks, and security rules that a private chaining profile would otherwise have to define. Both are Internet-Drafts; identity chaining is in the RFC Editor queue, and ID-JAG is a working-group draft.

**Evidence:** AS-1 records the ID-JAG issuance: user, session `A42`, validated actor, target, and `jti`.

### (8) AS-2 access token

The broker redeems the ID-JAG at AS-2 with the [RFC 7523 JWT-bearer grant](https://www.rfc-editor.org/rfc/rfc7523.html#section-2.1), authenticating again as `mecatl`. AS-2 checks the `typ`, that `aud` is its own issuer, and that `client_id` matches the authenticated client (§4.4.1). It also rejects a reused `jti` (RFC 7523 §3) and keeps used identifiers until they expire. Single use is Mecatl policy, stricter than ID-JAG §4.4.3, which lets a client re-submit an ID-JAG. It then resolves the native user:

| Mode | How AS-2 resolves and authorizes the user |
| --- | --- |
| Embedded | Users log in to AS-2 through AS-1, so the AS-1 subject maps directly. AS-1 is the SSO IdP AS-2 trusts (§4.1). Consent is implicit. AS-1 and AS-2 are distinct issuers with distinct keys, so no issuer redeems its own ID-JAG; embedded mode is still outside §9.3's cross-domain scope. |
| External | Identity chaining across trust domains. AS-2's users log in through their own IdP, so the §4.1 SSO precondition does not hold. AS-2 redeems only for an AS-1 `(iss, sub)` and client `mecatl` with a consent link to a native user, created once through AS-2's own login. No just-in-time provisioning. |

The consent link stops a compromised AS-1 from using the gateway credentials of a user who never chose Mecatl. Its creation flow, the link-swap attack it must resist, and the shared-IdP shortcut are in the [implementation companion](implementation.md#external-consent-link). AS-2 never resolves users through `sub_id` (§9.5).

AS-2 issues a short-lived access token and no refresh token (§4.4.3):

```json
{"typ": "at+jwt", "alg": "RS256", "kid": "gateway-example-key-1"}
{
  "iss": "https://gateway.example.com",
  "sub": "usr-alice-7c1d",
  "act": {"iss": "https://broker.example.com/as", "sub": "agent:7hq2x9"},
  "client_id": "mecatl",
  "aud": "https://gateway.example.com/mcp",
  "iat": 1800000005,
  "exp": 1800000120,
  "jti": "access-pr42-001"
}
```

The broker caches `V-reviewer` for `(A42, reviewer, gateway, Alice)`. It treats `V` as expired a small skew margin before `exp`; on a 401 `invalid_token` from the vMCP it drops `V` and retries the exchange once. A cache hit still needs a currently valid SVID for the same instance. vMCP binds its session (`Mcp-Session-Id`) to the user and `act.sub`, and the broker stores each session ID under the instance, gateway, and user, so each agent instance has its own vMCP session per user.

**Evidence:** AS-2 records the redemption or denial: ID-JAG `jti`, client `mecatl`, native user, actor, resource, and decision.

### (9) vMCP decision and upstream credential

vMCP validates `V-reviewer` against AS-2's keys, `typ`, audience, and expiry, and authorizes the operation. It then loads the upstream credential keyed by **(gateway, native user, provider)**: here `(gateway.example.com, usr-alice-7c1d, github)`. One record exists per user and provider. A delegated `V` comes from no interactive gateway login, so every gateway, including a standalone one, must find the credential from the user alone.

If no credential is stored, vMCP answers "needs authorization" and the user connects GitHub through the existing authorization flow. A later call after connection passes every check again.

**Evidence:** vMCP records its decision with the access-token reference `(iss, jti)`, user, actor, tool, and outcome. Neither a successful MCP response nor an authorization record proves provider-side effects.

**Correlation:** a cached `V-reviewer` serves several calls, so its `(iss, jti)` leads back through AS-2's redemption record and the ID-JAG `jti` to AS-1's issuance record, but not to one operation. To tie a gateway record to a single call, the broker propagates the operation and attempt IDs, here `op-pr42-001`, in each MCP request's `_meta`, and vMCP logs them as reported, unverified trace context. This propagation is required for correlation. Because `act` names only the reviewer, this operation ID is what lets an investigator reach delegation `D42` from a gateway log.

## User identity

AS-1 runs Alice's sign-in through the company IdP and represents her as `u-alice-91`, carrying the company identity in `sub_id`. mecak8s authenticates the API request with the AS-1 token and takes the session owner from its `sub_id`, the company identity. The broker receives Alice's AS-1 token once and converts it into `RT-A42`; every ID-JAG names the subject from that refresh token's record, not from caller input.

No mecak8s code path can name a user who did not sign in at AS-1. An attacker with code execution in mecak8s is another matter. While the broker runs inside mecak8s, that attacker holds AS-1's keys and can act as any user connected to the gateway. Once the broker is a separate service, the attacker can act only in open broker sessions bound to its workload and open new ones with fresh tokens from users currently using it. See [deployment and threats](README.md#deployment-and-threats). A compromised broker or AS-1 can still act for any user whose authority it holds; in external mode the consent link limits that to users who linked Mecatl.

AS-2 resolves `u-alice-91` to native Alice `usr-alice-7c1d` through shared login (embedded) or the consent link (external). The native identifier need not equal either upstream subject. Never infer the mapping from matching names or email addresses.

The agent remains the actor: AS-1 validates its SVID and the registered presenter combination and sets `act`. AS-2 receives neither the AS-1 access token nor the SVID. Run, delegation, and parent facts remain lifecycle and context evidence. Each call also carries the [run owner](run-owner.md), which the broker checks against the grant it uses; today it equals the session owner.

## Credential custody

The following custody boundaries do not imply a selected persistence implementation or process-isolation boundary.

| Credential | Holder and use | Custody and renewal |
| --- | --- | --- |
| AS-1 access token | Client presents it to mecak8s; mecak8s passes it to the broker once at session open. | Not stored by the server side. |
| Client refresh token | TUI keyring or Studio sealed cookie. | Never leaves the client. |
| Workload credential | Platform-managed; mecak8s presents it to the issuer and broker. | Projected credential or configured SPIRE identity. Authenticates the workload, not a user or agent. |
| Agent-instance JWT-SVID | Harness holds `mainSVID` / `reviewerSVID` outside model context; broker forwards it to AS-1 as `actor_token`. | Agent-instance issuer keeps its signing key. Renewal requires the same registered identity and does not widen authority. |
| Broker session `A42` (state) | Broker owns the workload and host-session binding. | Recovered under workload and continuity checks. Possession of the reference grants nothing. |
| Broker refresh token `RT-A42` | Broker; presented only to AS-1. | Bound to `mecatl`, `A42`, and Alice; does not rotate; hard expiry; revoked when the session closes. |
| Broker private key | Broker; signs client assertions for AS-1 and AS-2. | KMS or HSM where available; public key published through `jwks_uri`. |
| ID-JAG | Broker, from issuance to redemption. | Single-use, short-lived. |
| AS-2 access token `V` | Broker presents it to vMCP. | Cached per session, instance, gateway, and user; per-call checks still apply. No refresh token. |
| Consent link (state) | AS-2, external mode. | Until revoked in the gateway UI or by Mecatl. |
| Upstream provider credential | Gateway credential store. | Keyed by (gateway, user, provider); provider-specific renewal stays at the gateway. |

Keep credential values out of model context, tool arguments, results, transcripts, logs, environment variables, audit records, diagnostics, and errors. Hiding a credential from the model does not protect it from a compromised process that holds it. This protected MCP path does not relocate other harness credentials, such as those used to call an LLM provider.

## Credential lifetimes and renewal

An agent instance keeps its identity across runs. These lifetimes are independent.

| Item | Expiry or loss of authority | Replacement |
| --- | --- | --- |
| AS-1 access token | AS-1 policy; short. Expiry ends API authentication, not the broker session. | The client refreshes with its own refresh token. |
| Client refresh token | AS-1 policy and upstream sign-in status. | Sign in again. |
| Workload JWT | Expired credentials are rejected. | Platform rotation for the same workload. |
| `mainSVID` / `reviewerSVID` | Expiry prevents use as current actor evidence. | The agent-instance issuer renews for the same registered instance; the ceiling does not widen. |
| `RT-A42` | Hard expiry, session close, sign out everywhere, a failed upstream re-check, or revocation. It does not rotate. | A new session open with a fresh AS-1 access token. |
| ID-JAG | Short-lived and single-use. | A new exchange with `RT-A42` and a current SVID. |
| `V` | Short-lived; every cached use still passes broker checks. | A new ID-JAG. |
| Broker private key | Operator rotation. | New key published through `jwks_uri`. |
| Schedule authority | Its horizon, schedule deletion, sign out everywhere, or a failed upstream re-check. | The owner renews it. |
| Consent link | Revocation at AS-2. | The user links again. |
| Upstream provider credential | Provider expiry or revocation. | Refresh at the gateway, or the user connects again. |

The example times are illustrative, not selected defaults: `J-reviewer` is valid `1800000001..1800000121`, `V-reviewer` `1800000005..1800000120`. The values of `RT-A42`'s hard expiry and of a schedule's authority horizon are not selected. They bound how long Mecatl acts without a fresh sign-in, and the company IdP administrator should know them.

Closing or revoking the session revokes `RT-A42` and drops that session's cached ID-JAGs, `V`, and vMCP session IDs. A recipient validating only a self-contained token may still accept an already-issued `V` until it expires.

## Sign-out and revocation

Signing out of one client and stopping Mecatl's agents are different actions.

| User action or event | What ends | What remains |
| --- | --- | --- |
| **Sign out** in the TUI or Studio | That client's own AS-1 tokens, revoked at AS-1. | Running agents and schedules. Their authority is held by the broker, not by the client. |
| **Sign out everywhere** | Every agent acting for Alice: live runs stop, tool calls parked for authorization are cancelled, broker sessions and their refresh tokens are revoked, and the authority of each of her schedules is revoked. | Transcripts. Each schedule stays configured; its next run ends with "needs owner action" until Alice renews it. |
| **The company IdP disables Alice's account or revokes her grants** | The same as sign out everywhere, within one re-check interval. | Transcripts. |
| **Closing a session** | That session's refresh token. | Other sessions and schedules. |

**Sign out everywhere.** The client calls Mecatl before it clears its own tokens. For each of Alice's sessions, mecak8s cancels the live run and any tool call parked for authorization. The broker revokes Alice's grants in those sessions at AS-1, cancels their in-flight tool calls, and drops her cached ID-JAGs and `V`; with one owner per session, that ends each broker session. Revocation is by user, so a [shared session](run-owner.md) would keep running for its other members. The broker also revokes the authority of each of Alice's schedules. Runs and broker state can live on different replicas, so the revocation must reach every replica that serves one of Alice's sessions. Whether a client-driven revoke per session, a durable revocation marker checked when a session lease is renewed, or shared broker state does this is an implementation choice.

**Upstream re-check.** AS-1 keeps the company-IdP refresh token behind each broker refresh token and each schedule's authority; a client signing out does not remove it. When the broker exchanges a refresh token for an ID-JAG, AS-1 refreshes that upstream token if it has not done so within the re-check interval. AS-1 requests `offline_access` at sign-in so the IdP issues it a refresh token, and stores the newest one if the IdP rotates them. Only an `invalid_grant` from the IdP, meaning the account is disabled or its grants were revoked, makes AS-1 revoke every grant it holds for Alice, including schedule authority, and fail the exchange. A network error or a server error fails that exchange closed and revokes nothing. An upstream refresh token that merely reached its own maximum lifetime ends Alice's authority with a "needs owner action" outcome, not as a disabled account. This works with any IdP that issues refresh tokens and needs no IdP-specific events. It catches account disable and administrator revocation within about one re-check interval; it does not catch a plain sign-out at the company IdP. The interval is not selected; it bounds how long a disabled account can keep acting.

**What remains after revocation.** Issued ID-JAGs and AS-2 access tokens cannot be revoked. The broker stops using them at once, but a `V` already issued stays valid at the gateway until it expires, and the vMCP does not check for revocation. Keep both lifetimes short.

**Later.** Two steps would make revocation faster or broader. AS-1 could receive logout and account events from the company IdP: OIDC Back-Channel Logout from Keycloak, or event hooks from Okta. Entra and Google Workspace send neither to third-party applications. And each broker refresh token could be bound to Alice's AS-1 login session, so that signing out at AS-1 itself ends her agents. Shared Signals (SSF, CAEP, RISC) carry the same events but have little IdP support today.

## Pauses and restart

The broker's session reference and broker session incarnation are separate from the host session ID and [agent-instance incarnation](agent-definitions.md#definitions-and-executing-instances). An existing stable broker session can recover a new process-local attachment through continuity checks. Those references do not combine user, agent, definition, and authority into one record.

Workload-only session access checks the exact authenticated workload and configured deployment. It never takes the owner from a workload's assertion: an owner mecak8s reports is only compared with AS-1's record, and a mismatch refuses. A replacement Pod with the same workload identity can serve the existing partition; a different identity cannot assume access.

An approval wait or provider-connection pause leaves the current run unfinished. Before dispatching the pending operation, the broker checks it against the applicable policy again. Connecting a provider account does not approve the pending operation.

After restart, recovering credentials is not enough to continue safely. Recovery must restore `A42` and its workload and host binding, find `RT-A42`'s AS-1 record still valid, and reestablish the registered actors and the unfinished execution, including each run's owner. If `RT-A42` has expired or been revoked, tool calls fail closed until a new session open supplies fresh authority. The harness decides whether execution resumes; the broker authorizes subsequent calls.

Under the [identity and selection continuity rules](agent-definitions.md#identity-continuity-across-runs-and-restarts), continuation retains the unfinished run's original bounds. With the authority work in place, the broker rechecks the stored effective ceiling; the harness retains its run and delegation restrictions. Credential renewal does not widen recovered authority.

An operation that may have been dispatched but has no recorded completion has an unknown outcome. Restoring credentials does not make replay safe. Reconciliation requires evidence about the original operation, such as provider status or an idempotency mechanism.

## Scheduled runs

A scheduled invocation runs without Alice present. Its recorded owner identifies on whose behalf the work was configured; audit records must distinguish scheduled execution from a fresh human request.

A schedule usually outlives the session that created it, and `RT-A42` is revoked when that session closes. A schedule therefore has its own delegated authority from Alice. Mecatl obtains it when she sets up the schedule, in the same one-time conversion as at session open, but as a separate kind of record bound to the schedule rather than a session. A session cannot redeem a schedule's authority, and a schedule cannot redeem a session's refresh token. A scheduled run's owner is checked against the schedule's authority. It has an explicit horizon. Deleting the schedule, signing out everywhere, and a failed upstream re-check revoke it. Each run draws on that authority.

When Alice sets up the schedule, every connector the schedule may use must be connected: in external mode, the gateway link; in every mode, the provider connection. Anything missing is fixed right then through the existing authorization-pending flow, while she is present.

At run time nothing is interactive. Suppose something has lapsed since setup: a revoked link, a revoked provider credential, or authority past its horizon. Then the call fails closed, the run ends with a "needs owner action" outcome, and Alice is notified. The notification links into Mecatl, which starts a fresh fix flow. It does not carry a connect URL, because those can be single-use and short-lived. A provider token that merely expired but can be refreshed is not a lapse: the gateway refreshes it.

Pre-authorization is renewable without recreating the schedule. Renewing extends the authority horizon and reconnects anything that lapsed. Alice is notified before the horizon is reached.

A saved owner or an available provider connection does not authorize an MCP operation on its own. Each call still passes the broker checks, AS-2 redemption, and vMCP's credential lookup.

## Workload authentication

Workload authentication identifies the Mecatl service contacting the broker. Agent-instance identity distinguishes the agents hosted by that service. The proposed deployment options separate these two roles:

| Workload authentication to the broker | Agent-instance identity |
| --- | --- |
| Kubernetes ServiceAccount bearer over TLS | None: workload-only identification |
| Kubernetes ServiceAccount bearer over TLS | Broker-issued instance JWT-SVID |
| SPIRE workload JWT-SVID over TLS | Broker-issued instance JWT-SVID |
| SPIRE workload X.509-SVID over mTLS | Broker-issued instance JWT-SVID |

These are alternatives, not a required implementation sequence. Broker-issued agent-instance JWT-SVIDs do not require SPIRE.

| SPIRE alternative | Credential example and custody | Broker validation |
| --- | --- | --- |
| Workload JWT-SVID | SPIRE supplies `{"sub":"spiffe://platform.example.com/ns/mecatl/sa/mecak8s","aud":["https://broker.example.com"],"exp":1800003500}` through the Workload API; the harness client holds and presents it. | Check signature against the configured platform JWT bundle, required claims, audience, expiry, and permitted SPIFFE ID. |
| Workload X.509-SVID / mTLS | SPIRE supplies a certificate with URI SAN `spiffe://platform.example.com/ns/mecatl/sa/mecak8s` and illustrative validity `1799999900..1800003500`. The client retains the private key and proves possession in TLS. | Check the chain against the configured platform trust bundle, validity, SPIFFE identity, and permitted workload. TLS termination elsewhere requires trusted identity propagation. |

For SPIRE authentication, audit evidence identifies the configured trust domain/bundle association and verified SPIFFE ID. A stolen bearer remains usable until expiry or an effective revocation check; signature validation alone does not prove possession of a private key.

A workload credential alone cannot distinguish `code-reviewer` from `deployer` when both run in the same Mecatl service. A self-declared agent name does not establish that distinction for broker policy.

A future option is to authenticate the broker as OAuth client `mecatl` with its SPIFFE workload identity instead of a separate private key.

## AS-2 access-token use and actor history

The broker redeems the ID-JAG as described in [step (8)](#8-as-2-access-token). AS-2 issues an access token naming its native user in `sub`, the acting agent in `act`, the OAuth client `mecatl`, and the vMCP resource in `aud`. The broker presents this token to vMCP; the SVID is not an AS-2 input.

The ID-JAG `jti`, AS-2 token `(iss, jti)`, and operation ID are separate references. A token can be reused for multiple authorized calls, so its `jti` cannot replace a per-operation reference. Each recipient records only what it verifies or observes.

`act` identifies the current actor only ([RFC 8693 §4.1](https://www.rfc-editor.org/rfc/rfc8693.html#section-4.1)) and has one level. Actor history stays inside Mecatl: delegation in the harness and broker records, creation ancestry in the SPIFFE path. RFC 8693 treats prior actors in a nested `act` as informational only, so leaving them out removes nothing the gateway enforces. A later [chained exchange](mcp-authorization.md#chained-exchange) would let AS-1 nest the delegating instance as a prior actor. To reach the delegation from a gateway record, follow the propagated operation ID (see [step (9)](#9-vmcp-decision-and-upstream-credential)). Until the agent ceiling is also carried as scope or `authorization_details`, the gateway enforces user and connection, and the broker alone enforces the agent ceiling.

## Provider-connection selection

The vMCP gateway resolves the provider credential from the validated user and the route's provider, keyed by (gateway, native user, provider).

The model cannot select another user's credentials or redirect them by supplying a URL, header, audience, or credential selector. If required identity, link, connection, or authorization evidence is missing, invalid, stale, or revoked, the call fails closed. It does not fall back to a service credential, another user, or a more privileged agent. A denied tool call dispatches no backend operation. Missing credentials or consent surface through the existing `mcp_authorization_pending` flow; how an external gateway reports them to the broker as a structured outcome remains open.

## Instance identity and creation ancestry

The instance JWT-SVID identifies the acting agent instance through its SPIFFE ID. The proposed identity path records the instance's definition category and name, its agent-instance incarnation, and the ancestry of instances that created it.

Creation ancestry is distinct from execution delegation. The same instance can participate in multiple runs, so its SPIFFE ID alone does not identify which parent run delegated the current task. Run and call records supply that context.

A definition category and name are not enough to establish provenance. Two repositories can both define `code-reviewer`. Before selecting definition-specific policy, the broker needs a verified association with the source that supplied the definition.

### Example instance identities

Alice's main instance:

```text
spiffe://agents.example.com/mecatl/agent/system/main/inst/inc_cejrv7re4uy3ubxyzj2gfxfp2u
```

A reviewer instance created by that main:

```text
spiffe://agents.example.com/mecatl/agent/system/main/inst/inc_cejrv7re4uy3ubxyzj2gfxfp2u/child/project/code-reviewer/inst/inc_gh56irfcpnupjqgfdw7dhzzw4m
```

A new root instance after a fork or clear:

```text
spiffe://agents.example.com/mecatl/agent/system/main/inst/inc_45bvtrrrpkekb6336hwe6bvape
```

The reviewer's ID extends its creating instance's path with a child segment. A fork or clear creates a new root instance rather than extending that path. Restoring an existing instance preserves its identity.

These IDs describe instance identity and creation ancestry. The initiating user and the current run's delegation context are recorded separately.

## Sender binding

`RT-A42`, the ID-JAG, and `V` are all presented only by the broker. `RT-A42` and the ID-JAG are usable only together with the broker's client authentication, so they are sender-constrained through `mecatl`'s private key. The agent SVID is a bearer token too; its holder is limited only by the broker's workload-authentication and presenter checks, and at AS-1 by `mecatl`'s client authentication. `V` remains a bearer token presented to vMCP; its signature protects the claims but does not prove the holder. DPoP-binding `V` to a broker key would close that gap; it is later work.

Binding a credential to a worker's key would not establish which agent inside that worker made a call, protect against a compromised worker using its own key, or bind the MCP arguments. Agent-instance identity and tool-call authorization remain separate checks.
