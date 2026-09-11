---
title: Run a private local daemon
description: Start and dispose a private Mecatl daemon from a Node.js or Bun application.
sidebar_position: 3
---

# Run a private local daemon

Use `spawn()` when a Node.js or Bun application should own a private local
`mecated` process. Use `query()` when a script needs one prompt and automatic
cleanup.

## Prerequisites

You need Node.js 22 or Bun 1.4 on macOS or Linux. Install `mecated` from
Stacklok's Homebrew tap, then verify that it is available on `PATH`:

```sh
brew install stacklok/tap/mecatl
mecated --version
```

For release archives and source builds, see [Install Mecatl](/install.md). The
SDK locates the binary from `binaryPath`, then `MECATED_BIN`, then `PATH`. It
does not download a binary.

## Start a daemon with `spawn()`

This example uses the offline provider so you can verify process ownership
without model-provider credentials:

```ts
import { spawn } from "@stacklok-oss/mecatl-sdk/node";

await using client = await spawn({ args: ["--mock"] });
const session = await client.sessions.create({});
const result = await (await session.run("List the main packages")).result();

console.log(result.text);
```

For a live model, omit `--mock`. The child process inherits the parent
environment and ordinary `mecated` configuration. Pass `env` to override an
environment value or `args` to add `mecated serve` flags.

`spawn()` owns the daemon's private Unix socket, readiness file, lifetime pipe,
and shutdown arguments. Application-supplied `args` cannot replace those
values.

## Run one prompt with `query()`

`query()` composes daemon startup, session creation, one run, session deletion,
and daemon shutdown:

```ts
import { query } from "@stacklok-oss/mecatl-sdk/node";

const oneShot = await query("Summarize the current working tree", {
  spawn: { args: ["--mock"] },
});

for await (const event of oneShot) {
  if (event.kind === "result") console.log(event.payload.text);
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

The SDK reports cleanup faults through the configured `diagnostics` callback
and continues the remaining cleanup steps.

## Next steps

- [Register callback tools](./callback-tools.md) before creating a session on a
  private daemon.
- [Handle permissions and plans](./permissions-and-plans.md) in a long-running
  application or one-shot query.

## Related information

- [TypeScript SDK Node.js and Bun API](/reference/typescript-sdk-api/node.md)
  for all `spawn()` and `query()` options.

## Troubleshooting

<details>
<summary>spawn() reports spawn_failed</summary>

Run `mecated --version` from the parent process environment or pass the
executable's absolute path as `binaryPath`.

</details>

<details>
<summary>spawn() reports readiness_timeout</summary>

Inspect the structured diagnostic delivered to your `diagnostics` callback.
The SDK includes a bounded, credential-redacted tail of the daemon's stderr.

</details>
