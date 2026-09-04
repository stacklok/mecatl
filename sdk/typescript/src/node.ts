/**
 * Node.js and Bun mecatl SDK entry point.
 *
 * @packageDocumentation
 */

export * from "./index.js";
export type { NodeConnectOptions } from "./node-client.js";
export { connect } from "./node-client.js";
export { audioPartFromPath, imagePartFromPath } from "./node-media.js";
export type { NodeTransportCommonOptions, NodeTransportOptions } from "./node-transport.js";
export { createNodeTransport } from "./node-transport.js";
export type { Query, QueryOptions } from "./query.js";
export { query } from "./query.js";
export type { DaemonInfo, SpawnedClient, SpawnOptions } from "./spawn.js";
export { spawn } from "./spawn.js";
