import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import {
  CUSTOM_PALETTES_KEY,
  customPaletteDef,
  loadOperatorPalettes,
  mergePaletteCatalogue,
  OPERATOR_PALETTES_URL,
  paletteSwatchColor,
  readUserPalettes,
  resetCustomPalettesForTests,
  USER_PALETTE_LIMIT,
  useCustomPaletteCatalogue,
  useCustomPalettes,
  writeUserPalettes,
} from "./custom-palettes";
import {
  type CustomPalette,
  type PaletteDocument,
  toPaletteDocument,
} from "./palette-schema";
import { BUILT_IN_PALETTES } from "./palettes";

function palette(
  name: string,
  source: CustomPalette["source"] = "user",
  light: CustomPalette["light"] = { brand: "#123456" },
  dark: CustomPalette["dark"] = {},
): CustomPalette {
  return { name, label: name.toUpperCase(), light, dark, source };
}

function doc(name: string, brand = "#123456"): string {
  return JSON.stringify({ name, palette: { brand } });
}

function stubFetch(body: unknown, status = 200) {
  const fetchMock = vi.fn(
    async () =>
      new Response(JSON.stringify(body), {
        status,
        headers: { "content-type": "application/json" },
      }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
  resetCustomPalettesForTests();
  stubFetch({ palettes: [] });
});
afterEach(() => {
  resetCustomPalettesForTests();
});

/**
 * The user's palettes live in ONE localStorage key as documents; every entry
 * is re-validated on read so a hand-edited or stale entry can never reach the
 * stylesheet, later duplicates win, and the list is capped.
 */
describe("user palette storage", () => {
  it("round-trips through localStorage as documents", () => {
    const ember = palette(
      "ember",
      "user",
      { brand: "#f60" },
      { brand: "#f96" },
    );
    writeUserPalettes([ember, palette("sea")]);
    const stored = JSON.parse(
      window.localStorage.getItem(CUSTOM_PALETTES_KEY) ?? "null",
    ) as PaletteDocument[];
    expect(stored).toEqual([
      toPaletteDocument(ember),
      toPaletteDocument(palette("sea")),
    ]);
    expect(readUserPalettes()).toEqual([ember, palette("sea")]);
  });

  it("removes the key when the list is empty", () => {
    writeUserPalettes([palette("ember")]);
    writeUserPalettes([]);
    expect(window.localStorage.getItem(CUSTOM_PALETTES_KEY)).toBeNull();
    expect(readUserPalettes()).toEqual([]);
  });

  it("drops invalid stored entries on read and keeps the valid ones", () => {
    window.localStorage.setItem(
      CUSTOM_PALETTES_KEY,
      JSON.stringify([
        { name: "good", palette: { brand: "#fff" } },
        { name: "Bad Name", palette: { brand: "#fff" } },
        { name: "hostile", palette: { brand: "url(https://evil.example)" } },
        { name: "typo", palette: { bogus: "#fff" } },
        "junk",
        42,
        null,
      ]),
    );
    expect(readUserPalettes().map((p) => p.name)).toEqual(["good"]);
  });

  it("treats garbage or a non-array as no palettes", () => {
    window.localStorage.setItem(CUSTOM_PALETTES_KEY, "{not json");
    expect(readUserPalettes()).toEqual([]);
    window.localStorage.setItem(CUSTOM_PALETTES_KEY, JSON.stringify({ a: 1 }));
    expect(readUserPalettes()).toEqual([]);
  });

  it("keeps the last entry for a repeated name and caps at the limit", () => {
    const docs = [];
    for (let i = 0; i < USER_PALETTE_LIMIT + 3; i += 1) {
      docs.push({ name: `p${i}`, palette: { brand: "#fff" } });
    }
    docs.push({ name: "p0", label: "Later", palette: { brand: "#000" } });
    window.localStorage.setItem(CUSTOM_PALETTES_KEY, JSON.stringify(docs));
    const read = readUserPalettes();
    expect(read.length).toBe(USER_PALETTE_LIMIT);
    // p0 was re-set last, so it survives with the later value…
    expect(read.find((p) => p.name === "p0")?.label).toBe("Later");
    // …and the earliest un-repeated names are the ones dropped.
    expect(read.some((p) => p.name === "p1")).toBe(false);
  });

  it("survives a throwing localStorage", () => {
    vi.stubGlobal("localStorage", {
      getItem() {
        throw new Error("blocked");
      },
      setItem() {
        throw new Error("blocked");
      },
      removeItem() {
        throw new Error("blocked");
      },
    });
    expect(readUserPalettes()).toEqual([]);
    expect(() => writeUserPalettes([palette("x")])).not.toThrow();
  });
});

describe("mergePaletteCatalogue", () => {
  it("lists built-ins first, then operator, then user palettes as custom ids", () => {
    const merged = mergePaletteCatalogue(
      BUILT_IN_PALETTES,
      [palette("ops", "operator")],
      [palette("mine")],
    );
    expect(merged.map((p) => p.id)).toEqual([
      ...BUILT_IN_PALETTES.map((p) => p.id),
      "custom:ops",
      "custom:mine",
    ]);
    expect(merged.at(-2)?.source).toBe("operator");
    expect(merged.at(-1)?.source).toBe("user");
  });

  it("lets a user palette shadow an operator one of the same name, in place", () => {
    const merged = mergePaletteCatalogue(
      BUILT_IN_PALETTES,
      [
        palette("dusk", "operator", { brand: "#111" }),
        palette("ops", "operator"),
      ],
      [palette("dusk", "user", { brand: "#222" })],
    );
    const ids = merged.map((p) => p.id);
    expect(ids.filter((id) => id === "custom:dusk")).toHaveLength(1);
    expect(ids.indexOf("custom:dusk")).toBeLessThan(ids.indexOf("custom:ops"));
    const dusk = merged.find((p) => p.id === "custom:dusk");
    expect(dusk?.source).toBe("user");
    expect(dusk?.swatch).toBe("#222");
  });

  it("never shadows a built-in (the custom: prefix keeps the ids apart)", () => {
    const merged = mergePaletteCatalogue(
      BUILT_IN_PALETTES,
      [],
      [palette("aztec")],
    );
    expect(merged.filter((p) => p.id === "aztec")).toHaveLength(1);
    expect(merged.find((p) => p.id === "aztec")?.source).toBe("built-in");
    expect(merged.some((p) => p.id === "custom:aztec")).toBe(true);
  });

  it("describes a custom palette by its source and swatches its accent", () => {
    expect(customPaletteDef(palette("mine")).description).toMatch(
      /this browser/,
    );
    expect(customPaletteDef(palette("ops", "operator")).description).toMatch(
      /operator/,
    );
    expect(paletteSwatchColor(palette("a", "user", { brand: "#111" }))).toBe(
      "#111",
    );
    expect(
      paletteSwatchColor(palette("b", "user", { "btn-primary": "#222" })),
    ).toBe("#222");
    expect(
      paletteSwatchColor(palette("c", "user", { "nav-background": "#333" })),
    ).toBe("#333");
    expect(paletteSwatchColor(palette("d", "user", { link: "#444" }))).toBe(
      "#444",
    );
  });
});

/**
 * The hook is the one store every consumer shares: add/remove write the key
 * and re-render every mounted instance; the operator list is fetched once
 * per page and merged under the user's.
 */
describe("useCustomPalettes", () => {
  async function settled() {
    await act(async () => {
      await loadOperatorPalettes();
    });
  }

  it("adds a valid document, stores it and lists it in the catalogue", async () => {
    const { result } = renderHook(() => useCustomPalettes());
    await settled();
    let outcome: ReturnType<typeof result.current.addUserPalette> | undefined;
    act(() => {
      outcome = result.current.addUserPalette(doc("ember", "#f60"));
    });
    expect(outcome?.ok).toBe(true);
    expect(result.current.user.map((p) => p.name)).toEqual(["ember"]);
    expect(result.current.catalogue.map((p) => p.id)).toContain("custom:ember");
    expect(window.localStorage.getItem(CUSTOM_PALETTES_KEY)).toContain(
      '"ember"',
    );
  });

  it("returns the validator's error for a bad document and stores nothing", async () => {
    const { result } = renderHook(() => useCustomPalettes());
    await settled();
    let outcome: ReturnType<typeof result.current.addUserPalette> | undefined;
    act(() => {
      outcome = result.current.addUserPalette(
        '{"name":"x","palette":{"brand":"var(--x)"}}',
      );
    });
    expect(outcome).toEqual({
      ok: false,
      error: expect.stringContaining("not a supported colour"),
    });
    expect(result.current.user).toEqual([]);
    expect(window.localStorage.getItem(CUSTOM_PALETTES_KEY)).toBeNull();
  });

  it("refuses a 9th palette but lets a same-name document replace one", async () => {
    const { result } = renderHook(() => useCustomPalettes());
    await settled();
    act(() => {
      for (let i = 0; i < USER_PALETTE_LIMIT; i += 1) {
        result.current.addUserPalette(doc(`p${i}`));
      }
    });
    expect(result.current.user).toHaveLength(USER_PALETTE_LIMIT);
    let outcome: ReturnType<typeof result.current.addUserPalette> | undefined;
    act(() => {
      outcome = result.current.addUserPalette(doc("one-too-many"));
    });
    expect(outcome).toEqual({
      ok: false,
      error: expect.stringContaining(`up to ${USER_PALETTE_LIMIT}`),
    });
    act(() => {
      outcome = result.current.addUserPalette(doc("p3", "#abcdef"));
    });
    expect(outcome?.ok).toBe(true);
    expect(result.current.user).toHaveLength(USER_PALETTE_LIMIT);
    expect(result.current.user.find((p) => p.name === "p3")?.light.brand).toBe(
      "#abcdef",
    );
  });

  it("removes a palette and clears the key when none is left", async () => {
    const { result } = renderHook(() => useCustomPalettes());
    await settled();
    act(() => {
      result.current.addUserPalette(doc("ember"));
    });
    act(() => result.current.removeUserPalette("ember"));
    expect(result.current.user).toEqual([]);
    expect(result.current.catalogue.map((p) => p.id)).not.toContain(
      "custom:ember",
    );
    expect(window.localStorage.getItem(CUSTOM_PALETTES_KEY)).toBeNull();
  });

  it("keeps two mounted instances in sync (picker + style injector)", async () => {
    const a = renderHook(() => useCustomPalettes());
    const b = renderHook(() => useCustomPalettes());
    await settled();
    act(() => {
      a.result.current.addUserPalette(doc("ember"));
    });
    expect(b.result.current.user.map((p) => p.name)).toEqual(["ember"]);
  });

  it("fetches the operator palettes once per page and merges them under the user's", async () => {
    const fetchMock = stubFetch({
      palettes: [
        { name: "midnight", label: "Midnight", palette: { brand: "#4f7cff" } },
        // Something a tampered response might carry: dropped, never rendered.
        { name: "hostile", palette: { brand: "url(https://evil.example)" } },
        "junk",
      ],
    });
    const a = renderHook(() => useCustomPalettes());
    const b = renderHook(() => useCustomPalettes());
    expect(a.result.current.operatorState).toBe("loading");
    await settled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(OPERATOR_PALETTES_URL, {
      cache: "no-store",
    });
    expect(a.result.current.operatorState).toBe("ready");
    expect(a.result.current.operator.map((p) => p.name)).toEqual(["midnight"]);
    expect(a.result.current.operator[0]?.source).toBe("operator");
    expect(b.result.current.catalogue.map((p) => p.id)).toContain(
      "custom:midnight",
    );
    // User wins over operator by name.
    act(() => {
      a.result.current.addUserPalette(doc("midnight", "#000000"));
    });
    const midnight = b.result.current.catalogue.filter(
      (p) => p.id === "custom:midnight",
    );
    expect(midnight).toHaveLength(1);
    expect(midnight[0]?.source).toBe("user");
  });

  it("reports an HTTP failure in plain words and leaves the operator list empty", async () => {
    stubFetch({ error: "nope" }, 500);
    const { result } = renderHook(() => useCustomPalettes());
    await settled();
    expect(result.current.operatorState).toBe("error");
    expect(result.current.operator).toEqual([]);
    expect(result.current.operatorError).toBe(
      "Operator palettes could not be loaded (HTTP 500).",
    );
  });

  it("reports a network failure the same way", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    const { result } = renderHook(() => useCustomPalettes());
    await settled();
    expect(result.current.operatorState).toBe("error");
    expect(result.current.operatorError).toBe(
      "Operator palettes could not be loaded (Failed to fetch).",
    );
  });

  it("follows a change made in another tab", async () => {
    const { result } = renderHook(() => useCustomPalettes());
    await settled();
    window.localStorage.setItem(
      CUSTOM_PALETTES_KEY,
      JSON.stringify([{ name: "elsewhere", palette: { brand: "#fff" } }]),
    );
    act(() => {
      window.dispatchEvent(
        new StorageEvent("storage", { key: CUSTOM_PALETTES_KEY }),
      );
    });
    expect(result.current.user.map((p) => p.name)).toEqual(["elsewhere"]);
  });
});

describe("useCustomPaletteCatalogue", () => {
  it("reads the shared snapshot without starting the operator fetch", () => {
    const fetchMock = stubFetch({ palettes: [] });
    const { result } = renderHook(() => useCustomPaletteCatalogue());
    expect(result.current.operatorState).toBe("idle");
    expect(result.current.catalogue).toEqual(BUILT_IN_PALETTES);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});
