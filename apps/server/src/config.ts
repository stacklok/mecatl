// SPDX-License-Identifier: Apache-2.0

/**
 * Studio-owned configuration (`STUDIO_*`). Settings that describe the mecatl
 * TARGET (`MECATL_*`) are parsed next door in `mecatl/runtime.ts`; the split
 * is ADR 0351 decision 5.
 */

export class ConfigurationError extends Error {
  readonly variable: string;

  constructor(variable: string, message: string) {
    super(`${variable}: ${message}`);
    this.name = "ConfigurationError";
    this.variable = variable;
  }
}

export type LogLevel = "debug" | "info" | "warn" | "error";

export interface RateLimitConfig {
  readonly max: number;
  readonly windowMs: number;
}

export interface ActivityLimits {
  /** Concurrent activity streams one session admits per replica. */
  readonly maxStreams: number;
  /** Durable REPLAY events one activity request forwards; live events are unbounded. */
  readonly replayMax: number;
}

export interface StudioConfig {
  /** Bounds on durable activity replay (see docs/acceptance/studio-chat.md). */
  readonly activity: ActivityLimits;
  /** Inside the released image, allow static-token or no-auth runtimes. */
  readonly allowUnauthenticated: boolean;
  /** Set by the Dockerfile; refuses spawn/mock modes and gates unauthenticated runtimes. */
  readonly image: boolean;
  /** Listen address: loopback unless the image or `STUDIO_HOST` says otherwise. */
  readonly host: string;
  readonly logLevel: LogLevel;
  readonly port: number;
  /** Browser-facing origin; the OIDC callback and cookie `Secure` flag derive from it. */
  readonly publicUrl: URL | undefined;
  readonly rateLimit: RateLimitConfig;
  /** Raw UTF-8 secret bytes, at least 32 of them, when set. */
  readonly sessionSecret: string | undefined;
  readonly trustedProxyHops: number;
  /** Directory holding the built SPA, or undefined to serve no static assets. */
  readonly webDist: string | undefined;
}

export const minimumSessionSecretBytes = 32;

export function studioConfigFromEnvironment(
  environment: Readonly<NodeJS.ProcessEnv> = process.env,
): StudioConfig {
  const image = flag(environment, "STUDIO_IMAGE");
  const publicUrl = parsePublicUrl(environment.STUDIO_PUBLIC_URL);
  if (image && publicUrl === undefined) {
    throw new ConfigurationError(
      "STUDIO_PUBLIC_URL",
      "required inside the Studio image (the browser-facing origin, e.g. https://studio.example.com)",
    );
  }

  const sessionSecret = environment.STUDIO_SESSION_SECRET;
  if (sessionSecret !== undefined && sessionSecret !== "") {
    const bytes = new TextEncoder().encode(sessionSecret).byteLength;
    if (bytes < minimumSessionSecretBytes) {
      throw new ConfigurationError(
        "STUDIO_SESSION_SECRET",
        `must be at least ${minimumSessionSecretBytes} bytes (got ${bytes})`,
      );
    }
  }

  return {
    activity: {
      maxStreams: parseInteger(environment, "STUDIO_ACTIVITY_MAX_STREAMS", 4, 1, 1_000),
      replayMax: parseInteger(environment, "STUDIO_ACTIVITY_REPLAY_MAX", 2_000, 1, 1_000_000),
    },
    allowUnauthenticated: flag(environment, "STUDIO_ALLOW_UNAUTHENTICATED"),
    host: parseHost(environment.STUDIO_HOST, image),
    image,
    logLevel: parseLogLevel(environment.STUDIO_LOG_LEVEL),
    port: parseInteger(environment, "STUDIO_PORT", 3100, 1, 65535),
    publicUrl,
    rateLimit: {
      max: parseInteger(environment, "STUDIO_RATE_LIMIT_MAX", 20, 1, 1_000_000),
      windowMs: parseInteger(environment, "STUDIO_RATE_LIMIT_WINDOW_MS", 60_000, 1, 86_400_000),
    },
    sessionSecret: sessionSecret === "" ? undefined : sessionSecret,
    trustedProxyHops: parseInteger(environment, "STUDIO_TRUSTED_PROXY_HOPS", 0, 0, 16),
    webDist:
      environment.STUDIO_WEB_DIST === undefined || environment.STUDIO_WEB_DIST === ""
        ? undefined
        : environment.STUDIO_WEB_DIST,
  };
}

function flag(environment: Readonly<NodeJS.ProcessEnv>, variable: string): boolean {
  const value = environment[variable]?.trim();
  if (value === undefined || value === "") return false;
  if (value === "1") return true;
  throw new ConfigurationError(variable, "must be 1 when set");
}

function parseInteger(
  environment: Readonly<NodeJS.ProcessEnv>,
  variable: string,
  fallback: number,
  minimum: number,
  maximum: number,
): number {
  const raw = environment[variable]?.trim();
  if (raw === undefined || raw === "") return fallback;
  const value = Number(raw);
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new ConfigurationError(variable, `must be an integer between ${minimum} and ${maximum}`);
  }
  return value;
}

/**
 * Outside the image the BFF is a local development server and binds loopback
 * only; inside it the container network is the boundary, so it binds every
 * interface. An explicit `STUDIO_HOST` wins either way.
 */
function parseHost(raw: string | undefined, image: boolean): string {
  const value = raw?.trim();
  if (value === undefined || value === "") return image ? "0.0.0.0" : "127.0.0.1";
  if (!/^[A-Za-z0-9.:-]+$/.test(value) && !/^\[[0-9A-Fa-f:.]+\]$/.test(value)) {
    throw new ConfigurationError("STUDIO_HOST", "must be an IP address or host name");
  }
  return value.startsWith("[") ? value.slice(1, -1) : value;
}

function parseLogLevel(raw: string | undefined): LogLevel {
  const value = raw?.trim();
  if (value === undefined || value === "") return "info";
  if (value === "debug" || value === "info" || value === "warn" || value === "error") return value;
  throw new ConfigurationError("STUDIO_LOG_LEVEL", "must be one of debug, info, warn, error");
}

function parsePublicUrl(raw: string | undefined): URL | undefined {
  const value = raw?.trim();
  if (value === undefined || value === "") return undefined;
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new ConfigurationError("STUDIO_PUBLIC_URL", "must be an absolute http or https URL");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new ConfigurationError("STUDIO_PUBLIC_URL", "must use http or https");
  }
  if (url.username !== "" || url.password !== "" || url.search !== "" || url.hash !== "") {
    throw new ConfigurationError(
      "STUDIO_PUBLIC_URL",
      "must not carry credentials, a query string, or a fragment",
    );
  }
  if (url.pathname !== "/") {
    throw new ConfigurationError("STUDIO_PUBLIC_URL", "must be an origin without a path");
  }
  return url;
}
