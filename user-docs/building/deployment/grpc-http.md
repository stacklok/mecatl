---
sidebar_position: 120
title: Drive via gRPC / HTTP
description: Choose a client transport and find Mecatl's API contracts.
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
session lifecycle. Where workspace-service enrollment is enabled, HTTP clients can
also use the bodyless per-session connect, retry, and cancel controls documented in
the detailed HTTP reference; responses expose only the enrollment correlation,
status, service count, and any ephemeral browser presentation URL. They also expose manual history compaction through gRPC
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

### Use the TypeScript SDK

`@stacklok/mecatl-sdk` provides ergonomic clients for both transports. Node.js
and Bun applications can connect through gRPC. Browser applications use HTTP
and SSE through a same-origin backend-for-frontend.

Start with the [TypeScript SDK quickstart](/building/getting-started/typescript-sdk.md),
then choose the guide for your application:

- [Connect an application](/building/typescript-sdk/connect.md) to use an
  operator-owned daemon.
- [Run a private local daemon](/building/typescript-sdk/local-daemon.md) to own a
  `mecated` process or run a one-shot `query()` from Node.js or Bun.
- [Work with sessions and runs](/building/typescript-sdk/sessions-and-runs.md) to
  stream events, send controls, and read terminal results.
- [Register callback tools](/building/typescript-sdk/callback-tools.md) before
  creating sessions on an SDK-owned daemon.

For exact methods and types, see the
[TypeScript SDK API reference](/reference/typescript-sdk-api/index.md).

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
frames, see the [gRPC API reference](/reference/grpc-api.md).

One `Converse` stream drives one run: start it with exactly one `Prompt` or
`RetryStart`, then send only controls while it remains live. A received second start
frame is rejected, but a frame still in transit when the server has emitted its
terminal result can observe normal stream completion instead.

Mecatl does not enable gRPC server reflection. Use the checked-in proto files
with `grpcurl`, or use generated bindings in your client.

## HTTP/SSE client contract

HTTP endpoints use JSON request bodies. A prompt starts an SSE response: each
`data:` line contains the JSON projection of the shared event model.

For the route inventory, request/response schemas, event behavior, authentication,
and `curl` examples, see the [HTTP/SSE API reference](/reference/http-sse-api.md).

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
