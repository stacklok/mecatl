import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  applyPaletteAttribute,
  BUILT_IN_PALETTES,
  buildPaletteBootScript,
  CUSTOM_PALETTE_ID,
  DEFAULT_PALETTE_ID,
  isCustomPaletteId,
  isKnownPalette,
  PALETTE_ATTRIBUTE,
  PALETTE_ID,
  PALETTE_STORAGE_KEY,
  readStoredPalette,
  resolveDefaultPalette,
  resolvePaletteId,
  writeStoredPalette,
} from "./palettes";

/**
 * The catalogue is the "list themes" surface AND the grammar of the stored
 * preference / `BRAND_PALETTE`: ids must be unique, well-formed, and the
 * shipped default must be one of them.
 */
describe("BUILT_IN_PALETTES", () => {
  it("has unique ids that match PALETTE_ID, the default included", () => {
    const ids = BUILT_IN_PALETTES.map((palette) => palette.id);
    expect(new Set(ids).size).toBe(ids.length);
    for (const id of ids) expect(id).toMatch(PALETTE_ID);
    expect(ids).toContain(DEFAULT_PALETTE_ID);
    // mecatui's three built-ins all have a web counterpart.
    expect(ids).toEqual(
      expect.arrayContaining(["default", "aztec", "mono", "solar"]),
    );
  });

  it("labels and describes every palette in plain words", () => {
    for (const palette of BUILT_IN_PALETTES) {
      expect(palette.label.trim()).not.toBe("");
      expect(palette.description.trim()).not.toBe("");
      expect(palette.swatch.trim()).not.toBe("");
      expect(palette.source).toBe("built-in");
    }
  });
});

describe("resolvePaletteId / isKnownPalette", () => {
  it("keeps a known id and falls back for null, unknown or removed ids", () => {
    expect(isKnownPalette("mono")).toBe(true);
    expect(isKnownPalette("midnight")).toBe(false);
    expect(isKnownPalette(null)).toBe(false);
    expect(resolvePaletteId("solar", "default")).toBe("solar");
    expect(resolvePaletteId("midnight", "default")).toBe("default");
    expect(resolvePaletteId(null, "aztec")).toBe("aztec");
    expect(resolvePaletteId(undefined, "aztec")).toBe("aztec");
  });
});

/**
 * `BRAND_PALETTE` is an operator knob read on every render of the root
 * layout: it must never throw, and it folds case like `mecatui --theme Aztec`.
 */
describe("resolveDefaultPalette", () => {
  it("accepts a catalogue id, case-folded and trimmed", () => {
    expect(resolveDefaultPalette("aztec")).toBe("aztec");
    expect(resolveDefaultPalette(" Mono ")).toBe("mono");
    expect(resolveDefaultPalette("SOLAR")).toBe("solar");
    expect(resolveDefaultPalette("default")).toBe("default");
  });

  it("ignores unset, malformed and unknown values", () => {
    expect(resolveDefaultPalette(undefined)).toBe("default");
    expect(resolveDefaultPalette("")).toBe("default");
    expect(resolveDefaultPalette("midnight")).toBe("default");
    expect(resolveDefaultPalette("../etc")).toBe("default");
    expect(resolveDefaultPalette('aztec"; alert(1)')).toBe("default");
    expect(resolveDefaultPalette("x".repeat(40))).toBe("default");
  });
});

describe("applyPaletteAttribute", () => {
  afterEach(() => {
    document.documentElement.removeAttribute(PALETTE_ATTRIBUTE);
  });

  it("sets data-palette for a named palette and removes it for the default", () => {
    applyPaletteAttribute("aztec");
    expect(document.documentElement.getAttribute(PALETTE_ATTRIBUTE)).toBe(
      "aztec",
    );
    applyPaletteAttribute(DEFAULT_PALETTE_ID);
    expect(document.documentElement.hasAttribute(PALETTE_ATTRIBUTE)).toBe(
      false,
    );
  });

  it("targets the element it is given", () => {
    const el = document.createElement("div");
    applyPaletteAttribute("mono", el);
    expect(el.getAttribute("data-palette")).toBe("mono");
    expect(document.documentElement.hasAttribute(PALETTE_ATTRIBUTE)).toBe(
      false,
    );
  });
});

describe("stored palette", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });

  it("round-trips through localStorage and removes the key on null", () => {
    expect(readStoredPalette()).toBeNull();
    writeStoredPalette("solar");
    expect(window.localStorage.getItem(PALETTE_STORAGE_KEY)).toBe("solar");
    expect(readStoredPalette()).toBe("solar");
    writeStoredPalette(null);
    expect(window.localStorage.getItem(PALETTE_STORAGE_KEY)).toBeNull();
  });
});

/**
 * The boot script is what the browser runs before first paint. It is a
 * constant program over JSON-encoded catalogue ids: pin its shape, then RUN
 * it against jsdom to prove it agrees with resolvePaletteId for every case.
 */
describe("buildPaletteBootScript", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
  });
  afterEach(() => {
    document.documentElement.removeAttribute(PALETTE_ATTRIBUTE);
  });

  function run(defaultPalette: string) {
    // The layout inlines exactly this string; executing it here is the
    // closest offline stand-in for the browser's parse-time run.
    new Function(buildPaletteBootScript(defaultPalette))();
    return document.documentElement.getAttribute(PALETTE_ATTRIBUTE);
  }

  it("names the storage key, the attribute and every catalogue id", () => {
    const script = buildPaletteBootScript("aztec");
    expect(script).toContain(JSON.stringify(PALETTE_STORAGE_KEY));
    expect(script).toContain(JSON.stringify(PALETTE_ATTRIBUTE));
    expect(script).toContain('"aztec"');
    for (const palette of BUILT_IN_PALETTES) {
      expect(script).toContain(JSON.stringify(palette.id));
    }
  });

  it("only ever interpolates a catalogue id as the default", () => {
    // An operator value that slipped past resolveDefaultPalette still cannot
    // reach the script verbatim: the builder re-resolves against the catalogue.
    const script = buildPaletteBootScript('x");alert(1);("');
    expect(script).not.toContain("alert");
    expect(script).toContain('f="default"');
  });

  it("applies the stored palette before the deployment default", () => {
    window.localStorage.setItem(PALETTE_STORAGE_KEY, "solar");
    expect(run("aztec")).toBe("solar");
  });

  it("applies the deployment default when nothing is stored", () => {
    expect(run("mono")).toBe("mono");
  });

  it("removes the attribute for the shipped default", () => {
    document.documentElement.setAttribute(PALETTE_ATTRIBUTE, "stale");
    expect(run("default")).toBeNull();
  });

  it("treats an unknown stored id as the default, like resolvePaletteId", () => {
    window.localStorage.setItem(PALETTE_STORAGE_KEY, "midnight");
    expect(run("aztec")).toBe("aztec");
    window.localStorage.setItem(PALETTE_STORAGE_KEY, "midnight");
    expect(run("default")).toBeNull();
  });

  it("survives a throwing localStorage", () => {
    vi.stubGlobal("localStorage", {
      getItem() {
        throw new Error("blocked");
      },
    });
    expect(() => run("mono")).not.toThrow();
    expect(document.documentElement.getAttribute(PALETTE_ATTRIBUTE)).toBe(
      "mono",
    );
  });

  // A custom palette's CSS arrives with hydration, but the ATTRIBUTE can and
  // should land before first paint: otherwise a browser pinned to `aztec`
  // would flash aztec before its own palette. The grammar is checked
  // in-script; a malformed custom id gets the fallback like any unknown id.
  it("keeps a well-formed stored custom id and rejects a malformed one", () => {
    window.localStorage.setItem(PALETTE_STORAGE_KEY, "custom:midnight");
    expect(run("aztec")).toBe("custom:midnight");
    window.localStorage.setItem(PALETTE_STORAGE_KEY, "custom:Bad Name");
    expect(run("aztec")).toBe("aztec");
    window.localStorage.setItem(PALETTE_STORAGE_KEY, 'custom:x"]{}');
    expect(run("default")).toBeNull();
  });
});

describe("isCustomPaletteId", () => {
  it("matches only custom:<palette-name>", () => {
    expect(isCustomPaletteId("custom:midnight")).toBe(true);
    expect(isCustomPaletteId("custom:a1-b2")).toBe(true);
    expect(isCustomPaletteId("midnight")).toBe(false);
    expect(isCustomPaletteId("custom:")).toBe(false);
    expect(isCustomPaletteId("custom:Midnight")).toBe(false);
    expect(isCustomPaletteId("custom:1abc")).toBe(false);
    expect(isCustomPaletteId(`custom:${"a".repeat(33)}`)).toBe(false);
    expect(isCustomPaletteId(null)).toBe(false);
    expect(isCustomPaletteId(undefined)).toBe(false);
    // The two grammars agree on the name half.
    expect(CUSTOM_PALETTE_ID.source).toBe(
      `^custom:${PALETTE_ID.source.slice(1)}`,
    );
  });
});
