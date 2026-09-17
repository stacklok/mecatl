import { act, render, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  loadOperatorPalettes,
  resetCustomPalettesForTests,
  useCustomPalettes,
} from "@/lib/custom-palettes";
import { memoryStorage } from "@/test/memory-storage";
import {
  CUSTOM_PALETTE_STYLE_ID,
  CustomPaletteStyles,
} from "./custom-palette-styles";

function stubFetch(body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(
      async () =>
        new Response(JSON.stringify(body), {
          headers: { "content-type": "application/json" },
        }),
    ),
  );
}

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
  resetCustomPalettesForTests();
  stubFetch({ palettes: [] });
});
afterEach(() => resetCustomPalettesForTests());

/**
 * The one <style> element that carries every custom palette's CSS: nothing
 * while there is none, one block pair per palette (operator + user) once
 * there is — and only allowlist-built declarations, since the element's
 * content is set raw (React would HTML-escape a text child).
 */
describe("CustomPaletteStyles", () => {
  it("renders no element while there are no custom palettes", async () => {
    const { container } = render(<CustomPaletteStyles />);
    await act(async () => {
      await loadOperatorPalettes();
    });
    expect(container.querySelector("style")).toBeNull();
  });

  it("renders the operator and user palettes' CSS in one element", async () => {
    stubFetch({
      palettes: [
        {
          name: "midnight",
          palette: { brand: "#4f7cff" },
          dark: { brand: "#7c9cff" },
        },
      ],
    });
    const { container } = render(<CustomPaletteStyles />);
    const hook = renderHook(() => useCustomPalettes());
    await act(async () => {
      await loadOperatorPalettes();
    });
    act(() => {
      hook.result.current.addUserPalette(
        '{"name":"ember","palette":{"brand":"#ff6600"}}',
      );
    });
    const style = container.querySelector(`style#${CUSTOM_PALETTE_STYLE_ID}`);
    expect(style).not.toBeNull();
    expect(style?.textContent).toBe(
      [
        '[data-palette="custom:midnight"]{--brand:#4f7cff}',
        '.dark[data-palette="custom:midnight"]{--brand:#7c9cff}',
        '[data-palette="custom:ember"]{--brand:#ff6600}',
        '.dark[data-palette="custom:ember"]{--brand:#ff6600}',
      ].join("\n"),
    );

    act(() => hook.result.current.removeUserPalette("ember"));
    expect(
      container.querySelector(`style#${CUSTOM_PALETTE_STYLE_ID}`)?.textContent,
    ).not.toContain("custom:ember");
  });
});
