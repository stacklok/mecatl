import { useEffect, useState } from "react";

const MOBILE_BREAKPOINT = 500;
const COMPACT_BREAKPOINT = 900;

export function useIsMobile(): boolean {
  const [isMobile, setIsMobile] = useState<boolean | undefined>(undefined);

  useEffect(() => {
    const mql = window.matchMedia(`(max-width: ${MOBILE_BREAKPOINT - 1}px)`);
    const onChange = () => {
      setIsMobile(window.innerWidth < MOBILE_BREAKPOINT);
    };
    mql.addEventListener("change", onChange);
    setIsMobile(window.innerWidth < MOBILE_BREAKPOINT);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  return !!isMobile;
}

/**
 * True when viewport is in the compact range (500 – 899px).
 *
 * In this range the chat sidebar is hidden by default and, when opened,
 * floats above the chat content as an overlay rather than pushing it.
 * Below 500px we use the dedicated mobile layout; at 900px+ the sidebar
 * sits side-by-side with the chat.
 */
export function useIsCompact(): boolean {
  const [isCompact, setIsCompact] = useState<boolean | undefined>(undefined);

  useEffect(() => {
    const mql = window.matchMedia(
      `(min-width: ${MOBILE_BREAKPOINT}px) and (max-width: ${COMPACT_BREAKPOINT - 1}px)`,
    );
    const update = () => setIsCompact(mql.matches);
    mql.addEventListener("change", update);
    update();
    return () => mql.removeEventListener("change", update);
  }, []);

  return !!isCompact;
}
