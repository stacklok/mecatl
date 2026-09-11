---
title: TypeScript SDK Deno API
description: Look up the Deno TypeScript SDK local-process functions, methods, and types.
sidebar_position: 4
toc_max_heading_level: 2
---

import Heading from "@theme/Heading";

# TypeScript SDK Deno API

{/* Generated from API Extractor models and sdk/typescript/src TSDoc. Regenerate with task sdk:docs. DO NOT EDIT. */}

This page lists declarations added or changed by `@stacklok-oss/mecatl-sdk/deno`. The entry point also exports the [shared core API](./core.md).

## Symbol index

| Symbol | Kind |
| --- | --- |
| [`DaemonInfo`](#api-daemoninfo-interface) | Interface |
| [`query`](#api-query-function) | Function |
| [`Query`](#api-query-interface) | Interface |
| [`QueryOptions`](#api-queryoptions-interface) | Interface |
| [`spawn`](#api-spawn-function) | Function |
| [`SpawnedClient`](#api-spawnedclient-interface) | Interface |
| [`SpawnOptions`](#api-spawnoptions-interface) | Interface |

## Functions

<Heading as="h3" id="api-query-function"><code>query</code></Heading>

Spawns if needed, creates one session, runs one prompt, and cleans up owned resources.

```ts
export declare function query(prompt: PromptInput, options?: QueryOptions): Promise<Query>;
```

Parameters:

- `prompt` (`PromptInput`): Text or ordered text, image, and audio parts for the run.
- `options` (`QueryOptions`, optional): Session, responder, cancellation, retention, and daemon options.

Returns: `Promise<Query>`: A single-consumption event stream for the query-created session.

Throws: `PlanApprovalRequiredError` when plan mode has no approval responder.

<Heading as="h3" id="api-spawn-function"><code>spawn</code></Heading>

Starts one local `mecated` daemon with `Deno.Command` and waits for its ready-file barrier.

```ts
export declare function spawn(options?: SpawnOptions): Promise<SpawnedClient>;
```

Parameters:

- `options` (`SpawnOptions`, optional): Executable, daemon arguments, environment, readiness, and diagnostics options.

Returns: `Promise<SpawnedClient>`: A client that owns the daemon process and its private runtime directory.

## Interfaces

<Heading as="h3" id="api-daemoninfo-interface"><code>DaemonInfo</code></Heading>

Non-secret facts published by a Deno-owned daemon.

```ts
export interface DaemonInfo
```

<Heading as="h4" id="api-daemoninfo-apimajor-propertysignature"><code>DaemonInfo.apiMajor</code></Heading>

The ready document's wire API major.

```ts
readonly apiMajor: number;
```

<Heading as="h4" id="api-daemoninfo-features-propertysignature"><code>DaemonInfo.features</code></Heading>

Deployment-scoped feature identifiers reported by the daemon.

```ts
readonly features: readonly string[];
```

<Heading as="h4" id="api-daemoninfo-httpaddress-propertysignature"><code>DaemonInfo.httpAddress</code></Heading>

Loopback HTTP/SSE address used by this client.

```ts
readonly httpAddress: string;
```

<Heading as="h4" id="api-daemoninfo-pid-propertysignature"><code>DaemonInfo.pid</code></Heading>

The spawned daemon's process identifier.

```ts
readonly pid: number;
```

<Heading as="h4" id="api-daemoninfo-transport-propertysignature"><code>DaemonInfo.transport</code></Heading>

Deno-spawned clients use the HTTP/SSE transport.

```ts
readonly transport: "http";
```

<Heading as="h3" id="api-query-interface"><code>Query</code></Heading>

One query-owned event stream and its created session ID.

```ts
export interface Query extends AsyncIterable<Event>
```

<Heading as="h4" id="api-query-sessionid-propertysignature"><code>Query.sessionId</code></Heading>

The ID of the session created for this query.

```ts
readonly sessionId: string;
```

<Heading as="h3" id="api-queryoptions-interface"><code>QueryOptions</code></Heading>

Options for one Deno `query()` call.

```ts
export interface QueryOptions
```

<Heading as="h4" id="api-queryoptions-client-propertysignature"><code>QueryOptions.client</code></Heading>

Use an existing client instead of spawning a local daemon. The client remains caller-owned.

```ts
client?: Client;
```

<Heading as="h4" id="api-queryoptions-onpermissionask-propertysignature"><code>QueryOptions.onPermissionAsk</code></Heading>

Automatically answer permission asks. With no responder, query denies each ask safely.

```ts
onPermissionAsk?: PermissionAskResponder;
```

<Heading as="h4" id="api-queryoptions-onplanapproval-propertysignature"><code>QueryOptions.onPlanApproval</code></Heading>

Required in plan mode and invoked only for PresentPlan approval asks.

```ts
onPlanApproval?: PlanApprovalResponder;
```

<Heading as="h4" id="api-queryoptions-retainsession-propertysignature"><code>QueryOptions.retainSession</code></Heading>

Keep the created session after the query. SDK-spawned daemons use an in-memory store.

```ts
retainSession?: boolean;
```

<Heading as="h4" id="api-queryoptions-session-propertysignature"><code>QueryOptions.session</code></Heading>

Fields applied when query creates its session.

```ts
session?: CreateSessionOptions;
```

<Heading as="h4" id="api-queryoptions-signal-propertysignature"><code>QueryOptions.signal</code></Heading>

Abort this query and clean up every resource it created.

```ts
signal?: AbortSignal;
```

<Heading as="h4" id="api-queryoptions-spawn-propertysignature"><code>QueryOptions.spawn</code></Heading>

Deno daemon options used only when query creates its own client.

```ts
spawn?: SpawnOptions;
```

<Heading as="h3" id="api-spawnedclient-interface"><code>SpawnedClient</code></Heading>

A Client that owns one Deno.Command-launched local daemon.

```ts
export interface SpawnedClient extends Client
```

<Heading as="h4" id="api-spawnedclient-daemon-propertysignature"><code>SpawnedClient.daemon</code></Heading>

The ready document's non-secret daemon facts.

```ts
readonly daemon: DaemonInfo;
```

<Heading as="h3" id="api-spawnoptions-interface"><code>SpawnOptions</code></Heading>

Options for starting one Deno-owned local daemon.

```ts
export interface SpawnOptions extends ClientDiagnosticsOptions
```

<Heading as="h4" id="api-spawnoptions-args-propertysignature"><code>SpawnOptions.args</code></Heading>

Additional daemon arguments. SDK-owned listener and lifecycle flags cannot be replaced.

```ts
args?: readonly string[];
```

<Heading as="h4" id="api-spawnoptions-binarypath-propertysignature"><code>SpawnOptions.binaryPath</code></Heading>

Explicit mecated executable. Defaults to resolving `mecated` through PATH.

```ts
binaryPath?: string;
```

<Heading as="h4" id="api-spawnoptions-env-propertysignature"><code>SpawnOptions.env</code></Heading>

Environment overrides inherited by the daemon.

```ts
env?: Readonly<Record<string, string>>;
```

<Heading as="h4" id="api-spawnoptions-readinesstimeoutms-propertysignature"><code>SpawnOptions.readinessTimeoutMs</code></Heading>

Deadline for publication of a complete supported ready document.

```ts
readinessTimeoutMs?: number;
```

<Heading as="h4" id="api-spawnoptions-tempdirectory-propertysignature"><code>SpawnOptions.tempDirectory</code></Heading>

Parent directory for the private ready-file directory. Defaults to Deno's temporary directory.

```ts
tempDirectory?: string;
```
