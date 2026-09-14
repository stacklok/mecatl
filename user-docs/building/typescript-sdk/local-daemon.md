---
title: Run a private local daemon
description:
  Start and dispose a private Mecatl daemon from a Node.js, Bun, or Deno
  application.
sidebar_position: 3
---

# Run a private local daemon

Use `spawn()` when a Node.js, Bun, or Deno application should own a private
local `mecated` process. Use `query()` when a script needs one prompt and
automatic cleanup.

## Prerequisites

You need Node.js 22, Bun 1.4, or Deno 2.9.3 or later in the Deno 2.x line on
macOS or Linux. Install `mecated` from Stacklok's Homebrew tap, then verify that
it is available on `PATH`:

```sh
brew install stacklok/tap/mecatl
mecated --version
```

For release archives and source builds, see [Install Mecatl](/install.md). The
SDK uses the `binaryPath` option when set. Node.js and Bun then check
`MECATED_BIN` and `PATH`; Deno checks `PATH`. The SDK does not download a
binary.

## Start a daemon from Node.js or Bun

This example uses the offline provider so you can verify process ownership
without model-provider credentials:

```ts
import { spawn } from '@stacklok-oss/mecatl-sdk/node';

await using client = await spawn({ args: ['--mock'] });
const session = await client.sessions.create({});
const result = await (await session.run('List the main packages')).result();

console.log(result.text);
```

For a live model, omit `--mock`. The child process inherits the parent
environment and ordinary `mecated` configuration. Pass `env` to override an
environment value or `args` to add `mecated serve` flags.

`spawn()` owns the daemon's private Unix socket, readiness file, lifetime pipe,
and shutdown arguments. Application-supplied `args` cannot replace those values.

## Start a daemon from Deno

This example requires a build of the unreleased Deno integration. SDK v0.1.0
does not include it.

Import `spawn()` from `@stacklok-oss/mecatl-sdk/deno`. Deno starts the daemon
with `Deno.Command` and connects through an ephemeral loopback gRPC listener
using the shared ConnectRPC transport. The HTTP listener is disabled.

```ts title="deno-local.ts"
import { spawn } from '@stacklok-oss/mecatl-sdk/deno';

await Deno.mkdir('.mecatl-runtime', { recursive: true });
await using client = await spawn({
  args: ['--mock'],
  tempDirectory: '.mecatl-runtime',
});

const session = await client.sessions.create({});
const result = await (await session.run('List the main packages')).result();

console.log(result.text);
```

Run the application with access to the executable, runtime directory, and
loopback listener:

```sh
deno run \
  --allow-run=mecated \
  --allow-read=.mecatl-runtime \
  --allow-write=.mecatl-runtime \
  --allow-net=127.0.0.1 \
  deno-local.ts
```

The Deno client owns the readiness file and runtime directory. It keeps the
daemon's standard input open as a parent-liveness channel. Closing the client
closes that channel and waits for the daemon to exit. `client.daemon.grpcAddress`
reports the bound address, and `client.daemon.transport` is `"grpc"`. Deno clients
do not expose the Node/Bun callback-tool or filesystem media helpers.

## Run one prompt with `query()`

`query()` composes daemon startup, session creation, one run, session deletion,
and daemon shutdown:

```ts
import { query } from '@stacklok-oss/mecatl-sdk/node';

const oneShot = await query('Summarize the current working tree', {
  spawn: { args: ['--mock'] },
});

for await (const event of oneShot) {
  if (event.kind === 'result') console.log(event.payload.text);
}
```

For Deno, use the runtime directory covered by the `spawn()` example's
permission flags:

```ts
import { query } from '@stacklok-oss/mecatl-sdk/deno';

await Deno.mkdir('.mecatl-runtime', { recursive: true });
const oneShot = await query('Summarize the current working tree', {
  spawn: { args: ['--mock'], tempDirectory: '.mecatl-runtime' },
});

for await (const event of oneShot) {
  if (event.kind === 'result') console.log(event.payload.text);
}
```

Supplying `client` uses an existing client and leaves it open. The query still
deletes its session unless `retainSession` is `true`. A retained session from an
SDK-spawned daemon lasts only for that daemon's lifetime because the default
local store is in memory.

## Dispose owned resources

Call `client.close()` or use `await using`. Disposal cancels owned runs,
detaches durable watches, closes callback-tool and transport resources, stops
the child process, and removes the private runtime directory. Closing a client
created with `connect()` never signals an operator-owned daemon.

The SDK reports cleanup faults through the configured `diagnostics` callback and
continues the remaining cleanup steps.

## Next steps

- [Register callback tools](./callback-tools.md) before creating a session from
  Node.js or Bun on a private daemon.
- [Handle permissions and plans](./permissions-and-plans.md) in a long-running
  application or one-shot query.

## Related information

- [TypeScript SDK Node.js and Bun API](/reference/typescript-sdk-api/node.md)
  for all `spawn()` and `query()` options.
- [TypeScript SDK Deno API](/reference/typescript-sdk-api/deno.md) for Deno
  `spawn()` and `query()` options.

## Troubleshooting

<details>
<summary>spawn() reports spawn_failed</summary>

Run `mecated --version` from the parent process environment or pass the
executable's absolute path as `binaryPath`.

</details>

<details>
<summary>spawn() reports readiness_timeout</summary>

Inspect the structured diagnostic delivered to your `diagnostics` callback. The
SDK includes a bounded, credential-redacted tail of the daemon's stderr.

</details>

<details>
<summary>Deno reports a permission error</summary>

Grant `--allow-run` for `mecated`, read and write access to the configured
runtime directory, and `--allow-net=127.0.0.1` for the local HTTP and SSE
connection.

</details>
