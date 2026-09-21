// SPDX-License-Identifier: Apache-2.0

import { AsyncLocalStorage } from "node:async_hooks";
import type { RuntimeResponse } from "@mecatl-studio/contracts";
import type {
  ConnectionStatus,
  ConnectionStatusStore,
  DiagnosticRecord,
  ServerCapabilities,
  ServerCompatibility,
} from "@stacklok-oss/mecatl-sdk";
import { connect, spawn } from "@stacklok-oss/mecatl-sdk/node";
import { ConfigurationError } from "../config.js";
import { type Logger, silentLogger } from "../log.js";

export type AuthMode = "oidc" | "static" | "none";

/** The subset of the SDK client the runtime relies on; tests inject a fake. */
export interface RuntimeClient {
  close(): Promise<void>;
  readonly server: { compatibility(): Promise<ServerCompatibility> };
  readonly status: ConnectionStatusStore;
}

export interface MecatlRuntime {
  /** How callers are identified to mecatl; the bootstrap flips it to `oidc` once login is configured. */
  authMode: AuthMode;
  readonly client: RuntimeClient;
  close(): Promise<void>;
  ready(): Promise<void>;
  runWithCredential<T>(accessToken: string, operation: () => Promise<T>): Promise<T>;
  snapshot(): RuntimeResponse;
  verifyCredential(accessToken: string): Promise<void>;
}

interface LocalRuntimeConfig {
  readonly binaryPath?: string;
  readonly mock: boolean;
  readonly source: "local";
}

interface ExternalRuntimeConfig {
  readonly authToken?: string;
  /** http(s) authority of the mecatl gRPC listener. */
  readonly baseUrl: string;
  /** Canonical RFC 9728 resource whose well-known document Studio discovers. */
  readonly resourceUrl: string;
  readonly source: "external";
}

export type RuntimeConfig = LocalRuntimeConfig | ExternalRuntimeConfig;

export class RuntimeNotReadyError extends Error {
  constructor() {
    super("Mecatl runtime has not completed compatibility negotiation");
    this.name = "RuntimeNotReadyError";
  }
}

export function runtimeConfigFromEnvironment(
  environment: Readonly<NodeJS.ProcessEnv> = process.env,
): RuntimeConfig {
  const baseUrl = environment.MECATL_BASE_URL?.trim();
  const resourceUrl = environment.MECATL_RESOURCE_URL?.trim();
  const authToken = environment.MECATL_AUTH_TOKEN?.trim();
  const mockValue = environment.MECATL_DEV_MOCK?.trim();
  const image = environment.STUDIO_IMAGE?.trim() === "1";

  if (mockValue !== undefined && mockValue !== "" && mockValue !== "1") {
    throw new ConfigurationError("MECATL_DEV_MOCK", "must be 1 when set");
  }

  if (baseUrl !== undefined && baseUrl !== "") {
    const url = parseHttpUrl("MECATL_BASE_URL", baseUrl, "authority");
    if (mockValue === "1") {
      throw new ConfigurationError("MECATL_DEV_MOCK", "cannot be combined with MECATL_BASE_URL");
    }
    const resource =
      resourceUrl === undefined || resourceUrl === ""
        ? canonicalResource(url)
        : canonicalResource(parseHttpUrl("MECATL_RESOURCE_URL", resourceUrl, "resource"));
    return {
      ...(authToken === undefined || authToken === "" ? {} : { authToken }),
      baseUrl: canonicalResource(url),
      resourceUrl: resource,
      source: "external",
    };
  }

  if (authToken !== undefined && authToken !== "") {
    throw new ConfigurationError("MECATL_AUTH_TOKEN", "requires MECATL_BASE_URL");
  }
  if (resourceUrl !== undefined && resourceUrl !== "") {
    throw new ConfigurationError("MECATL_RESOURCE_URL", "requires MECATL_BASE_URL");
  }
  if (image) {
    throw new ConfigurationError(
      "MECATL_BASE_URL",
      "required inside the Studio image: the image ships no mecated to spawn (STUDIO_IMAGE=1)",
    );
  }

  const binaryPath = environment.MECATED_BIN?.trim();
  return {
    ...(binaryPath === undefined || binaryPath === "" ? {} : { binaryPath }),
    mock: mockValue === "1",
    source: "local",
  };
}

export interface RuntimeDependencies {
  /** Builds the SDK client; the default spawns or connects for real. */
  readonly createClient?: (
    config: RuntimeConfig,
    credentialProvider: () => HeadersInit,
  ) => Promise<RuntimeClient>;
  readonly logger?: Logger;
  /** Re-negotiation backoff steps in ms, bounded; tests shorten them. */
  readonly reconnectBackoffMs?: readonly number[];
  readonly setTimeoutFn?: typeof setTimeout;
}

const defaultBackoffMs = [1_000, 2_000, 5_000, 10_000, 30_000] as const;

export async function startMecatlRuntime(
  config: RuntimeConfig = runtimeConfigFromEnvironment(),
  dependencies: RuntimeDependencies = {},
): Promise<MecatlRuntime> {
  const logger = dependencies.logger ?? silentLogger;
  const credentialContext = new AsyncLocalStorage<string>();
  const credentialProvider = (): HeadersInit => {
    const accessToken = credentialContext.getStore();
    return accessToken === undefined ? {} : { Authorization: `Bearer ${accessToken}` };
  };
  const createClient = dependencies.createClient ?? realClient(logger);
  const client = await createClient(config, credentialProvider);
  const backoff = dependencies.reconnectBackoffMs ?? defaultBackoffMs;
  const schedule = dependencies.setTimeoutFn ?? setTimeout;

  let compatibility: ServerCompatibility | undefined;
  let compatibilityPromise: Promise<void> | undefined;
  let closed = false;
  let renegotiating = false;

  const negotiate = () => {
    if (compatibilityPromise !== undefined) return compatibilityPromise;
    compatibilityPromise = client.server
      .compatibility()
      .then((result) => {
        compatibility = result;
      })
      .finally(() => {
        compatibilityPromise = undefined;
      });
    return compatibilityPromise;
  };

  const ready = () => (compatibility !== undefined ? Promise.resolve() : negotiate());

  // AC2.6: once negotiated, a lost upstream re-negotiates with bounded backoff
  // while the snapshot keeps serving the last known capabilities and the live
  // connection status.
  const renegotiate = (attempt: number) => {
    if (closed || compatibility === undefined || renegotiating) return;
    renegotiating = true;
    negotiate()
      .then(() => {
        renegotiating = false;
        logger.info("runtime.renegotiated", { attempts: attempt + 1 });
      })
      .catch(() => {
        renegotiating = false;
        const delay = backoff[Math.min(attempt, backoff.length - 1)] ?? 30_000;
        schedule(() => renegotiate(attempt + 1), delay);
      });
  };
  const unsubscribe = client.status.subscribe((status: ConnectionStatus) => {
    logger.debug("runtime.connection", { status });
    if (status === "reconnecting" || status === "offline") renegotiate(0);
  });

  return {
    authMode: config.source === "external" && config.authToken !== undefined ? "static" : "none",
    client,
    close: async () => {
      closed = true;
      unsubscribe();
      await client.close();
    },
    ready,
    runWithCredential: (accessToken, operation) => credentialContext.run(accessToken, operation),
    snapshot: () => {
      if (compatibility === undefined) throw new RuntimeNotReadyError();
      return runtimeSnapshot(client, config, compatibility);
    },
    verifyCredential: (accessToken) =>
      credentialContext.run(accessToken, async () => {
        const result = await client.server.compatibility();
        compatibility ??= result;
      }),
  };
}

function realClient(logger: Logger) {
  const diagnostics = (record: DiagnosticRecord) => {
    const fields = { code: record.code, message: record.message };
    if (record.level === "error") logger.error("sdk.diagnostic", fields);
    else if (record.level === "warn") logger.warn("sdk.diagnostic", fields);
    else logger.debug("sdk.diagnostic", fields);
  };
  return async (
    config: RuntimeConfig,
    credentialProvider: () => HeadersInit,
  ): Promise<RuntimeClient> => {
    if (config.source === "local") {
      return spawn({
        args: config.mock ? ["--mock"] : [],
        ...(config.binaryPath === undefined ? {} : { binaryPath: config.binaryPath }),
        diagnostics,
      });
    }
    return connect({
      baseUrl: config.baseUrl,
      diagnostics,
      ...(config.authToken === undefined
        ? { credentialProvider }
        : { headers: { Authorization: `Bearer ${config.authToken}` } }),
    });
  };
}

function runtimeSnapshot(
  client: RuntimeClient,
  config: RuntimeConfig,
  compatibility: ServerCompatibility,
): RuntimeResponse {
  return {
    apiMajor: compatibility.apiMajor,
    capabilities: copyCapabilities(compatibility.capabilities),
    connection: client.status.getSnapshot(),
    ...(compatibility.deployment === undefined ? {} : { deployment: compatibility.deployment }),
    features: [...compatibility.features].sort(),
    mock: config.source === "local" && config.mock,
    source: config.source,
  };
}

function copyCapabilities(capabilities: ServerCapabilities): ServerCapabilities {
  return structuredClone(capabilities);
}

function canonicalResource(url: URL): string {
  const resource = new URL(url);
  resource.search = "";
  resource.hash = "";
  return resource.toString().replace(/\/$/u, "");
}

/**
 * `authority` rejects anything beyond scheme, host, and port: the contract
 * defines MECATL_BASE_URL as the gRPC listener's authority, so credentials, a
 * query, a fragment, or a path are misconfiguration and must fail before the
 * server listens rather than inside the SDK later. A resource URL may carry a
 * path (a protected resource can be namespaced) but nothing else.
 */
function parseHttpUrl(variable: string, raw: string, shape: "authority" | "resource"): URL {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    throw new ConfigurationError(variable, "must be an absolute http or https URL");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new ConfigurationError(variable, "must use http or https");
  }
  if (url.username !== "" || url.password !== "") {
    throw new ConfigurationError(variable, "must not embed credentials");
  }
  if (url.search !== "" || url.hash !== "") {
    throw new ConfigurationError(variable, "must not carry a query string or fragment");
  }
  if (shape === "authority" && url.pathname !== "/" && url.pathname !== "") {
    throw new ConfigurationError(variable, "must be a scheme, host, and port with no path");
  }
  return url;
}
