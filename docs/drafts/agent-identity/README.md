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

The [agent-definition loader](agent-definitions.md#loading-definitions-for-a-session) resolves definitions from configured sources, such as administrator-managed files, repositories, or APIs; the loop cannot establish identity by supplying a name. The selected definitions stay fixed for the session. Their sources and revisions must remain available across pauses and restoration, so repository changes cannot silently replace them.

The existing `AgentDef` structure currently combines instructions, execution settings, and tool configuration. This design proposes a split that gives the harness the configuration needed to execute the agent and the broker the definition records and authority ceilings needed to authorize MCP calls. Source and revision records still need a representation. Changing a turn limit should not redefine the agent's identity; loading a definition should not grant access to a user's accounts.

## Where trust lives

The agent loop uses those definitions to perform work, but it cannot decide which identities or permissions the rest of the system should trust. Definition loading, authorization, and audit therefore need trusted responsibilities outside the loop.

The harness hosts the API and agent loop and authenticates the initiating user. An **agent-definition loader** supplies harness execution configuration and broker definition records and authority ceilings. An **identity broker** is a separate trusted service that governs access to protected tools. It issues agent credentials, checks requested operations against policy, and arranges downstream access using credentials it controls. It also records the decisions and actions it observes.

This design scopes broker authorization to MCP tools. Extending that boundary to filesystem operations, Shell, and agent spawning is a [future direction](all-tool-mediation.md); their authorization and routing remain to be designed.

```mermaid
flowchart LR
    U["Client"] -->|"Session creation and requests"| H["mecak8s"]
    F["Agent-definition sources"] --> R["Agent-definition loader (mecak8s lifecycle)"]
    R -->|"Execution configuration"| H
    subgraph B["Identity broker"]
        I["Identity issuance and authorization"]
        G["Grant issuer (AS-1)"]
        I --> G
    end
    subgraph W["vMCP gateway (embedded or external)"]
        T["Gateway AS (AS-2) and credential storage"]
        V["vMCP"]
        T --- V
    end
    R -->|"Workload-authenticated definition and instance registration"| I
    H <-->|"User report, tool requests, and results"| I
    G -->|"Chaining grant"| T
    I <-->|"Authorized MCP calls"| V
    V <-->|"Protected calls"| P["MCP backends / providers"]
    I -->|"Decisions and observed outcomes"| A["Audit records"]
```

The diagram shows the broker-backed remote architecture's MCP path. Local deployments can retain direct tool access; protected operations in the remote deployment must pass through the broker.

Before the agent loop starts, mecak8s's trusted session lifecycle code [loads and persists the fixed definition selection](agent-definitions.md#loading-definitions-for-a-session), supplies execution configuration, and [registers definitions and the main instance](agent-definitions.md#registering-instances) with the broker under workload authentication. This loader responsibility is outside the untrusted loop, not a separate registrar or an isolation boundary. The registration API, durable schema, verified per-operation binding for reported context, and protection from same-process substitution remain open.

The broker contains the agent trust issuer, the authorization and audit logic, and a grant issuer (AS-1). The vMCP gateway contains its own authorization server (AS-2), the provider credential storage, and the vMCP. The gateway can run inside the Mecatl deployment or be operated separately, such as an enterprise connector gateway. In both cases AS-1 vouches for the user and acting agent with a short-lived grant that AS-2 redeems; see [MCP gateway modes](mcp-gateway-modes.md). The tools themselves execute the operations.

The remaining trust question is how the broker knows that the reported user and agent definition are genuine. The agent loop must not be able to substitute either. Giving agents distinct credentials helps identify them, but does not isolate agents sharing one `mecak8s` process.

## Following an action through the architecture

Alice asks her main agent to list pull requests and review PR 42. Once the session's definitions are loaded, the main calls `pull_request_list`, then delegates to `code-reviewer`. The reviewer calls `pull_request_read` for PR 42. Each call requires its own authorization decision.

The broker checks the reviewer's effective server/tool ceiling, its definition ceiling narrowed by the main's, with Cedar and retains existing user/connection checks. V1 does not evaluate PR-specific delegated bounds or comprehensive enterprise user policy. If all required checks pass, ToolHive dispatches the call using Alice's GitHub credential, which stays outside `mecak8s`. If denied, no request reaches GitHub.

GitHub sees access under Alice's account. The broker's audit record supplies the rest of the story: which reviewer acted, which agent delegated the work, why the call was permitted, and what result was observed. A missing result is recorded as unknown, not as proof that the operation never happened.

See [Interactive execution](credentials-and-lifecycle.md#interactive-execution) for the detailed credentials sequence.

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
