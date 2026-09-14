# ADR 0335 - Deno owns local daemons through Deno.Command

- Status: Accepted
- Date: 2026-09-11
- Superseded by: [ADR 0336](./0336-typescript-sdk-deno-grpc.md), for the HTTP-only transport and
  Node-compatibility exclusions.
- Scope: `sdk/typescript/`, daemon parent-liveness hosting, package exports, runtime qualification,
  release verification, and public documentation.
- Supersedes: [ADR 0334](./0334-typescript-sdk-deno.md), for its Deno public entry-point and
  local-process exclusions. ADR 0334's runtime range and emitted declaration decisions remain in
  force.

## Context

ADR 0334 qualified the transport-neutral TypeScript SDK and generated declarations in Deno. It
excluded local process ownership because the existing `./node` implementation combines Node child
processes, Unix-domain gRPC, filesystem media helpers, and callback-tool hosting.

Deno has its own stable process API. `Deno.Command` can launch `mecated`, retain a child handle,
pipe stderr, and hold a piped stdin open for the child's lifetime. Deno cannot assign an arbitrary
extra child descriptor such as the Node launcher's descriptor 3. A Deno implementation therefore
needs a separate adapter, but it does not need Node compatibility or a new server protocol.

## Decision

**1. Publish a Deno-specific `./deno` entry point.** The entry point re-exports the
transport-neutral API and adds Deno-native `spawn()` and `query()`. It imports no Node built-ins,
`@connectrpc/connect-node`, path media helpers, or callback-tool host. The root `.` entry point
continues to serve remote HTTP/SSE and browser callers.

**2. Launch local daemons with `Deno.Command`.** `spawn()` resolves the explicit `binaryPath`, or
passes `mecated` to Deno for PATH resolution. It invokes no shell. The child inherits the parent
environment, with `env` values applied as overrides. The SDK reserves its gRPC, HTTP, ready-file,
and lifetime arguments so caller arguments cannot replace the loopback topology or ownership
channel.

**3. Use loopback HTTP/SSE for the Deno client.** The SDK binds ephemeral `127.0.0.1` gRPC and HTTP
listeners because `mecated` always has a gRPC listener, then connects the returned client through
HTTP/SSE. The ready document must contain a matching child pid, a valid compatibility descriptor,
and the SDK-owned loopback HTTP address. `spawn()` returns only after the first compatibility call
succeeds. A TCP listener does not grant client-provided MCP authority, so Deno-spawned clients do
not expose callback-tool registration.

**4. Carry parent death over piped stdin.** `Deno.Command` starts the child with piped stdin and
holds its writer without sending bytes. The SDK passes `--lifetime-stdin`; `mecated` validates that
stdin is a pipe and watches it for EOF. Parent exit closes the pipe in the kernel and triggers the
ordinary graceful daemon shutdown. Explicit client disposal closes the same writer first, then
uses bounded `SIGTERM` and `SIGKILL` fallbacks through the captured child handle.

**5. Keep the existing readiness, diagnostics, and cleanup contract.** Each launch uses a private
temporary directory for `ready.json`. Startup failure disposes any transport, stops the captured
child, and removes that directory. Stderr capture stays bounded and applies the same whole-line
credential-shape redaction as the Node implementation. `Client.close()` owns only the child and
directory created by that client. Deno `query()` uses the shared query state machine with the Deno
spawn function injected at its runtime entry point.

**6. Qualify the floor and current Deno 2.x.** The package declares Deno `>=2.9.3 <3`. CI runs the
packed-package check and real `Deno.Command` integration at 2.9.3 and current Deno 2.x. Release
verification repeats the floor integration before publication.

## Consequences

- Deno applications can connect remotely through `.` or own a local daemon through `./deno`.
- Local spawn requires Deno run, read, write, and loopback network permissions. Deno enforces
  these grants before the SDK can launch or connect.
- Deno local clients use HTTP/SSE and do not provide real gRPC, Unix-domain sockets, callback
  tools, or path media helpers. Those capabilities remain in `./node` for Node and Bun.
- The daemon adds the advanced `--lifetime-stdin` hosting flag. It is mutually exclusive with
  `--lifetime-pipe-fd` and is absent from ACP help.
- Deno Deploy and other edge runtimes remain outside the runtime contract because they do not
  provide the same local process and filesystem authority.

## See also

- [TypeScript SDK Deno HTTP/SSE support](./0334-typescript-sdk-deno.md)
- [TypeScript SDK local daemon and callback tools](./0292-typescript-sdk-local-daemon-and-tools.md)
- [TypeScript SDK Deno support acceptance plan](../acceptance/sdk-typescript-deno.md)
- [TypeScript SDK architecture](../architecture.md#typescript-sdk)
