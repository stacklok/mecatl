# ADR 0340 - Deno reuses the ConnectRPC gRPC transport

- Status: Accepted
- Date: 2026-09-14
- Scope: TypeScript SDK Deno connections, local daemon transport, and runtime qualification.
- Supersedes: [ADR 0338](./0338-typescript-sdk-deno.md) and
  [ADR 0339](./0339-typescript-sdk-deno-command.md), for their Deno HTTP-only transport and
  Node-compatibility exclusions. Their declaration strategy, runtime range, native process
  ownership, and callback-tool authority decisions remain in force.

## Context

Deno's Node compatibility layer runs the same `@connectrpc/connect-node` HTTP/2 transport used
by Node and Bun. A real-wire Deno check completed unary and streaming RPCs against a daemon
launched with `Deno.Command`. Process ownership and transport selection are separate concerns:
native process control does not require an HTTP/SSE-only client.

## Decision

Reuse the existing ConnectRPC gRPC transport for `./deno`. Share connection assembly with the
Node client, including credentials, diagnostics, and transport disposal. `connect()` accepts a
TCP authority, Unix socket, or caller-owned injected transport and returns the ordinary `Client`.
The root entry point continues to provide HTTP/SSE for browsers and remote callers that choose it.

Keep local process ownership in `Deno.Command`. Start one ephemeral loopback TCP gRPC listener
with HTTP disabled. Validate the ready document's TCP transport, loopback address, and captured
child PID before connecting. Publish `grpcAddress` and `transport: "grpc"` in Deno daemon
metadata. Preserve piped-stdin parent liveness, bounded signal fallbacks, stderr redaction, and
private runtime-directory cleanup.

Qualify the packed package at Deno 2.9.3 and current Deno 2.x with real RPCs over TCP, TLS with a
trusted CA, and Unix sockets. Exercise streaming, durable attachment, active-run cancellation,
transport disposal, and native process cleanup. Keep `engines.deno` at `>=2.9.3 <3`; Deno 3
requires separate qualification.

Retain the existing callback-tool and filesystem media boundary. `./deno` does not import the
Node process launcher or expose callback-tool registration and path media helpers. A loopback
TCP connection does not receive local Unix-peer callback authority.

## Consequences

Deno applications share the ConnectRPC transport implementation and its dependency upgrades
with Node and Bun. The qualification matrix must catch Node compatibility regressions, including
HTTP/2 cancellation and session cleanup. Deno's permission checks still apply to the selected
endpoint, Unix socket, and native process operations.

The unreleased Deno entry point changes its default connection protocol and daemon metadata.
Root HTTP/SSE imports and existing Node/Bun imports keep their contracts. Deno remains excluded
from v0.1.0, with no later release assigned. Deno Deploy remains outside this CLI runtime contract.

## See also

- [Deno acceptance plan](../acceptance/sdk-typescript-deno.md)
- [TypeScript SDK architecture](../architecture.md#typescript-sdk)
- [Production readiness](../design/PRODUCTION-READINESS.md)
