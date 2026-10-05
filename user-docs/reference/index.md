---
title: Reference
description: Look up Mecatl configuration and client API contracts.
sidebar_position: 1
---

# Reference

Use these pages when you need exact fields, defaults, routes, methods, or wire
behavior:

- [Go engine API](https://pkg.go.dev/github.com/stacklok/mecatl/engine) provides
  exact package and symbol documentation;
  [API stability](/building/go/api-stability.md) explains the compatibility
  promise.
- [Server CLI reference](./server-cli.md) lists the flags registered by
  `mecated` and `mecak8s`.
- [Configuration reference](./configuration.md) lists every operator
  `settings.yaml` key generated from the schema used at runtime.
- [gRPC API reference](./grpc-api.md) lists the public gRPC services and stream
  behavior defined by the protobuf contracts.
- [HTTP and SSE API reference](./http-sse-api.md) lists the handwritten HTTP
  routes and SSE stream behavior.
- [TypeScript SDK API reference](./typescript-sdk-api/index.md) lists the
  published package entry points, methods, types, and errors.

For task-oriented instructions, start with
[Build with Mecatl](/building/index.md) or [Use Mecatl](/mecatui/index.md).
