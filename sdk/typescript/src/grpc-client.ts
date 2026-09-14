import { type Client, connectTransport, type InjectedTransportOptions } from "./client.js";
import type { ClientDiagnosticsOptions } from "./errors.js";
import { createNodeTransport, type NodeTransportOptions } from "./node-transport.js";

/** Shared gRPC client construction; process ownership and callback tools stay in runtime adapters. */
export function connectGrpc(
  options: (NodeTransportOptions | InjectedTransportOptions) & ClientDiagnosticsOptions,
): Client {
  const injected = "transport" in options;
  return connectTransport({
    ...(options.diagnostics === undefined ? {} : { diagnostics: options.diagnostics }),
    owned: !injected,
    transport: injected ? options.transport : createNodeTransport(options),
    transportKind: injected ? (options.transportKind ?? "grpc") : "grpc",
    visibility: false,
  });
}
