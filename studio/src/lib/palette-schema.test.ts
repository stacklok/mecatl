import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import {
  type CustomPalette,
  customPaletteId,
  EXAMPLE_PALETTE_DOCUMENT,
  isSafeCssColor,
  PALETTE_DOCUMENT_MAX_BYTES,
  PALETTE_MAX_TOKENS,
  PALETTE_NAME,
  PALETTE_TOKENS,
  parsePaletteDocument,
  renderPaletteCSS,
  toPaletteDocument,
} from "./palette-schema";

/**
 * The validator IS the injection boundary for user- and operator-supplied
 * CSS: token names must be allowlisted and colour values must fit a grammar
 * with no `;`, `{`, `}`, `*`, quotes or nested calls. These tests pin both
 * halves and the renderer that consumes them.
 */
describe("isSafeCssColor", () => {
  it("accepts hex, rgb, hsl, oklch, oklab and color() values", () => {
    for (const value of [
      "#fff",
      "#FFFa",
      "#036a49",
      "#036a49cc",
      "rgb(255 0 0)",
      "rgba(255, 0, 0, 0.5)",
      "rgb(255 0 0 / 50%)",
      "hsl(161 94% 21%)",
      "hsla(161, 94%, 21%, .5)",
      "oklch(0.696 0 0 / 89.8%)",
      "oklab(0.5 -0.1 0.1)",
      "color(display-p3 1 0 0)",
    ]) {
      expect(isSafeCssColor(value), value).toBe(true);
    }
  });

  it("rejects anything that could escape a declaration or pull in a resource", () => {
    for (const value of [
      "",
      "red",
      "#12",
      "#12345",
      "#gggggg",
      "url(https://evil.example/x.png)",
      "var(--foo)",
      "attr(title)",
      "expression(alert(1))",
      "rgb(calc(1 + 1) 0 0)",
      "#fff; background: url(x)",
      "#fff}",
      "#fff{",
      "rgb(0 0 0) /* comment */",
      'rgb(0 0 0) "x"',
      "rgb(0 0 0)</style>",
      `rgb(${"0 ".repeat(40)})`,
      42,
      null,
      undefined,
      { toString: () => "#fff" },
    ]) {
      expect(isSafeCssColor(value), String(value)).toBe(false);
    }
  });
});

describe("PALETTE_TOKENS", () => {
  const css = readFileSync(
    resolve(import.meta.dirname, "../app/globals.css"),
    "utf8",
  ).replace(/\/\*[\s\S]*?\*\//g, "");
  const root = /(^|\n):root\s*\{([^}]*)\}/.exec(css);
  const rootTokens = new Set(
    [...(root?.[2] ?? "").matchAll(/--([a-z0-9-]+)\s*:/g)].map((m) => m[1]),
  );

  it("only names tokens :root defines (a typo would be an unreachable override)", () => {
    expect(rootTokens.size).toBeGreaterThan(0);
    for (const token of PALETTE_TOKENS) {
      expect(rootTokens.has(token), `--${token} is not a :root token`).toBe(
        true,
      );
    }
  });

  it("covers every token the built-in palettes override", () => {
    const aztec = /\[data-palette="aztec"\]\s*\{([^}]*)\}/.exec(css);
    const overridden = [...(aztec?.[1] ?? "").matchAll(/--([a-z0-9-]+)\s*:/g)]
      .map((m) => m[1])
      .sort();
    expect(overridden.length).toBeGreaterThan(0);
    for (const token of overridden) {
      expect(PALETTE_TOKENS, `--${token}`).toContain(token);
    }
  });

  it("has no duplicates", () => {
    expect(new Set(PALETTE_TOKENS).size).toBe(PALETTE_TOKENS.length);
  });
});

describe("parsePaletteDocument", () => {
  it("accepts a partial palette from JSON text and defaults the label", () => {
    const result = parsePaletteDocument(
      '{"name":"midnight-blue","palette":{"brand":"#4f7cff","link":"#4f7cff"}}',
      "user",
    );
    expect(result).toEqual({
      ok: true,
      palette: {
        name: "midnight-blue",
        label: "Midnight blue",
        light: { brand: "#4f7cff", link: "#4f7cff" },
        dark: {},
        source: "user",
      },
    });
  });

  it("accepts an already-parsed value, a label and a dark block, trimming values", () => {
    const result = parsePaletteDocument(
      {
        name: "ember",
        label: "  Ember  ",
        palette: { brand: " hsl(20 90% 50%) " },
        dark: { brand: "rgb(255 120 60)" },
        $schema: "ignored",
      },
      "operator",
    );
    expect(result.ok).toBe(true);
    if (!result.ok) return;
    expect(result.palette.label).toBe("Ember");
    expect(result.palette.light).toEqual({ brand: "hsl(20 90% 50%)" });
    expect(result.palette.dark).toEqual({ brand: "rgb(255 120 60)" });
    expect(result.palette.source).toBe("operator");
  });

  it("parses the example document the settings page shows", () => {
    const result = parsePaletteDocument(EXAMPLE_PALETTE_DOCUMENT, "user");
    expect(result.ok).toBe(true);
    if (result.ok) expect(result.palette.name).toBe("midnight");
  });

  it.each([
    ["not json", "not valid JSON"],
    ["[]", "must be a JSON object"],
    ['{"palette":{"brand":"#fff"}}', '"name" must be'],
    ['{"name":"Bad Name","palette":{"brand":"#fff"}}', '"name" must be'],
    ['{"name":"custom:x","palette":{"brand":"#fff"}}', '"name" must be'],
    [
      `{"name":"${"a".repeat(33)}","palette":{"brand":"#fff"}}`,
      '"name" must be',
    ],
    ['{"name":"x"}', '"palette" is required'],
    ['{"name":"x","palette":[]}', '"palette" must be an object'],
    ['{"name":"x","palette":{}}', "at least one token"],
    ['{"name":"x","palette":{"bogus":"#fff"}}', 'unknown token "bogus"'],
    [
      '{"name":"x","palette":{"brand":"url(https://evil.example)"}}',
      '"palette.brand" is not a supported colour',
    ],
    ['{"name":"x","palette":{"brand":"var(--x)"}}', "not a supported colour"],
    [
      '{"name":"x","palette":{"brand":"#fff; background:url(x)"}}',
      "not a supported colour",
    ],
    ['{"name":"x","palette":{"brand":"#fff}"}}', "not a supported colour"],
    [
      '{"name":"x","palette":{"brand":"#fff"},"dark":"#000"}',
      '"dark" must be an object',
    ],
    [
      '{"name":"x","palette":{"brand":"#fff"},"dark":{"nope":"#000"}}',
      'unknown token "nope"',
    ],
    ['{"name":"x","label":"","palette":{"brand":"#fff"}}', '"label" must be'],
    [
      `{"name":"x","label":"${"L".repeat(41)}","palette":{"brand":"#fff"}}`,
      "longer than 40",
    ],
    ['{"name":"x","palette":{"__proto__":"#fff"}}', "unknown token"],
  ])("rejects %s", (text, message) => {
    const result = parsePaletteDocument(text, "user");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain(message);
  });

  it("rejects oversize text before parsing it", () => {
    const padding = " ".repeat(PALETTE_DOCUMENT_MAX_BYTES);
    const result = parsePaletteDocument(
      `{"name":"x","palette":{"brand":"#fff"}${padding}}`,
      "user",
    );
    expect(result).toEqual({
      ok: false,
      error: "the document is larger than 8 KiB",
    });
  });

  it("rejects more than the token cap in one map, before checking the names", () => {
    const palette: Record<string, string> = {};
    for (let i = 0; i <= PALETTE_MAX_TOKENS; i += 1) palette[`t${i}`] = "#fff";
    const result = parsePaletteDocument({ name: "x", palette }, "user");
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.error).toContain("the limit is 64");
  });

  it("round-trips through toPaletteDocument", () => {
    const first = parsePaletteDocument(EXAMPLE_PALETTE_DOCUMENT, "user");
    expect(first.ok).toBe(true);
    if (!first.ok) return;
    const again = parsePaletteDocument(
      toPaletteDocument(first.palette),
      "user",
    );
    expect(again).toEqual(first);
    // A palette without dark overrides serialises without a `dark` key.
    const plain = parsePaletteDocument(
      { name: "p", palette: { brand: "#fff" } },
      "user",
    );
    if (plain.ok)
      expect(toPaletteDocument(plain.palette)).not.toHaveProperty("dark");
  });
});

describe("renderPaletteCSS", () => {
  const palette: CustomPalette = {
    name: "midnight",
    label: "Midnight",
    light: { brand: "#4f7cff", link: "#4f7cff" },
    dark: { brand: "#7c9cff" },
    source: "user",
  };

  it("emits a light and a dark block keyed by the custom id", () => {
    const css = renderPaletteCSS(palette);
    expect(css).toBe(
      '[data-palette="custom:midnight"]{--brand:#4f7cff;--link:#4f7cff}\n' +
        '.dark[data-palette="custom:midnight"]{--brand:#7c9cff;--link:#4f7cff}',
    );
    expect(customPaletteId("midnight")).toBe("custom:midnight");
  });

  it("restates every light token in the dark block (the specificity tie)", () => {
    const css = renderPaletteCSS(palette);
    const dark = /\.dark\[data-palette="custom:midnight"\]\{([^}]*)\}/.exec(
      css,
    );
    expect(dark?.[1]).toContain("--link:#4f7cff");
  });

  it("contains only allowlisted declarations, even for a hand-built palette", () => {
    const hostile = {
      name: "midnight",
      label: "x",
      light: {
        brand: "#fff",
        "background-image": "url(https://evil.example)",
        link: "var(--x)",
      },
      dark: {},
      source: "user",
    } as unknown as CustomPalette;
    const css = renderPaletteCSS(hostile);
    for (const declaration of css.matchAll(/--([a-z0-9-]+):([^;}]+)/g)) {
      expect(PALETTE_TOKENS).toContain(declaration[1]);
      expect(isSafeCssColor(declaration[2])).toBe(true);
    }
    expect(css).not.toContain("url(");
    expect(css).not.toContain("var(");
    // Nothing in the output can close the style element or a block early.
    expect(css.match(/\}/g)?.length).toBe(2);
    expect(css).not.toContain("<");
  });

  it("renders nothing for a name outside the grammar", () => {
    expect(PALETTE_NAME.test("bad name")).toBe(false);
    expect(renderPaletteCSS({ ...palette, name: 'x"]{}' })).toBe("");
  });
});
