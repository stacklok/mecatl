// SPDX-License-Identifier: Apache-2.0

import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const css = readFileSync(new URL("../styles.css", import.meta.url), "utf8");

const roles = [
  "--brand",
  "--brand-ink",
  "--brand-label",
  "--btn-primary",
  "--btn-primary-hover",
  "--link",
  "--info",
  "--shell-gradient-start",
  "--shell-gradient-mid",
  "--shell-gradient-end",
  "--nav-pill-bg",
  "--nav-pill-text",
  "--nav-icon",
  "--nav-search-border",
  "--nav-search-text",
  "--nav-kbd-bg",
] as const;

function declarations(selector: string): Map<string, string> {
  const block = [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find(
    ([, head]) => head?.trim().split("\n").at(-1)?.trim() === selector,
  );
  expect(block, `CSS block ${selector}`).toBeDefined();
  const values = new Map<string, string>();
  for (const [, key, value] of (block?.[2] ?? "").matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)) {
    if (key && value) values.set(key, value.trim());
  }
  return values;
}

// These are the frozen six color sets after the approved contrast corrections.
// The role order is the order above, so a missing or swapped role fails.
const palettes = {
  aztec: {
    light:
      "#0f7f6c|#0f7f6c|#0f7f6c|#0f7f6c|#0b6656|#157a83|#157a83|#1f6b5c|#15302a|#0e1311|#bfe6dc|#0e2a24|#aeb9b2|#aab9b4|#d7deda|#2e4039",
    dark: "#1fb39a|#4fd1b8|#4fd1b8|#12846d|#0e6b59|#5cc8d0|#5cc8d0|#143d33|#0f1f1a|#0a0f0d|#a8ddd0|#0e2a24|#8a998f|#71877f|#a9b8b1|#26362f",
  },
  mono: {
    light:
      "#2563eb|#2563eb|#2563eb|#2563eb|#1d4ed8|#2563eb|#2563eb|#3a3a3a|#232323|#141414|#e4e4e7|#18181b|#a1a1aa|#85858d|#c4c4c8|#3f3f46",
    dark: "#3b82f6|#60a5fa|#60a5fa|#1d4ed8|#1e40af|#60a5fa|#60a5fa|#262626|#161616|#0b0b0b|#d4d4d8|#0b0b0b|#8a8a8a|#727272|#b5b5b5|#2a2a2a",
  },
  solar: {
    light:
      "#9a7300|#8a6800|#8a6800|#946e00|#7f5e00|#cb4b16|#1f6fa8|#775707|#6b4f0a|#3f3008|#f3e3b5|#3f3008|#d9c48a|#c0b188|#e6d8b4|#7a5c0c",
    dark: "#b58900|#e0b52e|#e0b52e|#8a6800|#705400|#f0884d|#5fb0ee|#4a3708|#2c2206|#191404|#e8d59a|#2c2206|#b8a878|#918561|#d6c89a|#3a2c08",
  },
} as const;

describe("Studio palette tokens", () => {
  it("keeps each palette's light and dark token sets paired", () => {
    for (const [palette, modes] of Object.entries(palettes)) {
      const light = declarations(`[data-palette="${palette}"]`);
      const dark = declarations(`.dark[data-palette="${palette}"]`);
      for (const [mode, actual] of [
        ["light", light],
        ["dark", dark],
      ] as const) {
        const expected = modes[mode].split("|");
        expect([...actual.keys()], `${palette} ${mode} role set`).toEqual(roles);
        expect(
          roles.map((role) => actual.get(role)),
          `${palette} ${mode} values`,
        ).toEqual(expected);
        expect(actual.has("--shiki-light")).toBe(false);
        expect(actual.has("--shiki-dark")).toBe(false);
      }
    }

    const light = declarations(":root");
    const dark = declarations(".dark");
    for (const [role, expectedLight, expectedDark] of [
      ["--nav-search-border", "#a5b0ae", "#728481"],
      ["--nav-search-text", "#cbd4d4", "#b4c0c1"],
      ["--ring", "#6b6b75", "#a0a0a0"],
      ["--warning", "hsl(35 92% 34%)", "hsl(35 92% 62%)"],
      ["--destructive-foreground", "hsl(0 0% 98%)", "#18181b"],
      ["--control-border", "#83838a", "#8c8c8c"],
      ["--warning-foreground", "#ffffff", "#18181b"],
      ["--destructive-strong", "hsl(0 74% 40%)", "hsl(358.7 78% 55%)"],
      ["--sidebar", "hsl(240 4.8% 98.5%)", "hsl(240 6.3% 12.5%)"],
      ["--avatar-background", "oklch(0.696 0 0 / 89.8%)", "oklch(0.696 0 0 / 89.8%)"],
      ["--logo", "hsl(0 0% 28%)", "hsl(0 0% 58%)"],
      ["--brand-label", "hsl(161 94% 21%)", "hsl(158 64% 52%)"],
      ["--brand-ink", "hsl(161 94% 21%)", "hsl(142 50% 60%)"],
      ["--link", "hsl(211 92% 43%)", "hsl(211 92% 66%)"],
      ["--input-icon", "hsl(240 3.8% 46.1%)", "hsl(0 0% 100%)"],
    ] as const) {
      expect(light.get(role), `Default light ${role}`).toBe(expectedLight);
      expect(dark.get(role), `Default dark ${role}`).toBe(expectedDark);
    }

    for (const role of [
      ...roles,
      "--destructive-strong",
      "--sidebar",
      "--avatar-background",
      "--logo",
      "--input-icon",
      "--control-border",
      "--warning-foreground",
    ]) {
      expect(declarations("@theme inline").get(`--color-${role.slice(2)}`)).toBe(`var(${role})`);
    }
    for (const role of [
      "--background",
      "--foreground",
      "--card",
      "--popover",
      "--success",
      "--warning",
      "--destructive",
    ]) {
      expect(light.has(role)).toBe(true);
      expect(dark.has(role)).toBe(true);
    }
  });

  it("suppresses transitions only while the root appearance changes", () => {
    expect(css).toMatch(
      /html\.appearance-changing\s*,\s*html\.appearance-changing \*\s*,\s*html\.appearance-changing \*::before\s*,\s*html\.appearance-changing \*::after\s*\{[^}]*transition-property:\s*none !important;/,
    );
  });
});
