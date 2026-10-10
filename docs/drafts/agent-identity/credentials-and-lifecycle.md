# Credentials and lifecycle

*This chapter describes the intended architecture, not current implementation status.*

An agent instance can participate in multiple runs. Its instance credential identifies it to the broker; presenting that credential does not by itself authorize an MCP tool call. The harness manages run execution, while the broker authorizes MCP tool calls using the acting instance's identity and the applicable user, delegation, and run context. Renewing the instance credential does not itself extend or widen permission to call tools.

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

Run IDs, session attachments, and broker context references are separate from credentials. Their precise purpose remains to be designed; they do not imply that the broker authorizes run execution.

## Interactive execution

Alice asks a reviewer to read PR 42 in `acme/payments`. The session's definitions have already been loaded into the harness and broker. Mecatl starts and manages run `R42`; the broker authorizes its tool calls. `R42` is an illustrative execution identifier, not an authorization credential.

Instance credential issuance, provider enrollment, and token exchange occur only when needed. The broker checks every requested operation (see [delegated authority](mcp-authorization.md#delegated-authority) and [operations and resources](mcp-authorization.md#operations-and-resources)).

```mermaid
sequenceDiagram
    actor U as Alice / human client
    participant H as Mecatl API and dispatch
    box Combined broker
        participant I as Agent-instance issuer
        participant B as Tool authorization / broker OAuth client
        participant A as ToolHive AS / upstream OAuth client
        participant C as ToolHive upstream token storage / reader
        participant V as Protected vMCP
    end
    participant P as GitHub provider

    Note over H,I: Session definitions initialized before the agent loop starts
    U->>H: Read acme/payments PR 42, login bearer
    H->>H: Validate login and session owner; start reviewer run R42

    opt No suitable valid instance JWT-SVID
        H->>I: Authenticated workload + instance credential request
        I->>I: Check instance and issuance limits against trusted definition records
        I-->>H: Short-lived instance JWT-SVID
        H->>H: Validate credential and expected instance
    end

    H->>B: Authenticated workload + reported initiating user + instance SVID + execution context
    Note over H,B: User report is a trust assumption, not independent proof of login

    opt Provider connection needed
        B-->>H: Provider enrollment required
        H->>H: Pause pending work
        H-->>U: Broker enrollment link
        U->>A: Authorize broker OAuth client using broker state and PKCE
        A-->>U: Redirect to provider authorization
        U->>P: Approve provider connection
        P-->>U: Redirect with provider authorization code
        U->>A: ToolHive callback with provider state and code
        A->>A: Verify upstream OAuth transaction
        A->>P: Redeem provider code as ToolHive upstream client
        P-->>A: Native provider access/refresh tokens
        A->>C: Store native provider grant
        A-->>U: Redirect with ToolHive authorization code
        U->>B: Broker callback with ToolHive code and broker state
        B->>B: Consume one-time state and retrieve retained PKCE verifier
        B->>A: Redeem ToolHive code with client authentication and verifier
        A->>A: Validate broker client and PKCE
        A-->>B: ToolHive user access/refresh grant
        B->>B: Establish authorized caller/account association
    end

    Note over H,B: Authorize the pending operation, including after enrollment
    H->>H: Apply local permissions and hooks
    H->>B: Workload + instance SVID + execution context + call ID + PR 42 arguments
    B->>B: Validate identity and context; check target, arguments and applicable policy

    alt Denied
        B-->>H: Denial without backend dispatch
        H-->>U: Refusal
    else Allowed
        opt No suitable valid delegated token
            B->>B: Obtain current ToolHive user access token from custody or refresh
            B->>A: User subject_token + instance actor_token + broker client authentication
            A->>A: Validate user token, actor credential, presenter and bounded access
            A-->>B: Delegated token with sub=user, act.sub=instance, client_id=broker
            B->>B: Restrict token use to the applicable execution context
        end
        B->>V: Delegated token + authorized PR 42 read
        V->>V: Validate token and operation policy; resolve authorized connection
        V->>C: Resolve valid upstream grant for authorized connection and target
        C-->>V: Native provider credential
        V->>P: Read PR 42 with injected provider credential
        P-->>V: PR 42 result
        V-->>B: Result
        B-->>H: Sanitized result and observed outcome
        H-->>U: Answer without credentials in model history
    end

    Note over H,B: Every subsequent tool call repeats authorization
```

- **User reporting:** the broker trusts the API component's report; protecting that report from substitution by the agent loop remains unresolved.
- **Account association:** completing OAuth enrollment does not itself establish that the initiating user may use the connected account. That authorization mechanism remains open.
- **Execution context:** the diagram does not select a binding format or registration API, and no broker response grants permission to execute a run.
- **Exchange audience:** the instance credential must be valid for ToolHive AS. The resulting delegated token targets the protected MCP endpoint, not GitHub.
- **Delegation and audit:** child calls carry their own instance and execution relationships. The broker records decisions, dispatch, and observed outcomes; provider-side correlation remains conditional on provider support.

## User identity

Mecatl identifies the authenticated user by the issuer and subject of the validated login token, represented by `session.Principal`. The user's association with the acting agent must remain traceable through delegation and audit. This design does not introduce a user SPIFFE path or a separate user SVID; the agent-instance SPIFFE ID identifies the agent, not the user.

The Mecatl user identity and the ToolHive user subject are not necessarily the same identifier. Their association with the authorized provider connection must be established rather than inferred from matching names or subjects.

The authority design proposes trusting Mecatl's authenticated API component to report the initiating user to the broker, rather than forwarding the original login token. The broker authenticates the reporting workload, but this does not independently prove the user's authentication. How the report is protected from substitution by the untrusted agent loop remains to be specified. Audit records must distinguish this reported identity from facts the broker verified itself.

## Credential custody

Human login tokens terminate at Mecatl's API. They are validated for incoming requests, not retained for downstream access or scheduled execution.

Mecatl's client code holds and presents agent-instance credentials without exposing them to the model. They stay out of tool arguments, results, conversation history, logs, and environment variables. Keeping credentials hidden from the model does not protect them from a compromised harness process or isolate agents sharing that process.

The broker's issuer holds the agent signing authority. Mecatl receives signed instance credentials and public verification keys, never the issuer's private signing key.

The broker holds its ToolHive user access and refresh tokens. ToolHive holds, refreshes, and injects native provider credentials for the authorized connection. Neither Mecatl nor the model receives those native provider credentials. This boundary concerns protected tool access; it does not relocate other harness credentials, such as those used to call an LLM provider.

Agent identity does not protect against compromise of the broker's signing authority or provider-credential storage. Separating issuance, authorization, and custody into modules does not create security boundaries against compromise of their shared process or host.

## Credential lifetimes and renewal

An agent instance keeps its identity across runs, while its short-lived instance JWT-SVID can expire and be renewed. Renewing that credential establishes continued use of the same identity; the broker still authorizes each MCP tool call.

A single run may need several delegated access tokens, either for different protected MCP endpoints or because an earlier token expires. Each token has its own audience, expiry, and access limits. Obtaining or refreshing one does not widen permission to call tools.

The user's login token, the agent-instance credential, and downstream access tokens have separate lifetimes. Expiry of the login token used to start work does not, by itself, end that work. A provider connection can also remain available after that login token expires. Neither continued connectivity nor successful token refresh authorizes a later tool call without the broker's policy checks.

Ending a run in the harness does not automatically invalidate tokens already issued. A recipient that validates a self-contained bearer token without checking current authorization state may accept it until expiry. The mechanism for communicating cancellation or withdrawal to the broker and downstream recipients remains to be specified.

## Pauses and restart

An approval wait or provider-enrollment pause leaves the current run unfinished. Before dispatching the pending MCP operation, the broker checks it against the applicable authorization policy. Connecting a provider account does not approve the pending operation.

After restart, recovering credentials is not enough to continue safely. The system must recover the unfinished execution context and establish the association between the user, acting agent, and provider connection. If that association cannot be established, the tool call must fail closed. The harness determines whether execution resumes; the broker authorizes subsequent MCP calls.

Continuation must preserve the unfinished run's original authorization bounds. The broker checks subsequent MCP calls against those bounds and applicable current policy. Restrictions or withdrawal can constrain continuation; credential renewal or policy expansion must not automatically widen the recovered authority.

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

A workload credential alone cannot distinguish `code-reviewer` from `deployer` when both run in the same Mecatl service. A self-declared agent name does not establish that distinction for broker policy.

## Downstream token exchange

To obtain delegated access to a protected MCP endpoint, the broker presents three distinct credentials to ToolHive's authorization server: its ToolHive user access token as `subject_token`, the agent-instance JWT-SVID as `actor_token`, and its own OAuth client authentication.

The instance credential must be valid for presentation to ToolHive's authorization server. Acceptance by the broker alone does not establish that audience.

ToolHive issues a delegated access token identifying the user in `sub`, the acting instance in `act.sub`, and the broker OAuth client in `client_id`. Its audience is the protected MCP endpoint, not the external provider such as GitHub. These identities do not by themselves authorize particular tool arguments; the broker still checks the requested MCP operation.

The protected MCP endpoint validates the delegated token and applies operation policy. ToolHive resolves the authorized provider connection and uses its native credential for the external call.

## Provider-connection selection

ToolHive resolves the provider connection from validated identity and authoritative connection records.

The model cannot select another user's credentials or redirect them by supplying a URL, header, audience, or credential selector. The broker and ToolHive resolve the target and credentials within the authorized connection.

If the required identity, connection, or authorization evidence is missing, invalid, stale, or revoked, the call fails closed. It must not fall back to a service credential, another user, or a more privileged agent. A denied tool call dispatches no backend operation.

## Instance identity and creation ancestry

The instance JWT-SVID identifies the acting agent instance through its SPIFFE ID. The proposed identity path records the instance's definition category and name, its session incarnation, and the ancestry of instances that created it.

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

Workload-level DPoP is follow-on work to bind the broker-facing OAuth credential to a presenting worker's key and reduce replay of stolen access tokens. It does not establish which agent inside that worker made a call, protect against a compromised worker using its own key, or bind the MCP arguments. Agent identity and tool-call authorization remain separate checks.

Key custody, renewal, restart behavior, and replay handling belong in the implementation design.
