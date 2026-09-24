// SPDX-License-Identifier: Apache-2.0

import type { GetRuntimeResponse } from "@mecatl-studio/contracts/generated";
import { useSyncExternalStore } from "react";

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
