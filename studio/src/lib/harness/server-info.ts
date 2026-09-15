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

export async function fetchHarnessServerInfo(
  providerId?: string,
  signal?: AbortSignal,
): Promise<HarnessServerInfo | null> {
  try {
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
  } catch (error) {
    if (isUnsupportedByDaemon(error)) return null; // pre-ADR-0245 daemon
    throw error;
  }
}
