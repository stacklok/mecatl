// SPDX-License-Identifier: Apache-2.0

import { useSyncExternalStore } from "react";

const MOBILE_BREAKPOINT = 500;
const mobileQuery = `(max-width: ${MOBILE_BREAKPOINT - 1}px)`;
const desktopQuery = `(min-width: ${MOBILE_BREAKPOINT}px)`;

function isMobileViewport(): boolean {
  if (typeof window === "undefined") return false;
  return window.matchMedia?.(mobileQuery).matches ?? window.innerWidth < MOBILE_BREAKPOINT;
}

/**
 * True from the 500px pivot up. It asks the same media query as `min-[500px]:`,
 * so a layout chosen here agrees with those classes at every width, including
 * the fractional ones between 499 and 500px that `useIsMobile`'s query leaves
 * to neither side. Outside a browser it answers desktop, as `useIsMobile` does.
 */
export function isDesktopViewport(): boolean {
  if (typeof window === "undefined") return true;
  return window.matchMedia?.(desktopQuery).matches ?? window.innerWidth >= MOBILE_BREAKPOINT;
}

function subscribeTo(query: string) {
  return (listener: () => void): (() => void) => {
    const media = window.matchMedia?.(query);
    media?.addEventListener?.("change", listener);
    window.addEventListener("resize", listener);
    return () => {
      media?.removeEventListener?.("change", listener);
      window.removeEventListener("resize", listener);
    };
  };
}

const subscribeMobile = subscribeTo(mobileQuery);
const subscribeDesktop = subscribeTo(desktopQuery);

/** True below the mobile breakpoint (matches the app's own `min-[500px]` convention). */
export function useIsMobile(): boolean {
  return useSyncExternalStore(subscribeMobile, isMobileViewport, () => false);
}

/** `isDesktopViewport`, kept current as the window resizes. */
export function useIsDesktop(): boolean {
  return useSyncExternalStore(subscribeDesktop, isDesktopViewport, () => true);
}
