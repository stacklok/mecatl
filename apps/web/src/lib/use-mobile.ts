// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from "react";

const MOBILE_BREAKPOINT = 500;

/** True below the mobile breakpoint (matches the app's own `min-[500px]` convention). */
export function useIsMobile(): boolean {
  const [isMobile, setIsMobile] = useState<boolean | undefined>(undefined);

  useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`);
    const onChange = () => setIsMobile(window.innerWidth < MOBILE_BREAKPOINT);
    mql.addEventListener("change", onChange);
    onChange();
    return () => mql.removeEventListener("change", onChange);
  }, []);

  return Boolean(isMobile);
}
