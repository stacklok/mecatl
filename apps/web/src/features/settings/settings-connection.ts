// SPDX-License-Identifier: Apache-2.0

import type { GetRuntimeResponse } from "@mecatl-studio/contracts/generated";
import { useLayoutEffect, useRef, useState, useSyncExternalStore } from "react";

function subscribe(onChange: () => void) {
  window.addEventListener("online", onChange);
  window.addEventListener("offline", onChange);
  return () => {
    window.removeEventListener("online", onChange);
    window.removeEventListener("offline", onChange);
  };
}

function browserOnline() {
  return typeof navigator === "undefined" || navigator.onLine !== false;
}

/** Browser network state prevents stale cached BFF facts from looking current. */
export function useBrowserOnline() {
  return useSyncExternalStore(subscribe, browserOnline, () => true);
}

/** Query polling keeps visible deployment facts current after route-entry validation. */
export const freshDeploymentQuery = {
  refetchInterval: 30_000,
  refetchOnMount: false,
  refetchOnReconnect: "always",
  refetchOnWindowFocus: "always",
  retry: false,
  staleTime: Infinity,
} as const;

/** Recheck cached facts before paint whenever a deployment route is entered. */
export function useRefreshOnEntry(
  entryKey: string,
  enabled: boolean,
  hasCachedData: boolean,
  refetch: () => Promise<unknown>,
) {
  const lastEntry = useRef<string | null>(null);
  const [validating, setValidating] = useState(false);
  useLayoutEffect(() => {
    if (!enabled) {
      lastEntry.current = null;
      return;
    }
    if (lastEntry.current === entryKey) return;
    lastEntry.current = entryKey;
    if (!hasCachedData) return;
    let active = true;
    setValidating(true);
    void refetch().finally(() => {
      if (active) setValidating(false);
    });
    return () => {
      active = false;
    };
  }, [enabled, entryKey, hasCachedData, refetch]);
  return validating;
}

export function connectionMessage(connection: GetRuntimeResponse["connection"]): string | null {
  switch (connection) {
    case "online":
      return null;
    case "offline":
      return "Offline. Connect to the agent to read current deployment settings.";
    case "connecting":
      return "Connecting to the agent…";
    case "reconnecting":
      return "Reconnecting to the agent. Current deployment settings are unavailable.";
    case "unauthorized":
      return "Sign in to read current deployment settings.";
    case "incompatible":
      return "This agent version is incompatible with Studio settings.";
  }
}
