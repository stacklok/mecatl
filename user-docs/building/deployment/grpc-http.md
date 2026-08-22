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
session lifecycle. The server decides which optional features are available and
returns a capability snapshot when it creates a session.

## The common lifecycle

1. Create a session with a workspace and any desired model/provider or permission
   selection.
2. Start a prompt. The server streams agent events until it reaches a terminal
   result.
3. If the run asks for permission, resolve the ask and continue the same session.
4. Read the final result, or resume/replay a durable session when the configured
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

## Related information

- [Run mecated standalone](./mecated.md)
- [Cloud-native k8s with mecak8s](./mecak8s.md)
- [Scheduled tasks](../what-you-get/scheduled-tasks.md)
- [Start and resume sessions](/features/start-and-resume-sessions.md)
