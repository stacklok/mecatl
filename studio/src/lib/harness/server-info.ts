import type { ServerInfoLookup } from "./diagnostics-report";
import { HarnessApiError } from "./errors";
import { getHarnessClient, harness, isUnsupportedByDaemon } from "./sdk";

/**
 * The safe server-identity probe (GET /v1/info, ADR 0245) over the SDK's
 * `client.server.info()`: an opaque build id, the composition family (mecated
 * / mecak8s / embedded), and — when a provider id is named — a sanitized
 * provider endpoint projection. Explicitly designed for client About /
 * bug-report surfaces; the only daemon-side identity available in external
 * mode. Returns null against an older daemon (404).
 */
export interface HarnessServerInfo {
  buildId: string;
  serverImplementation: string;
  providerEndpoint: string;
}

async function readServerInfo(
  providerId?: string,
  signal?: AbortSignal,
): Promise<HarnessServerInfo> {
  const info = await harness(() =>
    getHarnessClient().server.info(providerId ? { providerId } : {}, {
      signal,
    }),
  );
  return {
    buildId: info.buildId,
    serverImplementation: info.serverImplementation,
    providerEndpoint: info.llmProviderDisplayEndpoint ?? "",
  };
}

export async function fetchHarnessServerInfo(
  providerId?: string,
  signal?: AbortSignal,
): Promise<HarnessServerInfo | null> {
  try {
    return await readServerInfo(providerId, signal);
  } catch (error) {
    if (isUnsupportedByDaemon(error)) return null; // pre-ADR-0245 daemon
    throw error;
  }
}

/**
 * How the identity probe went — the TUI's `client.SafeInfoFailure`
 * vocabulary (cmd/mecatui/client/serverinfo.go). The closed set lives in
 * diagnostics-report.ts (the pure module that prints it); re-exported here
 * as the probe's result type.
 */
export type { ServerInfoLookup } from "./diagnostics-report";

/** Classifies one probe failure into the closed lookup vocabulary. */
export function serverInfoLookup(
  error: unknown,
): Exclude<ServerInfoLookup, "ok"> {
  if (isUnsupportedByDaemon(error)) return "not-supported";
  if (error instanceof HarnessApiError) {
    // The SDK keeps its own transport failures under status 0 with a
    // stable code; the same-origin proxy answers 502/503/504 for a daemon
    // it could not reach (tests/rendered-html.test.mjs pins the 503).
    if (
      error.status === 0 &&
      (error.code === "transport" || error.code === "readiness_timeout")
    ) {
      return "unreachable";
    }
    if (error.status >= 502 && error.status <= 504) return "unreachable";
    return "invalid-response";
  }
  // A bare `fetch` rejection is a TypeError ("Failed to fetch").
  if (error instanceof TypeError) return "unreachable";
  return "invalid-response";
}

export interface HarnessServerInfoProbe {
  /** The identity when `lookup` is "ok"; null otherwise. */
  info: HarnessServerInfo | null;
  lookup: ServerInfoLookup;
}

/**
 * The non-throwing form of the probe: the identity plus how the lookup
 * went, so an About card can stay rendered (with an honest "unavailable on
 * this daemon" row) and the `/diagnostics` report can name the failure
 * class. An aborted call still resolves — the caller drops a stale answer
 * by checking its own signal.
 */
export async function probeHarnessServerInfo(
  providerId?: string,
  signal?: AbortSignal,
): Promise<HarnessServerInfoProbe> {
  try {
    return { info: await readServerInfo(providerId, signal), lookup: "ok" };
  } catch (error) {
    return { info: null, lookup: serverInfoLookup(error) };
  }
}
