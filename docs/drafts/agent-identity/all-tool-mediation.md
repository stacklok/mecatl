# All-tool mediation

*This chapter describes a future direction, not current implementation status. It is outside the current MCP authorization and audit design scope.*

The goal in broker-backed remote deployments is for the broker to authorize all tools and spawning or delegation operations, including filesystem tools such as `Read`, `Write`, `Grep`, and `Glob`, and `Shell`. Local direct deployments can retain direct tool access. This direction does not require an immediate migration. Authorizing a spawn or delegation here means checking that request like any other protected operation. It does not make the broker the owner of agent lifecycle or run execution, and the identity work does not depend on it.

The [MCP authorization](mcp-authorization.md) chapter covers checks on protected MCP calls. That boundary alone does not govern a file read or a Shell command performed through another execution path. For example, authorizing an MCP repository read says nothing about whether the same agent may read a checkout through `Read`; the broader goal requires a decision for that operation too.

## Authorization and execution

Broker authorization does not require moving all execution into the broker. A remote driver, execution provider, or harness can execute an operation. The authorization/execution integration must bind enforcement to the exact authorized request and its execution context, and prevent bypass through alternate paths. How to establish that binding, handle request changes, and enforce it at execution remains to be designed.

## Unresolved design

Policy representation remains open, including how to express filesystem resources, Shell operations, and delegated limits. The spawn boundary also needs a design: authorizing child creation or delegation must be distinct from authorizing the child's later operations.

The [audit requirements](audit.md) distinguish authorization decisions, dispatch, and observed outcomes. Extending them beyond MCP requires deciding which component records each fact, how execution evidence reaches the audit trail, and how denied, failed, or unknown outcomes remain distinguishable. This chapter selects neither a wire API nor a new service; those integration and evidence questions remain future work.
