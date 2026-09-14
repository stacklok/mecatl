import type { InjectedTransportOptions } from "./client.js";
import type { ClientDiagnosticsOptions } from "./errors.js";
import { connectGrpc } from "./grpc-client.js";
import type { NodeTransportOptions } from "./node-transport.js";
import { type NodeClient, withToolRegistration } from "./tool.js";

/** Options accepted by `connect()` in Node.js or Bun. @public */
export type NodeConnectOptions = (NodeTransportOptions | InjectedTransportOptions) &
  ClientDiagnosticsOptions;

/**
 * Creates a client for Node.js or Bun over gRPC or a caller-provided transport.
 *
 * @param options - gRPC endpoint, credentials, diagnostics, or a caller-owned transport.
 * @returns A high-level client with callback-tool registration.
 * @public
 */
export function connect(options: NodeConnectOptions): NodeClient {
  return withToolRegistration(connectGrpc(options));
}
