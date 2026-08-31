# ADR 0250 — Expose the running agent session ID as a tool

- Status: Accepted
- Date: 2026-08-31
- Scope: model-visible session identity and debugger handoff
- Supersedes: —
- Superseded by: —

## Context

Operators sometimes need the opaque ID of the session an agent is currently driving, most
notably to hand that session to `mecatui debug`. Capturing an ID while catalogs are built is
incorrect for delegated agents because each child has its own session. Injecting IDs into
prompts or shell environments also enlarges unrelated surfaces and can become stale.

## Decision

Provide a zero-argument, read-only `CurrentSession` tool in normal main and child catalogs.
Resolve the ID only when the tool executes, using `port.SessionIDFromContext`; return a tool
error when no non-empty, valid-UTF-8 ID is present. Treat the tool as a derived catalog
capability and a built-in-default floor Allow, so configured Ask or Deny rules still override
it. Agent-definition `disallowed_tools` also remains authoritative.

Describe the returned value as an opaque correlation/debugging reference that grants no
authority. Do not inject literal IDs into prompts, environments, request/protobuf types, or
perform a store lookup.

Keep dedicated debug-session catalogs unchanged at exactly `{InspectSession}`. A debug
session already has a target-bound evidence capability; adding general identity affordances
would weaken ADR 0248's deliberately exact surface.

## Consequences

A normal agent can report its exact current ID, and every delegated child reports its own ID
from its run context rather than a captured parent ID. Operators can ask the normal agent to
call `CurrentSession`, then pass the returned ID to `mecatui debug`. Possession of that ID is
not authorization; server ownership and debug-creation checks remain authoritative.

The exported constructor and tool-name constant add to the engine API. Hosts constructing
custom catalogs may opt in explicitly; mecatl composition registers the tool across its
normal catalog paths.

## See also

- [ADR 0248](./0248-session-debugger-admin-transport.md)
- [Architecture overview](../architecture.md)
- [Usage guide](../usage.md)
- [Implementation notes](../design/IMPLEMENTATION-NOTES.md)
