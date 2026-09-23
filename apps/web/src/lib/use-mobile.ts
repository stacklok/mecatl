// SPDX-License-Identifier: Apache-2.0

import { useSyncExternalStore } from "react";

const MOBILE_BREAKPOINT = 500;
const mobileQuery = `(max-width: ${MOBILE_BREAKPOINT - 1}px)`;

function isMobileViewport(): boolean {
  if (typeof window === "undefined") return false;
  return window.matchMedia?.(mobileQuery).matches ?? window.innerWidth < MOBILE_BREAKPOINT;
}

function subscribe(listener: () => void): () => void {
  const query = window.matchMedia?.(mobileQuery);
  query?.addEventListener?.("change", listener);
  window.addEventListener("resize", listener);
  return () => {
    query?.removeEventListener?.("change", listener);
    window.removeEventListener("resize", listener);
  };
}

/** True below the mobile breakpoint (matches the app's own `min-[500px]` convention). */
export function useIsMobile(): boolean {
  return useSyncExternalStore(subscribe, isMobileViewport, () => false);
}
