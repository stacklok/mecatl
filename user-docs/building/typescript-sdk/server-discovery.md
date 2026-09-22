---
title: Inspect a server before creating a session
description:
  Check Mecatl compatibility and safe server identity before creating a
  session.
sidebar_position: 8
---

# Inspect a server before creating a session

Use `client.server` to check what a Mecatl server supports and read its safe
build identity. These calls do not create or bind a session.

## Check compatibility

Call `client.server.compatibility()` when your application needs a current
compatibility descriptor:

```ts
const compatibility = await client.server.compatibility({
  timeoutMs: 5_000,
});
```

Each explicit call starts a fresh compatibility request. The newest request
also becomes the compatibility check shared by subsequent ordinary SDK
operations. Ordinary operations reuse that request until it fails or another
explicit call refreshes it.

Interpret the result according to the question your application needs to
answer:

|Field|Use it for|
|-|-|
|`apiMajor`|Confirming the server speaks the API major supported by this SDK. The SDK throws before returning an incompatible value.|
|`capabilities`|Checking which deployment capabilities are available, such as scheduling, skills, or manual dream targets.|
|`features`|Checking open feature identifiers that this build makes available on the connected listener. Unknown identifiers remain in the set.|
|`deployment`|Displaying an optional operator-defined deployment label. Treat it as opaque text.|

Use `ServerFeature` for known feature identifiers. Use `ServerPosture` when
comparing known `capabilities.posture` values, but preserve and handle unknown
posture strings so newer servers remain observable.

## Read safe server identity

Call `client.server.info()` to read the authenticated server's build identity:

```ts
const info = await client.server.info();
```

The SDK first checks the ordinary cached compatibility descriptor and refuses
the call locally unless the server advertises `ServerFeature.ServerInfo`.
Calling `info()` does not force a compatibility refresh.

When you already know which provider you want to inspect, pass its exact ID:

```ts
const info = await client.server.info({ providerId: selectedProviderId });
```

The SDK sends only the provider ID supplied for that call. It does not infer a
provider from a session, model, or server default.

`info.buildId` identifies the composed server build, and
`info.serverImplementation` identifies its composition family. The optional
`info.llmProviderDisplayEndpoint` is sanitized diagnostic text. Display or log
that endpoint for troubleshooting; use your application's configured Mecatl
URL to create connections.

## Handle discovery failures

Handle typed errors according to the action your application can take:

- `IncompatibleServerError` means the compatibility endpoint is unavailable or
  the server uses a different API major. Connect to a supported server.
- `UnsupportedFeatureError` from `info()` means this server does not advertise
  the server-info feature. Other supported SDK operations remain available.
- `ProtocolError` means a successful response contained malformed compatibility
  or identity data. Treat the server response as unusable.
- Authentication, server, and transport failures use the SDK's existing typed
  errors. A later explicit compatibility call retries with a fresh request.

## Next steps

- [Connect an application](./connect.md) to configure gRPC or HTTP access.
- [Work with sessions and runs](./sessions-and-runs.md) after checking the
  capabilities your application needs.

## Related information

- [TypeScript SDK API reference](/reference/typescript-sdk-api/index.md)
- [Feature availability](/features/capability-matrix.md)
- [Drive Mecatl through gRPC or HTTP](/building/grpc-http.md)
