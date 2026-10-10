# Agent definitions

*This chapter describes the intended architecture. The `AgentDef` inventory below describes the existing code; the harness/broker split is proposed.*

## Definitions and executing instances

An agent definition describes how an agent should perform work. An executing instance uses that definition within a session and can perform multiple runs. Mecatl represents the definition with `AgentDef`, a value containing metadata and instructions, rather than an executing loop.

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

The concrete field mapping remains to be specified. The existing definition-source snapshot contract is not, by itself, broker initialization.

## Loading definitions for a session

The agent-definition loader resolves the definitions selected for a session from configured sources, such as administrator-managed files, repositories, or APIs. It supplies the harness with execution configuration and the broker with the corresponding definition records and authority ceilings.

Broker initialization must complete before the agent loop starts. The selected definitions remain fixed for the session, so both components work from the same selection.

The loader is outside the untrusted agent loop. Its process placement and how the broker authenticates its input remain to be specified.

Restoring a session must preserve its selected definitions rather than reload whatever happens to be current at their sources. The original source association and revision must remain recoverable; restoration must not silently substitute another definition.

Keeping a definition fixed does not freeze user permissions or broker policy. The broker evaluates subsequent MCP calls against the applicable authorization policy.

## Source records and policy representation

Definition-source and revision records, and the representation of broker policy, remain unresolved. Generating Cedar from definition tool lists was discussed as an option, not selected as the mechanism. The relationship between definition-derived ceilings and separately managed user and agent permissions also needs to be described without conflating them.
