/**
 * Node.js and Bun Mecatl SDK entry point.
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
export type {
  CallToolContent,
  CallToolResult,
  NodeClient,
  ToolDefinition,
  ToolHandler,
  ToolHandlerContext,
  ToolJsonValue,
  ToolOptions,
  ToolRegistrationReason,
  ToolSchema,
} from "./tool.js";
export { ToolRegistrationError } from "./tool.js";
