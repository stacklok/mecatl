---
sidebar_position: 4
title: Connect with gRPC or HTTP
description: Choose a Mecatl client transport and find its API contracts.
---

# Connect with gRPC or HTTP

`mecated` and `mecak8s` expose the same agent service through gRPC and HTTP.
Choose the transport that matches your client, then use the linked reference for
exact fields, routes, and response codes.

## Choose a transport

|Choose gRPC for|Choose HTTP/SSE for|
|-|-|
|Generated, typed clients|JSON over ordinary HTTP|
|A bidirectional `Converse` stream|Browsers and clients without gRPC support|
|In-flight steering controls|A request that returns an SSE event stream|
|A protobuf contract|An HTTP route and JSON schema contract|

Both transports support server-side sessions, live run events, permission
approval, cancellation, capability discovery, and session lifecycle operations.
The server returns a capability snapshot when it creates a session. Check that
snapshot before exposing optional features such as manual compaction.

Authenticated clients can inspect the deployment before creating a session:

- gRPC `GetCompatibilityInfo` and HTTP `GET /v1/compatibility` return the API
  major, enabled capabilities, build feature identifiers, and optional
  deployment label.
- gRPC `GetServerInfo` and HTTP `GET /v1/info` return build identity and
  sanitized diagnostic display endpoints. They do not return configuration or
  connection instructions.

### Use the TypeScript SDK

`@stacklok-oss/mecatl-sdk` provides ergonomic clients for both transports.
Node.js and Bun applications can connect through gRPC. Browser applications use
HTTP and SSE through a same-origin backend-for-frontend.

Start with the
[TypeScript SDK quickstart](/building/getting-started/typescript-sdk.md), then
continue with the guide for your application:

- [Connect an application](/building/typescript-sdk/connect.md) to use an
  operator-owned daemon.
- [Run a private local daemon](/building/typescript-sdk/local-daemon.md) to own
  a `mecated` process or run a one-shot `query()` from Node.js or Bun.
- [Work with sessions and runs](/building/typescript-sdk/sessions-and-runs.md)
  to stream events, send controls, and read terminal results.
- [Register callback tools](/building/typescript-sdk/callback-tools.md) before
  creating sessions on an SDK-owned daemon.

For exact methods and types, see the
[TypeScript SDK API reference](/reference/typescript-sdk-api/index.md).

## The common lifecycle

1. Create a session with a workspace and any provider, model, or permission
   selection.
2. Start a prompt and process events until the server returns a terminal result.
3. For a failed result marked `retryable` and `precommit`, make one bounded
   prompt-free retry. Send `RetryStart` as the first gRPC `Converse` frame, or
   call `POST /v1/sessions/{id}/retry` with no body. Treat missing or unknown
   retry metadata as non-retryable.
4. If the run asks for permission, resolve the ask and continue the same
   session.
5. Read the final result. If the configured store supports durable sessions, you
   can later resume the session or replay its events.

See [Session continuity](/features/sessions/session-continuity.md) for durable
storage, event logs, recovery, and retention behavior.

Use the detailed references below for exact fields, response codes, event
payloads, and feature-specific APIs such as schedules, teams, learning, and MCP
inventories.

## gRPC client contract

The checked-in protobuf files define the gRPC contract:

- [`HarnessService` and session/event messages](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/harness.proto)
- [`ScheduleService`](https://github.com/stacklok/mecatl/blob/main/contracts/proto/mecatl/v1/schedule.proto)
- [Remote driver services](https://github.com/stacklok/mecatl/tree/main/contracts/proto/mecatl/driver/v1)
  for storage and content-source backends

Generated Go bindings live in `contracts/gen/go/mecatl/v1` and use the package
alias:

```go
import mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
```

For RPC-by-RPC behavior, request fields, response semantics, and stream control
frames, see the [gRPC API reference](/reference/grpc-api.md).

One `Converse` stream drives one run. Start it with exactly one `Prompt` or
`RetryStart`, then send only control frames while the run remains live. The
server rejects a second start frame received during the run. A start frame still
in transit after the terminal result can instead observe normal stream closure.

Mecatl does not enable gRPC server reflection. Use the checked-in proto files
with `grpcurl`, or use generated bindings in your client.

## HTTP/SSE client contract

HTTP endpoints use JSON request bodies. A prompt starts an SSE response: each
`data:` line contains the JSON projection of the shared event model.

For the route inventory, request/response schemas, event behavior,
authentication, and `curl` examples, see the
[HTTP/SSE API reference](/reference/http-sse-api.md).

### Steering requires gRPC

gRPC can send an in-flight steering instruction, or cancel one, through the live
`Converse` stream. HTTP/SSE has no client-to-server mid-run steering channel.

## Connect securely

A server bound beyond loopback needs an authentication and transport-security
configuration before clients connect. See
[Run mecated standalone](/operating/mecated.md) for bearer authentication,
TLS/mTLS, OIDC caller identity, rate limits, and health endpoints.

Browser clients also require an allowed origin. See
[Browsers and CORS](/operating/mecated.md#browsers-and-cors). In production, put
a same-origin backend-for-frontend in front of `mecated` so browser JavaScript
does not receive the server bearer token.

## Next steps

- [Run mecated standalone](/operating/mecated.md) to operate a long-running
  server.
- [Connect an application](/building/typescript-sdk/connect.md) with the
  TypeScript SDK.
- [Start and resume sessions](/features/sessions/start-and-resume-sessions.md)
  through either transport.
