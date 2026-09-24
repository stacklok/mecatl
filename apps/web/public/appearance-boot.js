// SPDX-License-Identifier: Apache-2.0

// This classic script runs before Vite's stylesheet and module scripts. Keep
// its validation and fallbacks aligned with src/lib/theme.ts and profile-preferences.ts.
(() => {
  const root = document.documentElement;
  const read = (key) => {
    try {
      return window.localStorage.getItem(key);
    } catch {
      return null;
    }
  };

  const savedTheme = read("mecatl-studio-theme");
  const theme = savedTheme === "light" || savedTheme === "dark" ? savedTheme : "system";
  const savedPalette = read("mecatl-studio.palette");
  const palette =
    savedPalette === "aztec" || savedPalette === "mono" || savedPalette === "solar"
      ? savedPalette
      : "default";
  let dark = theme === "dark";
  if (theme === "system") {
    try {
      dark = window.matchMedia?.("(prefers-color-scheme: dark)").matches === true;
    } catch {
      dark = false;
    }
  }

  root.classList.toggle("dark", dark);
  root.dataset.theme = theme;
  if (palette === "default") root.removeAttribute("data-palette");
  else root.dataset.palette = palette;

  const savedScale = Number.parseFloat(read("studio.profile.ui-scale") ?? "");
  const scale = Number.isFinite(savedScale)
    ? Math.min(1.3, Math.max(0.85, Math.round(savedScale * 20) / 20))
    : 1;
  if (scale === 1) root.style.removeProperty("--ui-scale");
  else root.style.setProperty("--ui-scale", String(scale));
})();
