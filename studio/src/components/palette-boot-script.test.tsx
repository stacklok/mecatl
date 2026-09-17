import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { PALETTE_ATTRIBUTE, PALETTE_STORAGE_KEY } from "@/lib/palettes";
import { PaletteBootScript } from "./palette-boot-script";

/**
 * The root layout inlines this in <head>. Server-render it the way Next does
 * and pin that the shipped HTML carries the marker the hermetic suite looks
 * for, the storage key it reads, and the validated default it falls back to.
 */
describe("PaletteBootScript", () => {
  it("renders one blocking inline script with the palette marker", () => {
    const html = renderToStaticMarkup(
      <PaletteBootScript defaultPalette="aztec" />,
    );
    expect(html).toMatch(/^<script>/);
    expect(html).not.toContain("async");
    expect(html).not.toContain("defer");
    expect(html).toContain(PALETTE_ATTRIBUTE);
    expect(html).toContain(PALETTE_STORAGE_KEY);
    expect(html).toContain('f="aztec"');
  });

  it("falls back to the shipped default for a value the layout did not validate", () => {
    const html = renderToStaticMarkup(
      <PaletteBootScript defaultPalette="not-a-palette" />,
    );
    expect(html).toContain('f="default"');
    expect(html).not.toContain("not-a-palette");
  });
});
