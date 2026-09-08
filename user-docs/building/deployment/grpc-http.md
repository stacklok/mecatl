---
sidebar_position: 6
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

### Start a private daemon from Node or Bun

The in-repository TypeScript SDK's `@stacklok/mecatl-sdk/node` entry point provides
`spawn()` for applications that want to own one local `mecated` process. It finds an
already-installed binary from an explicit `binaryPath`, `MECATED_BIN`, or `PATH`; it never
downloads one or invokes a shell. The spawned daemon uses a private Unix socket with HTTP
disabled, and the client is returned only after the daemon publishes its ready document.
Its `client.daemon` facts come from that document's non-secret allowlist. Setting
`http: true` adds an ephemeral loopback HTTP listener but removes callback-tool support;
setting `lifetimePipe: false` opts out of parent-crash cleanup without changing `close()`.
Closing the client first cancels its owned runs and detaches its durable watches, then closes its
owned transport, sends `SIGTERM` to that child, escalates to `SIGKILL` only after a bounded grace,
and removes its private runtime directory. Cleanup continues after a failing step and reports the
fault through `diagnostics`; `close()` and `Symbol.asyncDispose` remain idempotent and do not reject
for teardown faults. If the daemon exits first, later operations fail with typed `invalid_state`.
Startup errors distinguish an exited child from a live child that missed its readiness deadline.
Their stderr report is bounded and redacted by whole line; applications can install the
structured `diagnostics` callback, while the default writes nothing to `console`.

Use `connect()` instead when another operator or service owns the daemon. A connected client
never signals a process or removes a server directory.

For a concise package-level introduction, browser BFF guidance, and focused examples for
permissions, durable attachment, teams, and schedules, see
[Use the TypeScript SDK](/building/getting-started/typescript-sdk.md).

For a one-shot prompt, `query()` composes the same spawn, session, and run APIs and yields their
ordinary events:

```ts
import { query } from "@stacklok/mecatl-sdk/node";

const oneShot = await query("Summarize this repository", {
  spawn: { binaryPath: "/opt/mecatl/bin/mecated" },
});
for await (const event of oneShot) {
  // Handle the same Event union returned by Session.run().
}
```

The default deletes the transient session and stops a daemon it spawned. Pass an existing
`client` when the daemon must remain available; `retainSession: true` then leaves the session
loadable by `oneShot.sessionId` for that daemon's lifetime. SDK-spawned daemons use an in-memory
store unless you configure durable storage, so retention is not a persistence promise. Breaking
iteration or aborting `signal` still cleans up. Without `onPermissionAsk`, an ordinary ask is
denied and reported through the client's structured diagnostics sink while the run continues.

Plan-mode queries must provide the separate approval callback before the SDK creates a session:

```ts
const planned = await query("Plan and implement the change", {
  onPlanApproval: () => "approve",
  session: { mode: 2 },
});
```

The callback may return `"approve"`, `"accept_edits"`, or `"iterate"`. Approval finishes the
current plan run, then opens a fresh run with the harness proceed prompt; the query yields both run
streams in order. It never starts twice on one live stream.

To resolve a plan that is already durably parked and has no locally live run, use
`session.resolvePlan()` instead:

```ts
const { resumed, continuation } = await session.resolvePlan("approve").result();
```

The resumed run is always present. The optional continuation has a new run ID and exists only
after `plan_approved`. A run attachment stops at its selected run's terminal; use
`session.activity()` when one view must observe both IDs.

A spawned client can register local callback tools before it creates a session:

```ts
import { spawn } from "@stacklok/mecatl-sdk/node";

const client = await spawn();
client.tool(
  "lookup",
  {
    type: "object",
    properties: { query: { type: "string" } },
    required: ["query"],
    additionalProperties: false,
  },
  async ({ query }) => `Result for ${query}`,
  { readOnly: true },
);
const session = await client.sessions.create({});
```

The SDK accepts plain JSON Schema 2020-12 objects, validates arguments before the handler, and
mounts the client-wide tool set under `mcp__sdk__*`. Registration closes after a session is
created. Tools are treated as mutating unless `readOnly: true` is set. That flag is an unverified
caller assertion with a dispatch consequence: mecatl may run asserted-read-only callbacks in its
parallel read batch, so set it only when the handler truly has no side effects. Callback tools
require the default private-UDS, HTTP-disabled spawned-daemon topology; use a different
`toolServerName` if the operator already owns the `sdk` MCP namespace.

Callback tools currently need the daemon to run with `--authority-evaluator noop`. Under the
default `local` evaluator the daemon mints its capability set from the process-wide tool catalog
before a session's client tools are mounted, so the call is denied — after the permission ask has
already been allowed — with `tool "mcp__sdk__…" denied by authority: tool is absent from the
capability set`. Pass the flag through `spawn({ args: ["--authority-evaluator", "noop"] })` for now,
and only where that relaxation is acceptable. Lifting this needs a server change so the capability
set carries a session's client tool names.

Calling `tool()` on a connected client is refused locally with typed `unsupported_feature` before
any RPC. The same code is returned by a spawned daemon that does not advertise
`mcp_servers_on_create` (including `http: true`), with the missing feature named in the message.
If session creation reaches the daemon, its `client_mcp_unsupported` or
`client_mcp_unreachable` code is preserved unchanged.

The SDK serves those callbacks from a bearer-protected ephemeral `127.0.0.1` listener. It exposes
no CORS surface, rejects foreign origin or host headers, caps request bodies and queue growth, and
runs at most eight handlers at once; `concurrency` can tighten that bound for one tool. Each handler
receives an `AbortSignal` which fires on caller cancellation, deadline or client shutdown. Strings
become MCP text, JSON values become structured content with a text mirror, and an explicit
`CallToolResult` can intentionally return `isError: true`. A thrown exception is deliberately opaque
to the model: it receives only a correlation id, while the full cause is sent to the optional
`diagnostics` callback. Closing the client aborts active callbacks, drops queued work and releases
the listener before stopping the daemon.

The repository's offline SDK gate runs this public `spawn()` → callback → shutdown path on
Node 22, Node 24, and Bun 1.4.1, including abrupt parent death through the lifetime descriptor. See the
[full SDK lifecycle and protocol reference](https://github.com/stacklok/mecatl/blob/main/docs/architecture.md#typescript-sdk)
for binary resolution, readiness, disposal, and the hand-written MCP subset.

See the detailed gRPC and HTTP
references for their request, response, privacy, and compatibility contracts.

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

### TypeScript direct-team handles

The in-repository TypeScript SDK exposes the direct team lifecycle through
`client.teams.create()`. The returned handle binds the server-created team ID across
member spawn, message, cancel, run, list, and cleanup calls. A team run is single-use:
iterate its typed member/outcome events or call `result()`, not both. Finishing that stream
does not clean up the team; call `cleanup()` explicitly.

The optional `maxTeamTokens` creation field maps directly to `max_team_tokens`. The server
owns its unadvertised cap and treats the request as tighten-only; omission leaves that cap
unchanged, and the client neither clamps nor invents a default.

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

Calling the HTTP API **from a browser** additionally needs the origin allowed:
see [Browsers and CORS](./mecated.md#browsers-and-cors). For production, front
`mecated` with a same-origin backend-for-frontend rather than shipping a bearer
token to JavaScript.

## Related information

- [Run mecated standalone](./mecated.md)
- [Cloud-native k8s with mecak8s](./mecak8s.md)
- [Scheduled tasks](../what-you-get/scheduled-tasks.md)
- [Start and resume sessions](/features/start-and-resume-sessions.md)
