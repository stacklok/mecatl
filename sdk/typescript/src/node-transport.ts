import type { ClientSessionOptions } from "node:http2";
import { connect as connectSocket } from "node:net";
import type { Transport } from "@connectrpc/connect";
import type { GrpcTransportOptions } from "@connectrpc/connect-node";
import { createGrpcTransport } from "@connectrpc/connect-node";
import type { CredentialOptions } from "./credentials.js";
import { credentialInterceptor } from "./credentials.js";
import { registerTransport } from "./raw.js";

/** @public */
export interface NodeTransportCommonOptions extends CredentialOptions {
  /** Additional HTTP/2 session options. createConnection is owned by socketPath. */
  nodeOptions?: Omit<ClientSessionOptions, "createConnection">;
}

/** @public */
export type NodeTransportOptions = NodeTransportCommonOptions &
  (
    | {
        /** HTTP(S) authority of a TCP gRPC listener. */
        baseUrl: string;
        socketPath?: never;
      }
    | {
        /** Optional HTTP/2 :authority; no unix:// URL is put on the wire. */
        baseUrl?: string;
        /** Filesystem path of a gRPC Unix domain socket. */
        socketPath: string;
      }
  );

/** Creates the Node/Bun real-gRPC-over-HTTP/2 transport for TCP or UDS. @public */
export function createNodeTransport(options: NodeTransportOptions): Transport {
  const credentials: CredentialOptions = {
    ...(options.headers === undefined ? {} : { headers: options.headers }),
    ...(options.credentialProvider === undefined
      ? {}
      : { credentialProvider: options.credentialProvider }),
  };
  const grpcOptions: GrpcTransportOptions = {
    baseUrl: options.baseUrl ?? "http://localhost",
    interceptors: [credentialInterceptor(credentials)],
    ...(options.nodeOptions === undefined && options.socketPath === undefined
      ? {}
      : {
          nodeOptions: {
            ...options.nodeOptions,
            ...(options.socketPath === undefined
              ? {}
              : { createConnection: () => connectSocket(options.socketPath) }),
          },
        }),
  };
  return registerTransport(createGrpcTransport(grpcOptions), "grpc");
}
