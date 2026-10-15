# Agent identity and authority

This document proposes how Mecatl identifies **who is acting** in a multi-user, multi-session deployment: a user starts an agent, the agent delegates to subagents, each delegation stays within its parent's authority, and the resulting actions leave a durable audit trail. The goal is to let an operator trace an action back to the agent and user behind it, with authorization enforced outside the agent loop.

**Workload identity alone cannot answer these questions.** It identifies the Mecatl service, but one service can host many users, agent definitions, and running instances. This architecture gives those agents identities of their own and connects them to the users they act for. It follows an agent from the trusted definition loaded at session creation, through its runs and delegated child instances, to the tool calls made on its behalf, which may use a human user's credentials to access an external service.

*This document describes the intended architecture, not current implementation status.*

## Why this matters

Suppose Alice asks her main agent to review PR 42. It delegates to `code-reviewer`, which calls an MCP tool using Alice's GitHub credentials. GitHub sees access under Alice's account. That alone does not tell an auditor which agent made the call, why it had access, or whether the action stayed within the delegated task.

Four problems follow:

- **An agent name is only a claim until its definition is established.** An audit record naming `code-reviewer` is useful only if we can establish which definition it refers to, where that definition came from, and what was loaded for the session. Those facts need a trusted source outside the agent loop.
- **The user's credentials hide the acting agent.** We need to preserve both identities: whose access was used and which agent exercised it.
- **Access to a user's credentials is not permission to exercise all of their authority.** A reviewer must remain limited to reviewing even if Alice's GitHub credential permits merging. The design needs to bring user permissions and agent permissions together, and delegation must preserve or narrow the resulting authority.
- **The harness cannot be its own independent witness.** Its execution log helps explain what it attempted, but evidence of an external action must come from the component that checked and dispatched it, together with the observed result.

These guarantees also depend on the path an operation takes: a call that bypasses the authorization boundary escapes its checks and audit trail.

The architecture must connect these facts. An auditor should be able to follow Alice's request through the delegation to the tool call, identify the authority under which it was permitted, and distinguish a successful operation from a denied call or an unknown outcome.

## The identity model

A definition describes a kind of agent, such as `code-reviewer`. Its source is part of that identity: a repository's definition and an administrator's definition with the same name are not interchangeable.

An instance is one use of a definition in a session. Alice and Bob can use the same definition but have distinct instances. Restoration preserves an instance; a new session creates another.

A run is one execution by an instance. Reviewing PR 42 and later PR 57 uses the same reviewer identity but separate authorization contexts. A delegated child has its own instance and run, linked to the creating instance and delegating run.

The user is the person on whose behalf the agents act. The workload is the Mecatl service hosting them, potentially serving many users at once.

## Establishing an agent's identity

The [agent-definition loader](agent-definitions.md#loading-definitions) resolves definitions from configured sources, such as administrator-managed files, repositories, or APIs; the loop cannot establish identity by supplying a name. The selected definitions stay fixed for the session. Their sources and revisions must remain available across pauses and restoration, so repository changes cannot silently replace them.

The existing `AgentDef` structure currently combines instructions, execution settings, and tool configuration. This design proposes a split that gives the harness the configuration needed to execute the agent and the broker the definition records and authority ceilings needed to authorize MCP calls. Source and revision records still need a representation. Changing a turn limit should not redefine the agent's identity; loading a definition should not grant access to a user's accounts.

## Where trust lives

The agent loop uses those definitions to perform work, but it cannot decide which identities or permissions the rest of the system should trust. Definition loading, authorization, and audit therefore need trusted responsibilities outside the loop.

The harness hosts the API and agent loop and validates the initiating user's AS-1 access token. An **agent-definition loader**, a broker module, loads definitions from configured sources into the broker and supplies the harness with execution configuration. An **identity broker** is a trusted component that governs access to protected tools. It starts as modules inside the mecak8s process and later becomes a separate service; see [deployment and threats](#deployment-and-threats). It issues agent credentials, checks requested operations against policy, and arranges downstream access using credentials it controls. It also records the decisions and actions it observes.

This design scopes broker authorization to MCP tools. Extending that boundary to filesystem operations, Shell, and agent spawning is a [future direction](all-tool-mediation.md); their authorization and routing remain to be designed.

The broker does not decide whether agents start or whether runs execute; the harness owns agent lifecycle and run execution. Registering an instance with the broker records its identity and grants no permission to run.

```mermaid
flowchart LR
    U["Client"] -->|"Sign-in"| A1
    U -->|"Session creation and requests, AS-1 access token"| H["mecak8s"]
    F["Agent-definition sources"] -->|"Global definitions at startup"| R
    R -->|"Execution configuration"| H
    subgraph B["Identity broker"]
        R["Agent-definition loader"]
        I["Authorization and audit<br/>OAuth client mecatl"]
        A1["AS-1: login and ID-JAG issuer"]
        R -->|"Definition records and ceilings"| I
    end
    A1 -->|"Redirect to company sign-in"| IdP["Company IdP"]
    subgraph W["vMCP gateway (embedded or external)"]
        T["Gateway AS (AS-2) and credential storage"]
        V["vMCP"]
        T --- V
    end
    H -->|"Workload-authenticated instance registration by definition reference"| I
    H <-->|"Workload-authenticated MCP calls; AS-1 access token once at session open; agent SVID per call"| I
    I -->|"RFC 8693: session refresh token, then ID-JAG per call"| A1
    I -->|"RFC 7523: redeem ID-JAG for V"| T
    I <-->|"Authorized MCP calls with V"| V
    V <-->|"Protected calls"| P["MCP backends / providers"]
    I -->|"Decisions and observed outcomes"| A["Audit records"]
```

The diagram shows the broker-backed remote architecture's MCP path. Local deployments can retain direct tool access; protected operations in the remote deployment must pass through the broker.

At startup, the broker's [definition loader](agent-definitions.md#loading-definitions) loads the global agent definitions from configured sources and records their sources, revisions, and ceilings in the broker. How other definitions, such as repository definitions, are loaded is not yet decided. Before the agent loop starts, mecak8s's trusted session lifecycle code persists the session's fixed definition selection and [registers the main instance](agent-definitions.md#registering-instances) with the broker under workload authentication, naming its definition by reference. The registration API, durable schema, verified per-operation binding for reported context, and protection from same-process substitution remain open.

The broker contains the definition loader, the agent trust issuer, authorization and audit logic, and AS-1, Mecatl's login authorization server. The vMCP gateway contains AS-2, provider credential storage, and the vMCP. The gateway can run inside the Mecatl deployment or as an external gateway.

Alice signs in through AS-1, which redirects her to the company IdP. Her client then calls mecak8s with an AS-1 access token. mecak8s validates that token and current session ownership. At session open it passes the token to the broker once, and the broker exchanges it at AS-1 for a refresh token bound to that broker session. On each tool call the broker presents that refresh token and the acting agent's JWT-SVID to AS-1, which returns an ID-JAG naming Alice and the agent. The broker redeems the ID-JAG at AS-2 for the vMCP access token `V`. The broker is a single OAuth client, `mecatl`, at both AS-1 and AS-2, and authenticates with `private_key_jwt`. AS-2 maps the AS-1 subject to its native user; an external gateway also requires a one-time consent link created at AS-2's own login. AS-2 receives neither Alice's AS-1 token nor the agent SVID. See [credentials and lifecycle](credentials-and-lifecycle.md#interactive-execution) for the walkthrough and [MCP gateway modes](mcp-gateway-modes.md) for the gateway side. The tools themselves execute the operations.

AS-1 witnessed Alice's sign-in itself, checks that the acting instance is registered to the broker session, and authorizes the client, subject, actor, and target. It does not prove that the agent registration, ancestry, run, or delegation reported by trusted lifecycle code is genuine. The agent loop must not choose those facts, and distinct agent credentials do not isolate agents sharing one `mecak8s` process.

## Deployment and threats

The broker starts as modules inside the mecak8s process: broker core, AS-1, the agent-instance issuer, and the definition loader. Later these modules move together into one separate broker service. The protocol and token formats are the same in both deployments; what changes is which process holds the broker's keys, refresh tokens, and records.

Some protections do not depend on where the broker runs:

- **Hostile workspace code** runs in executor Pods, behind the execution provider. In both deployments, executors get no broker credentials, no database credentials, and no network path to mecak8s internals or the KMS.
- **A prompt-injected agent** never sees or chooses credentials. Harness code attaches them, and the model supplies only tool names and arguments.
- **The user comes from a sign-in at AS-1**, not from a harness report. mecak8s passes on the AS-1-signed access token once, AS-1 converts each token at most once, and every ID-JAG names the user from AS-1's record. No mecak8s code path can name a user who did not sign in.

Where the broker runs matters against one attacker: someone with code execution in the mecak8s process.

| Threat from a compromised mecak8s | Broker inside mecak8s | Separate broker service |
| --- | --- | --- |
| Act as any user, by signing an ID-JAG for a user who is not present | Open. The process holds AS-1's signing key and the broker's client key. In embedded mode this reaches every user connected to the gateway; in external mode, only users with a consent link. | Narrowed. mecak8s holds no keys. It can act only in open broker sessions bound to its workload, until each closes or its refresh token's hard expiry passes, and it can open sessions with fresh tokens from users currently using it. |
| Steal refresh tokens or signing keys | Open. A KMS prevents copying a key out, not signing with it while the attacker is inside. | Closed, if the broker has its own database credentials and only the broker's service account may sign with its KMS keys. |
| Read users' provider tokens in embedded mode | Open, if the gateway store shares storage credentials with mecak8s. | Closed, if the embedded gateway is its own deployment with its own credentials. |
| Tamper with broker and AS-1 audit records | Partly closed. Records already delivered to an external append-only store survive; records from the compromised process can be forged or suppressed from then on. | Closed for broker and AS-1 records, which mecak8s no longer writes, if they go to an append-only store that mecak8s cannot write. |

In both deployments, the broker takes these facts on mecak8s's word: which agent instance made a call, which loaded definition a session uses as its root, which instance created which child, the run and delegation context, and which open session a call belongs to. The broker loads global definitions itself, so mecak8s cannot supply their content or ceilings. Audit records mark the facts mecak8s reported.

A separate broker service delivers its protections only if the split is real:

- the broker has its own database, KMS permissions, and namespace;
- mecak8s cannot create pods, read secrets, or change configuration in the broker's namespace;
- mecak8s cannot write the broker's workload allowlist, trust bundle, or policy inputs;
- the embedded gateway is its own deployment.

While the broker runs inside mecak8s, the [modulith constraints](implementation.md#modulith-constraints) keep the later move a deployment change.

## Following an action through the architecture

Alice asks her main agent to list pull requests and review PR 42. Once the session's definitions are loaded, the main calls `pull_request_list`, then delegates to `code-reviewer`. The reviewer calls `pull_request_read` for PR 42. Each call requires its own authorization decision.

The broker checks the reviewer's effective server/tool ceiling, its definition ceiling narrowed by the main's, with Cedar. User and connection are enforced downstream: AS-2 redeems only for a mapped, and in external mode linked, user, and vMCP dispatches only with Alice's stored credential. This ceiling check is part of the [authority work](#order-of-work), which follows the identity work. Even then, the design does not evaluate PR-specific delegated bounds or comprehensive enterprise user policy. If all required checks pass, ToolHive dispatches the call through the authorized connection using its native provider credential. That credential stays outside the harness and model; in embedded mode it can reside inside the Mecatl deployment. If denied, no request reaches GitHub.

GitHub sees access under Alice's account. The broker's audit record supplies the rest of the story: which reviewer acted, which agent delegated the work, why the call was permitted, and what result was observed. A missing result is recorded as unknown, not as proof that the operation never happened.

See [Interactive execution](credentials-and-lifecycle.md#interactive-execution) for the detailed credentials sequence.

## Order of work

Mecatl builds this design in two steps. The identity work comes first, and the authority work follows once the identity work is in place.

The **identity work** establishes who acts and records it:

- the broker modules: definition loader, agent-instance issuer, AS-1, and broker core;
- definitions loaded with their provenance;
- instance registration, with the incarnation, definition reference, and creation parent;
- agent-instance credentials;
- the credential flow: sign-in at AS-1, the session-bound refresh token, ID-JAG issuance, AS-2 redemption, and per-user provider credentials at the gateway;
- the broker's identity checks: session, workload, and that the acting instance is registered to the broker session;
- sign-out and revocation: signing out everywhere stops every agent and schedule acting for the user, and AS-1's upstream re-check does the same when the company IdP disables the account;
- audit of who acted, for whom, through which delegation, and what was decided, dispatched, and observed.

The **authority work** decides what each agent may do:

- resolving each definition's ceiling into permitted MCP server/tool pairs;
- each instance's effective ceiling, its definition ceiling narrowed by its parent's;
- Cedar policy generation and a ceiling check on every call;
- task-specific delegation bounds, and user, resource, and argument policy.

Until the authority work is in place, the harness's existing delegation restrictions are the only ceiling checks; the broker adds none. Registration already records the definition reference and creation parent, so the authority work adds computation, not a migration. The chapters describe the whole design and say which parts belong to the authority work.

## Detailed chapters

The proposed chapters develop each part of the architecture.

| Chapter | Focus |
| --- | --- |
| [Audit](audit.md) | Define attribution, correlation, evidence, redaction, and durability requirements. |
| [Agent definitions](agent-definitions.md) | Explain `AgentDef` and separate harness execution configuration from broker-enforced authority. |
| [MCP authorization](mcp-authorization.md) | Explain how user and agent permissions govern MCP calls and how the broker uses ToolHive. |
| [MCP gateway modes](mcp-gateway-modes.md) | Chain the broker's grant issuer to the vMCP gateway's authorization server, so one protocol works with an embedded or an external gateway. |
| [Credentials and lifecycle](credentials-and-lifecycle.md) | Follow one credential-to-outcome walkthrough with validation and audit evidence, then explain renewal and recovery. |
| [All-tool mediation](all-tool-mediation.md) | Develop the broader authorization boundary for filesystem tools, Shell, and spawning. |

The [implementation and verification companion](implementation.md) records integration requirements and checks. This broker-centered architecture supersedes the earlier identity model's harness-owned signing and audit responsibilities; the donor documents are retained in Git history.
