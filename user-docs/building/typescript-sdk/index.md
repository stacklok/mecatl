---
title: TypeScript SDK
description:
  Build Node.js, Bun, Deno, and browser applications that create and control
  Mecatl sessions.
sidebar_position: 1
---

# TypeScript SDK

Use `@stacklok-oss/mecatl-sdk` to create sessions, run agents, handle approvals,
follow durable activity, and call the rest of the Mecatl API from TypeScript.
The package supports Node.js, Bun, Deno, and browser applications.

The Deno integration described here is unreleased and excluded from SDK v0.1.0.
Its first supported SDK release has not been assigned.

Start with [Use the TypeScript SDK](/building/getting-started/typescript-sdk.md)
to run one prompt against a private offline daemon.

## Choose a workflow

- [Connect an application](./connect.md) to use an operator-owned daemon from
  Node.js, Bun, Deno, or a browser.
- [Run a private local daemon](./local-daemon.md) when a Node.js, Bun, or Deno
  process should own `mecated`, or when a script needs one `query()` call.
- [Work with sessions and runs](./sessions-and-runs.md) to stream events,
  receive a terminal result, send controls, or include media in a prompt.
- [Handle permissions and plans](./permissions-and-plans.md) to resolve asks in
  application code and continue an approved plan.
- [Resume durable activity](./durable-activity.md) to follow one run or a
  session's cross-run timeline from an application-owned cursor.
- [Register callback tools](./callback-tools.md) to expose local Node.js or Bun
  handlers to sessions created by a private daemon.
- [Inspect a server before creating a session](./server-discovery.md) to check
  compatibility, deployment capabilities, and safe build identity.
- [Enroll MCP workspace services](./mcp-connectors.md) to inspect a session's
  connector inventory and drive whole-bundle enrollment.

The SDK also exposes typed namespaces for models, agents, skills, teams,
schedules, learning, MCP inventory, and storage operations. See the
[TypeScript SDK API reference](/reference/typescript-sdk-api/index.md) for the
complete public surface.

## Package entry points

|Import|Use it for|
|-|-|
|`@stacklok-oss/mecatl-sdk`|Deno and browser HTTP and SSE connections, injected transports, and transport-neutral types.|
|`@stacklok-oss/mecatl-sdk/node`|Node.js and Bun gRPC connections, local daemons, one-shot queries, filesystem media helpers, and callback tools.|
|`@stacklok-oss/mecatl-sdk/deno`|Deno gRPC connections, `Deno.Command` local daemons, and one-shot queries.|
|`@stacklok-oss/mecatl-sdk/gen`|Generated protobuf-es messages and service descriptors for low-level calls and typed namespace requests.|

## Related information

- [TypeScript SDK API reference](/reference/typescript-sdk-api/index.md)
- [Subagents, teams, and parallel work](/features/subagents-and-teams.md)
- [Scheduled tasks](/features/scheduled-tasks.md)
- [Drive Mecatl through gRPC or HTTP](/operating/grpc-http.md)
- [Feature availability](/features/capability-matrix.md)
