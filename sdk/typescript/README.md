# `@stacklok-oss/mecatl-sdk`

The TypeScript SDK for the [mecatl](https://github.com/stacklok/mecatl) agentic coding
harness. The package is ESM-only and supports Node.js 22 or newer, Bun,
Deno `>=2.9.3 <3`, and modern browsers.

The Deno integration described here is unreleased and excluded from v0.1.0.
Its first supported SDK release has not been assigned.

## Install

The package is public on [npmjs](https://www.npmjs.com/package/@stacklok-oss/mecatl-sdk):

```sh
pnpm add @stacklok-oss/mecatl-sdk
```

See the [SDK changelog](https://github.com/stacklok/mecatl/blob/main/sdk/typescript/CHANGELOG.md)
for release notes.

## Public entry points

The package has four public entry points:

- `@stacklok-oss/mecatl-sdk` - transport-neutral core plus browser and Deno remote HTTP/SSE;
- `@stacklok-oss/mecatl-sdk/node` - Node/Bun gRPC and local-process features;
- `@stacklok-oss/mecatl-sdk/deno` - Deno gRPC plus `Deno.Command` local-process features;
- `@stacklok-oss/mecatl-sdk/gen` - protobuf-es types and service descriptors.

## Examples

The focused programs in [the repository examples](https://github.com/stacklok/mecatl/tree/main/sdk/typescript/examples)
cover remote `connect()`, Node/Bun and Deno local `spawn()` and `query()`, callback tools,
browser+BFF deployment guidance, permissions, durable attachment, teams, schedules, and
the two-run `PlanResolution`. CI builds the package first and type-checks those programs
through only `.`, `./node`, `./deno`, and `./gen`.
The larger Slack bot is a separate pnpm project with its own package-export typecheck leg.

## Durable attachment

`session.attach(runId?)` follows one run, while `session.activity()` follows the
cross-run durable timeline. Both expose a serializable cursor for application-owned
checkpoint persistence. A checkpoint advances when the consumer requests the next
envelope, so reconnecting from the exposed cursor can redeliver the last processed
envelope; consumers with side effects must therefore be idempotent.

Both views omit log-only/debugger records by default; pass `includeLogOnly: true` to
add them without changing the order or cursors of records already visible. Activity
also includes run-less `schedule.*` records, even though the same session has no run
for `attach()` to select.

The guarantee is ordered, at-least-once delivery **for durably appended events**.
It is deliberately not absolute: if the event-log backend is totally unavailable and
the process recording the event is lost, the failed append can leave an undetectable
gap. When a gap is detectable, the raw watch reports `{ kind: "gap" }`; the ergonomic
run attachment ends with `ActivityGapError`. Session activity yields the gap delivery
fact, then raises the same error if iteration continues. Both leave the cursor at the
last envelope before the gap. `CursorExpiredError` also ends the attachment and requires
the caller to choose an explicit restart from the beginning or a transcript reload.

## Run controls by run id

A `Run` returned by `session.run()` carries its own `approve`, `cancel`, and `steer`.
When the client did not start the run — it re-attached after a reload, or it follows
the run through `session.activity()` — `session.controls(runId)` gives the same
controls addressed by run id, over HTTP only. Every control is strict: it names the
run through `expected_run_id`, so one that outlives its run is refused as
`stale_run_control` instead of acting on the session's next run. `steer` returns the
server's `accepted`, `appended`, or `too_late` outcome (the caller keeps the text on
`too_late`) and takes a client-minted `messageId` that the run's later `steer` event
echoes as the drained bundle's watermark; `cancelSteer` retracts the pending bundle.
`client.server.compatibility()` reports whether the server advertises the `http_steer` feature
these two controls require.

## MCP authorization, connectors, and workspace enrollment

When a tool call parks on `authorization.required`, `session.mcpAuthorization(id)`
returns the controls for that one authorization: `presentation()` reads the live
browser URL, `recheck()` asks the daemon to re-inspect it and streams the outcome
(and the resumed run when it succeeds), and `cancel()` abandons it with the same
stream shape. `session.mcpConnectors()` inspects the session's broker-local MCP
connector catalogue without probing upstreams, and `session.workspaceEnrollment`
(`connect()`, `retry(id)`, `cancel(id)`) drives the pre-prompt workspace-services
enrollment. `session.cancelChild(childId)` stops one running subagent, parallel
branch, or team member without cancelling the whole run. All are HTTP controls;
gate the first three on the `mcp_connector_status` and `workspace_enrollment`
server capabilities.

## Node and Bun local daemon

Node/Bun callers can import `spawn` from `@stacklok-oss/mecatl-sdk/node`. It resolves an existing
`mecated` executable from `binaryPath`, `MECATED_BIN`, then `PATH`, starts a private UDS-only
daemon, and resolves after the daemon publishes its supported ready document:

```ts
import { spawn } from "@stacklok-oss/mecatl-sdk/node";

await using client = await spawn({
  binaryPath: "/opt/mecatl/bin/mecated",
  diagnostics: (record) => console.error(record),
});
console.log(client.daemon.features);
const session = await client.sessions.create({});
```

The SDK does not download a binary or invoke a shell. Its listener, ready-file, and lifetime
arguments are reserved; `args` can add other `mecated serve` flags but cannot replace those
owned values. The default daemon exposes only its private Unix socket. `http: true` adds an
ephemeral loopback HTTP listener, which makes callback-tool session creation unavailable;
the client reports that capability from `client.daemon.features`. The lifetime endpoint is
enabled by default so a vanished parent produces EOF in the daemon; `lifetimePipe: false`
opts out of crash cleanup, while `close()` still stops the child. `env` values override the
otherwise inherited process environment and are never exposed through `client.daemon`.
Closing the client cancels its owned runs, detaches durable watches, closes its owned transport,
then stops only the daemon that client spawned and removes its private runtime directory. Shutdown
uses `SIGTERM` with a bounded grace before `SIGKILL`; cleanup faults go to `diagnostics`, do not
skip later steps, and do not make `close()` reject. Connected clients never signal a process.
`close()` and `Symbol.asyncDispose` are the same idempotent operation. A daemon that exits later
without disposal makes subsequent client and session calls fail with typed `invalid_state` instead
of a dead-socket transport error. A child exit before readiness is a typed `spawn_failed`; a live child that misses
`readinessTimeoutMs` is a typed `readiness_timeout` and is stopped. Both include only the
bounded, whole-line-redacted end of stderr. The optional `diagnostics` callback receives one
structured safe record; without it the SDK never writes to `console`.

## Deno local daemon

Deno callers import `spawn` from `@stacklok-oss/mecatl-sdk/deno`. The SDK launches an
installed `mecated` with `Deno.Command`, opens an ephemeral loopback gRPC
connection, and returns after the ready-file and compatibility barriers succeed:

```ts
import { spawn } from "@stacklok-oss/mecatl-sdk/deno";

await Deno.mkdir(".mecatl-runtime", { recursive: true });
await using client = await spawn({
  binaryPath: "/opt/mecatl/bin/mecated",
  tempDirectory: ".mecatl-runtime",
});
const session = await client.sessions.create({});
```

Run this program with permission to execute the binary, read and write the
runtime parent, and connect to loopback:

```sh
deno run \
  --allow-run=/opt/mecatl/bin/mecated \
  --allow-read=.mecatl-runtime \
  --allow-write=.mecatl-runtime \
  --allow-net=127.0.0.1 \
  deno-local.ts
```

`binaryPath` defaults to `mecated` through Deno's PATH resolution. The daemon
inherits the parent environment, and `env` values override individual entries.
The SDK reserves its listener, ready-file, and lifetime arguments. It holds a
piped stdin open as the parent-liveness channel, so parent exit produces EOF and
gracefully stops the daemon. `close()` closes that channel, applies bounded
signal fallbacks, and removes the private runtime directory. Deno uses the same
ConnectRPC gRPC transport as Node and Bun. Local spawn disables the HTTP listener;
`client.daemon` reports `grpcAddress` and `transport: "grpc"`.

For an operator-owned daemon, import `connect` from `@stacklok-oss/mecatl-sdk/deno`
and pass its gRPC `baseUrl` or Unix `socketPath`. TLS settings use `nodeOptions`.
Grant Deno network access to the selected host; for Unix sockets, use
`--allow-net=unix:<ABSOLUTE_SOCKET_PATH>`. The root import still provides HTTP/SSE.
Path media helpers and callback-tool registration remain Node/Bun features.

## One-shot queries

The Node/Bun and Deno entry points export `query()`, which composes spawn, session creation, one run,
and cleanup while yielding the ordinary SDK event union:

```ts
import { query } from "@stacklok-oss/mecatl-sdk/node";

const oneShot = await query("Summarize this repository", {
  spawn: { binaryPath: "/opt/mecatl/bin/mecated" },
});
for await (const event of oneShot) {
  console.log(event.kind);
}
```

The default deletes the transient session and closes only a client it created. Supplying `client`
keeps that client and its daemon caller-owned while still deleting the query's session. With a
supplied client, `retainSession: true` skips deletion and `oneShot.sessionId` can load the session
for the daemon's remaining lifetime. A daemon created by `query()` is still stopped at cleanup and
uses an in-memory store by default, so retention does not promise persistence. Signal abort and
early iterator return follow the same cleanup path. Plan mode requires `onPlanApproval` before any
resource is created. The plan-specific responder returns `"approve"`, `"accept_edits"`, or
`"iterate"`; an approval drains the current plan run and then opens a fresh run carrying the
harness proceed prompt. Both runs' events are yielded in order. Without `onPermissionAsk`, query
denies each ordinary ask, reports it through the client diagnostics sink, and lets the run continue.

## Parked plan resolution

For a plan durably parked on `PresentPlan` with no locally live `Run`,
`session.resolvePlan()` consumes the server's atomic stream. Iterate its events or call `result()`,
never both:

```ts
const resolution = session.resolvePlan("approve");
const { resumed, continuation } = await resolution.result();
```

The resumed result always comes first. `continuation` exists only when the resumed run ends with
`plan_approved`, and has a different run ID. Attachments stay bound to one run; use
`session.activity()` to observe both IDs. A continuation that fails before receiving an ID throws
`PlanContinuationStartError` with the server's original message.

## Development

Run the SDK gates from the repository root:

```sh
task sdk:install
task sdk:lint
task sdk:typecheck
task sdk:test
task sdk:build
task sdk:api:check
task sdk:pack
task sdk:deno
```

The package is licensed under Apache-2.0.

## Direct teams

`client.teams.create()` returns a `Team` handle bound to the daemon-created team ID. The
handle exposes the seven direct-team RPCs as `spawn`, `message`, `cancel`, `run`, `list`,
and `cleanup`; consuming `run()` never cleans the team up implicitly. A team run can be
iterated or drained with `result()`, but not both. Its member frames are the ordinary
discriminated SDK events tagged with the producing member, and its terminal frame carries
the typed team outcome. A missing or duplicate terminal outcome is a protocol error.

`maxTeamTokens` is optional and is sent verbatim as `max_team_tokens`. The daemon owns and
does not advertise its cap: it treats a positive request value as tighten-only. Omitting the
option supplies no client default, and the SDK never treats it as a way to increase the
daemon budget.

## Explicit session affinity

`withSessionAffinity(sessionId, options)` binds one legal, byte-exact
`X-Mecatl-Session-ID` routing hint while preserving caller headers. It throws a
synchronous `RangeError` when the ID is empty, non-ASCII, control-bearing, or has a
boundary space. High-level sessions returned by a server remain usable without the hint
when an external ID cannot be represented and the caller did not explicitly request a
binding.
