import type { Transport } from "@connectrpc/connect";

import { type Client, connectTransport, type InjectedTransportOptions } from "./client.js";
import { createNodeTransport, type NodeTransportOptions } from "./node-transport.js";

/** Options accepted by the Node/Bun connect() entry point. @public */
export type NodeConnectOptions = NodeTransportOptions | InjectedTransportOptions;

/** Creates a Node/Bun Client over gRPC TCP/UDS or a caller-injected transport. @public */
export function connect(options: NodeConnectOptions): Client {
  if ("transport" in options) {
    return connectTransport({
      owned: false,
      transport: options.transport,
      transportKind: options.transportKind ?? "grpc",
      visibility: false,
    });
  }
  const transport: Transport = createNodeTransport(options);
  return connectTransport({
    owned: true,
    transport,
    transportKind: "grpc",
    visibility: false,
  });
}
