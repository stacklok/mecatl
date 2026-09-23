// SPDX-License-Identifier: Apache-2.0

import { useEffect, useState } from "react";

export type Theme = "light" | "dark" | "system";
export type EffectiveTheme = "light" | "dark";

const storageKey = "mecatl-studio-theme";
const themeChangedEvent = "mecatl-studio:theme-changed";

function storedTheme(): Theme {
  try {
    const value = window.localStorage.getItem(storageKey);
    return value === "light" || value === "dark" || value === "system" ? value : "system";
  } catch {
    return "system";
  }
}

function resolveTheme(theme: Theme): EffectiveTheme {
  if (theme !== "system") return theme;
  return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function applyTheme(theme: Theme) {
  const effectiveTheme = resolveTheme(theme);
  document.documentElement.classList.toggle("dark", effectiveTheme === "dark");
  document.documentElement.dataset.theme = theme;
  return effectiveTheme;
}

export function initializeTheme() {
  applyTheme(storedTheme());
}

export function useTheme() {
  const [theme, setThemeState] = useState(storedTheme);
  const [effectiveTheme, setEffectiveTheme] = useState(() => resolveTheme(storedTheme()));

  useEffect(() => {
    const media = window.matchMedia("(prefers-color-scheme: dark)");
    const synchronize = () => {
      const next = storedTheme();
      setThemeState(next);
      setEffectiveTheme(applyTheme(next));
    };
    window.addEventListener(themeChangedEvent, synchronize);
    window.addEventListener("storage", synchronize);
    media.addEventListener("change", synchronize);
    return () => {
      window.removeEventListener(themeChangedEvent, synchronize);
      window.removeEventListener("storage", synchronize);
      media.removeEventListener("change", synchronize);
    };
  }, []);

  function setTheme(nextTheme: Theme) {
    try {
      if (nextTheme === "system") window.localStorage.removeItem(storageKey);
      else window.localStorage.setItem(storageKey, nextTheme);
    } catch {
      // The selected theme still applies for this page when storage is unavailable.
    }
    setThemeState(nextTheme);
    setEffectiveTheme(applyTheme(nextTheme));
    window.dispatchEvent(new Event(themeChangedEvent));
  }

  return { effectiveTheme, setTheme, theme };
}
