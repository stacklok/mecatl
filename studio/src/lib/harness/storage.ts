/**
 * Storage health (ADR 0226): `GET /v1/storage/health` over the SDK
 * (`client.storage.getHealth`), gated by `capabilities.storage_health` on
 * GET /v1/compatibility.
 *
 * Studio reads this for ONE purpose — the degraded-store banner that explains
 * why sessions may be missing from the sidebar. Migration/cleanup job control
 * stays CLI/TUI; only the banner-relevant subset is projected here.
 */

import type { GetStorageHealthResponse } from "@stacklok-oss/mecatl-sdk/gen";

import { getHarnessClient, harness } from "./sdk";

export interface StorageHealth {
  /** False when the session store itself cannot be read. */
  available: boolean;
  unavailableReason: string;
  sessionCount: number;
  /** Session families the store holds but cannot load — "missing" sessions. */
  corruptCount: number;
  /**
   * Legacy-layout families awaiting migration (CLI/TUI job). The current
   * proto carries no such count, so this is always 0; kept so the degraded
   * classification's "v1 is not degraded" rule stays stated in one place.
   */
  v1Count: number;
  /** The last background sweep/migration failure, "" when none. */
  lastFailure: string;
  activeJob: string;
}

/** Projects the SDK response onto the banner subset. */
export function decodeStorageHealth(
  response: Pick<
    GetStorageHealthResponse,
    | "available"
    | "unavailableReason"
    | "sessionCount"
    | "corruptCount"
    | "lastFailure"
    | "activeJob"
  >,
): StorageHealth {
  return {
    available: response.available,
    unavailableReason: response.unavailableReason,
    sessionCount: Number(response.sessionCount),
    corruptCount: Number(response.corruptCount),
    v1Count: 0,
    lastFailure: response.lastFailure,
    activeJob: response.activeJob,
  };
}

/**
 * True when the store's state can explain sessions missing from the sidebar:
 * the store is unreadable, some families no longer load, or a background job
 * failed. Plain unmigrated v1 sessions still list, so they are NOT degraded.
 */
export function isStorageDegraded(health: StorageHealth): boolean {
  return (
    !health.available || health.corruptCount > 0 || health.lastFailure !== ""
  );
}

export async function fetchStorageHealth(
  signal?: AbortSignal,
): Promise<StorageHealth> {
  const response = await harness(() =>
    getHarnessClient().storage.getHealth(
      { $typeName: "mecatl.v1.GetStorageHealthRequest" },
      { signal },
    ),
  );
  return decodeStorageHealth(response);
}
