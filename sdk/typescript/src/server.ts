import type {
  DescMessage,
  DescMethodUnary,
  MessageInitShape,
  MessageShape,
} from "@bufbuild/protobuf";

import { ProtocolError, type TransportKind, UnsupportedFeatureError } from "./errors.js";
import {
  type GetCompatibilityInfoResponse,
  type GetServerInfoResponse,
  HarnessService,
} from "./gen/mecatl/v1/harness_pb.js";
import type { RequestOptions } from "./namespaces-core.js";
import { projectServerCapabilities, type ServerCapabilities } from "./session-projections.js";

/** The API major implemented by this SDK. @public */
export const SUPPORTED_API_MAJOR = 1;

// BEGIN MECATL_SERVER_FEATURES
/** Known server feature identifiers. Unknown identifiers remain observable. @public */
export const ServerFeature = {
  HttpSteer: "http_steer",
  McpServersOnCreate: "mcp_servers_on_create",
  ServerInfo: "server_info",
  SessionActivityInventory: "session_activity_inventory",
  WatchSessionEvents: "watch_session_events",
} as const;
// END MECATL_SERVER_FEATURES

/** One known server feature identifier. @public */
export type ServerFeature = (typeof ServerFeature)[keyof typeof ServerFeature];

// BEGIN MECATL_SERVER_POSTURES
/** Known server posture values. Unknown capability values remain observable. @public */
export const ServerPosture = {
  Strict: "strict",
  Trusted: "trusted",
  Auto: "auto",
  Yolo: "yolo",
} as const;
// END MECATL_SERVER_POSTURES

/** One known server posture value. @public */
export type ServerPosture = (typeof ServerPosture)[keyof typeof ServerPosture];

/**
 * Known watch-session-events feature identifier.
 *
 * @deprecated Use `ServerFeature.WatchSessionEvents`.
 * @public
 */
export const WATCH_SESSION_EVENTS_FEATURE = ServerFeature.WatchSessionEvents;

/** A detached view of one server compatibility negotiation. @public */
export interface ServerCompatibility {
  /** The wire-contract major supported by this SDK. */
  readonly apiMajor: typeof SUPPORTED_API_MAJOR;
  /** Deployment capabilities currently enabled by the operator. */
  readonly capabilities: ServerCapabilities;
  /** Open build-feature identifiers advertised on this listener. */
  readonly features: ReadonlySet<string>;
  /** Optional operator-authored deployment label. */
  readonly deployment?: string;
}

/** Safe, display-only identity information for the connected server. @public */
export interface ServerInfo {
  /** Linker-stamped server build identity. */
  readonly buildId: string;
  /** Stable server composition family, or `unknown`. */
  readonly serverImplementation: string;
  /** Sanitized provider endpoint for diagnostics, never connection configuration. */
  readonly llmProviderDisplayEndpoint?: string;
}

/** Selector accepted by {@link Server.info}. @public */
export interface ServerInfoOptions {
  /** Already-known provider ID to select for the diagnostic endpoint projection. */
  readonly providerId?: string;
}

/** Pre-session compatibility and safe server-identity operations. @public */
export interface Server {
  /**
   * Starts a fresh compatibility negotiation and makes it the generation shared
   * by subsequent ordinary operations.
   *
   * @param options - Request headers, cancellation signal, and deadline.
   * @returns A detached compatibility projection.
   */
  compatibility(options?: RequestOptions): Promise<ServerCompatibility>;
  /**
   * Reads safe server identity after an ordinary cached compatibility preflight.
   *
   * @param options - Optional exact provider selector.
   * @param requestOptions - Request headers, cancellation signal, and deadline.
   * @returns Detached display-only server identity.
   */
  info(options?: ServerInfoOptions, requestOptions?: RequestOptions): Promise<ServerInfo>;
}

interface ServerOperations {
  compatibility(
    options: RequestOptions | undefined,
    refresh: boolean,
  ): Promise<ServerCompatibility>;
  readonly transportKind: TransportKind;
  unary<I extends DescMessage, O extends DescMessage>(
    method: DescMethodUnary<I, O>,
    input: MessageInitShape<I>,
    options?: RequestOptions,
  ): Promise<MessageShape<O>>;
}

function validUnicode(value: string): boolean {
  for (let index = 0; index < value.length; index += 1) {
    const unit = value.charCodeAt(index);
    if (unit < 0xd800 || unit > 0xdfff) continue;
    if (unit > 0xdbff) return false;
    const next = value.charCodeAt(index + 1);
    if (next < 0xdc00 || next > 0xdfff) return false;
    index += 1;
  }
  return true;
}

function utf8Bytes(value: string): number {
  return new TextEncoder().encode(value).byteLength;
}

function printableSingleLine(value: string): boolean {
  return /^(?:[\p{L}\p{M}\p{N}\p{P}\p{S}]| )+$/u.test(value);
}

function protocol(message: string, transport: TransportKind): ProtocolError {
  return new ProtocolError(message, { transport });
}

function deployment(value: string, transport: TransportKind): string | undefined {
  if (value === "") return undefined;
  if (
    !validUnicode(value) ||
    utf8Bytes(value) > 128 ||
    value.trim() === "" ||
    !printableSingleLine(value)
  ) {
    throw protocol("GetCompatibilityInfo returned an invalid deployment label", transport);
  }
  return value;
}

/** Validates and detaches a successful compatibility response. */
export function projectServerCompatibility(
  value: GetCompatibilityInfoResponse,
  transport: TransportKind,
): ServerCompatibility {
  if (value.capabilities === undefined) {
    throw protocol("GetCompatibilityInfo returned no capabilities", transport);
  }
  if (value.features.length === 0) {
    throw protocol("GetCompatibilityInfo returned no feature identifiers", transport);
  }
  const features = new Set<string>();
  for (const feature of value.features) {
    if (feature === "") {
      throw protocol("GetCompatibilityInfo returned an empty feature identifier", transport);
    }
    if (features.has(feature)) {
      throw protocol("GetCompatibilityInfo returned a duplicate feature identifier", transport);
    }
    features.add(feature);
  }
  const deploymentValue = deployment(value.deployment, transport);
  return {
    apiMajor: SUPPORTED_API_MAJOR,
    capabilities: projectServerCapabilities(value.capabilities),
    features,
    ...(deploymentValue === undefined ? {} : { deployment: deploymentValue }),
  };
}

/** Returns a caller-owned copy without imposing runtime freeze semantics. */
export function cloneServerCompatibility(value: ServerCompatibility): ServerCompatibility {
  return structuredClone(value);
}

function cleanEscapedPath(value: string): string {
  const output: string[] = [];
  for (const segment of value.split("/")) {
    if (segment === "" || segment === ".") continue;
    if (segment === "..") output.pop();
    else output.push(segment);
  }
  return `/${output.join("/")}`;
}

function canonicalDiagnosticEndpoint(value: string): boolean {
  if (
    value === "" ||
    !validUnicode(value) ||
    utf8Bytes(value) > 2_048 ||
    /[\p{Cc}\p{Cf}]/u.test(value)
  ) {
    return false;
  }
  const match = /^([a-z][a-z0-9+.-]*):\/\/([^\\/?#@]+)(\/[^?#]*)?$/u.exec(value);
  if (match === null) return false;
  const [, scheme, authority, path = ""] = match;
  if (
    scheme === undefined ||
    authority === undefined ||
    authority === "" ||
    authority.includes("%")
  ) {
    return false;
  }
  if (path === "" || !/^\/(?:[A-Za-z0-9._~!$&'()*+,;=:@%-]|\/)*$/u.test(path)) return false;
  if (/%(?![0-9A-Fa-f]{2})/u.test(path)) return false;
  if (cleanEscapedPath(path) !== path) return false;
  try {
    const parsed = new URL(value);
    return (
      parsed.protocol === `${scheme}:` &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.search === "" &&
      parsed.hash === ""
    );
  } catch {
    return false;
  }
}

function compatibilityPreflightOptions(
  options: RequestOptions | undefined,
): RequestOptions | undefined {
  if (options === undefined) return undefined;
  const copy = { ...options };
  Reflect.deleteProperty(copy, "onHeader");
  Reflect.deleteProperty(copy, "onTrailer");
  return copy;
}

function serverImplementation(value: string, transport: TransportKind): string {
  const trimmed = value.trim();
  if (trimmed === "") return "unknown";
  if (!/^[a-z][a-z0-9-]{0,63}$/u.test(trimmed)) {
    throw protocol("GetServerInfo returned an invalid server implementation", transport);
  }
  return trimmed;
}

/** Validates and detaches a successful server-info response. */
export function projectServerInfo(
  value: GetServerInfoResponse,
  transport: TransportKind,
): ServerInfo {
  const buildId = value.buildId.trim();
  if (buildId === "") {
    throw protocol("GetServerInfo returned an invalid build identity", transport);
  }
  const implementation = serverImplementation(value.serverImplementation, transport);
  const endpoint = value.llmProviderDisplayEndpoint;
  if (endpoint !== "" && !canonicalDiagnosticEndpoint(endpoint)) {
    throw protocol("GetServerInfo returned an invalid provider display endpoint", transport);
  }
  return {
    buildId,
    serverImplementation: implementation,
    ...(endpoint === "" ? {} : { llmProviderDisplayEndpoint: endpoint }),
  };
}

/** Internal constructor used by the transport-neutral client. */
export function createServer(operations: ServerOperations): Server {
  return {
    compatibility: (options) => operations.compatibility(options, true),
    info: async (options = {}, requestOptions) => {
      const compatibility = await operations.compatibility(
        compatibilityPreflightOptions(requestOptions),
        false,
      );
      if (!compatibility.features.has(ServerFeature.ServerInfo)) {
        throw new UnsupportedFeatureError(ServerFeature.ServerInfo, {
          transport: operations.transportKind,
        });
      }
      const input =
        Object.hasOwn(options, "providerId") && options.providerId !== undefined
          ? { providerId: options.providerId }
          : {};
      const response = await operations.unary(
        HarnessService.method.getServerInfo,
        input,
        requestOptions,
      );
      return projectServerInfo(response, operations.transportKind);
    },
  };
}
