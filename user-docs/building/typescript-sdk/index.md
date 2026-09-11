---
title: TypeScript SDK
description: Build Node.js, Bun, and browser applications that create and control Mecatl sessions.
sidebar_position: 1
---

# TypeScript SDK

Use `@stacklok/mecatl-sdk` to create sessions, run agents, handle approvals,
follow durable activity, and call the rest of the Mecatl API from TypeScript.
The package supports Node.js, Bun, and browser applications.

Start with [Use the TypeScript SDK](/building/getting-started/typescript-sdk.md)
to run one prompt against a private offline daemon.

## Choose a workflow

- [Connect an application](./connect.md) to use an operator-owned daemon from
  Node.js, Bun, or a browser.
- [Run a private local daemon](./local-daemon.md) when a Node.js or Bun process
  should own `mecated`, or when a script needs one `query()` call.
- [Work with sessions and runs](./sessions-and-runs.md) to stream events, receive
  a terminal result, send controls, or include media in a prompt.
- [Handle permissions and plans](./permissions-and-plans.md) to resolve asks in
  application code and continue an approved plan.
- [Resume durable activity](./durable-activity.md) to follow one run or a
  session's cross-run timeline from an application-owned cursor.
- [Register callback tools](./callback-tools.md) to expose local Node.js or Bun
  handlers to sessions created by a private daemon.

The SDK also exposes typed namespaces for models, agents, skills, teams,
schedules, learning, MCP inventory, and storage operations. See the
[TypeScript SDK API reference](/reference/typescript-sdk-api/index.md) for the
complete public surface.

## Package entry points

| Import | Use it for |
| --- | --- |
| `@stacklok/mecatl-sdk` | Browser HTTP and SSE connections, injected transports, and transport-neutral types. |
| `@stacklok/mecatl-sdk/node` | Node.js and Bun gRPC connections, local daemons, one-shot queries, filesystem media helpers, and callback tools. |
| `@stacklok/mecatl-sdk/gen` | Generated protobuf-es messages and service descriptors for low-level calls and typed namespace requests. |

## Related information

- [TypeScript SDK API reference](/reference/typescript-sdk-api/index.md)
- [Drive Mecatl through gRPC or HTTP](/building/deployment/grpc-http.md)
- [Feature availability](/features/capability-matrix.md)
