// SPDX-License-Identifier: Apache-2.0

import { useSyncExternalStore } from "react";
import type { Palette } from "./palettes";

export type Theme = "light" | "dark" | "system";
export type EffectiveTheme = "light" | "dark";

type Appearance = Readonly<{ theme: Theme; effectiveTheme: EffectiveTheme; palette: Palette }>;
type Listener = () => void;

const themeKey = "mecatl-studio-theme";
const paletteKey = "mecatl-studio.palette";
const darkQuery = "(prefers-color-scheme: dark)";
const fallback: Appearance = { theme: "system", effectiveTheme: "light", palette: "default" };

function parseTheme(value: string | null): Theme {
  return value === "light" || value === "dark" ? value : "system";
}

function parsePalette(value: string | null): Palette {
  return value === "aztec" || value === "mono" || value === "solar" ? value : "default";
}

function readStorage(key: string): { available: boolean; value: string | null } {
  try {
    return { available: true, value: window.localStorage.getItem(key) };
  } catch {
    return { available: false, value: null };
  }
}

function writeStorage(key: string, value: string | null) {
  try {
    if (value === null) window.localStorage.removeItem(key);
    else window.localStorage.setItem(key, value);
  } catch {
    // The in-memory choice remains authoritative for this page.
  }
}

function mediaQuery(): MediaQueryList | undefined {
  try {
    return window.matchMedia?.(darkQuery);
  } catch {
    return undefined;
  }
}

function resolveTheme(theme: Theme): EffectiveTheme {
  if (theme !== "system") return theme;
  return mediaQuery()?.matches === true ? "dark" : "light";
}

function applyRoot(appearance: Appearance) {
  const root = document.documentElement;
  root.classList.toggle("dark", appearance.effectiveTheme === "dark");
  root.dataset.theme = appearance.theme;
  if (appearance.palette === "default") root.removeAttribute("data-palette");
  else root.dataset.palette = appearance.palette;
}

/** Shared browser state for every mounted theme and palette control. */
export function createAppearanceStore() {
  let snapshot: Appearance | undefined;
  let listening = false;
  let frame: number | undefined;
  let generation = 0;
  const listeners = new Set<Listener>();

  function getSnapshot(): Appearance {
    if (!snapshot) {
      const theme = parseTheme(readStorage(themeKey).value);
      const palette = parsePalette(readStorage(paletteKey).value);
      snapshot = { theme, effectiveTheme: resolveTheme(theme), palette };
    }
    return snapshot;
  }

  function suppressTransitions() {
    const root = document.documentElement;
    const current = ++generation;
    root.classList.add("appearance-changing");
    if (frame !== undefined) window.cancelAnimationFrame(frame);
    frame = window.requestAnimationFrame(() => {
      if (current !== generation) return;
      frame = window.requestAnimationFrame(() => {
        if (current !== generation) return;
        frame = undefined;
        root.classList.remove("appearance-changing");
      });
    });
  }

  function update(theme: Theme, palette: Palette) {
    const next: Appearance = { theme, effectiveTheme: resolveTheme(theme), palette };
    const current = getSnapshot();
    if (
      current.theme === next.theme &&
      current.effectiveTheme === next.effectiveTheme &&
      current.palette === next.palette
    ) {
      return;
    }
    suppressTransitions();
    snapshot = next;
    applyRoot(next);
    for (const listener of listeners) listener();
  }

  function onStorage(event: StorageEvent) {
    if (event.key !== null && event.key !== themeKey && event.key !== paletteKey) return;
    const current = getSnapshot();
    const savedTheme = event.key === null || event.key === themeKey ? readStorage(themeKey) : null;
    const savedPalette =
      event.key === null || event.key === paletteKey ? readStorage(paletteKey) : null;
    update(
      savedTheme?.available ? parseTheme(savedTheme.value) : current.theme,
      savedPalette?.available ? parsePalette(savedPalette.value) : current.palette,
    );
  }

  function onSystemChange() {
    const current = getSnapshot();
    if (current.theme === "system") update(current.theme, current.palette);
  }

  function listen() {
    if (listening) return;
    listening = true;
    window.addEventListener("storage", onStorage);
    mediaQuery()?.addEventListener?.("change", onSystemChange);
  }

  function initialize() {
    applyRoot(getSnapshot());
    listen();
  }

  function subscribe(listener: Listener) {
    listeners.add(listener);
    listen();
    return () => listeners.delete(listener);
  }

  function setTheme(next: Theme) {
    const current = getSnapshot();
    update(next, current.palette);
    writeStorage(themeKey, next === "system" ? null : next);
  }

  function setPalette(next: Palette) {
    const current = getSnapshot();
    update(current.theme, next);
    writeStorage(paletteKey, next === "default" ? null : next);
  }

  return {
    getServerSnapshot: () => fallback,
    getSnapshot,
    initialize,
    setPalette,
    setTheme,
    subscribe,
  };
}

export const appearanceStore = createAppearanceStore();

export function initializeTheme() {
  appearanceStore.initialize();
}

export function useTheme() {
  const snapshot = useSyncExternalStore(
    appearanceStore.subscribe,
    appearanceStore.getSnapshot,
    appearanceStore.getServerSnapshot,
  );
  return {
    effectiveTheme: snapshot.effectiveTheme,
    setTheme: appearanceStore.setTheme,
    theme: snapshot.theme,
  };
}
