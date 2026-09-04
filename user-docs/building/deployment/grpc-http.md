---
sidebar_position: 6
title: Drive via gRPC / HTTP
description: Choose a client transport and find mecatl's API contracts.
---

# Drive via gRPC / HTTP

`mecated` and `mecak8s` expose the same agent service through two client
transports. Start here to choose one and find the contract you need; this page
does not duplicate every RPC or HTTP route.

## Choose a transport

| Use gRPC when you need | Use HTTP/SSE when you need |
| --- | --- |
| Generated, typed client bindings | JSON over ordinary HTTP |
| A bidirectional `Converse` stream | A browser or a client without gRPC support |
| In-flight control frames, including steering | A request that returns an SSE event stream |
| The strongest machine-readable contract: protobuf | The detailed HTTP route reference |

Both transports create and run server-side sessions. Both expose the same core
live run events, permission approval, cancellation, capability discovery, and
session lifecycle. They also expose manual history compaction through gRPC
`CompactSession` and bodyless HTTP `POST /v1/sessions/{id}/compact`; check the
additive `manual_compaction` capability before offering it. The server decides which
optional features are available and returns a capability snapshot when it creates a
session. Authenticated callers may also use gRPC `GetServerInfo` or HTTP `GET
/v1/info` to obtain the server build identity and sanitized diagnostic display
endpoint projections; these are not connection configuration or instructions.

Authenticated callers may also use gRPC `GetCompatibilityInfo` or HTTP `GET
/v1/compatibility` to read the deployment's compatibility descriptor — the API
major, the operator-enabled capability set, the build's feature identifiers, and
an optional deployment label — without creating a session first. That is a
separate endpoint from `GetServerInfo` / `GET /v1/info` above on purpose: the
latter answers *which build is this?* and is bound by a privacy contract that
keeps capabilities and configuration out of its response, while this one is
exactly that negotiation data. A client wanting both makes both calls.

### Start a private daemon from Node or Bun

The in-repository TypeScript SDK's `@stacklok/mecatl-sdk/node` entry point provides
`spawn()` for applications that want to own one local `mecated` process. It finds an
already-installed binary from an explicit `binaryPath`, `MECATED_BIN`, or `PATH`; it never
downloads one or invokes a shell. The spawned daemon uses a private Unix socket with HTTP
disabled, and the client is returned only after the daemon publishes its ready document.
Its `client.daemon` facts come from that document's non-secret allowlist. Setting
`http: true` adds an ephemeral loopback HTTP listener but removes callback-tool support;
setting `lifetimePipe: false` opts out of parent-crash cleanup without changing `close()`.
Closing the client first cancels its owned runs and detaches its durable watches, then closes its
owned transport, sends `SIGTERM` to that child, escalates to `SIGKILL` only after a bounded grace,
and removes its private runtime directory. Cleanup continues after a failing step and reports the
fault through `diagnostics`; `close()` and `Symbol.asyncDispose` remain idempotent and do not reject
for teardown faults. If the daemon exits first, later operations fail with typed `invalid_state`.
Startup errors distinguish an exited child from a live child that missed its readiness deadline.
Their stderr report is bounded and redacted by whole line; applications can install the
structured `diagnostics` callback, while the default writes nothing to `console`.

Use `connect()` instead when another operator or service owns the daemon. A connected client
never signals a process or removes a server directory.

For a one-shot prompt, `query()` composes the same spawn, session, and run APIs and yields their
ordinary events:

```ts
import { query } from "@stacklok/mecatl-sdk/node";

const oneShot = await query("Summarize this repository", {
  spawn: { binaryPath: "/opt/mecatl/bin/mecated" },
});
for await (const event of oneShot) {
  // Handle the same Event union returned by Session.run().
}
```

The default deletes the transient session and stops a daemon it spawned. Pass an existing
`client` when the daemon must remain available; `retainSession: true` then leaves the session
loadable by `oneShot.sessionId` for that daemon's lifetime. SDK-spawned daemons use an in-memory
store unless you configure durable storage, so retention is not a persistence promise. Breaking
iteration or aborting `signal` still cleans up. Plan mode is refused until the separate plan
resolution API lands. Without `onPermissionAsk`, an ask is denied and reported through the
client's structured diagnostics sink while the run continues.

See the detailed gRPC and HTTP
references for their request, response, privacy, and compatibility contracts.

## The common lifecycle

1. Create a session with a workspace and any desired model/provider or permission
   selection.
2. Start a prompt. The server streams agent events until it reaches a terminal
   result. Inspect the presence-aware retry disposition and stream progress on a
   failed result.
3. For typed `retryable + precommit`, a client may make one bounded prompt-free
   failed-step retry. Send `RetryStart` as the first gRPC `Converse` frame, or call
   bodyless `POST /v1/sessions/{id}/retry`. A visible failure requires an explicit
   retry decision; absent or unknown metadata is not safe evidence.
4. If the run asks for permission, resolve the ask and continue the same session.
5. Read the final result, or resume/replay a durable session when the configured
   store supports it.

Use the detailed references below for exact fields, response codes, event
payloads, and feature-specific APIs such as schedules, teams, learning, and MCP
inventories.

## gRPC client contract

The checked-in protobuf files are the gRPC source contract:

- [`HarnessService` and session/event messages](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/harness.proto)
- [`ScheduleService`](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/schedule.proto)
- [Remote driver services](https://github.com/stacklok/mecatl/tree/main/contracts/proto/mecatl/driver/v1), for operators building a storage or content-source backend rather than an ordinary agent client

Generated Go bindings live in `contracts/gen/go/mecatl/v1` and use the package
alias:

```go
import mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
```

For RPC-by-RPC behavior, request fields, response semantics, and stream control
frames, see the [gRPC API reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/grpc-api.md).

Mecatl does not enable gRPC server reflection. Use the checked-in proto files
with `grpcurl`, or use generated bindings in your client.

## HTTP/SSE client contract

HTTP endpoints use JSON request bodies. A prompt starts an SSE response: each
`data:` line contains the JSON projection of the shared event model.

For the route inventory, request/response schemas, event behavior, authentication,
and `curl` examples, see the [HTTP/SSE API reference](https://github.com/stacklok/mecatl/blob/main/docs/usage/http-sse-api.md).

### Important difference: steering

gRPC can send control frames to a live `Converse` stream, including an in-flight
steering instruction or its cancellation. HTTP/SSE has no equivalent client-to-
server mid-run steering channel. Use gRPC when your client needs that control.

## Connect securely

A server bound beyond loopback needs an authentication and transport-security
configuration before clients connect. See [Run mecated standalone](./mecated.md)
for bearer authentication, TLS/mTLS, OIDC caller identity, rate limits, and
health endpoints.

Calling the HTTP API **from a browser** additionally needs the origin allowed:
see [Browsers and CORS](./mecated.md#browsers-and-cors). For production, front
`mecated` with a same-origin backend-for-frontend rather than shipping a bearer
token to JavaScript.

## Related information

- [Run mecated standalone](./mecated.md)
- [Cloud-native k8s with mecak8s](./mecak8s.md)
- [Scheduled tasks](../what-you-get/scheduled-tasks.md)
- [Start and resume sessions](/features/start-and-resume-sessions.md)
