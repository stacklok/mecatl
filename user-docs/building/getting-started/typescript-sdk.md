---
sidebar_position: 4
title: Use the TypeScript SDK
description: Connect Node, Bun, or browser applications to Mecatl with the published TypeScript SDK.
---

# Use the TypeScript SDK

`@stacklok/mecatl-sdk` is an ESM-only client for Node.js 22+, Bun, and modern
browsers.

## Install the interim preview

The `0.0.x` preview line is hosted on GitHub Packages. While the source
repository is internal, readers must be Stacklok organization members and use
a GitHub personal access token with `read:packages`. Expose the token as
`GITHUB_PACKAGES_TOKEN` and add this to a user or project `.npmrc`; do not put
the token value in the file:

```ini
@stacklok:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${GITHUB_PACKAGES_TOKEN}
```

Install the first preview explicitly:

```sh
pnpm add @stacklok/mecatl-sdk@0.0.1
```

The range `^0.0.1` is patch-pinned by npm semver, so opt into each preview
update deliberately. At `0.1.0`, the package moves to npmjs after the repository
is public and trusted publishing is configured. The canonical registry flips;
no GitHub Packages `0.0.x` artifact or version is republished to npmjs.

## Choose an entry point

The package has three public entry points:

| Import | Use it for |
| --- | --- |
| `@stacklok/mecatl-sdk` | Browser HTTP/SSE and transport-neutral types. |
| `@stacklok/mecatl-sdk/node` | Node/Bun gRPC, local `spawn()` and `query()`, and callback tools. |
| `@stacklok/mecatl-sdk/gen` | Generated protobuf-es messages and service descriptors. |

The repository's [focused examples](https://github.com/stacklok/mecatl/tree/main/sdk/typescript/examples)
compile against those package exports in CI. They do not import the SDK source
tree, so an example fails when the published surface drifts.

## Connect to a daemon or start one locally

Use `connect()` when an operator owns the daemon:

```ts
import { connect } from "@stacklok/mecatl-sdk/node";

await using client = connect({ baseUrl: "https://mecatl.example.com" });
const session = await client.sessions.create({});
const result = await (await session.run("Summarize this repository")).result();
console.log(result.text);
```

Use `spawn()` when the application should own a private local `mecated`, or
`query()` for one spawn-create-run-cleanup operation. The SDK locates an
already-installed binary; it does not download one. See the
[`spawn()` and `query()` examples](https://github.com/stacklok/mecatl/tree/main/sdk/typescript/examples)
and the [full lifecycle reference](https://github.com/stacklok/mecatl/blob/main/docs/architecture.md#typescript-sdk).

## Put a BFF in front of browser clients

Browser code imports the transport-neutral entry point and connects through a
same-origin backend-for-frontend (BFF). In production, that BFF should inject
the daemon credential and enforce Origin and CSRF policy; do not expose a
privileged daemon credential to browser JavaScript.

```ts
import { connect } from "@stacklok/mecatl-sdk";

await using client = connect({ baseUrl: "/mecatl", credentials: "include" });
```

The [browser+BFF example](https://github.com/stacklok/mecatl/blob/main/sdk/typescript/examples/browser-bff.ts)
is deployment guidance and a browser-facing shape only. The package does
**not** ship a BFF server, library, or service.

## Resolve permissions and plans

Pass `onPermissionAsk` to a run when the application can make a narrow approval
decision. Returning `undefined` leaves the raw ask unresolved; headless code
should choose an explicit verdict. Server-side deny rules and plan-mode mutation
denials still win. See the
[permissions example](https://github.com/stacklok/mecatl/blob/main/sdk/typescript/examples/permissions.ts)
and [permissions and posture guide](/features/permissions-and-posture.md).

`session.resolvePlan()` models two runs, not one renamed result. It first resumes
the durably parked plan run. Only when that run ends with `plan_approved` does
the server start an optional continuation with a new run ID. Iterate the merged
events or call `result()` once to receive `{ resumed, continuation }`; do not do
both. A run attachment remains bound to one ID, while `session.activity()` can
observe both.

## Follow durable activity

`session.attach(runId)` replays and follows one run. `session.activity()` follows
the ordered cross-run session timeline, including schedule activity. Both expose
an opaque serializable cursor for application-owned checkpoint storage and use
at-least-once delivery, so side effects must be idempotent. The SDK never writes
that cursor to browser storage or the filesystem for you. See the
[durable attachment example](https://github.com/stacklok/mecatl/blob/main/sdk/typescript/examples/durable-attachment.ts).

## Use teams, schedules, and callback tools

- `client.teams.create()` returns a server-owned `Team` handle. Consume
  `team.run()` by iteration or `result()`, then call `team.cleanup()` explicitly.
  See the [teams example](https://github.com/stacklok/mecatl/blob/main/sdk/typescript/examples/teams.ts)
  and [team behavior](/building/what-you-get/subagents-teams-parallel.md).
- `client.schedules` exposes typed create, inspect, list, update, pause, resume,
  fire-now, and fire-history operations. Availability depends on a configured
  schedule store. See the [schedules example](https://github.com/stacklok/mecatl/blob/main/sdk/typescript/examples/schedules.ts)
  and [scheduled tasks guide](/features/scheduled-tasks.md).
- A client returned by `spawn()` can register callback tools before its first
  session create. Connected remote clients cannot host callbacks. See the
  [callback-tool example](https://github.com/stacklok/mecatl/blob/main/sdk/typescript/examples/callback-tool.ts)
  and the [transport guide](/building/deployment/grpc-http.md#start-a-private-daemon-from-node-or-bun).

## Next steps

Read [Drive via gRPC / HTTP](/building/deployment/grpc-http.md) for transport and
server API details, or the
[TypeScript SDK architecture reference](https://github.com/stacklok/mecatl/blob/main/docs/architecture.md#typescript-sdk)
for lifecycle, compatibility, and error contracts.
