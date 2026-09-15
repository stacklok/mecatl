"use client";

import { useEffect, useState } from "react";
import {
  fetchStorageHealth,
  isStorageDegraded,
  type StorageHealth,
} from "@/lib/harness/storage";
import { useRuntimeStatus } from "../runtime-status";

/**
 * Reads the daemon's storage health (ADR 0226) once per (re)connect, gated on
 * `capabilities.storage_health`. Quiet by design: an unsupported daemon, a
 * management-authorization refusal, or any read failure all report null —
 * the banner this feeds must only ever appear on a POSITIVE degraded signal,
 * never because the probe itself could not run.
 */
export function useStorageHealth(): {
  health: StorageHealth | null;
  degraded: boolean;
} {
  const { connected, serverCapabilities } = useRuntimeStatus();
  const supported = serverCapabilities.storage_health === true;
  const [health, setHealth] = useState<StorageHealth | null>(null);

  useEffect(() => {
    if (!connected || !supported) {
      setHealth(null);
      return;
    }
    const controller = new AbortController();
    fetchStorageHealth(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setHealth(result);
      })
      .catch(() => {
        if (!controller.signal.aborted) setHealth(null);
      });
    return () => controller.abort();
  }, [connected, supported]);

  return {
    health,
    degraded: health !== null && isStorageDegraded(health),
  };
}
