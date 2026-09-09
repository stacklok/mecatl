# ADR 0319 — Broker-attached MCP query projection

- Status: Proposed
- Date: 2026-09-09
- Scope: `CallMcpWithQuery` and process-local MCP broker attachments
- Supersedes: none
- Superseded by: none

## Context

`CallMcpWithQuery` narrows an MCP JSON result with bounded in-memory jq before it
enters model context (ADR 0063). Its direct implementation reaches a direct MCP
manager. Broker-backed tools instead exist only as wrappers on a session's
process-local attachment. Sending a broker target through a new direct manager
would bypass the attachment's frozen catalogue, route ownership, and
authorization custody. Returning the successful raw result outside that
attachment for filtering would defeat the projection boundary.

The existing attachment wrapper acquires an operation, validates the frozen
route, and invokes protected routes through their established authorization path
(`internal/adapter/mcpbroker/runtime.go` (`sessionTool.Execute`)). The engine
already evaluates `CallMcpWithQuery` against its addressed MCP capability while
retaining the envelope as the action (`engine/agent/dispatch.go`
(`authorityTarget`)). This decision does not change either lifecycle.

## Decision

Keep the existing one model-facing `CallMcpWithQuery` schema and usage instruction.
The real session factory registers one attachment-bound query wrapper whenever
eligible broker targets exist, even without a global direct manager. A built-factory
proof covers this broker-only path and the unchanged schema/instruction.
A broker-backed call uses a small, root-internal attachment query operation that:

1. resolves only the current session attachment's frozen route and invokes it
   through the same operation and authorization path as the registered wrapper;
2. applies ADR 0063's structured-content-first JSON selection and bounded jq
   filtering before returning; and
3. returns only the bounded projection or a bounded error.

Calling `sessionTool.Execute` alone cannot acquire a missing grant. The catalogued
query wrapper must implement existing `tool.AuthorizationRequester`
(`engine/tool/authorization.go`): delegate `RequestAuthorization` to the exact native
target requester, returning `required=false` for an unprotected target, and implement
`AbortAuthorization` through the bound attachment's exact-reference cancellation,
including the interface's cleanup/fail-closed obligation. Presentation, status, and
cancellation controls remain on the Service's existing attachment lifecycle.

Request and execute share deterministic mapping from the hook-effective envelope
and bound source to the native call: same ID, exact target name, byte-identical
normalized args. This preserves `callHash` and the protected-call replay claim
(`internal/adapter/mcpbroker/auth.go`). Existing `PendingAuthorization.Call` stores
the query envelope including jq, so live/restarted host continuation looks up that
query wrapper and retains filtering. No durable mapping or cache is needed. Grant
continues once; deny/cancel never invokes the target. A missing broker incarnation
or binding mismatch fails closed rather than creating a replacement authorization.
Acceptance proofs cover both live and restarted continuation with surviving broker
state, and failure when that state is lost.

It must not open a direct connection to a broker-owned upstream, select a route
from another session or manager, expose a successful raw payload to the harness,
or replay a call after delivery may have occurred. Invalid jq fails before
invocation. A post-dispatch filtering or ambiguous transport failure reports that
the target may already have succeeded and does not retry, rebind, or hedge.

Direct-manager queries retain their current implementation and behavior. This
ADR does not add a protocol, flag, engine API, target-aware dispatcher,
`PendingAuthorization` representation, permission migration, or lifecycle
redesign.

## Consequences

- Broker-backed tools gain the existing context-saving query affordance without
  weakening broker session isolation or authorization.
- The broker attachment interface may gain a narrow internal query seam, but no
  raw-result DTO or public engine surface.
- Conversation, events, audit, and durable logging receive only the filtered
  result or bounded error.
- Existing authority, permission, hooks, dispatch, and authorization behavior
  remains the compatibility baseline; this ADR does not authorize a target that
  its existing wrapper would reject.
- Implementation updates living architecture and public `user-docs/`, with
  `task site:build` verification; this proposed ADR does not claim shipped support.
- A remote action can still succeed before filtering fails; no-replay wording is
  required but cannot make the operation transactional.

## Rejected alternatives

- **Open a direct manager connection for a broker-owned target.** It bypasses
  the attachment's session-scoped route and authorization gates.
- **Return raw broker output to the harness for local filtering.** It crosses
  the boundary the tool is intended to enforce.
- **Redesign engine dispatch or pending authorization for this fix.** The
  existing authority and lifecycle seams already govern the envelope; widening
  them is unrelated to making broker-backed projection work.

## See also

- [ADR 0063 — MCP structured results: fail-closed + CallMcpWithQuery](./0063-mcp-structured-failclosed-callmcpwithquery.md)
- [ADR 0234 — Authority evaluator port](./0234-authority-evaluator-port.md)
- [CallMcpWithQuery broker support acceptance plan](../acceptance/callmcpwithquery-broker-support.md)
