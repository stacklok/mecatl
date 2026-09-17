"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  fetchStorageHealth,
  isStorageDegraded,
  type StorageHealth,
} from "@/lib/harness/storage";
import { useRuntimeStatus } from "../runtime-status";

/**
 * Reads the daemon's storage health (ADR 0226) once per (re)connect and on
 * every mount, gated on `capabilities.storage_health`; `refresh()` re-reads
 * on demand (the Storage page calls it after a retention save, so the table
 * shows the policy the restarted daemon actually applies). Quiet by design:
 * an unsupported daemon, a management-authorization refusal, or any read
 * failure all report null — the banner this feeds must only ever appear on
 * a POSITIVE degraded signal, never because the probe itself could not run.
 */
export function useStorageHealth(): {
  health: StorageHealth | null;
  degraded: boolean;
  /** `capabilities.storage_health === true` on the connected daemon. */
  supported: boolean;
  refresh: () => void;
} {
  const { connected, serverCapabilities } = useRuntimeStatus();
  const supported = serverCapabilities.storage_health === true;
  const [health, setHealth] = useState<StorageHealth | null>(null);
  // The in-flight read, so a refresh (or unmount) cancels a superseded one
  // and a late answer can never overwrite a newer read.
  const active = useRef<AbortController | null>(null);

  const load = useCallback(() => {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    fetchStorageHealth(controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setHealth(result);
      })
      .catch(() => {
        if (!controller.signal.aborted) setHealth(null);
      });
  }, []);

  useEffect(() => {
    if (!connected || !supported) {
      setHealth(null);
      return;
    }
    load();
    return () => active.current?.abort();
  }, [connected, supported, load]);

  const refresh = useCallback(() => {
    if (connected && supported) load();
  }, [connected, supported, load]);

  return {
    health,
    degraded: health !== null && isStorageDegraded(health),
    supported,
    refresh,
  };
}
