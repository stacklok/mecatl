/**
 * Deno mecatl SDK entry point.
 *
 * @packageDocumentation
 */

import type { Client, InjectedTransportOptions } from "./client.js";
import type { ClientDiagnosticsOptions } from "./errors.js";
import { connectGrpc } from "./grpc-client.js";
import type { NodeTransportOptions } from "./node-transport.js";

export type { Query, QueryOptions } from "./deno-query.js";
export { query } from "./deno-query.js";
export type { DaemonInfo, SpawnedClient, SpawnOptions } from "./deno-spawn.js";
export { spawn } from "./deno-spawn.js";
export * from "./index.js";
export type { NodeTransportCommonOptions, NodeTransportOptions } from "./node-transport.js";

/** Options accepted by gRPC `connect()` in Deno. @public */
export type DenoConnectOptions = (NodeTransportOptions | InjectedTransportOptions) &
  ClientDiagnosticsOptions;

/**
 * Creates a Deno client using the shared ConnectRPC gRPC transport.
 *
 * @param options - TCP authority, Unix socket, credentials, HTTP/2 settings, or a caller-owned transport.
 * @returns A high-level client without callback-tool registration.
 * @public
 */
export function connect(options: DenoConnectOptions): Client {
  return connectGrpc(options);
}
