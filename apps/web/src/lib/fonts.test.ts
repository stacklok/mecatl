// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");
const pkg = JSON.parse(readFileSync(new URL("../../package.json", import.meta.url), "utf8"));

describe("Studio typography", () => {
  it("loads Inter and Merriweather from same-origin assets", async () => {
    expect(css).not.toMatch(/fonts\.googleapis\.com|fonts\.gstatic\.com/);
    expect(css).toContain('@import "@fontsource-variable/inter/wght.css";');
    for (const weight of [300, 400, 700]) {
      expect(css).toContain(`@import "@fontsource/merriweather/${weight}.css";`);
    }
    expect(pkg.dependencies).toHaveProperty("@fontsource-variable/inter");
    expect(pkg.dependencies).toHaveProperty("@fontsource/merriweather");
    expect(css).toMatch(/--font-sans:\s*"Inter Variable"/);
    expect(css).toMatch(/--font-serif:\s*"Merriweather"/);
    expect(css).toMatch(/body\s*\{[^}]*font-family:\s*var\(--font-sans\)/s);
    expect(css).toMatch(/\.text-page-title\s*\{[^}]*font-family:\s*var\(--font-serif\)/s);
    expect(css).toMatch(/\.text-page-title\s*\{[^}]*font-weight:\s*300/s);

    const { pageTitleClass } = await import("./typography");
    expect(pageTitleClass()).toContain("text-page-title");
    expect(pageTitleClass("text-center")).toContain("text-center");
  });

  it("scales type and standard Lucide glyphs at the 500px pivot", () => {
    expect(css).toMatch(/:root\s*\{[^}]*font-size:\s*calc\(16px \* var\(--ui-scale, 1\)\)/s);
    expect(css).toMatch(/@media\s*\(max-width:\s*499px\)/);
    expect(css).toMatch(/:root\s*\{[^}]*font-size:\s*calc\(18px \* var\(--ui-scale, 1\)\)/s);
    const mobileGlyphs = css.match(/svg\.lucide\.size-4,([^{}]+)\{([^{}]+)\}/);
    expect(mobileGlyphs?.[1]).toContain('[data-slot="button"] svg.lucide:not([class*="size-"])');
    expect(mobileGlyphs?.[1]).toContain('[data-slot="dropdown-menu-item"]');
    expect(mobileGlyphs?.[1]).toContain('[data-slot="dialog-close"]');
    expect(mobileGlyphs?.[2]).toMatch(/width:\s*20px;[^}]*height:\s*20px/s);
    expect(css).toMatch(
      /svg\.lucide\[class~="size-3\.5"\]\s*\{[^}]*width:\s*18px;[^}]*height:\s*18px/s,
    );
  });
});
