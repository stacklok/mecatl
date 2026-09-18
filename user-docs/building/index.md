---
sidebar_position: 1
title: Build with Mecatl
description:
  Embed the Go engine or connect applications through the TypeScript SDK and
  public APIs.
---

# Build with Mecatl

Use Mecatl inside your own application by embedding the Go engine or connecting
through the TypeScript SDK. This section also covers the public extension
points for providers, storage, policies, tools, and other adapters.

## Choose where to start

- [Build your first agent](/building/getting-started/first-agent.md) to embed
  the Go engine in a small application.
- [Use the TypeScript SDK](/building/getting-started/typescript-sdk.md) to
  connect a Node.js, Bun, or browser application.
- [Run the offline demo](/building/getting-started/demo.md) to see an engine
  turn without a model provider account.

## Explore by topic

- [Embed the Go engine](/building/embed-engine.md) covers the engine and session
  model, minimum wiring, and the dependencies your application supplies.
- [TypeScript SDK](/building/typescript-sdk/index.md) covers application
  connections, sessions and runs, approvals, durable activity, and callback
  tools.
- [Connect with gRPC or HTTP](/building/grpc-http.md) covers direct client
  integration and links to the transport contracts.
- [Extension points](/building/extension-points/index.md) covers the Go ports
  for model providers, storage, permissions, tools, and other adapters.
- [API stability](/building/api-stability.md) identifies the supported Go
  packages and compatibility guarantees.
- [Reference](/reference/index.md) provides exact configuration fields and gRPC
  and HTTP/SSE contracts.

To host Mecatl for multiple clients, start with
[Deploy and operate Mecatl](/operating/index.md).
