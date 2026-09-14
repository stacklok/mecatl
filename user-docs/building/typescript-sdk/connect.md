---
title: Connect an application
description:
  Connect Node.js, Bun, Deno, or browser applications to an operator-owned
  Mecatl daemon.
sidebar_position: 2
---

# Connect an application

Use `connect()` when an operator owns the Mecatl daemon and your application
owns only the client connection. Node.js, Bun, and Deno connect to the gRPC listener.
Browser applications use the HTTP and SSE API and
connect through a same-origin backend-for-frontend (BFF).

## Connect from Node.js or Bun

Import `connect()` from the Node.js and Bun entry point, then pass the gRPC
listener's HTTP or HTTPS authority:

```ts
import { connect } from '@stacklok-oss/mecatl-sdk/node';

const client = connect({
  baseUrl: process.env.MECATL_URL ?? 'http://127.0.0.1:8080',
});

try {
  const session = await client.sessions.create({});
  const result = await (
    await session.run('Summarize this repository')
  ).result();
  console.log(result.text);
} finally {
  await client.close();
}
```

Pass fixed request headers with `headers`:

```ts
const token = process.env.MECATL_TOKEN;
if (token === undefined) throw new Error('MECATL_TOKEN is required');

const client = connect({
  baseUrl: 'https://mecatl.example.com',
  headers: { authorization: `Bearer ${token}` },
});
```

Use `credentialProvider` instead when the application refreshes credentials. The
SDK calls the provider for every request and does not persist its returned
headers.

## Connect from Deno

The Deno integration is unreleased and excluded from SDK v0.1.0.

Import `connect()` from the Deno entry point and pass the gRPC listener's HTTP
or HTTPS authority. Deno uses the same ConnectRPC transport as Node.js and Bun:

```ts
import { connect } from '@stacklok-oss/mecatl-sdk/deno';

await using client = connect({
  baseUrl: 'https://mecatl.example.com',
});

const session = await client.sessions.create({});
const result = await (await session.run('Summarize this repository')).result();
console.log(result.text);
```

Grant network access to the daemon host when you run the application. For the
example URL, use `--allow-net=mecatl.example.com`. Credentials use the same
`headers` and `credentialProvider` options as Node.js and Bun. For a private CA,
pass its certificate through `nodeOptions.ca`; grant read access if your
application loads the certificate from disk.

To connect to a Unix-domain socket, pass `socketPath` instead of `baseUrl` and
grant network access with `--allow-net=unix:<ABSOLUTE_SOCKET_PATH>`. The socket's
operating-system permissions also apply. See the
[Deno API reference](/reference/typescript-sdk-api/deno.md) for connection options.
If your daemon exposes only HTTP and SSE, import `connect()` from the root
`@stacklok-oss/mecatl-sdk` entry point instead.

## Connect from a browser

Import from the transport-neutral entry point and use the BFF's same-origin
path:

```ts
import { connect } from '@stacklok-oss/mecatl-sdk';

await using client = connect({
  baseUrl: '/mecatl',
  credentials: 'include',
});

const session = await client.sessions.create({});
const result = await (await session.run('Explain the selected file')).result();
console.log(result.text);
```

The BFF must inject the daemon credential and enforce Origin and CSRF policy.
Keep privileged daemon credentials out of browser JavaScript. The SDK supplies
the browser-facing HTTP and SSE client; it does not include a BFF server.

For local browser development, an operator can configure the daemon's exact CORS
origins. See
[Drive Mecatl through gRPC or HTTP](/building/deployment/grpc-http.md) for
listener and transport configuration.

## Observe connection status

`client.status` is a multicast status store. Read the current value with
`getSnapshot()` or subscribe to changes:

```ts
const unsubscribe = client.status.subscribe((status) => {
  console.log('Mecatl connection:', status);
});

unsubscribe();
```

The status vocabulary is `connecting`, `online`, `reconnecting`, `offline`,
`unauthorized`, and `incompatible`.

## Next steps

- [Work with sessions and runs](./sessions-and-runs.md) to consume events and
  send run controls.
- [Resume durable activity](./durable-activity.md) when an application must
  survive a disconnect or restart.
- [TypeScript SDK API reference](/reference/typescript-sdk-api/index.md) for
  connection options and error types.

## Related information

- [Drive Mecatl through gRPC or HTTP](/building/deployment/grpc-http.md)
- [HTTP and SSE API reference](/reference/http-sse-api.md)
- [gRPC API reference](/reference/grpc-api.md)
