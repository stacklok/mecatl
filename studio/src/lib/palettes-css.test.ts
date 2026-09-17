import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { BUILT_IN_PALETTES, DEFAULT_PALETTE_ID } from "./palettes";

/**
 * Mechanical anti-drift between the palette catalogue (src/lib/palettes.ts)
 * and the stylesheet (src/app/globals.css). A catalogue entry with no CSS
 * would render as the default and lie in the picker; a CSS block with no
 * entry would be unreachable. Both blocks of a pair must define the same
 * token set — `[data-palette]` ties `.dark` on specificity and wins by source
 * order, so a token the light block touches but the dark block omits would
 * leak the light value into dark mode. And the blocks must come AFTER
 * `:root` / `.dark`, or that same tie goes the other way.
 */
const css = readFileSync(
  resolve(import.meta.dirname, "../app/globals.css"),
  "utf8",
).replace(/\/\*[\s\S]*?\*\//g, "");

/** The `{…}` body of the first rule whose selector list is exactly `selector`. */
function block(selector: string): { start: number; body: string } | null {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = new RegExp(`(^|\\n)${escaped}\\s*\\{([^}]*)\\}`).exec(css);
  if (!match) return null;
  return { start: match.index, body: match[2] };
}

function tokens(body: string): string[] {
  return [...body.matchAll(/--([a-z0-9-]+)\s*:/g)].map((m) => m[1]).sort();
}

const root = block(":root");
const dark = block(".dark");
if (!root || !dark)
  throw new Error("globals.css lost its :root or .dark block");

const SHELL_TOKENS = [
  "shell-gradient-start",
  "shell-gradient-mid",
  "shell-gradient-end",
  "nav-pill-bg",
  "nav-pill-text",
  "nav-icon",
  "nav-search-border",
  "nav-search-text",
  "nav-kbd-bg",
];

describe("globals.css shell tokens", () => {
  it("defines the promoted shell tokens on both :root and .dark", () => {
    for (const token of SHELL_TOKENS) {
      expect(tokens(root.body), `:root --${token}`).toContain(token);
      expect(tokens(dark.body), `.dark --${token}`).toContain(token);
    }
  });

  it("paints the canvas with the shell token, not a literal", () => {
    expect(css).toMatch(
      /html,\s*body\s*\{\s*background-color:\s*var\(--shell-gradient-end\);/,
    );
  });

  it("uses the default palette's swatch as :root --brand", () => {
    const def = BUILT_IN_PALETTES.find((p) => p.id === DEFAULT_PALETTE_ID);
    expect(def).toBeDefined();
    expect(root.body).toContain(`--brand: ${def?.swatch};`);
  });
});

describe.each(
  BUILT_IN_PALETTES.filter((palette) => palette.id !== DEFAULT_PALETTE_ID),
)("palette $id", (palette) => {
  const light = block(`[data-palette="${palette.id}"]`);
  const darkBlock = block(`.dark[data-palette="${palette.id}"]`);

  it("has a light and a dark block", () => {
    expect(light, `[data-palette="${palette.id}"]`).not.toBeNull();
    expect(darkBlock, `.dark[data-palette="${palette.id}"]`).not.toBeNull();
  });

  it("places both blocks after :root and .dark (the specificity tie)", () => {
    expect(light?.start).toBeGreaterThan(root.start);
    expect(light?.start).toBeGreaterThan(dark.start);
    expect(darkBlock?.start).toBeGreaterThan(dark.start);
  });

  it("defines the same token set in light and dark", () => {
    const lightTokens = tokens(light?.body ?? "");
    expect(lightTokens.length).toBeGreaterThan(0);
    expect(tokens(darkBlock?.body ?? "")).toEqual(lightTokens);
  });

  it("overrides only tokens :root already defines (no typos, no new tokens)", () => {
    const known = new Set(tokens(root.body));
    for (const token of tokens(light?.body ?? "")) {
      expect(known.has(token), `--${token} is not a :root token`).toBe(true);
    }
  });

  it("restyles the shell band, not just the accent", () => {
    for (const token of SHELL_TOKENS) {
      expect(tokens(light?.body ?? ""), `--${token}`).toContain(token);
    }
  });

  it("uses the swatch the picker shows as its light --brand", () => {
    expect(light?.body).toContain(`--brand: ${palette.swatch};`);
  });
});

describe("no orphan palette blocks", () => {
  it("every [data-palette] block in the stylesheet is a catalogue id", () => {
    const ids = new Set(BUILT_IN_PALETTES.map((palette) => palette.id));
    for (const match of css.matchAll(/\[data-palette="([^"]+)"\]/g)) {
      expect(ids.has(match[1]), `stray palette block "${match[1]}"`).toBe(true);
    }
  });
});

describe("shell consumers read the tokens", () => {
  const layout = readFileSync(
    resolve(import.meta.dirname, "../app/workspace/layout.tsx"),
    "utf8",
  );
  const topNav = readFileSync(
    resolve(import.meta.dirname, "../components/shell/top-nav.tsx"),
    "utf8",
  );

  it("the workspace gradient is built from --shell-gradient-*", () => {
    expect(layout).toContain("var(--shell-gradient-start)");
    expect(layout).toContain("var(--shell-gradient-mid)");
    expect(layout).toContain("var(--shell-gradient-end)");
  });

  it("the top nav carries no hard-coded hex colour", () => {
    expect(topNav).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    for (const token of [
      "nav-pill-bg",
      "nav-pill-text",
      "nav-icon",
      "nav-search-border",
      "nav-search-text",
      "nav-kbd-bg",
    ]) {
      expect(topNav, `--${token}`).toContain(`(--${token})`);
    }
  });
});
