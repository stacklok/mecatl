---
title: TypeScript SDK API reference
description:
  Look up TypeScript SDK entry points, runtime support, methods, types, and
  errors.
sidebar_position: 1
---

# TypeScript SDK API reference

`@stacklok-oss/mecatl-sdk` provides browser, Node.js, Bun, and Deno clients. The
package is ESM-only.

## Entry points

|Entry point|Runtime|Contents|
|-|-|-|
|[`@stacklok-oss/mecatl-sdk`](./core.md)|Browsers and shared runtimes|HTTP and SSE transport, clients, sessions, runs, events, durable activity, and shared types.|
|[`@stacklok-oss/mecatl-sdk/node`](./node.md)|Node.js and Bun|gRPC transport, local daemon management, one-shot queries, filesystem media helpers, callback tools, and the core API.|
|[`@stacklok-oss/mecatl-sdk/deno`](./deno.md)|Deno|gRPC over TCP, TLS, or Unix sockets, `Deno.Command` local daemon management, one-shot queries, and the core API.|
|`@stacklok-oss/mecatl-sdk/gen`|All runtimes|Generated protobuf-es messages and service descriptors. See the [gRPC API reference](/reference/grpc-api.md) for the service contract.|

## Runtime support

|Runtime|Version|Support|
|-|-|-|
|Node.js|22 or later|Remote connections on macOS, Linux, and Windows. Local daemon management on macOS and Linux.|
|Bun|1.4 or later|Remote connections and local daemon management on macOS and Linux.|
|Deno|2.9.3 or later in the 2.x line|Remote connections and local daemon management on macOS and Linux.|
|Browsers|Latest two stable Chrome, Firefox, and Safari releases|HTTP and SSE through a same-origin backend-for-frontend.|
|TypeScript|5.7 or later|Published declarations. The SDK is developed with TypeScript 6.|

## Error model

SDK failures extend `MecatlError`. Each failure includes a machine-readable
`code`, an origin, and any available transport metadata.

Terminal run outcomes declared by the server, including cancellation and limits,
resolve as `RunResult` values. Transport, protocol, authentication, and invalid
local lifecycle failures throw.

## Related information

- [TypeScript SDK guides](/building/typescript-sdk/index.md)
- [gRPC API reference](/reference/grpc-api.md)
- [HTTP and SSE API reference](/reference/http-sse-api.md)
