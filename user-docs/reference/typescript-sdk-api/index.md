---
title: TypeScript SDK API reference
description: Look up TypeScript SDK entry points, runtime support, methods, types, and errors.
sidebar_position: 1
---

# TypeScript SDK API reference

`@stacklok/mecatl-sdk` provides a transport-neutral client for browsers and a
Node.js and Bun client with gRPC and local-process capabilities.

## Entry points

| Entry point | Contents |
| --- | --- |
| [`@stacklok/mecatl-sdk`](./core.md) | HTTP and SSE transport, ergonomic client, sessions, runs, events, durable activity, and shared types. |
| [`@stacklok/mecatl-sdk/node`](./node.md) | Node.js and Bun gRPC transport, local daemon management, one-shot queries, filesystem media helpers, and callback tools. It also exports the core API. |
| `@stacklok/mecatl-sdk/gen` | Generated protobuf-es messages and service descriptors. Use the [gRPC API reference](/reference/grpc-api.md) for the service contract. |

The core and Node.js/Bun method pages are generated from the declarations in
the published package. Edit their TSDoc under `sdk/typescript/src/`, then run
`task sdk:docs`.

## Runtime support

| Runtime | Support |
| --- | --- |
| Node.js | Version 22 or later. Remote connections and local daemon management are supported on macOS and Linux. Windows supports remote connections. |
| Bun | Version 1.4 or later on macOS and Linux. |
| Browsers | The latest two stable Chrome, Firefox, and Safari releases. Browser applications use the HTTP and SSE transport through a same-origin backend-for-frontend. |
| TypeScript | Declarations compile with TypeScript 5.7 or later. The SDK is developed with TypeScript 6. |

The package is ESM-only.

## Error model

SDK-authored failures extend `MecatlError` and carry a machine-readable `code`,
an error origin, and available transport metadata. Server-declared terminal run
outcomes, including cancellation and limits, resolve as `RunResult` values.
Transport, protocol, authentication, and invalid local lifecycle failures throw.

## Related information

- [TypeScript SDK guides](/building/typescript-sdk/index.md)
- [gRPC API reference](/reference/grpc-api.md)
- [HTTP and SSE API reference](/reference/http-sse-api.md)
