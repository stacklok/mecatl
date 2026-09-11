import type { Transport } from "@connectrpc/connect";

import { connectTransport, type InjectedTransportOptions } from "./client.js";
import type { ClientDiagnosticsOptions } from "./errors.js";
import { createNodeTransport, type NodeTransportOptions } from "./node-transport.js";
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
  if ("transport" in options) {
    return withToolRegistration(
      connectTransport({
        ...(options.diagnostics === undefined ? {} : { diagnostics: options.diagnostics }),
        owned: false,
        transport: options.transport,
        transportKind: options.transportKind ?? "grpc",
        visibility: false,
      }),
    );
  }
  const transport: Transport = createNodeTransport(options);
  return withToolRegistration(
    connectTransport({
      ...(options.diagnostics === undefined ? {} : { diagnostics: options.diagnostics }),
      owned: true,
      transport,
      transportKind: "grpc",
      visibility: false,
    }),
  );
}
