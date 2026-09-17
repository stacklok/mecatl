/**
 * Custom palette documents — the web analogue of mecatui's `{name, palette}`
 * JSON themes (`--theme-dir`, `~/.config/mecatui/themes/*.json`).
 *
 * A document names a palette and lists the colour tokens it overrides; a
 * partial document is merged over the active base by the cascade itself (a
 * `[data-palette]` block only redefines the tokens it lists, everything else
 * keeps the `:root` / `.dark` value), so "merge over base" is structural.
 *
 * This module is isomorphic (no DOM, no Node) because the SAME validator runs
 * in two places: the `/api/palettes` route (operator files from
 * `STUDIO_PALETTE_DIR`) and the browser (the user's own paste / upload, and
 * every stored or fetched entry re-validated on read). The security boundary
 * is here, not in the renderer: a token name must be in `PALETTE_TOKENS` and a
 * value must match `isSafeCssColor`, whose grammar has no `;`, `{`, `}`, `*`
 * or `(` inside an argument list — so `url(`, `var(`, `attr(`, `expression(`,
 * a closing brace or a comment opener cannot appear in the generated CSS at
 * all. `renderPaletteCSS` re-checks both before emitting each declaration.
 */

/**
 * The token names a document may override: the accent + shell subset the
 * built-in palettes restyle (see globals.css), plus the surface and text
 * tokens a "dark on light" palette needs. Every entry is a `:root` token
 * (palette-schema.test.ts pins that against globals.css).
 */
export const PALETTE_TOKENS = [
  // accent
  "brand",
  "brand-foreground",
  "brand-ink",
  "brand-label",
  "btn-primary",
  "btn-primary-hover",
  "ring",
  "link",
  // semantic status
  "info",
  "success",
  "warning",
  "destructive",
  "destructive-foreground",
  // shell band (top nav + workspace gradient)
  "nav-background",
  "nav-border",
  "nav-button-active-bg",
  "nav-button-active-text",
  "shell-gradient-start",
  "shell-gradient-mid",
  "shell-gradient-end",
  "nav-pill-bg",
  "nav-pill-text",
  "nav-icon",
  "nav-search-border",
  "nav-search-text",
  "nav-kbd-bg",
  // surfaces and text
  "background",
  "foreground",
  "card",
  "card-foreground",
  "popover",
  "popover-foreground",
  "muted",
  "muted-foreground",
  "border",
  "input",
  "accent",
  "accent-foreground",
  "sidebar",
  "title",
] as const;

type PaletteToken = (typeof PALETTE_TOKENS)[number];

const TOKEN_SET: ReadonlySet<string> = new Set(PALETTE_TOKENS);

/** Same grammar as a built-in palette id (`PALETTE_ID` in palettes.ts). */
export const PALETTE_NAME = /^[a-z][a-z0-9-]{0,31}$/;
/** The prefix that keeps custom ids apart from the built-in catalogue
 * (`CUSTOM_PALETTE_ID` in palettes.ts is the matching full-id grammar). */
const CUSTOM_PALETTE_PREFIX = "custom:";
/** A document (as text) may not exceed this many bytes. */
export const PALETTE_DOCUMENT_MAX_BYTES = 8 * 1024;
/** Each of `palette` / `dark` may list at most this many entries. */
export const PALETTE_MAX_TOKENS = 64;
const PALETTE_LABEL_MAX_CHARS = 40;
const COLOR_MAX_CHARS = 64;

/**
 * `#rgb[a]` / `#rrggbb[aa]`, or one functional notation whose argument list is
 * drawn from `[0-9a-z%.,/\s-]` only. No nested call, no `;`, `{`, `}`, `*`,
 * quotes, `<` or backslash can pass, whatever the source of the string.
 */
const COLOR_GRAMMAR =
  /^(?:#(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})|(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\([0-9a-z%.,/\s-]*\))$/i;

export function isSafeCssColor(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length > 0 &&
    value.length <= COLOR_MAX_CHARS &&
    COLOR_GRAMMAR.test(value)
  );
}

export type PaletteSource = "user" | "operator";

/** A validated palette: only allowlisted tokens, only safe colour values. */
export interface CustomPalette {
  name: string;
  label: string;
  /** Token overrides for the light block (and the dark block's base). */
  light: Partial<Record<PaletteToken, string>>;
  /** Extra overrides for dark mode; tokens not listed keep the `light` value. */
  dark: Partial<Record<PaletteToken, string>>;
  source: PaletteSource;
}

/** The wire / storage shape: what a user pastes and what the API returns. */
export interface PaletteDocument {
  name: string;
  label?: string;
  palette: Record<string, string>;
  dark?: Record<string, string>;
}

export type PaletteParseResult =
  | { ok: true; palette: CustomPalette }
  | { ok: false; error: string };

export function customPaletteId(name: string): string {
  return `${CUSTOM_PALETTE_PREFIX}${name}`;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return (
    typeof value === "object" &&
    value !== null &&
    !Array.isArray(value) &&
    Object.getPrototypeOf(value) === Object.prototype
  );
}

function byteLength(text: string): number {
  return new TextEncoder().encode(text).length;
}

/** "midnight-blue" → "Midnight blue": the label a document did not give. */
function labelFromName(name: string): string {
  const words = name.replace(/-+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

type MapResult =
  | { ok: true; map: Partial<Record<PaletteToken, string>> }
  | { ok: false; error: string };

function parseTokenMap(field: string, raw: unknown): MapResult {
  if (!isPlainObject(raw)) {
    return {
      ok: false,
      error: `"${field}" must be an object of token: colour`,
    };
  }
  const entries = Object.entries(raw);
  if (entries.length > PALETTE_MAX_TOKENS) {
    return {
      ok: false,
      error: `"${field}" lists ${entries.length} tokens; the limit is ${PALETTE_MAX_TOKENS}`,
    };
  }
  const map: Partial<Record<PaletteToken, string>> = {};
  for (const [token, value] of entries) {
    if (!TOKEN_SET.has(token)) {
      return {
        ok: false,
        error: `"${field}" names an unknown token "${token}" — see the allowed tokens below`,
      };
    }
    const trimmed = typeof value === "string" ? value.trim() : value;
    if (!isSafeCssColor(trimmed)) {
      return {
        ok: false,
        error: `"${field}.${token}" is not a supported colour — use #hex, rgb(), hsl(), oklch(), oklab() or color()`,
      };
    }
    map[token as PaletteToken] = trimmed;
  }
  return { ok: true, map };
}

/**
 * Parses and validates one palette document — JSON text or an already-parsed
 * value. Never throws; every failure names the field that failed so the UI
 * can show it verbatim.
 */
export function parsePaletteDocument(
  input: unknown,
  source: PaletteSource,
): PaletteParseResult {
  let value: unknown = input;
  if (typeof input === "string") {
    if (byteLength(input) > PALETTE_DOCUMENT_MAX_BYTES) {
      return {
        ok: false,
        error: `the document is larger than ${PALETTE_DOCUMENT_MAX_BYTES / 1024} KiB`,
      };
    }
    try {
      value = JSON.parse(input);
    } catch (error) {
      return {
        ok: false,
        error: `not valid JSON: ${error instanceof Error ? error.message : String(error)}`,
      };
    }
  }
  if (!isPlainObject(value)) {
    return {
      ok: false,
      error: 'the document must be a JSON object with "name" and "palette"',
    };
  }

  const name = value.name;
  if (typeof name !== "string" || !PALETTE_NAME.test(name)) {
    return {
      ok: false,
      error:
        '"name" must be 1–32 lowercase letters, digits or hyphens, starting with a letter',
    };
  }

  let label = labelFromName(name);
  if (value.label !== undefined) {
    if (typeof value.label !== "string" || value.label.trim() === "") {
      return { ok: false, error: '"label" must be a non-empty string' };
    }
    label = value.label.trim();
    if (label.length > PALETTE_LABEL_MAX_CHARS) {
      return {
        ok: false,
        error: `"label" is longer than ${PALETTE_LABEL_MAX_CHARS} characters`,
      };
    }
  }

  if (value.palette === undefined) {
    return { ok: false, error: '"palette" is required' };
  }
  const light = parseTokenMap("palette", value.palette);
  if (!light.ok) return light;
  if (Object.keys(light.map).length === 0) {
    return { ok: false, error: '"palette" must set at least one token' };
  }

  let dark: Partial<Record<PaletteToken, string>> = {};
  if (value.dark !== undefined) {
    const parsed = parseTokenMap("dark", value.dark);
    if (!parsed.ok) return parsed;
    dark = parsed.map;
  }

  return {
    ok: true,
    palette: { name, label, light: light.map, dark, source },
  };
}

/** The storage / wire form of a validated palette (round-trips through parse). */
export function toPaletteDocument(palette: CustomPalette): PaletteDocument {
  const document: PaletteDocument = {
    name: palette.name,
    label: palette.label,
    palette: { ...palette.light },
  };
  if (Object.keys(palette.dark).length > 0) document.dark = { ...palette.dark };
  return document;
}

function renderDeclarations(map: Partial<Record<PaletteToken, string>>) {
  const declarations: string[] = [];
  // Belt and braces: a CustomPalette normally comes out of
  // parsePaletteDocument, but a hand-built one must not widen the boundary.
  for (const [token, value] of Object.entries(map)) {
    if (!TOKEN_SET.has(token) || !isSafeCssColor(value)) continue;
    declarations.push(`--${token}:${value}`);
  }
  return declarations.join(";");
}

/**
 * The stylesheet for one palette: a light block and a dark block, keyed by
 * the `data-palette="custom:<name>"` attribute the provider puts on <html>.
 *
 * The dark block restates every light token (then applies the `dark`
 * overrides): `[data-palette]` ties `.dark` on specificity and this
 * stylesheet comes later in the document, so a token the light block set but
 * the dark block omitted would leak the light value into dark mode. Restating
 * makes the semantics explicit — one palette for both modes unless `dark`
 * says otherwise — exactly like the built-in block pairs in globals.css.
 *
 * Every selector piece is a validated name and every declaration a validated
 * token + colour, which is why the output can be placed in a <style> element
 * without escaping: nothing in it can close the block, the element or start
 * a comment.
 */
export function renderPaletteCSS(palette: CustomPalette): string {
  if (!PALETTE_NAME.test(palette.name)) return "";
  const selector = `[data-palette="${customPaletteId(palette.name)}"]`;
  const light = renderDeclarations(palette.light);
  const dark = renderDeclarations({ ...palette.light, ...palette.dark });
  if (light === "" && dark === "") return "";
  return `${selector}{${light}}\n.dark${selector}{${dark}}`;
}

/** The document the Personalize page shows as a starting point. */
export const EXAMPLE_PALETTE_DOCUMENT = `{
  "name": "midnight",
  "label": "Midnight",
  "palette": {
    "brand": "#4f7cff",
    "btn-primary": "#4f7cff",
    "btn-primary-hover": "#3b63d9",
    "link": "#4f7cff",
    "nav-background": "#0b1020",
    "shell-gradient-start": "#1b2a5a",
    "shell-gradient-mid": "#101a3a",
    "shell-gradient-end": "#070b18"
  },
  "dark": {
    "brand": "#7c9cff"
  }
}`;
