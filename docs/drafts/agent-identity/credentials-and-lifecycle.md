# Credentials and lifecycle

*This chapter describes the intended architecture, not current implementation status.*

An agent instance can participate in multiple runs. Its instance credential identifies it to the broker; presenting that credential does not by itself authorize an MCP tool call. The harness manages run execution. In v1, the broker checks the acting instance's effective server/tool ceiling, its definition ceiling narrowed by its parent's, with Cedar, alongside existing user/connection checks and execution-context checks. User, resource, argument, and dynamic delegation policies are deferred; carrying run or delegation context does not mean Cedar evaluates those bounds. Renewing the instance credential does not itself extend or widen permission to call tools.

## Credential roles

| Credential | What it establishes | Where it is presented |
| --- | --- | --- |
| **Human login token** | The identity of the user making a request to Mecatl. | Human client → Mecatl's API. It is not forwarded as the downstream user token. |
| **Workload credential** | Which Mecatl service is contacting the broker, not which user or agent it represents. | Mecatl → broker. |
| **Agent-instance JWT-SVID** | Which agent instance is acting. It can span multiple runs. | Mecatl → broker; the broker also uses it as the actor token in the proposed ToolHive exchange. |
| **Broker OAuth client credential** | Which OAuth client is requesting access from ToolHive. | Broker → ToolHive authorization server. |
| **ToolHive user grant** | The user's authorization granted through ToolHive. Its access token supplies the user side of the exchange; its refresh token supports renewal. | Held by the broker; the access token is presented to ToolHive as `subject_token`. |
| **Delegated access token** | The user and acting agent together: `sub=user`, `act.sub=instance`, with the protected MCP endpoint as audience. | Broker → protected MCP endpoint. |
| **Native provider credential** | Access to the external provider under the connected account. | ToolHive → provider, such as GitHub. It stays outside Mecatl. |

These are roles, not necessarily seven credentials sent on every call. Enrollment, issuance, and refresh happen when needed.

Run IDs, session attachments, and broker session references are separate from credentials. A broker session reference grants neither run execution nor MCP access; the proposed registration discussion distinguishes existing references from registration responsibilities.

## Interactive execution

Alice, identified by company issuer `https://login.example.com` and subject `alice-204`, owns harness session `S42`. She asks Mecatl to list pull requests in `acme/payments` and review PR 42. Main run `R41` first calls `pull_request_list`. After the list returns, it delegates review of PR 42 through `D42` to reviewer run `R42`, which calls `pull_request_read`. These tool names and arguments are illustrative, not a selected MCP server contract. The main instance has incarnation `inc_cejrv7re4uy3ubxyzj2gfxfp2u`; its reviewer child has incarnation `inc_gh56irfcpnupjqgfdw7dhzzw4m`. Runs identify execution, not credentials. In the diagram, `main` and `reviewer` abbreviate the exact SPIFFE subjects in [example instance identities](#example-instance-identities). The reviewer uses definition `project/code-reviewer`. Creation ancestry and delegation `D42` are separate facts, even though the main both creates this reviewer and delegates this task to it.

The diagram starts from the [fixed definition selection and workload-authenticated registration](agent-definitions.md#loading-definitions-for-a-session) supplied by mecak8s's trusted lifecycle code. The main is registered before execution; the reviewer is registered when created, using the selected reviewer definition and its own persisted agent-instance incarnation. Section (3) identifies the inputs for each. Registration records identity, not permission to spawn, run, or call tools. It trusts mecak8s lifecycle assertions and does not establish same-process isolation; the [registration contract and open questions](agent-definitions.md#registering-instances) remain proposed, not implemented behavior.

Operation `op-list-prs-001` identifies the main's list call. Operation `op-pr42-001` identifies the reviewer's PR read; the detailed credential and audit examples below follow this second call. Times are Unix seconds around `1800000000`, matching the delegated token. Examples are fictional decoded payloads or credential references, never usable bearer values. Audit examples contain only selected metadata; they are not copies of credentials or implemented cross-service events. The common operation ID denotes desired correlation, not an established authenticated propagation mechanism. Audit placement does not specify durable-write ordering.

For this example only, configured workload audience `https://broker.example.com` covers the combined broker's issuance and invocation endpoints. The instance credential has two explicit audiences: that broker and `https://toolhive.example.com/token`. This illustrates a multi-audience deployment so the same actor credential can reach both validators; it does not select the architecture over separately issued credentials. Each recipient requires its own configured audience and trust material, never endpoints chosen by token claims.

Instance credential issuance, provider enrollment, and token exchange occur only when needed. The broker checks every requested operation (see [delegated authority](mcp-authorization.md#delegated-authority) and [operations and resources](mcp-authorization.md#operations-and-resources)).

The diagram follows two successful calls, with enrollment already complete and usable provider credentials in custody. Failure handling, enrollment, and internal checks are in the linked legend rather than separate arrows. The numbered explanatory sections are not message order. Participants are logical roles, so grouping them does not establish process isolation.

```mermaid
sequenceDiagram
    actor U as Alice / human client
    participant H as mecak8s (API / harness client)
    box Combined broker
        participant I as Agent-instance issuer
        participant B as Tool authorization / broker OAuth client
        participant A as ToolHive AS
        participant V as Protected vMCP
    end

    Note over H,I: Proposed session bootstrap with fixed definitions and host-allocated incarnations
    H->>I: Register main under workload SA authentication<br/>Main incarnation + selected definition + no parent
    I-->>H: Registered main reference and canonical subject
    H->>I: Request main SVID<br/>Workload credential + registered main reference
    I-->>H: Main JWT-SVID

    U->>H: List pull requests and review PR 42 in acme/payments<br/>API access token
    Note over H,V: First call: main run R41 lists pull requests
    H->>B: pull_request_list(repository=acme/payments)<br/>Workload + main SVID + R41 + op-list-prs-001
    B->>B: Authenticate and check context<br/>Check main server/tool ceiling and connection access
    B->>A: Exchange user subject_token + main actor_token<br/>Broker client authentication
    A->>A: Validate user and main actor tokens and broker client<br/>Authorize requested MCP target and access bounds
    A-->>B: Delegated token with sub=ToolHive user and act.sub=main
    B->>V: pull_request_list with main's delegated token
    V->>V: Authenticate delegated token and authorize list operation
    V-->>B: Pull request list including PR 42
    B-->>H: Sanitized list including PR 42 and observed outcome

    H->>H: Main delegates PR 42 review via D42 to reviewer R42<br/>Allocate and persist child incarnation and definition
    H->>I: Register reviewer under workload SA authentication<br/>Child persisted incarnation + registered reviewer definition + registered parent reference
    I-->>H: Registered reviewer reference and child subject
    H->>I: Request reviewer SVID<br/>Workload credential + registered reviewer reference
    I-->>H: Reviewer's own JWT-SVID

    Note over H,V: Second call: reviewer run R42 reads PR 42
    H->>B: pull_request_read(repository=acme/payments, number=42)<br/>Workload + reviewer SVID + R42 + D42 + op-pr42-001
    B->>B: Authenticate and check context<br/>Check reviewer server/tool ceiling and connection access
    B->>A: Exchange user subject_token + reviewer actor_token<br/>Broker client authentication
    A->>A: Validate user and reviewer actor tokens and broker client<br/>Authorize requested MCP target and delegated access bounds
    A-->>B: Delegated token with sub=ToolHive user and act.sub=reviewer
    B->>V: pull_request_read PR 42 with reviewer's delegated token
    V->>V: Authenticate delegated token and authorize PR 42 read
    V-->>B: PR 42 result
    B-->>H: Sanitized PR result and observed outcome
    H-->>U: Review produced from reviewer findings<br/>No credentials in model history
```

| Legend key | Explanatory check or record (not a chronological message) |
| --- | --- |
| [(1) API access token](#1-company-api-access-token) | After the API request and before the main run, mecak8s validates the token and checks its issuer and subject against the stored session owner; only then does it start `R41`. This authenticates the API request and enforces session ownership, not MCP dispatch authorization. |
| [(2) Workload credential](#2-workload-credential) | The broker authenticates mecak8s for registration, issuance, and both invocations. This authenticates the service's lifecycle assertions, not isolation from compromised code in that service. Invalid workload credentials stop the request. |
| [(3) Broker session and instance registration](#3-broker-session-and-instance-registration) | Before returning the registered reference and before (4), the issuer validates the workload and persisted registration records, then constructs the canonical subject. Registration remains identity recording only; it authorizes neither spawning, run execution, nor a tool call. |
| [(4) Agent-instance JWT-SVID](#4-agent-instance-jwt-svid) | After the SVID is returned and before an invocation, mecak8s verifies its signature using the configured agent trust bundle and checks the registered subject, allowed algorithm, audience, and expiry. |
| [(5) Enrollment](#5-enrollment-and-authorized-connection) | If an invocation reveals missing enrollment or a grant requiring user reauthorization, the broker returns an enrollment-required response without dispatch. mecak8s pauses, presents the broker link, and retries after completion through all checks. Enrollment is not permission to execute. Refreshable expiry is handled in custody where possible. |
| [(6) Per-operation authorization](#6-per-operation-authorization) | Main's list and reviewer's read each pass local permissions and hooks, broker credential/context checks, the actor's effective ceiling, and connection checks before dispatch. `D42` is context, not v1 dynamic delegation policy. The [ceiling rules](mcp-authorization.md#definition-derived-ceiling) fail closed, including on any Cedar diagnostic error. |
| [(7) Token exchange and delegated MCP access token](#7-token-exchange-and-delegated-mcp-access-token) | Before requesting exchange, the broker obtains a current user access token from custody or refreshes it when needed. Before returning the delegated token, ToolHive AS validates the user token, actor credential, presenter, and bounded access. The broker restricts any delegated token to the applicable execution context; reuse never exceeds those bounds. |
| [(8) Dispatch and observed outcome](#8-dispatch-and-observed-outcome) | Before resolving the upstream grant or calling the provider, vMCP validates the delegated token and operation policy and resolves the authorized connection. It performs each GitHub operation using the native provider credential held by ToolHive, never an SVID as the provider credential. After the provider result and before returning the sanitized result, the broker records the observed outcome and provider correlation when available. |

Decision, dispatch, and outcome require durable evidence; persistence ordering and cross-service propagation are unspecified. Every subsequent tool call repeats authorization.

A child call uses the same invocation flow with the child's own JWT-SVID and attributed run and delegation context. It does not require or present the parent's SVID. The broker checks the child's registered parent-instance reference, not a parent token, when it constructs the child subject and computes the child's [effective ceiling](mcp-authorization.md#delegated-authority) from its definition ceiling and the main's. It evaluates that stored effective ceiling for the call. Broker enforcement of task-specific delegated bounds remains future work.

### (1) Company API access token

The company IdP issues an API access token to Alice's client, which presents it to Mecatl over TLS. The client holds it; it terminates at the API and is not retained for downstream access. This fictional JWT access-token payload is not an ID token.

```json
{
  "iss": "https://login.example.com",
  "sub": "alice-204",
  "aud": "https://mecatl.example.com/api",
  "iat": 1799999900,
  "exp": 1800003500
}
```

This example uses mecak8s with OIDC authentication and session ownership enforcement enabled. Alice submits her request to existing session `S42`, which belongs to her. mecak8s validates her access token and checks that its issuer and subject match the session's stored owner. If either check fails, it rejects the request. Otherwise, the harness starts main run `R41`.

**Illustrative API evidence:** mecak8s accepted request `req-pr42-001` from authenticated user `(https://login.example.com, alice-204)` for session `S42`. The user identity comes from the validated token; session ownership comes from server-side state. This records API authentication and session ownership only. The broker has not yet authorized an MCP call to read PR 42.

### (2) Workload credential

Kubernetes supplies mecak8s with a projected ServiceAccount token. mecak8s presents it to the broker over TLS, keeping it outside model context. This fictional decoded payload identifies the calling service:

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

The broker checks the signature using trusted cluster keys, the issuer, allowed signing algorithm, broker audience, and expiry. It then checks that the authenticated ServiceAccount is allowed to contact it. If either check fails, it rejects the request.

**Illustrative broker evidence:** the caller authenticated as `(https://kubernetes.example.com, system:serviceaccount:mecatl:mecak8s)`. This identifies the mecak8s service, not Alice or the reviewer instance, and does not authorize the PR 42 call.

[SPIRE alternatives](#workload-authentication) replace this workload credential without changing its role.

### (3) Broker session and instance registration

The broker session reference identifies broker-owned state; knowing it grants no access. The broker checks the calling service's authenticated identity and deployment. This associates the session with mecak8s, not with Alice, and is separate from harness session `S42`. See [pauses and restart](#pauses-and-restart) for attachment and recovery details.

For the diagram's main registration, mecak8s supplies the persisted main agent-instance incarnation and selected definition, with no parent. For the reviewer, it supplies the child's persisted incarnation, the already registered reviewer definition, and the registered main-instance reference, not the main's token. The broker constructs each canonical subject under the [registration checks](agent-definitions.md#registering-instances).

Alice's identity remains a separate API report, not established by the workload credential. The issuance example in (4) preserves that distinction. See [source records and policy representation](agent-definitions.md#source-records-and-policy-representation) for the open API, schema, per-operation context-binding, and same-process protection questions.

### (4) Agent-instance JWT-SVID

The broker's agent issuer signs each registered instance's JWT-SVID. This payload shows the reviewer's credential for the second call. The harness client holds it outside model history and presents it to the broker; the broker later presents it as the actor token to ToolHive AS. The issuer retains its private signing key. This decoded example deliberately uses only required JWT-SVID claims:

```json
{
  "sub": "spiffe://agents.example.com/mecatl/agent/system/main/inst/inc_cejrv7re4uy3ubxyzj2gfxfp2u/child/project/code-reviewer/inst/inc_gh56irfcpnupjqgfdw7dhzzw4m",
  "aud": ["https://broker.example.com", "https://toolhive.example.com/token"],
  "exp": 1800000600
}
```

Before issuance, the issuer checks the workload and reviewer instance against the proposed registration in step (3); a definition's existence alone is insufficient. The harness checks that the returned subject is the registered reviewer instance, then validates its signature, allowed algorithm, expiry, and intended audiences against the configured agent trust-domain bundle. The broker and ToolHive AS each require their own audience and a permitted presenter. JWT-SVID requires `sub`, `aud`, and `exp`, but not `iss` or `jti`.

**Illustrative issuance evidence for (3) and (4):** only if mecak8s's proposed lifecycle registration supplies the definition provenance and instance association, and the API supplies its user report, could the broker correlate the issued subject with the following facts. Today's broker contract does not supply these registration/user proofs, and `AgentDef` has no source or revision fields today. This is conditional audit evidence, not implemented behavior, additional JWT claims, or a registration API; protection against same-process substitution remains unresolved.

```json
{
  "issuance_ref": "issue-42",
  "definition": {
    "name": "project/code-reviewer",
    "revision": "review-def-r17",
    "source": "repo://acme/payments/.mecatl/agents/code-reviewer.md",
    "evidence_source": "mecak8s lifecycle registration"
  },
  "initiating_user": {
    "iss": "https://login.example.com",
    "sub": "alice-204",
    "evidence_source": "API report",
    "request_ref": "req-pr42-001"
  }
}
```

The JWT's `sub` identifies the reviewer instance. It does not carry Alice's identity or the definition revision; the correlated evidence records those separately and preserves their different sources.

See [credential lifetimes and renewal](#credential-lifetimes-and-renewal) for renewal across runs.

### (5) Enrollment and authorized connection

For this same PR read, `th-user-731` is the ToolHive-side subject associated with Alice **in this example**, not her company-login subject. The example assumes Alice is authorized to use that subject and provider account `github-account-731` through connection `github-conn-42` for `acme/payments`. The association mechanism remains open: referring to the same person, matching names or ID suffixes, and successful OAuth do not prove authorization. `github-account-731` is a distinct provider account ID; its shared suffix with the ToolHive subject has no evidentiary meaning. Model arguments cannot select another user's connection.

If enrollment is needed, Alice completes it before the pending call is reauthorized. The broker holds the ToolHive user grant and uses its access token as `subject_token`; ToolHive holds the native provider grant for this connection. Neither grant reaches the harness or model. Client authentication and credential placeholders are shown once under [credential custody](#credential-custody); see also [provider-connection selection](#provider-connection-selection).

**Illustrative enrollment evidence:** ToolHive records verified client `mecatl-broker`, authenticated subject `th-user-731`, enrollment `enroll-42`, connection `github-conn-42`, and the provider-reported account. The broker records connection readiness with Alice still API-reported. Records exclude tokens, client authentication material, codes, and transaction secrets.

Refresh behavior belongs in [credential lifetimes and renewal](#credential-lifetimes-and-renewal).

### (6) Per-operation authorization

Local permissions and hooks run before each call. For the second call, trusted harness client code presents the workload and reviewer credentials with operation `op-pr42-001`, run `R42`, delegation `D42`, and this illustrative request:

```json
{
  "tool": "pull_request_read",
  "arguments": {"repository": "acme/payments", "number": 42}
}
```

The broker checks main's list against the main's effective ceiling and this read against the reviewer's, following the [definition-derived ceiling rules](mcp-authorization.md#definition-derived-ceiling). Both calls require valid workload and actor credentials, execution-context checks, and authorization to use the ToolHive subject and provider connection; in v1, user, run, and delegation remain mecak8s-reported. The reviewer ceiling gives the same result for PR 43 on this server/tool pair: v1 does not verify PR-specific bounds from `D42`. Existing connection and downstream checks still apply; see [two requests, two authorization decisions](mcp-authorization.md#two-requests-two-authorization-decisions).

**Illustrative broker evidence:** `op-pr42-001` passed policy revision `review-ceiling-r17-g1` without evaluation errors and was allowed after the required connection and context checks. The workload and reviewer credential were verified; Alice remains API-reported and `D42` reported execution context. This decision proves neither dispatch nor success. The [audit evidence shape](audit.md#illustrative-authorization-evidence) owns the full attribution, request, attempt, policy, and redaction requirements.

Recovered authority remains subject to the original bounds described in [pauses and restart](#pauses-and-restart).

### (7) Token exchange and delegated MCP access token

In this token exchange, ToolHive exchanges the user access token and agent-instance JWT-SVID for the access token that the broker presents to vMCP. This example proposes the [RFC 9068 JWT access-token profile](https://www.rfc-editor.org/rfc/rfc9068.html#section-2) with [RFC 8693 delegation claims](https://www.rfc-editor.org/rfc/rfc8693.html#section-4.1). It is a proposed contract, not a claim about existing ToolHive behavior. The exchange output is an OAuth access token, not a JWT-SVID.

Before reading the token: `sub=th-user-731` names Alice's assumed authorized subject in ToolHive's namespace, not `(https://login.example.com, alice-204)` from her company login. `act.sub` names the reviewer instance executing `R42` under delegation `D42`; `client_id=mecatl-broker` names the broker requesting the token, not another agent or user. The token is for the same PR read at `https://mcp.example.com/payments/mcp` through `github-conn-42`; it does not itself establish that connection association.

The decoded header and payload below use fictional values. A real token is signed; its signature and serialized bearer value are omitted here.

**Header:**

```json
{
  "typ": "at+jwt",
  "alg": "RS256",
  "kid": "toolhive-example-key-1"
}
```

**Payload:**

```json
{
  "iss": "https://toolhive.example.com",
  "sub": "th-user-731",
  "act": {
    "sub": "spiffe://agents.example.com/mecatl/agent/system/main/inst/inc_cejrv7re4uy3ubxyzj2gfxfp2u/child/project/code-reviewer/inst/inc_gh56irfcpnupjqgfdw7dhzzw4m"
  },
  "client_id": "mecatl-broker",
  "aud": "https://mcp.example.com/payments/mcp",
  "iat": 1800000000,
  "exp": 1800000300,
  "jti": "d680052b-7081-41aa-b777-147239446ab3"
}
```

ToolHive AS validates the user access token, the instance SVID for its own audience, and the broker's client authentication. It checks that the broker may present this actor and that the requested MCP target and access bounds are authorized before issuing the token.

The audience is vMCP, not GitHub. The token lasts five minutes. No scope was requested in this example; operation policy still bounds access, so omitting `scope` does not grant unrestricted access.

**Illustrative AS evidence:** exchange `exchange-42` records the verified user, actor, client, target, and issued token's `(iss, jti)`. Caller-supplied run context remains reported. The broker holds the bearer outside the harness and limits reuse to the applicable execution context.

See [downstream token exchange](#downstream-token-exchange) for token correlation and actor-history limits, and [sender binding](#sender-binding) for replay protection beyond this bearer example.

### (8) Dispatch and observed outcome

The broker presents the delegated token from step (7) to vMCP with the PR 42 request. vMCP checks the expected token type, trusted issuer, allowed algorithm, signature against configured issuer keys, audience, and expiry before relying on its claims. It then checks the operation and arguments against policy and verifies authorization to use `github-conn-42`.

ToolHive supplies the provider credential from custody for the authorized target. GitHub validates that native credential, not the SVID or delegated MCP token.

**Illustrative vMCP evidence:**

```json
{
  "event": "vmcp.authorization",
  "time": 1800000002,
  "operation_id": "op-pr42-001",
  "provider_connection": "github-conn-42",
  "token_ref": {
    "iss": "https://toolhive.example.com",
    "jti": "d680052b-7081-41aa-b777-147239446ab3"
  },
  "validated_identity": {
    "subject": "th-user-731",
    "actor": "spiffe://agents.example.com/mecatl/agent/system/main/inst/inc_cejrv7re4uy3ubxyzj2gfxfp2u/child/project/code-reviewer/inst/inc_gh56irfcpnupjqgfdw7dhzzw4m",
    "oauth_client": "mecatl-broker"
  },
  "request": {
    "tool": "pull_request_read",
    "arguments": {
      "repository": "acme/payments",
      "number": 42
    }
  },
  "authorization": {
    "decision": "allow",
    "source": "vmcp-policy-evaluation"
  }
}
```

This illustrative record captures validated claims and vMCP's policy decision, not a bearer credential or proof of dispatch.

**Illustrative outcome evidence:** the broker and vMCP record their dispatch attempts and observed responses for `op-pr42-001`. vMCP retains provider reference `provider-request-42` only if returned and safe to store. The broker records the MCP response it received; the API returns a sanitized answer without credentials in model history. A successful response describes what the observer received, not guaranteed provider-side effects or a complete provider audit trail. Missing completion leaves an unknown outcome; see [pauses and restart](#pauses-and-restart).

The [general audit contract](audit.md) requires durable decision, dispatch, and outcome evidence. Persistence ordering and authenticated cross-service propagation remain unspecified; recipients must label propagated context as reported unless independently verified.

## User identity

Mecatl identifies the authenticated user by the issuer and subject of the validated login token, represented by `session.Principal`. The user's association with the acting agent must remain traceable through delegation and audit. This design does not introduce a user SPIFFE path or a separate user SVID; the agent-instance SPIFFE ID identifies the agent, not the user.

The Mecatl user identity and the ToolHive user subject are not necessarily the same identifier. Their association with the authorized provider connection must be established rather than inferred from matching names or subjects.

The authority design proposes trusting Mecatl's authenticated API component to report the initiating user to the broker, rather than forwarding the original login token. The broker authenticates the reporting workload, but this does not independently prove the user's authentication. How the report is protected from substitution by the untrusted agent loop remains to be specified. Audit records must distinguish this reported identity from facts the broker verified itself.

## Credential custody

Human login tokens terminate at Mecatl's API. They are validated for incoming requests, not retained for downstream access or scheduled execution.

Mecatl's client code holds and presents agent-instance credentials without exposing them to the model. They stay out of tool arguments, results, conversation history, logs, and environment variables. Keeping credentials hidden from the model does not protect them from a compromised harness process or isolate agents sharing that process.

The broker's issuer holds the agent signing authority. Mecatl receives signed instance credentials and public verification keys, never the issuer's private signing key.

The broker authenticates as OAuth client `mecatl-broker` to ToolHive AS; ToolHive authenticates separately to the provider. Operators provision each client's authentication material and server-side registration. These references leave the authentication methods open; a client ID alone is not a credential.

| OAuth client | Client ID | Authentication material held by that client |
| --- | --- | --- |
| Broker → ToolHive AS | `mecatl-broker` | `<BROKER_CLIENT_CREDENTIAL_REF>` |
| ToolHive → provider AS | `<TOOLHIVE_PROVIDER_CLIENT_ID>` | `<TOOLHIVE_UPSTREAM_CLIENT_CREDENTIAL_REF>` |

Enrollment yields these opaque credential placeholders; their token formats are not selected:

```json
{
  "toolhive": {
    "access_token": "<TOOLHIVE_USER_ACCESS_TOKEN>",
    "refresh_token": "<TOOLHIVE_USER_REFRESH_TOKEN>"
  },
  "provider": {
    "access_token": "<PROVIDER_ACCESS_TOKEN>",
    "refresh_token": "<PROVIDER_REFRESH_TOKEN_IF_ISSUED>"
  }
}
```

This groups examples for reading, not storage or transport. Each AS validates its OAuth client's authentication against its registration. ToolHive AS validates the ToolHive user grant, subject, intended use, and validity; the provider validates its native access token. Enrollment requires OAuth transaction protections; callbacks and code redemption are omitted. Client credentials and grants stay outside the harness and model.

The broker holds its ToolHive user access and refresh tokens. ToolHive holds, refreshes, and injects native provider credentials for the authorized connection. Neither Mecatl nor the model receives those native provider credentials. This boundary concerns protected tool access; it does not relocate other harness credentials, such as those used to call an LLM provider.

Agent-instance identity does not protect against compromise of the broker's signing authority or provider-credential storage. Separating issuance, authorization, and custody into modules does not create security boundaries against compromise of their shared process or host.

## Credential lifetimes and renewal

An agent instance keeps its identity across runs, while its short-lived instance JWT-SVID can expire and be renewed. Renewing that credential establishes continued use of the same identity; the broker still authorizes each MCP tool call.

A single run may need several delegated access tokens, either for different protected MCP endpoints or because an earlier token expires. Each token has its own audience, expiry, and access limits. Obtaining or refreshing one does not widen permission to call tools.

The broker presents its ToolHive refresh token to ToolHive AS with required client authentication to renew the user access token. ToolHive similarly presents an optional provider refresh token to the provider AS with required upstream client authentication. Each AS validates its refresh grant; custodians must safely store rotated replacements. Without a provider refresh token, renewal requires another supported path or enrollment.

The user's login token, the agent-instance credential, and downstream access tokens have separate lifetimes. Expiry of the login token used to start work does not, by itself, end that work. A provider connection can also remain available after that login token expires. Neither continued connectivity nor successful token refresh authorizes a later tool call without the broker's policy checks.

Ending a run in the harness does not automatically invalidate tokens already issued. A recipient that validates a self-contained bearer token without checking current authorization state may accept it until expiry. The mechanism for communicating cancellation or withdrawal to the broker and downstream recipients remains to be specified.

## Pauses and restart

The broker's session reference and broker session incarnation are separate from the host session ID and [agent-instance incarnation](agent-definitions.md#definitions-and-executing-instances). An attachment binding identifies the exact logical attachment incarnation; separate recovery references and continuity checks govern stored-credential recovery. An existing stable broker session can recover a new process-local attachment and binding through those checks. Expired custody withdraws connected authority without necessarily destroying the broker session. Those references do not combine user, agent, definition, and authority into one record.

Workload-only session access checks the exact authenticated workload and configured deployment, rejecting caller-supplied owner assertions. A replacement Pod with the same workload identity can serve that partition; a different identity cannot assume access.

An approval wait or provider-enrollment pause leaves the current run unfinished. Before dispatching the pending MCP operation, the broker checks it against the applicable authorization policy. Connecting a provider account does not approve the pending operation.

After restart, recovering credentials is not enough to continue safely. The system must recover the unfinished execution context and establish the association between the user, acting agent, and provider connection. If that association cannot be established, the tool call must fail closed. The harness determines whether execution resumes; the broker authorizes subsequent MCP calls.

Under the [identity and selection continuity rules](agent-definitions.md#identity-continuity-across-runs-and-restarts), continuation retains the unfinished run's original bounds. In v1, the broker rechecks the stored effective ceiling and existing connection restrictions; the harness retains its run/delegation restrictions. Broker evaluation of task-specific delegated bounds is deferred. Credential renewal does not widen recovered authority.

An operation that may have been dispatched but has no recorded completion has an unknown outcome. Restoring credentials does not make replay safe. Reconciliation requires evidence about the original operation, such as provider status or an applicable idempotency mechanism.

## Scheduled runs

A scheduled invocation executes without a live interactive user. Its recorded owner identifies on whose behalf the work was configured; audit records must distinguish scheduled execution from a fresh human request.

Human login tokens are not retained for later invocations. Provider credentials remain in broker/ToolHive custody. A saved owner or an available provider connection does not, by itself, authorize an MCP operation.

The harness starts and manages scheduled runs. The broker authorizes their MCP tool calls. The mechanism for establishing unattended authority is a separate design concern.

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

## Downstream token exchange

To obtain delegated access to a protected MCP endpoint, the broker presents three distinct credentials to ToolHive's authorization server: its ToolHive user access token as `subject_token`, the agent-instance JWT-SVID as `actor_token`, and its own OAuth client authentication.

The instance credential must be valid for presentation to ToolHive's authorization server. Acceptance by the broker alone does not establish that audience.

ToolHive issues a delegated access token identifying the user in `sub`, the acting instance in `act.sub`, and the broker OAuth client in `client_id`. Its audience is the protected MCP endpoint, not the external provider such as GitHub. These identities do not by themselves authorize particular tool arguments; the broker still checks the requested MCP operation.

The protected MCP endpoint validates the delegated token and applies operation policy. ToolHive resolves the authorized provider connection and uses its native credential for the external call. Validated claims identify the user and current actor directly; permissions and account associations can still require authoritative records.

The issuer-qualified `jti` supports token correlation but does not prevent replay or replace a per-operation identifier: several calls can reuse one token. The walkthrough's shared operation ID expresses desired correlation, not an established authenticated join between services.

The token's single `act` object identifies the current actor. RFC 8693 nested `act` objects describe previous actors and are informational for access control. Creation ancestry and current execution delegation remain separate evidence, not actor history inferred from the SPIFFE path or copied into nested claims.

## Provider-connection selection

ToolHive resolves the provider connection from validated identity and authoritative connection records.

The model cannot select another user's credentials or redirect them by supplying a URL, header, audience, or credential selector. The broker and ToolHive resolve the target and credentials within the authorized connection.

If the required identity, connection, or authorization evidence is missing, invalid, stale, or revoked, the call fails closed. It must not fall back to a service credential, another user, or a more privileged agent. A denied tool call dispatches no backend operation.

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

The walkthrough's delegated token is a bearer without sender binding. Its signature protects the claims from alteration but does not prove which holder presented it.

Workload-level DPoP is follow-on work to bind the broker-facing OAuth credential to a presenting worker's key and reduce replay of stolen access tokens. It does not establish which agent inside that worker made a call, protect against a compromised worker using its own key, or bind the MCP arguments. Agent-instance identity and tool-call authorization remain separate checks.

Key custody, renewal, restart behavior, and replay handling belong in the implementation design.
