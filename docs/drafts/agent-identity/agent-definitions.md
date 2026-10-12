# Agent definitions

*This chapter describes the intended architecture. The `AgentDef` inventory below describes the existing code; the harness/broker split is proposed.*

## Definitions and executing instances

An agent definition describes how an agent should perform work. Mecatl represents it with `AgentDef`, a value containing metadata and instructions, rather than an executing loop. This chapter uses the following vocabulary:

| Term | Meaning |
| --- | --- |
| Definition | A kind of agent, identified with its source and admitted revision, not just its name. |
| Session | The harness's persisted context for work, identified by a host session ID such as `S42`. |
| Agent instance | One use of a selected definition in a session, with its own identity and any creation-parent association. A delegated child has its own instance. |
| Run | One execution by an instance, such as `R41` or `R42`. An instance can perform multiple runs. |
| Agent-instance incarnation | The durable identity allocated by mecak8s for that instance's lifetime, preserved across restoration and credential renewal. A fresh incarnation identifies a distinct instance. |

The existing implementation persists a [session incarnation](../../../engine/session/incarnation.go) on each session aggregate, including child sessions. The proposal uses that durable incarnation for agent-instance identity; it is not the host session ID or a run ID. The broker's own session incarnation and attachment binding are separate [recovery concepts](credentials-and-lifecycle.md#pauses-and-restart).

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

The broker ceiling is the set of permitted registered MCP server/tool pairs resolved under the existing `Tools`, `DisallowedTools`, and `MCPServers` semantics. That fixed admitted ceiling drives [deterministic Cedar policy generation](mcp-authorization.md#definition-derived-ceiling), tied to the definition source and revision. The existing definition-source snapshot contract is not, by itself, broker initialization.

## Loading definitions for a session

In the proposed lifecycle, mecak8s's trusted session lifecycle code resolves the session's definitions from configured sources, such as administrator-managed files, repositories, or APIs. It persists the fixed selection, source associations, revisions, and authority ceilings, and supplies execution configuration to the harness. This is a responsibility within mecak8s, not a new registrar component.

For Alice's session `S42` in the [walkthrough](credentials-and-lifecycle.md#interactive-execution), the selection includes the main definition and `project/code-reviewer`. Selecting the reviewer definition does not create a reviewer instance. The particular child is allocated later when the harness creates it.

Before the main loop starts, mecak8s allocates and persists the main's durable agent-instance incarnation, then authenticates to the broker with its workload ServiceAccount to register the selected definitions and main instance with no parent. Broker initialization and registration must complete before execution; a suitable instance SVID can be obtained when needed, before protected calls. The selected definitions remain fixed for the session so that the harness and broker use the same selection.

## Registering instances

The broker validates the workload-authenticated registration records and constructs the canonical instance subject. It checks that the workload may act for the session and instance and associates the supplied incarnation with the registered definition, source, revision, ceiling, and creating instance, if any. mecak8s allocates incarnations; the broker does not. A known definition or loop-supplied name alone is insufficient for issuance.

When the main creates the reviewer, mecak8s allocates and persists the child's incarnation. It registers that particular child using the persisted incarnation, the reviewer definition already registered from the session selection, and the registered main-instance reference. The broker validates these records and constructs the child subject. This is a lifecycle assertion authenticated by mecak8s's workload credential, not proof derived from possession of the parent's SVID.

Creation ancestry records who created an instance. Execution delegation separately links parent run `R41`, reviewer run `R42`, and the bounds of delegation `D42`. Registration establishes identity records; it does not authorize spawning, run execution, or a tool call. The harness manages execution, and the broker [authorizes each MCP request](mcp-authorization.md#two-requests-two-authorization-decisions).

The broker explicitly trusts mecak8s's workload-authenticated lifecycle assertions. These responsibilities are outside the untrusted loop, but a ServiceAccount cannot distinguish trusted lifecycle code from compromised code in the same process. This design does not establish same-process isolation. The API's initiating-user report is also a trusted assertion, separate from workload verification.

## Identity continuity across runs and restarts

An instance can perform several runs without changing identity. Its fixed definition selection provides continuity, while each call remains subject to applicable current user permissions and broker policy.

| Event | What remains fixed or must be checked |
| --- | --- |
| New run on the same instance | The incarnation and selected definition stay fixed. The new run has its own execution context and applicable bounds; the instance credential alone does not authorize it. |
| Definition source edited | The session's pinned source and revision selection does not change. Restoration must not substitute whatever is current at the source. |
| Process restart or session restoration | Restore the original incarnation, definition selection, parent association, and authority bounds. Recover the unfinished execution context and its user/actor/connection association or fail closed. |
| Instance credential expires | Renew for the registered identity after the required checks. Renewal grants no new authority and does not widen recovered bounds. |
| Fresh incarnation | Register a distinct identity. Reusing a session or definition name does not inherit the old instance's authority. |

Continuation preserves the unfinished run's original bounds. Current restrictions or withdrawal can constrain it; credential renewal or policy expansion must not automatically widen recovered authority. If the original identity or selection cannot be recovered, restoration must fail closed rather than silently substitute another instance or definition.

Identity continuity is separate from broker attachment recovery. See [pauses and restart](credentials-and-lifecycle.md#pauses-and-restart) for attachment checks, credential and execution recovery, and unknown operation outcomes.

## Source records and policy representation

The proposal adopts workload-authenticated mecak8s lifecycle assertions, host-allocated durable incarnations, and broker validation and subject construction as its semantic mechanism. The exact registration wire API, durable record schema, and execution-context binding remain unresolved, as does protection from same-process substitution. These are proposed semantics, not implemented contracts.

Cedar is selected for the definition-derived ceiling. The representation of definition-source and revision records, registration API, and Cedar policies remains an implementation choice. V1 generates policy only from resolved definition facts; separately managed user, resource, argument, and dynamic delegation policies are deferred. Existing user/connection authorization checks remain required.
