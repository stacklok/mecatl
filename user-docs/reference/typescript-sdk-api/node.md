---
title: TypeScript SDK Node.js and Bun API
description: Look up the Node.js and Bun TypeScript SDK functions, methods, and types.
sidebar_position: 3
toc_max_heading_level: 2
---

import Heading from "@theme/Heading";

# TypeScript SDK Node.js and Bun API

{/* Generated from API Extractor models and sdk/typescript/src TSDoc. Regenerate with task sdk:docs. DO NOT EDIT. */}

This page lists declarations added or changed by `@stacklok-oss/mecatl-sdk/node`. The entry point also exports the [shared core API](./core.md).

## Symbol index

|Symbol|Kind|
|-|-|
|[`audioPartFromPath`](#api-audiopartfrompath-function)|Function|
|[`CallToolContent`](#api-calltoolcontent-typealias)|Type alias|
|[`CallToolResult`](#api-calltoolresult-interface)|Interface|
|[`connect`](#api-connect-function)|Function|
|[`createNodeTransport`](#api-createnodetransport-function)|Function|
|[`DaemonInfo`](#api-daemoninfo-interface)|Interface|
|[`imagePartFromPath`](#api-imagepartfrompath-function)|Function|
|[`NodeClient`](#api-nodeclient-interface)|Interface|
|[`NodeConnectOptions`](#api-nodeconnectoptions-typealias)|Type alias|
|[`NodeTransportCommonOptions`](#api-nodetransportcommonoptions-interface)|Interface|
|[`NodeTransportOptions`](#api-nodetransportoptions-typealias)|Type alias|
|[`query`](#api-query-function)|Function|
|[`Query`](#api-query-interface)|Interface|
|[`QueryOptions`](#api-queryoptions-interface)|Interface|
|[`spawn`](#api-spawn-function)|Function|
|[`SpawnedClient`](#api-spawnedclient-interface)|Interface|
|[`SpawnOptions`](#api-spawnoptions-interface)|Interface|
|[`ToolDefinition`](#api-tooldefinition-interface)|Interface|
|[`ToolHandler`](#api-toolhandler-typealias)|Type alias|
|[`ToolHandlerContext`](#api-toolhandlercontext-interface)|Interface|
|[`ToolJsonValue`](#api-tooljsonvalue-typealias)|Type alias|
|[`ToolOptions`](#api-tooloptions-interface)|Interface|
|[`ToolRegistrationError`](#api-toolregistrationerror-class)|Class|
|[`ToolRegistrationReason`](#api-toolregistrationreason-typealias)|Type alias|
|[`ToolSchema`](#api-toolschema-typealias)|Type alias|

## Classes

<Heading as="h3" id="api-toolregistrationerror-class"><code>ToolRegistrationError</code></Heading>

A callback tool could not be added to the client registry.

```ts
export declare class ToolRegistrationError extends MecatlError
```

Callable members: [`constructor`](#api-toolregistrationerror-constructor-constructor)

<Heading as="h4" id="api-toolregistrationerror-constructor-constructor"><code>ToolRegistrationError.constructor</code></Heading>

Constructs a new instance of the `ToolRegistrationError` class

```ts
constructor(reason: ToolRegistrationReason, message: string, cause?: unknown);
```

Parameters:

- `reason` (`ToolRegistrationReason`)
- `message` (`string`)
- `cause` (`unknown`, optional)

<Heading as="h4" id="api-toolregistrationerror-reason-property"><code>ToolRegistrationError.reason</code></Heading>

Stable reason distinguishing the rejected registration input.

```ts
readonly reason: ToolRegistrationReason;
```

## Functions

<Heading as="h3" id="api-audiopartfrompath-function"><code>audioPartFromPath</code></Heading>

Reads a Node.js or Bun path into an audio prompt part.

```ts
export declare function audioPartFromPath(
  path: string | URL,
  mimeType: string
): Promise<AudioPromptPart>;
```

Parameters:

- `path` (`string | URL`): File path or file URL to read.
- `mimeType` (`string`): Audio MIME type for the file contents.

Returns: `Promise<AudioPromptPart>`: A validated audio prompt part containing the file's bytes.

Throws: `PromptValidationError` when the MIME type or size is invalid.

<Heading as="h3" id="api-connect-function"><code>connect</code></Heading>

Creates a client for Node.js or Bun over gRPC or a caller-provided transport.

```ts
export declare function connect(options: NodeConnectOptions): NodeClient;
```

Parameters:

- `options` (`NodeConnectOptions`): gRPC endpoint, credentials, diagnostics, or a caller-owned transport.

Returns: `NodeClient`: A high-level client with callback-tool registration.

<Heading as="h3" id="api-createnodetransport-function"><code>createNodeTransport</code></Heading>

Creates a gRPC transport for Node.js or Bun over HTTP/2 or a Unix domain socket.

```ts
export declare function createNodeTransport(
  options: NodeTransportOptions
): Transport;
```

Parameters:

- `options` (`NodeTransportOptions`): TCP authority or Unix socket plus credentials and HTTP/2 settings.

Returns: `Transport`: A Connect-ES gRPC transport.

<Heading as="h3" id="api-imagepartfrompath-function"><code>imagePartFromPath</code></Heading>

Reads a Node.js or Bun path into an image prompt part.

```ts
export declare function imagePartFromPath(
  path: string | URL,
  mimeType: string
): Promise<ImagePromptPart>;
```

Parameters:

- `path` (`string | URL`): File path or file URL to read.
- `mimeType` (`string`): Image MIME type for the file contents.

Returns: `Promise<ImagePromptPart>`: A validated image prompt part containing the file's bytes.

Throws: `PromptValidationError` when the MIME type or size is invalid.

<Heading as="h3" id="api-query-function"><code>query</code></Heading>

Spawns if needed, creates one session, runs one prompt, and cleans up owned resources.

```ts
export declare function query(
  prompt: PromptInput,
  options?: QueryOptions
): Promise<Query>;
```

Parameters:

- `prompt` (`PromptInput`): Text or ordered text, image, and audio parts for the run.
- `options` (`QueryOptions`, optional): Session, responder, cancellation, retention, and daemon options.

Returns: `Promise<Query>`: A single-consumption event stream for the query-created session.

Throws: `PlanApprovalRequiredError` when plan mode has no approval responder.

<Heading as="h3" id="api-spawn-function"><code>spawn</code></Heading>

Starts one local `mecated` daemon and resolves when it reports that it is ready.

```ts
export declare function spawn(options?: SpawnOptions): Promise<SpawnedClient>;
```

Parameters:

- `options` (`SpawnOptions`, optional): Executable, environment, daemon, readiness, and diagnostic options.

Returns: `Promise<SpawnedClient>`: A client that owns the ready daemon and its private runtime directory.

Throws: `MecatlError` with `unsupported_platform` on an unsupported operating system.

Throws: `MecatlError` with `spawn_failed` when the daemon cannot start correctly.

Throws: `MecatlError` with `readiness_timeout` when a live daemon misses its deadline.

## Interfaces

<Heading as="h3" id="api-calltoolresult-interface"><code>CallToolResult</code></Heading>

An explicit MCP callback-tool result, including intentional error results.

```ts
export interface CallToolResult
```

<Heading as="h4" id="api-calltoolresult-content-propertysignature"><code>CallToolResult.content</code></Heading>

```ts
readonly content: readonly CallToolContent[];
```

<Heading as="h4" id="api-calltoolresult-iserror-propertysignature"><code>CallToolResult.isError</code></Heading>

```ts
readonly isError?: boolean;
```

<Heading as="h4" id="api-calltoolresult-structuredcontent-propertysignature"><code>CallToolResult.structuredContent</code></Heading>

```ts
readonly structuredContent?: ToolJsonValue;
```

<Heading as="h3" id="api-daemoninfo-interface"><code>DaemonInfo</code></Heading>

Non-secret facts published by an SDK-owned daemon.

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

<Heading as="h4" id="api-daemoninfo-pid-propertysignature"><code>DaemonInfo.pid</code></Heading>

The spawned daemon's process identifier.

```ts
readonly pid: number;
```

<Heading as="h4" id="api-daemoninfo-socketpath-propertysignature"><code>DaemonInfo.socketPath</code></Heading>

The private Unix-domain gRPC socket path.

```ts
readonly socketPath: string;
```

<Heading as="h4" id="api-daemoninfo-transport-propertysignature"><code>DaemonInfo.transport</code></Heading>

Spawned clients always use the Unix-domain gRPC transport.

```ts
readonly transport: "unix";
```

<Heading as="h3" id="api-nodeclient-interface"><code>NodeClient</code></Heading>

A client for Node.js or Bun with local callback-tool registration.

```ts
export interface NodeClient extends Client
```

Callable members: [`tool()`](#api-nodeclient-tool-methodsignature)

<Heading as="h4" id="api-nodeclient-tool-methodsignature"><code>NodeClient.tool</code></Heading>

Registers one callback tool in the client-wide immutable tool set.

```ts
tool(name: string, schema: ToolSchema, handler: ToolHandler, options?: ToolOptions): ToolDefinition;
```

Parameters:

- `name` (`string`): Name advertised by the local MCP server.
- `schema` (`ToolSchema`): JSON Schema 2020-12 value for the tool arguments.
- `handler` (`ToolHandler`): Function invoked with validated arguments and an abort signal.
- `options` (`ToolOptions`, optional): Read-only assertion and per-tool concurrency limit.

Returns: `ToolDefinition`: The immutable registered-tool description and model-facing name.

Throws: `ToolRegistrationError` when the name, schema, or options are invalid.

<Heading as="h3" id="api-nodetransportcommonoptions-interface"><code>NodeTransportCommonOptions</code></Heading>

Shared credentials and HTTP/2 settings for the gRPC transport in Node.js or Bun.

```ts
export interface NodeTransportCommonOptions extends CredentialOptions
```

<Heading as="h4" id="api-nodetransportcommonoptions-nodeoptions-propertysignature"><code>NodeTransportCommonOptions.nodeOptions</code></Heading>

Additional HTTP/2 session options. The SDK controls `createConnection` when using `socketPath`.

```ts
nodeOptions?: Omit<ClientSessionOptions, "createConnection">;
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

Options for one `query()` call.

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

Daemon options used only when query creates its own client.

```ts
spawn?: SpawnOptions;
```

<Heading as="h3" id="api-spawnedclient-interface"><code>SpawnedClient</code></Heading>

A Client that owns one locally spawned daemon.

```ts
export interface SpawnedClient extends NodeClient
```

<Heading as="h4" id="api-spawnedclient-daemon-propertysignature"><code>SpawnedClient.daemon</code></Heading>

The ready document's non-secret daemon facts.

```ts
readonly daemon: DaemonInfo;
```

<Heading as="h3" id="api-spawnoptions-interface"><code>SpawnOptions</code></Heading>

Options for starting one SDK-owned local daemon.

```ts
export interface SpawnOptions extends ClientDiagnosticsOptions
```

<Heading as="h4" id="api-spawnoptions-args-propertysignature"><code>SpawnOptions.args</code></Heading>

Additional daemon arguments. SDK-owned listener and lifecycle flags cannot be replaced.

```ts
args?: readonly string[];
```

<Heading as="h4" id="api-spawnoptions-binarypath-propertysignature"><code>SpawnOptions.binaryPath</code></Heading>

Explicit mecated executable. Resolution otherwise uses MECATED_BIN, then PATH.

```ts
binaryPath?: string;
```

<Heading as="h4" id="api-spawnoptions-env-propertysignature"><code>SpawnOptions.env</code></Heading>

Environment overrides merged over the parent process environment.

```ts
env?: Readonly<NodeJS.ProcessEnv>;
```

<Heading as="h4" id="api-spawnoptions-http-propertysignature"><code>SpawnOptions.http</code></Heading>

Also expose the daemon's HTTP/SSE listener on an ephemeral loopback port.

```ts
http?: boolean;
```

<Heading as="h4" id="api-spawnoptions-lifetimepipe-propertysignature"><code>SpawnOptions.lifetimePipe</code></Heading>

Keep the daemon tied to the parent-liveness descriptor. Defaults to `true`.

```ts
lifetimePipe?: boolean;
```

<Heading as="h4" id="api-spawnoptions-readinesstimeoutms-propertysignature"><code>SpawnOptions.readinessTimeoutMs</code></Heading>

Deadline for publication of a complete supported ready document.

```ts
readinessTimeoutMs?: number;
```

<Heading as="h4" id="api-spawnoptions-toolservername-propertysignature"><code>SpawnOptions.toolServerName</code></Heading>

Stable MCP namespace for callback tools. Defaults to `sdk`.

```ts
toolServerName?: string;
```

<Heading as="h3" id="api-tooldefinition-interface"><code>ToolDefinition</code></Heading>

The immutable public description returned for a registered callback tool.

```ts
export interface ToolDefinition
```

<Heading as="h4" id="api-tooldefinition-concurrency-propertysignature"><code>ToolDefinition.concurrency</code></Heading>

Per-tool handler concurrency cap, when one was requested.

```ts
readonly concurrency: number | undefined;
```

<Heading as="h4" id="api-tooldefinition-modelname-propertysignature"><code>ToolDefinition.modelName</code></Heading>

Model-facing name after applying the client-wide MCP server namespace.

```ts
readonly modelName: string;
```

<Heading as="h4" id="api-tooldefinition-name-propertysignature"><code>ToolDefinition.name</code></Heading>

Name advertised by the local MCP server.

```ts
readonly name: string;
```

<Heading as="h4" id="api-tooldefinition-readonly-propertysignature"><code>ToolDefinition.readOnly</code></Heading>

The unverified read-only assertion carried as MCP `readOnlyHint`.

```ts
readonly readOnly: boolean;
```

<Heading as="h4" id="api-tooldefinition-schema-propertysignature"><code>ToolDefinition.schema</code></Heading>

The JSON Schema 2020-12 value advertised for tool arguments.

```ts
readonly schema: ToolSchema;
```

<Heading as="h3" id="api-toolhandlercontext-interface"><code>ToolHandlerContext</code></Heading>

Context supplied to one callback tool invocation.

```ts
export interface ToolHandlerContext
```

<Heading as="h4" id="api-toolhandlercontext-signal-propertysignature"><code>ToolHandlerContext.signal</code></Heading>

Aborted when the host cancels this invocation or shuts down.

```ts
readonly signal: AbortSignal;
```

<Heading as="h3" id="api-tooloptions-interface"><code>ToolOptions</code></Heading>

Registration options for one callback tool.

```ts
export interface ToolOptions
```

<Heading as="h4" id="api-tooloptions-concurrency-propertysignature"><code>ToolOptions.concurrency</code></Heading>

Tightens the client-wide handler concurrency cap for this tool. Values above the client cap never raise it.

```ts
concurrency?: number;
```

<Heading as="h4" id="api-tooloptions-readonly-propertysignature"><code>ToolOptions.readOnly</code></Heading>

Unverified caller assertion that the callback has no side effects. The SDK carries this as MCP's `readOnlyHint`; Mecatl trusts that hint when scheduling concurrent read-only calls. A callback marked read-only may run concurrently even if it has side effects. Plan mode does not automatically classify MCP tool names as mutations.

```ts
readOnly?: boolean;
```

## Type aliases

<Heading as="h3" id="api-calltoolcontent-typealias"><code>CallToolContent</code></Heading>

One JSON-serializable MCP content block returned by a callback tool.

```ts
export type CallToolContent = Readonly<Record<string, ToolJsonValue>> & {
  readonly type: string;
};
```

<Heading as="h3" id="api-nodeconnectoptions-typealias"><code>NodeConnectOptions</code></Heading>

Options accepted by `connect()` in Node.js or Bun.

```ts
export type NodeConnectOptions = (
  NodeTransportOptions | InjectedTransportOptions
) &
  ClientDiagnosticsOptions;
```

<Heading as="h3" id="api-nodetransportoptions-typealias"><code>NodeTransportOptions</code></Heading>

Selects a TCP authority or Unix domain socket for the gRPC transport.

```ts
export type NodeTransportOptions = NodeTransportCommonOptions &
  (
    | {
        baseUrl: string;
        socketPath?: never;
      }
    | {
        baseUrl?: string;
        socketPath: string;
      }
  );
```

<Heading as="h3" id="api-toolhandler-typealias"><code>ToolHandler</code></Heading>

A locally registered callback tool implementation.

```ts
export type ToolHandler = (
  arguments_: Readonly<Record<string, ToolJsonValue>>,
  context: ToolHandlerContext
) => unknown | Promise<unknown>;
```

<Heading as="h3" id="api-tooljsonvalue-typealias"><code>ToolJsonValue</code></Heading>

The JSON values accepted by callback tool schemas and handlers.

```ts
export type ToolJsonValue =
  | boolean
  | number
  | string
  | null
  | readonly ToolJsonValue[]
  | {
      readonly [key: string]: ToolJsonValue;
    };
```

<Heading as="h3" id="api-toolregistrationreason-typealias"><code>ToolRegistrationReason</code></Heading>

Stable authoring-error reasons carried by ToolRegistrationError.

```ts
export type ToolRegistrationReason =
  | 'duplicate_name'
  | 'invalid_options'
  | 'invalid_schema'
  | 'invalid_server_name'
  | 'invalid_tool_name';
```

<Heading as="h3" id="api-toolschema-typealias"><code>ToolSchema</code></Heading>

A plain JSON Schema 2020-12 value; no schema-builder library is required.

```ts
export type ToolSchema = boolean | Readonly<Record<string, ToolJsonValue>>;
```
