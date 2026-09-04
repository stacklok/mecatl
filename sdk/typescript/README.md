# `@stacklok/mecatl-sdk`

The TypeScript SDK for the [mecatl](https://github.com/stacklok/mecatl) agentic coding
harness. The package is ESM-only and supports Node.js 24 or newer.

This first milestone establishes the package and its public entry points:

- `@stacklok/mecatl-sdk` — transport-neutral core and the browser HTTP/SSE transport;
- `@stacklok/mecatl-sdk/node` — Node/Bun gRPC transport and local-process features;
- `@stacklok/mecatl-sdk/gen` — protobuf-es types and service descriptors.

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

## Local daemon

Node/Bun callers can import `spawn` from `@stacklok/mecatl-sdk/node`. It resolves an existing
`mecated` executable from `binaryPath`, `MECATED_BIN`, then `PATH`, starts a private UDS-only
daemon, and resolves after the daemon publishes its supported ready document:

```ts
import { spawn } from "@stacklok/mecatl-sdk/node";

await using client = await spawn({ binaryPath: "/opt/mecatl/bin/mecated" });
const session = await client.sessions.create({});
```

The SDK does not download a binary or invoke a shell. Its listener, ready-file, and lifetime
arguments are reserved; `args` can add other `mecated serve` flags but cannot replace those
owned values. Closing the client stops only the daemon that client spawned and removes its
private runtime directory.

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
```

The package is licensed under Apache-2.0.

## Explicit session affinity

`withSessionAffinity(sessionId, options)` binds one legal, byte-exact
`X-Mecatl-Session-ID` routing hint while preserving caller headers. It throws a
synchronous `RangeError` when the ID is empty, non-ASCII, control-bearing, or has a
boundary space. High-level sessions returned by a server remain usable without the hint
when an external ID cannot be represented and the caller did not explicitly request a
binding.
