# MCP authorization

*This chapter describes the intended architecture, not current implementation status.*

## Delegated authority

A child's permitted MCP operations must stay within the authority delegated by its parent and the child's definition ceiling. Delegation can preserve or narrow that authority, not widen it.

Delegation history records how the acting instance came to act. An ancestor's permissions do not give the current actor additional rights. The broker authorizes the requested operation against the applicable user permissions, delegated limits, and definition ceiling.

An instance JWT-SVID identifies the agent; it does not select the policy representation or prove that a child policy is a valid subset. Policy storage, token-carried restrictions, and subset evaluation remain separate design choices.

## Operations and resources

Authorization must distinguish the requested operation from the resource it acts on. Permission to call a tool does not imply permission to use it against every resource or with arbitrary arguments.

Agent identity does not prevent a prompt-injected agent from misusing operations within its permitted authority. The broker constrains protected MCP calls through independent authorization checks; it does not establish that an allowed call faithfully serves the user's intent or protect the entire agent loop.

See [Interactive execution](credentials-and-lifecycle.md#interactive-execution) for the detailed issuance, enrollment, exchange, and dispatch sequence.
