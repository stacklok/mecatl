# ADR 0223 — Pin MCP transport-error semantics that do not replay rejected calls

- Status: Accepted
- Date: 2026-08-15
- Scope: official MCP Go SDK version and streaming-HTTP reconnect boundary
- Supersedes: none
- Superseded by: none

## Context

Mecatl retries an MCP operation once after a lost session. That is useful when a
server restart invalidates a session, but dangerous when the server has already
processed a potentially mutating tool call and rejects only its response. The
previous adapter classified every SDK error containing `rejected by transport`
as session loss. The SDK uses that wrapper for more than broken connections, so
structured JSON-RPC failures and transient HTTP responses could trigger a
reconnect and replay.

The official Go SDK commit `64e454e35c23c473e1fcf1e1c3a6f623260ca773`
contains three lifecycle fixes needed together:

- structured JSON-RPC errors at HTTP 400/404 and HTTP 429/502/503/504 responses
  are per-call transport rejections that leave the session usable;
- cancellation retires the caller immediately while sending
  `notifications/cancelled` asynchronously with a bound; and
- a client session is closed when initialization negotiates an unsupported
  protocol version.

No tagged SDK release contains that exact qualified revision yet. Waiting for a
tag leaves mutating-call replay possible; floating to a newer tag without
qualification would not establish that it contains equivalent behavior.

ADR 0219's OAuth qualification remains necessary. These transport fixes do not
remove its metadata and authorization-profile constraints.

## Decision

Pin `github.com/modelcontextprotocol/go-sdk` exactly to pseudo-version
`v1.7.1-0.20260813084956-64e454e35c23`. Do not carry a local fork, replacement,
or local HTTP-status/JSON-RPC parser.

Treat these outcomes as one-call failures and surface them without reconnect or
replay:

- any error chain containing the public SDK `*jsonrpc.Error`, regardless of its
  HTTP status or peer-controlled message; and
- HTTP 429, 502, 503, or 504 transport rejections.

The exported `ErrConnectionClosed` and `ErrSessionMissing` checks run first so a
real typed lifecycle loss remains reconnectable. Typed `io`/`net`/`syscall`
causes joined to the SDK's generic transport-rejection wrapper also remain
reconnectable without consulting text. The structured JSON-RPC check then runs
before every legacy text fallback: messages such as `session not found`,
`connection closed`, `EOF`, or `rejected by transport` are server-controlled
response data and provide no evidence that the session died.

Keep one bounded reconnect and one retry only for concrete session or transport
loss: the exported SDK `ErrConnectionClosed` and `ErrSessionMissing` sentinels,
a plain missing-session 404, EOF, refused connections, Go's concrete
`http: server closed idle connection`, and the existing connection-closed,
client-closing, and legacy session-missing fallbacks. A `rejected by transport`
wrapper reconnects only when one of those concrete drop signatures is nested
beneath it. The structured JSON-RPC guard remains authoritative: a peer error
whose message contains the same concrete text is not replayed. Context
cancellation always wins over drop classification and never reconnects.

Qualify this boundary with the real SDK streamable-HTTP client and server over
loopback. Tests count handshakes, requests, and mutation side effects; require a
rejected call not to replay; prove the same session remains usable; prove a
plain missing session reconnects exactly once; and bound cancellation and
failed-connect cleanup. Keep the complete ADR 0219 OAuth qualification suite
unchanged.

Retain ADR 0219's residual OAuth limits: RFC 9728 metadata and an exact resource
with one authorization server, S256, Basic-only confidential clients, bounded
repeated rejection, no automatic invalid-grant reauthorization, no durable
dynamic client registration, and caller-owned redirect/destination policy.

Move to the first tagged SDK release that contains `64e454e` or a verified
successor. Before changing the pin, rerun all transport and OAuth fixtures plus
`task vuln`; do not migrate merely because a tag has a higher version.

## Consequences

Potentially mutating MCP calls are not automatically replayed after a
server-declared call failure. A caller may choose to issue a later call after a
429 or 5xx response, but mecatl does not hide that decision behind reconnect.
Real session loss still heals once, and concurrent reconnects retain their
existing serialization.

The root module temporarily depends on an unreleased pseudo-version. That is
less convenient for dependency automation and requires an explicit tagged-
release follow-up, but it is reproducible and avoids a local SDK fork. The
adapter remains coupled only to exported sentinels and concrete legacy network
signatures, not SDK internals.

OAuth capability does not broaden. Operators still carry the limitations and
network-policy responsibilities recorded in ADR 0219.

## See also

- [ADR 0056 — MCP client reconnect](./0056-mcp-client-reconnect.md)
- [ADR 0219 — Qualify the official MCP SDK authorization-code profile](./0219-mcp-oauth-sdk-profile.md)
- [Extensibility architecture](../architecture/extensibility.md)
- [Production readiness](../design/PRODUCTION-READINESS.md)
