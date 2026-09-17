/**
 * Named colour palettes — the web analogue of mecatui's built-in themes
 * (`--theme aztec|mono|solar`, `--list-themes`, `MECATUI_THEME`).
 *
 * Two independent axes, matching how the TUI pins one theme while the web
 * keeps light/dark:
 *
 *   APPEARANCE — light / dark / system (next-themes, the `.dark` class), and
 *   PALETTE    — a named token set applied as `data-palette` on `<html>`.
 *
 * Every palette ships a light AND a dark block in `globals.css`
 * (`[data-palette="<id>"]` + `.dark[data-palette="<id>"]`), so picking a
 * palette never changes whether the page is light or dark, and the Shiki code
 * themes stay bound to the light/dark axis.
 *
 * The catalogue below IS the "list themes" surface (Settings → Personalize →
 * Palette renders it); `BRAND_PALETTE` is the operator's `MECATUI_THEME`
 * analogue — the deployment default a browser with no stored choice gets; and
 * the choice itself is browser-local (localStorage), like the other Personalize
 * preferences. There is no runtime loader for user-supplied palette files: a
 * new palette is a block pair in `globals.css` plus a catalogue entry here,
 * and `palettes-css.test.ts` fails when the two drift.
 *
 * This module is pure (no React, no DOM at import time) so the root layout —
 * a Server Component — can read the operator default and build the anti-flash
 * boot script from it. The hook lives in `components/palette-provider.tsx`.
 */

export const PALETTE_ATTRIBUTE = "data-palette";
export const PALETTE_STORAGE_KEY = "mecatl-studio.palette";
/** The shipped Stacklok tokens: no attribute, nothing stored. */
export const DEFAULT_PALETTE_ID = "default";
/** The grammar a palette id (and the `BRAND_PALETTE` env value) must match. */
export const PALETTE_ID = /^[a-z][a-z0-9-]{0,31}$/;
/**
 * The id of a custom palette (`lib/custom-palettes.ts`): the `custom:`
 * prefix keeps user/operator names apart from the built-in catalogue, so a
 * custom "aztec" never shadows the shipped one.
 */
export const CUSTOM_PALETTE_ID = /^custom:[a-z][a-z0-9-]{0,31}$/;

export function isCustomPaletteId(id: string | null | undefined): id is string {
  return typeof id === "string" && CUSTOM_PALETTE_ID.test(id);
}

export interface PaletteDef {
  id: string;
  label: string;
  /** One plain sentence for the picker — what the palette looks like. */
  description: string;
  /** The swatch the picker shows: the palette's light-block `--brand`. */
  swatch: string;
  /** Built-in (globals.css), or a custom palette the user or operator added. */
  source: "built-in" | "user" | "operator";
}

/**
 * The built-in catalogue. Ids are the `data-palette` values and the stored
 * preference; labels/descriptions follow user-docs/mecatui/themes.md.
 */
export const BUILT_IN_PALETTES: readonly PaletteDef[] = [
  {
    id: DEFAULT_PALETTE_ID,
    label: "Default",
    description: "Stacklok green.",
    swatch: "hsl(161 94% 21%)",
    source: "built-in",
  },
  {
    id: "aztec",
    label: "Aztec",
    description: "Jade, turquoise and gold on obsidian.",
    swatch: "#0f7f6c",
    source: "built-in",
  },
  {
    id: "mono",
    label: "Mono",
    description: "Neutral greys with a blue accent.",
    swatch: "#2563eb",
    source: "built-in",
  },
  {
    id: "solar",
    label: "Solar",
    description: "Warm and light-leaning.",
    swatch: "#9a7300",
    source: "built-in",
  },
];

/** Whether `id` names a palette in the catalogue. */
export function isKnownPalette(
  id: string | null | undefined,
  catalogue: readonly PaletteDef[] = BUILT_IN_PALETTES,
): id is string {
  return (
    typeof id === "string" && catalogue.some((palette) => palette.id === id)
  );
}

/**
 * Resolves a stored (or requested) id against the catalogue: a known id is
 * kept, anything else — null, an unknown or a removed palette — falls back.
 */
export function resolvePaletteId(
  stored: string | null | undefined,
  fallback: string,
  catalogue: readonly PaletteDef[] = BUILT_IN_PALETTES,
): string {
  return isKnownPalette(stored, catalogue) ? stored : fallback;
}

/**
 * The deployment default from `BRAND_PALETTE`: a catalogue id, case-folded
 * and trimmed like mecatui's `--theme Aztec`; unset, malformed, or unknown
 * values mean the shipped default. Never throws — a typo in a Helm value must
 * not take the page down.
 */
export function resolveDefaultPalette(
  raw: string | undefined,
  catalogue: readonly PaletteDef[] = BUILT_IN_PALETTES,
): string {
  const id = (raw ?? "").trim().toLowerCase();
  if (!PALETTE_ID.test(id)) return DEFAULT_PALETTE_ID;
  return resolvePaletteId(id, DEFAULT_PALETTE_ID, catalogue);
}

/**
 * Applies a palette to the root element: the default REMOVES the attribute
 * (the `:root`/`.dark` tokens stand), any other id sets it.
 */
export function applyPaletteAttribute(
  id: string,
  root: Element | undefined = typeof document === "undefined"
    ? undefined
    : document.documentElement,
) {
  if (!root) return;
  if (id === DEFAULT_PALETTE_ID) root.removeAttribute(PALETTE_ATTRIBUTE);
  else root.setAttribute(PALETTE_ATTRIBUTE, id);
}

export function readStoredPalette(): string | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage.getItem(PALETTE_STORAGE_KEY);
  } catch {
    return null;
  }
}

/** Writes the preference; null removes it (the deployment default stands). */
export function writeStoredPalette(id: string | null) {
  if (typeof window === "undefined") return;
  try {
    if (id === null) window.localStorage.removeItem(PALETTE_STORAGE_KEY);
    else window.localStorage.setItem(PALETTE_STORAGE_KEY, id);
  } catch {
    // Storage disabled or full — the preference just doesn't persist.
  }
}

/**
 * The blocking boot script the root layout inlines in `<head>` so the stored
 * (or deployment-default) palette lands on `<html>` before first paint — the
 * anti-flash technique next-themes uses for the light/dark class. A constant
 * program: every interpolation is `JSON.stringify` of catalogue ids or the
 * validated default, never user input. An unknown stored id resolves to the
 * default here exactly as `resolvePaletteId` does in React.
 *
 * A stored CUSTOM id (`custom:<name>`, the grammar checked in-script) is kept
 * as well: its stylesheet arrives with hydration (user palettes from
 * localStorage, operator palettes from `/api/palettes`), so until then the
 * attribute matches no rule and the `:root` tokens stand — but the page
 * never flashes the deployment default first, and PaletteProvider resolves
 * a removed palette back to the default once the catalogue is known.
 */
export function buildPaletteBootScript(
  defaultPalette: string,
  catalogue: readonly PaletteDef[] = BUILT_IN_PALETTES,
): string {
  const ids = JSON.stringify(catalogue.map((palette) => palette.id));
  const fallback = JSON.stringify(
    resolvePaletteId(defaultPalette, DEFAULT_PALETTE_ID, catalogue),
  );
  const key = JSON.stringify(PALETTE_STORAGE_KEY);
  const attribute = JSON.stringify(PALETTE_ATTRIBUTE);
  const none = JSON.stringify(DEFAULT_PALETTE_ID);
  const custom = JSON.stringify(CUSTOM_PALETTE_ID.source);
  return (
    "(function(){try{" +
    `var d=document.documentElement,i=${ids},f=${fallback},s=null;` +
    `try{s=localStorage.getItem(${key})}catch(e){}` +
    `var v=s&&(i.indexOf(s)>=0||new RegExp(${custom}).test(s))?s:f;` +
    `if(v===${none})d.removeAttribute(${attribute});else d.setAttribute(${attribute},v)` +
    "}catch(e){}})();"
  );
}
