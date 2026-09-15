import type {
  DescMessage,
  DescMethodStreaming,
  DescMethodUnary,
  JsonValue,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";
import type { CallOptions, Transport } from "@connectrpc/connect";
import { Code } from "@connectrpc/connect";

import {
  IncompatibleServerError,
  normalizeError,
  ServerError,
  type TransportKind,
} from "./errors.js";
import type { GetCompatibilityInfoResponse } from "./gen/mecatl/v1/harness_pb.js";
import { HarnessService } from "./gen/mecatl/v1/harness_pb.js";
import {
  cloneServerCompatibility,
  projectServerCompatibility,
  type ServerCompatibility,
  SUPPORTED_API_MAJOR,
} from "./server.js";

export { SUPPORTED_API_MAJOR } from "./server.js";

/** Canonical routing hint for session-bound Mecatl requests. It grants no authority. @public */
export const SESSION_ID_HEADER_NAME = "X-Mecatl-Session-ID";
const MAX_SESSION_AFFINITY_BYTES = 256;

function validSessionAffinity(value: string): boolean {
  if (
    value.length === 0 ||
    value.length > MAX_SESSION_AFFINITY_BYTES ||
    value.charCodeAt(0) === 0x20 ||
    value.charCodeAt(value.length - 1) === 0x20
  ) {
    return false;
  }
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if (code < 0x20 || code > 0x7e) return false;
  }
  return true;
}

/**
 * Returns call options bound to one explicit session without replacing caller headers.
 * Throws synchronously when sessionId cannot be represented byte-exactly as the affinity header.
 *
 * The binding is a routing hint only; authentication and authorization remain independent.
 *
 * @param sessionId - Session ID to carry as the affinity header.
 * @param options - Existing call options whose headers must be preserved.
 * @returns Call options containing exactly one session-affinity header.
 * @throws `RangeError` when the session ID is not printable ASCII or is otherwise invalid.
 * @public
 */
export function withSessionAffinity(sessionId: string, options: CallOptions = {}): CallOptions {
  if (!validSessionAffinity(sessionId)) {
    throw new RangeError(
      "Invalid session affinity: expected 1-256 bytes of printable ASCII without boundary spaces",
    );
  }
  const headers = new Headers(options.headers);
  headers.set(SESSION_ID_HEADER_NAME, sessionId);
  return { ...options, headers };
}

export function sessionAffinityIfRepresentable(
  sessionId: string,
  options?: CallOptions,
): CallOptions | undefined {
  return validSessionAffinity(sessionId)
    ? withSessionAffinity(sessionId, options)
    : withoutSessionAffinity(options);
}

function withoutSessionAffinity(options?: CallOptions): CallOptions | undefined {
  if (options?.headers === undefined) return options;
  const headers = new Headers(options.headers);
  headers.delete(SESSION_ID_HEADER_NAME);
  return { ...options, headers };
}

const transportKinds = new WeakMap<Transport, TransportKind>();
const transportOperations = new WeakMap<Transport, TransportOperations>();
const rawJsonValues = new WeakMap<object, JsonValue>();
const compatibilityInvalidators = new WeakMap<RawClient, () => void>();
const compatibilityReaders = new WeakMap<RawClient, CompatibilityReader>();

interface TransportOperations {
  cancelRun(sessionId: string, runId: string, signal: AbortSignal): Promise<void>;
}

interface CompatibilityResult {
  header: Headers;
  message: GetCompatibilityInfoResponse;
  projection: ServerCompatibility;
  trailer: Headers;
}

interface CompatibilityReader {
  ordinary(options?: CallOptions): Promise<CompatibilityResult>;
  refresh(options?: CallOptions): Promise<CompatibilityResult>;
}

export function registerTransport(
  transport: Transport,
  kind: TransportKind,
  operations?: TransportOperations,
): Transport {
  transportKinds.set(transport, kind);
  if (operations !== undefined) transportOperations.set(transport, operations);
  return transport;
}

export function registeredTransportOperations(
  transport: Transport,
): TransportOperations | undefined {
  return transportOperations.get(transport);
}

export function registerRawJson(message: object, value: JsonValue): void {
  rawJsonValues.set(message, value);
}

/**
 * Returns the exact JSON value received by the HTTP transport, including unknown fields.
 *
 * @param message - Decoded protobuf message returned by the SDK.
 * @returns The original JSON value, or `undefined` when none was recorded.
 * @public
 */
export function getRawJson(message: object): JsonValue | undefined {
  return rawJsonValues.get(message);
}

/** Clears one raw client's cached compatibility descriptor before a reconnect. */
export function invalidateRawCompatibility(client: RawClient): void {
  compatibilityInvalidators.get(client)?.();
}

async function projectedCompatibility(
  client: RawClient,
  options: CallOptions | undefined,
  refresh: boolean,
): Promise<ServerCompatibility> {
  const reader = compatibilityReaders.get(client);
  if (reader === undefined) throw new TypeError("Unknown raw client");
  const result = await (refresh ? reader.refresh(options) : reader.ordinary(options));
  options?.onHeader?.(result.header);
  options?.onTrailer?.(result.trailer);
  return cloneServerCompatibility(result.projection);
}

/** Reads the current shared compatibility generation for a high-level operation. */
export function readRawCompatibility(
  client: RawClient,
  options?: CallOptions,
): Promise<ServerCompatibility> {
  return projectedCompatibility(client, options, false);
}

/** Starts and installs a fresh compatibility generation for explicit discovery. */
export function refreshRawCompatibility(
  client: RawClient,
  options?: CallOptions,
): Promise<ServerCompatibility> {
  return projectedCompatibility(client, options, true);
}

/** Transport-neutral, descriptor-driven operations beneath Client/Session/Run. @public */
export interface RawClient {
  /** Returns the build features learned from the shared compatibility probe. */
  features(options?: CallOptions): Promise<ReadonlySet<string>>;
  /** Invokes one unary RPC after enforcing the SDK compatibility floor. */
  unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    options?: CallOptions,
  ): Promise<MessageShape<O>>;
  /** Invokes one streaming RPC after enforcing the SDK compatibility floor. */
  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    options?: CallOptions,
  ): AsyncIterable<MessageShape<O>>;
}

/** Options for constructing the transport-neutral raw client. @public */
export interface RawClientOptions {
  /** A caller-owned Connect-ES transport. */
  transport: Transport;
  /** Required only for an unregistered injected transport. Defaults to gRPC. */
  transportKind?: TransportKind;
}

function incompatible(cause: unknown, transport: TransportKind): IncompatibleServerError {
  return new IncompatibleServerError(
    `The server must implement GetCompatibilityInfo with API major ${SUPPORTED_API_MAJOR}`,
    { cause, transport },
  );
}

/**
 * Creates a transport-neutral client for low-level RPC operations.
 *
 * Before the first requested operation, the client performs a stateless
 * compatibility check.
 *
 * @param options - Caller-owned transport and its protocol kind.
 * @returns A low-level client that enforces SDK compatibility before operations.
 * @public
 */
export function createRawClient(options: RawClientOptions): RawClient {
  const transport = options.transport;
  const transportKind = options.transportKind ?? transportKinds.get(transport) ?? "grpc";
  let nextGeneration = 0;
  let compatibility:
    | { readonly generation: number; readonly promise: Promise<CompatibilityResult> }
    | undefined;

  const startCompatibility = (callOptions?: CallOptions): Promise<CompatibilityResult> => {
    const probeOptions = withoutSessionAffinity(callOptions);
    const generation = ++nextGeneration;
    const response = (() => {
      try {
        return transport.unary(
          HarnessService.method.getCompatibilityInfo,
          probeOptions?.signal,
          probeOptions?.timeoutMs,
          probeOptions?.headers,
          {},
          probeOptions?.contextValues,
        );
      } catch (cause) {
        return Promise.reject(cause);
      }
    })();
    const promise = response
      .then((response) => {
        const info = response.message;
        if (info.apiMajor !== SUPPORTED_API_MAJOR) throw incompatible(undefined, transportKind);
        return {
          header: response.header,
          message: info,
          projection: projectServerCompatibility(info, transportKind),
          trailer: response.trailer,
        };
      })
      .catch((cause: unknown) => {
        // A transient floor failure must not poison this client permanently.
        // Connection monitoring and a later ordinary operation may retry it.
        if (compatibility?.generation === generation) compatibility = undefined;
        if (cause instanceof IncompatibleServerError) throw cause;
        const normalized = normalizeError(cause, transportKind);
        if (
          (normalized instanceof ServerError &&
            (normalized.status === Code.Unimplemented ||
              (transportKind === "http" && normalized.status === 404))) ||
          (normalized instanceof ServerError && normalized.code === "unimplemented")
        ) {
          throw incompatible(normalized, transportKind);
        }
        throw normalized;
      });
    compatibility = { generation, promise };
    return promise;
  };

  const ensureCompatibility = (callOptions?: CallOptions): Promise<CompatibilityResult> => {
    return compatibility?.promise ?? startCompatibility(callOptions);
  };

  const client: RawClient = {
    async features(callOptions?: CallOptions): Promise<ReadonlySet<string>> {
      const result = await ensureCompatibility(callOptions);
      return new Set(result.projection.features);
    },
    async unary<I extends DescMessage, O extends DescMessage>(
      method: DescMethodUnary<I, O>,
      input: MessageInitShape<I>,
      callOptions?: CallOptions,
    ): Promise<MessageShape<O>> {
      if (method.name === "GetCompatibilityInfo") {
        const result = await ensureCompatibility(callOptions);
        callOptions?.onHeader?.(result.header);
        callOptions?.onTrailer?.(result.trailer);
        return result.message as unknown as MessageShape<O>;
      }
      await ensureCompatibility(callOptions);
      try {
        const response = await transport.unary(
          method,
          callOptions?.signal,
          callOptions?.timeoutMs,
          callOptions?.headers,
          input,
          callOptions?.contextValues,
        );
        callOptions?.onHeader?.(response.header);
        callOptions?.onTrailer?.(response.trailer);
        return response.message;
      } catch (cause) {
        throw normalizeError(cause, transportKind);
      }
    },
    stream<I extends DescMessage, O extends DescMessage>(
      method: DescMethodStreaming<I, O>,
      input: AsyncIterable<MessageInitShape<I>>,
      callOptions?: CallOptions,
    ): AsyncIterable<MessageShape<O>> {
      return (async function* () {
        await ensureCompatibility(callOptions);
        try {
          const response = await transport.stream(
            method,
            callOptions?.signal,
            callOptions?.timeoutMs,
            callOptions?.headers,
            input,
            callOptions?.contextValues,
          );
          callOptions?.onHeader?.(response.header);
          yield* response.message;
          callOptions?.onTrailer?.(response.trailer);
        } catch (cause) {
          throw normalizeError(cause, transportKind);
        }
      })();
    },
  };
  compatibilityInvalidators.set(client, () => {
    compatibility = undefined;
  });
  compatibilityReaders.set(client, {
    ordinary: ensureCompatibility,
    refresh: startCompatibility,
  });
  return client;
}
