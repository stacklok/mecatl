# ADR 0310 — Lazy ToolHive authorization for statically declared protected tools

- Status: Accepted
- Date: 2026-09-06
- Scope: session-scoped MCP broker protected-tool admission
- Supersedes: ADR 0311's pre-prompt-only static-tool admission decision
- Superseded by: 0326 (the pre-prompt-only authenticated-discovery clause only — lazy bundle grants now also trigger authenticated discovery for the declared surface; the declared-tool membership boundary and pre-prompt enrollment's exclusive right to publish undeclared tools are unchanged)

## Context

ADR 0311 correctly made ToolHive the owner of the configured upstream OAuth chain,
but staged static protected tool declarations until pre-prompt workspace enrollment.
That made a declared tool unavailable to the model and prevented the promised
on-demand authorization path from ever starting.

Direct authorization against a real upstream is rejected: ToolHive must own every
protected backend's interactive OAuth flow, token custody, and injection.

## Decision

Publish statically declared protected tools in the initial session catalogue. Their
first call starts a normal mecatl authorization pause against ToolHive's embedded
authorization server. The resulting transaction represents the complete ordered
ToolHive protected-backend bundle, not an upstream-specific grant. A bundle credential
satisfies later declared-tool calls and those calls execute through ToolHive.

Pre-prompt workspace enrollment remains the only discovery path. It replaces the
visible static stand-ins with the complete authenticated catalogue after success.
A lazy authorization does not add undeclared tools to the active session catalogue.

## Consequences

A model can use declared tools without an explicit connect step, while the browser
still completes one ToolHive-owned aggregate authorization. A backend without static
declarations remains invisible until pre-prompt enrollment, and a declared backend's
undeclared tools also remain unavailable until then. Operators should declare the
minimum useful initial tool surface when lazy connection is desired.

## See also

- [ADR 0311 — ToolHive-owned multi-upstream MCP broker OAuth](./0311-per-upstream-mcp-broker-oauth-grants.md)
- [Architecture guide](../architecture.md)
