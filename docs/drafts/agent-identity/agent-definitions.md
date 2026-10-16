# Agent definitions

*This chapter describes the intended architecture. The `AgentDef` inventory below describes the existing code; the harness/broker split is proposed.*

## Definitions and executing instances

An agent definition describes how an agent should perform work. Mecatl represents it with `AgentDef`, a value containing metadata and instructions, rather than an executing loop. This chapter uses the following vocabulary:

| Term | Meaning |
| --- | --- |
| Definition | A kind of agent, identified with its source and admitted revision, not just its name. |
| Host session | The harness's persisted context for one agent instance, identified by a host session ID such as `hostS42`. A child instance has its own child session, linked to its parent's by `ParentSessionID`; a root session and its children form a session tree. |
| Broker session | The broker's session for one session tree, opened once for the root session, such as `A42`. Every instance in the tree registers to it. |
| Agent instance | One use of a selected definition, backed by its own host session, with its own identity and any creation-parent association. A delegated child has its own instance. |
| Run | One execution by an instance, such as `R41` or `R42`. An instance can perform multiple runs. |
| Execution context | The workload, acting instance, broker session, initiating user, run, and any delegation a protected call is made under. The user comes from the broker refresh token AS-1 issued at session open, after Alice signed in through AS-1; AS-1 also checks that the acting instance is registered to the broker session. Run and delegation context remain mecak8s-reported. See [authentication and execution context](mcp-authorization.md#authentication-and-execution-context). |
| Agent-instance incarnation | The durable identity allocated by mecak8s for that instance's lifetime, preserved across restoration and credential renewal. A fresh incarnation identifies a distinct instance. |
| Workload | The Mecatl service process hosting instances for many users and sessions, with its own infrastructure identity. |

The existing implementation persists a [session incarnation](../../../engine/session/incarnation.go) on each session aggregate, including child sessions. The proposal uses that durable incarnation for agent-instance identity; it is not the host session ID or a run ID. The broker's own session incarnation and attachment binding are separate [recovery concepts](credentials-and-lifecycle.md#pauses-and-restart).

## Relationships and lifetimes

The broker's [definition loader](#loading-definitions) loads definitions up front. An agent definition can be reused by many agent instances. Each instance has its own identity and persisted context, and can perform several runs over its lifetime.

```mermaid
flowchart LR
    L["Definition loader<br/>Broker module"]
    D["AgentDef<br/>Reusable definition"]
    S["Host session<br/>Persisted context"]
    I["Agent instance<br/>One particular agent"]
    R["Run<br/>One unit of work"]
    C["Child agent instance<br/>Own identity, session, and runs"]
    CR["Child run"]

    L -->|"loads at startup"| D
    D -->|"one definition, many instances"| I
    S ---|"backs this instance"| I
    I -->|"one instance, many runs"| R
    I -->|"may create child instances"| C
    C -->|"one instance, many runs"| CR
    R -.->|"delegates work to"| CR
```

Here, the host session is the persisted context for one agent instance. A child has its own child session. The root session and its children form the session tree, which shares one broker session. Each child follows the same relationships shown for the parent instance, and can use a different selected definition. A root instance has no creation parent; a child has one creation-parent link.

Reviewing PR 42 and later reviewing PR 57 can use the same reviewer instance in separate runs. The selected definition stays fixed for the instance. Pausing for approval and resuming continues the same run, including after restoration.

| Event | Instance identity | Run |
| --- | --- | --- |
| Start a new root session | Create a new main instance. | New work starts a run. |
| Assign more work to an existing instance | Keep the same instance and selected definition. | Start another run. |
| Create a child agent | Create a distinct instance with its own Session and a creation-parent link. | The child's work has its own run. |
| Pause and resume unfinished work | Keep the same instance. | Continue the existing run. |
| Restore after a process restart | Recover the original instance, selected definition, and creation-parent link. | Continue a recoverable unfinished run, or fail closed; later work starts a new run. |

Creation ancestry and execution delegation describe different relationships. The creation-parent link records which instance created a child. A delegation links the run assigning work to the run performing it. The child's identity remains stable across its runs; each run supplies its own execution context. See [registering instances](#registering-instances).

The [domain model](../../architecture/mecatl.modelith.md) provides the broader execution model; this view focuses on agent identity and continuity.

## What AgentDef contains

The existing [`AgentDef`](../../../engine/tool/agentsource.go) combines several concerns:

| Concern | Fields | Current purpose |
| --- | --- | --- |
| Naming and routing | `Name`, `Description` | Identify a definition and help the model select it. |
| Instructions and context | `Body`, `Skills`, `Memory` | Supply instructions, preloaded skills, and a memory-tier selection. |
| Model selection | `Model`, `Provider` | Configure the specialist's model and provider. |
| Tool configuration | `Tools`, `DisallowedTools`, `MCPServers` | Filter the specialist's tool catalog and configure its MCP servers. |
| Execution controls | `PermissionMode`, `MaxTurns`, `MaxToolCalls`, `Hooks` | Supply a session permission-mode hint, execution budgets, and lifecycle hooks. |
| Presentation | `Color` | Supply a UI hint. |
| Source tier | `Origin` | Record the admission tier: `explicit`, `project`, `user`, or `driver`. |

`Origin` is an observability label, not a source location or an authorization input. `AgentDef` has no definition-source identifier or revision field.

Tool catalog filtering and authority enforcement are distinct in the existing code. Composition registers the tools selected for the specialist. It also derives a harness-side authority ceiling from resolved tools and MCP capabilities for managed, explicitly sourced definitions. Delegation intersects that ceiling with parent and candidate authority. The ceiling is derived separately, not stored as a field in `AgentDef`, and this path does not establish broker enforcement.

## Harness configuration and broker authority

The proposed split keeps execution configuration in the harness and supplies the broker with the authority ceiling needed for independent MCP authorization. Tool information serves both components for different reasons: the harness configures which tools are available, while the broker decides whether a requested MCP call is allowed. Turn and tool-call budgets remain harness concerns.

The harness uses `Tools`, `DisallowedTools`, and `MCPServers` to configure the agent's available tools. The broker needs an independently established ceiling on permitted MCP operations. A tool appearing in the harness catalog is not sufficient authorization to call it, and a model-supplied tool list cannot establish the broker's ceiling.

The broker ceiling is the set of permitted registered MCP server/tool pairs resolved under the existing `Tools`, `DisallowedTools`, and `MCPServers` semantics. That fixed admitted ceiling drives [deterministic Cedar policy generation](mcp-authorization.md#definition-derived-ceiling), tied to the definition source and revision. At registration, the broker narrows it to each instance's [effective ceiling](mcp-authorization.md#delegated-authority). The existing definition-source snapshot contract is not, by itself, broker initialization.

## Loading definitions

The **definition loader** is a module of the identity broker. At broker startup it loads the global agent definitions from configured sources, such as administrator-managed files or APIs, and records each definition's source and revision in the broker. Once the [authority work](README.md#order-of-work) is in place, it also resolves and records each definition's ceiling. The operator's loader configuration then also says which loaded definitions may serve as a session's root, optionally per user or group, and caps the ceiling of definitions from other sources; the broker refuses to register a root outside that set. It also supplies the harness with execution configuration. While the broker runs as modules inside mecak8s, the loader runs there too; when the broker becomes a separate service, the loader moves with it. How other definitions, such as repository definitions, are loaded and when is not yet decided.

Each session uses a fixed selection of loaded definitions. mecak8s's trusted session lifecycle code persists that selection and names definitions to the broker by reference; it does not supply their content or ceilings.

For Alice's session `hostS42` in the [walkthrough](credentials-and-lifecycle.md#interactive-execution), the selection includes the main definition and `project/code-reviewer`. Selecting the reviewer definition does not create a reviewer instance. The particular child is allocated later when the harness creates it.

Today the root session has no `AgentDef`. In this design, a session started without a named definition uses the built-in `system/main` definition, and the default anonymous child uses `system/explorer`; neither requires a definition file. Built-in definitions are registered and ceiling-resolved like any other, with the Mecatl release that ships them as their source and revision. `system/main` grants no name-based privilege: Alice's and Bob's default mains share the definition but not instances, users, or authority. A session started with a named definition, such as `project/code-reviewer`, registers that definition as its main instead. Whether the harness constructs the main loop from a real `AgentDef` or a built-in equivalent is an implementation choice.

Before the main loop starts, mecak8s allocates and persists the main's durable agent-instance incarnation, then authenticates to the broker with its workload ServiceAccount to register the main instance with no parent, naming its definition by reference. Broker initialization and registration must complete before execution; a suitable instance SVID can be obtained when needed, before protected calls. The selected definitions remain fixed for the session so that the harness and broker use the same selection.

## Registering instances

The broker validates the workload-authenticated registration records and constructs the canonical instance subject. It checks that the workload may act for the session and instance and associates the supplied incarnation with the registered definition, source, revision, ceiling, and creating instance, if any, and stores the effective ceiling computed from them. mecak8s allocates incarnations; the broker does not. A known definition or loop-supplied name alone is insufficient for issuance.

When the main creates the reviewer, mecak8s allocates and persists the child's incarnation. It registers that particular child using the persisted incarnation, the reviewer definition already registered from the session selection, and the registered main-instance reference. The broker validates these records and constructs the child subject. This is a lifecycle assertion authenticated by mecak8s's workload credential, not proof derived from possession of the parent's SVID.

Creation ancestry records who created an instance. Execution delegation separately links parent run `R41`, reviewer run `R42`, and the bounds of delegation `D42`. Registration establishes identity records; it does not authorize spawning, run execution, or a tool call. The harness manages execution, and the broker [authorizes each MCP request](mcp-authorization.md#two-requests-two-authorization-decisions).

The broker explicitly trusts mecak8s's workload-authenticated lifecycle assertions. These responsibilities are outside the untrusted loop, but a ServiceAccount cannot distinguish trusted lifecycle code from compromised code in the same process. This design does not establish same-process isolation. The user side does not rest on mecak8s's reports: AS-1 witnessed Alice's sign-in and names her from the session's broker refresh token, but that does not prove run or delegation context. While the broker runs inside mecak8s, an attacker with code execution in that process also holds AS-1's keys; see [deployment and threats](README.md#deployment-and-threats). Unlike a SPIRE agent, which inspects a workload before issuing it an identity, the broker cannot observe mecak8s's instances; mecak8s is a trusted attestor, not an independently verified one. The [audit evidence table](audit.md#evidence-and-provenance) lists which registration facts the broker verifies and which it accepts as mecak8s assertions.

## Identity continuity across runs and restarts

An instance can perform several runs without changing identity. Its fixed definition selection provides continuity, while each call remains subject to applicable current user permissions and broker policy.

| Event | What remains fixed or must be checked |
| --- | --- |
| New run on the same instance | The incarnation and selected definition stay fixed. The new run has its own execution context and applicable bounds; the instance credential alone does not authorize it. |
| Definition source edited | The session's pinned source and revision selection does not change. Restoration must not substitute whatever is current at the source. |
| Process restart or session restoration | Restore the original incarnation, definition selection, parent association, stored effective ceiling, and harness delegation bounds. Recover the unfinished execution context and its user/actor/connection association or fail closed. |
| Instance credential expires | Renew for the registered identity after the required checks. Renewal grants no new authority and does not widen recovered bounds. |
| Fresh incarnation | Register a distinct identity. Reusing a session or definition name does not inherit the old instance's authority. |

Continuation preserves the unfinished run's original bounds. Current restrictions or withdrawal can constrain it; credential renewal or policy expansion must not automatically widen recovered authority. If the original identity or selection cannot be recovered, restoration must fail closed rather than silently substitute another instance or definition.

Identity continuity is separate from broker attachment recovery. See [pauses and restart](credentials-and-lifecycle.md#pauses-and-restart) for attachment checks, credential and execution recovery, and unknown operation outcomes.

## Definition provenance

Each instance's registration carries a provenance record for the exact definition it uses. mecak8s's trusted resolver produces it from source resolution; the model never supplies it, and it is never reconstructed later from whatever is current at the source.

The record has a versioned **profile** and profile-specific metadata. A Git profile might carry repository, path, and full commit; a Kubernetes profile the cluster, object UID, and observed revision; a service-backed profile a tenant namespace, object ID, and revision. Each profile defines its required fields and revision semantics. Operator configuration names the profiles and evidence it accepts; a source cannot weaken that by selecting a more permissive profile.

The record carries two digests:

- **Source content digest:** the bytes read from the source.
- **Accepted definition digest:** the normalized definition mecak8s admitted and constructs the agent from.

Parsing and normalization mean these differ. A commit plus a digest does not prove the bytes came from that commit; a locally modified file is the obvious counterexample.

The broker records provenance as a mecak8s assertion unless it independently verified a fact, and audit marks which. "mecak8s reports loading these bytes from commit X" is distinct from "the broker verified these bytes against commit X"; the design requires only the former for now. Provenance lives in the broker's per-instance registration record, not in instance credentials. It grants no permission by itself; later policy may require approved revisions or particular source authorities.

## Source records and policy representation

The proposal adopts workload-authenticated mecak8s lifecycle assertions, host-allocated durable incarnations, and broker validation and subject construction as its semantic mechanism. The exact registration wire API, durable record schema, and verified per-operation binding for reported context remain unresolved, as does protection from same-process substitution. These are proposed semantics, not implemented contracts.

Cedar is selected for the effective server/tool ceiling. The representation of definition-source and revision records, registration API, and Cedar policies remains an implementation choice. The authority work generates policy only from resolved definition facts; separately managed user, resource, argument, and dynamic delegation policies are deferred. Existing user/connection authorization checks remain required.
