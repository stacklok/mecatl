/// <reference types="node" preserve="true" />

import type { SecureClientSessionOptions } from "node:http2";
import { connect as connectSocket } from "node:net";
import type { Transport } from "@connectrpc/connect";
import type { GrpcTransportOptions } from "@connectrpc/connect-node";
import { createGrpcTransport, Http2SessionManager } from "@connectrpc/connect-node";
import type { CredentialOptions } from "./credentials.js";
import { credentialInterceptor } from "./credentials.js";
import { registerTransport } from "./raw.js";

/** Shared credentials and HTTP/2 settings for gRPC in Node.js, Bun, or Deno. @public */
export interface NodeTransportCommonOptions extends CredentialOptions {
  /** HTTP/2 and TLS session options. The SDK controls `createConnection` when using `socketPath`. */
  nodeOptions?: Omit<SecureClientSessionOptions, "createConnection">;
}

/** Selects a TCP authority or Unix domain socket for the gRPC transport. @public */
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

/**
 * Creates a gRPC transport for Node.js, Bun, or Deno over HTTP/2 or a Unix domain socket.
 *
 * @param options - TCP authority or Unix socket plus credentials and HTTP/2 settings.
 * @returns A Connect-ES gRPC transport.
 * @public
 */
export function createNodeTransport(options: NodeTransportOptions): Transport {
  const credentials: CredentialOptions = {
    ...(options.headers === undefined ? {} : { headers: options.headers }),
    ...(options.credentialProvider === undefined
      ? {}
      : { credentialProvider: options.credentialProvider }),
  };
  const baseUrl = options.baseUrl ?? "http://localhost";
  const sessionManager = new Http2SessionManager(baseUrl, undefined, {
    ...options.nodeOptions,
    ...(options.socketPath === undefined
      ? {}
      : { createConnection: () => connectSocket(options.socketPath) }),
  });
  const grpcOptions: GrpcTransportOptions = {
    baseUrl,
    interceptors: [credentialInterceptor(credentials)],
    sessionManager,
  };
  const transport = createGrpcTransport(grpcOptions) as Transport & AsyncDisposable;
  transport[Symbol.asyncDispose] = async () => sessionManager.abort();
  return registerTransport(transport, "grpc");
}
