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

/** @public */
export const SUPPORTED_API_MAJOR = 1;

const transportKinds = new WeakMap<Transport, TransportKind>();
const rawJsonValues = new WeakMap<object, JsonValue>();

interface CompatibilityResult {
  header: Headers;
  message: GetCompatibilityInfoResponse;
  trailer: Headers;
}

export function registerTransport(transport: Transport, kind: TransportKind): Transport {
  transportKinds.set(transport, kind);
  return transport;
}

export function registerRawJson(message: object, value: JsonValue): void {
  rawJsonValues.set(message, value);
}

/** Returns the exact JSON value received by the HTTP transport, including unknown fields. @public */
export function getRawJson(message: object): JsonValue | undefined {
  return rawJsonValues.get(message);
}

/** Transport-neutral, descriptor-driven operations beneath Client/Session/Run. @public */
export interface RawClient {
  unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    options?: CallOptions,
  ): Promise<MessageShape<O>>;
  stream<I extends DescMessage, O extends DescMessage>(
    method: DescMethodStreaming<I, O>,
    input: AsyncIterable<MessageInitShape<I>>,
    options?: CallOptions,
  ): AsyncIterable<MessageShape<O>>;
}

/** @public */
export interface RawClientOptions {
  /** A Connect-ES transport, including createRouterTransport() in tests. */
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
 * Creates the transport-neutral raw operation seam.
 *
 * Its first operation performs the state-free compatibility probe. No session is
 * created to infer support and there is no legacy mode.
 *
 * @public
 */
export function createRawClient(options: RawClientOptions): RawClient {
  const transport = options.transport;
  const transportKind = options.transportKind ?? transportKinds.get(transport) ?? "grpc";
  let compatibility: Promise<CompatibilityResult> | undefined;

  const ensureCompatibility = (callOptions?: CallOptions): Promise<CompatibilityResult> => {
    compatibility ??= transport
      .unary(
        HarnessService.method.getCompatibilityInfo,
        callOptions?.signal,
        callOptions?.timeoutMs,
        callOptions?.headers,
        {},
        callOptions?.contextValues,
      )
      .then((response) => {
        const info = response.message;
        if (info.apiMajor !== SUPPORTED_API_MAJOR) throw incompatible(undefined, transportKind);
        return { header: response.header, message: info, trailer: response.trailer };
      })
      .catch((cause: unknown) => {
        // A transient floor failure must not poison this client permanently.
        // Connection monitoring and a later ordinary operation may retry it.
        compatibility = undefined;
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
    return compatibility;
  };

  return {
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
}
