"use client";

import { createContext, useCallback, useContext, useEffect, useState } from "react";

/**
 * UI font scaling.
 *
 * Implemented by setting `font-size` on the ROOT element, not by rewriting a
 * token: every size in the app is either a rem-derived Tailwind utility or a
 * px value inside the legacy component CSS, and the root font-size is the one
 * lever that moves the rem-based majority together. The px holdouts are the
 * conversation surface's own type, which is why `--ui-scale` is also published
 * for the few rules that opt in.
 *
 * The value is persisted locally so a reload does not snap the operator back to
 * 100%, and applied in an effect rather than during render because it mutates
 * the document.
 */
const SCALES = [0.875, 1, 1.125, 1.25] as const;
export type FontScale = (typeof SCALES)[number];

const STORAGE_KEY = "mecatl-studio-font-scale";
const DEFAULT_SCALE: FontScale = 1;

export const FONT_SCALE_LABELS: Record<FontScale, string> = {
  0.875: "Small",
  1: "Default",
  1.125: "Large",
  1.25: "Larger",
};

function isFontScale(value: number): value is FontScale {
  return (SCALES as readonly number[]).includes(value);
}

const FontScaleContext = createContext<{
  scale: FontScale;
  setScale: (scale: FontScale) => void;
  scales: readonly FontScale[];
}>({ scale: DEFAULT_SCALE, setScale: () => {}, scales: SCALES });

export function FontScaleProvider({ children }: { children: React.ReactNode }) {
  const [scale, setScaleState] = useState<FontScale>(DEFAULT_SCALE);

  // Restore once on mount. Reading localStorage during render would break SSR,
  // so the first paint is always the default and this corrects it.
  useEffect(() => {
    const stored = Number(localStorage.getItem(STORAGE_KEY));
    if (Number.isFinite(stored) && isFontScale(stored) && stored !== DEFAULT_SCALE) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setScaleState(stored);
    }
  }, []);

  useEffect(() => {
    const root = document.documentElement;
    // 16px is the browser default we are scaling from; expressing it in px
    // rather than % avoids compounding if a user agent has its own base size.
    root.style.fontSize = `${16 * scale}px`;
    root.style.setProperty("--ui-scale", String(scale));
    return () => {
      root.style.removeProperty("font-size");
      root.style.removeProperty("--ui-scale");
    };
  }, [scale]);

  const setScale = useCallback((next: FontScale) => {
    setScaleState(next);
    localStorage.setItem(STORAGE_KEY, String(next));
  }, []);

  return (
    <FontScaleContext.Provider value={{ scale, setScale, scales: SCALES }}>
      {children}
    </FontScaleContext.Provider>
  );
}

export const useFontScale = () => useContext(FontScaleContext);
