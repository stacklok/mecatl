# ADR 0309 — MCP closed-idle POST failures are ambiguous and never replayed

- Status: Accepted
- Date: 2026-09-06
- Scope: MCP streaming-HTTP transport failure classification and reconnect boundary
- Supersedes: ADR 0223
- Superseded by: none

## Context

Mecatl reuses HTTP keep-alive connections to call streaming-HTTP MCP servers. The remote peer can close an idle connection while the client writes the next MCP `POST`. The peer may be the MCP server itself or infrastructure in front of it, such as a reverse proxy or load balancer.

Go's `net/http` transport reports this race as `http: server closed idle connection`. The sentinel is unexported. Its documented condition permits request-body bytes to have already been written when the close is observed, so the client cannot determine whether the MCP server received none, some, or all of the request. In particular, it cannot determine whether a potentially mutating tool call was applied before its response was lost.

ADR 0223 classified this concrete error as reconnectable. That classification caused `withSession` to reconnect and replay the call. The existing structured JSON-RPC and HTTP-status protections remain necessary, but they do not make this delivery ambiguity safe.

The MCP SDK wraps `http.Client.Do` failures with its public transport-rejection error while preserving the original `*url.Error` in the error chain. The adapter can therefore distinguish the locally-originated closed-idle transport shape from peer-controlled JSON-RPC response text. Go exposes no typed or exported sentinel for this specific condition, so the narrow exact-message check remains isolated to that `*url.Error` shape.

## Decision

Supersede ADR 0223 while retaining its SDK pin and its no-replay rules for structured JSON-RPC errors and HTTP 429/502/503/504 responses.

Treat a locally-originated `*url.Error` whose underlying error is exactly `http: server closed idle connection` as an **ambiguous delivery failure**:

- do not reconnect or replay the original MCP operation;
- return a normalized model-facing unavailable error which states that the request outcome is unknown; and
- keep the raw transport message out of model-facing output.

Continue to reconnect once and retry once only for unambiguous session or transport loss: the SDK's `ErrConnectionClosed` and `ErrSessionMissing` sentinels, a plain missing-session 404, and the existing typed EOF, closed-connection, refused-connection, reset, aborted, and broken-pipe causes. Retain the existing mutex serialization of concurrent reconnects.

Keep the structured JSON-RPC guard authoritative. A peer response whose message merely contains `http: server closed idle connection` remains a one-call failure and must not be classified as local transport loss.

Test the boundary with the real SDK call path: prove the locally-shaped error is normalized with zero reconnect attempts, prove peer-controlled text cannot take the ambiguous path, and retain the handshake/request/side-effect qualification coverage for all rejected calls.

## Consequences

An MCP tool call interrupted by this particular close race is no longer retried automatically. This may require the model or operator to inspect remote state before issuing a compensating or follow-up operation, but it prevents duplicate mutations when the original call may have been applied.

The adapter intentionally retains a narrow message comparison because Go does not export a stable sentinel or type for the condition. It is constrained by the local `*url.Error` wrapper and exact equality, rather than by arbitrary error text or a peer-controlled JSON-RPC message. An upstream exported classification from Go or the MCP SDK would be preferable and could replace this compatibility boundary.

The SDK remains pinned to the qualified pseudo-version from ADR 0223. This ADR changes only the ambiguous closed-idle classification; it does not broaden OAuth capability or weaken the existing transport-rejection safety rules.

## See also

- [ADR 0056 — MCP client reconnect](./0056-mcp-client-reconnect.md)
- [ADR 0219 — Qualify the official MCP SDK authorization-code profile](./0219-mcp-oauth-sdk-profile.md)
- [ADR 0223 — Pin MCP transport-error semantics that do not replay rejected calls](./0223-mcp-sdk-transport-error-semantics.md)
- [Extensibility architecture](../architecture/extensibility.md)
- [Documentation lifecycle](./0002-documentation-lifecycle.md)
