// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { highlightCode, langForClassName } from "./code-highlight";

describe("langForClassName", () => {
  it("maps a markdown fence's language class to a Shiki language id", () => {
    expect(langForClassName("language-ts")).toBe("ts");
    expect(langForClassName("language-tsx")).toBe("tsx");
    expect(langForClassName("language-python")).toBe("python");
    expect(langForClassName("language-bash")).toBe("bash");
  });

  it("falls back to plain text for a missing or unbundled language", () => {
    expect(langForClassName(undefined)).toBe("text");
    expect(langForClassName("")).toBe("text");
    expect(langForClassName("not-a-language-class")).toBe("text");
    expect(langForClassName("language-not-a-real-language")).toBe("text");
  });
});

describe("highlightCode", () => {
  function textOf(lines: readonly (readonly { content: string }[])[]): string {
    return lines.map((line) => line.map((token) => token.content).join("")).join("\n");
  }

  it("splits into one token line per source line and preserves the text", async () => {
    const source = 'const answer = 42; // useful\nreturn "yes";';
    const lines = await highlightCode(source, "typescript");
    expect(lines.length).toBe(2);
    expect(textOf(lines)).toBe(source);
  });

  it("assigns real colours to a known language", async () => {
    const lines = await highlightCode("const answer = 42;", "typescript");
    const colours = lines.flat().map((token) => token.light);
    // At least one token is coloured rather than inheriting the foreground.
    expect(colours.some((colour) => colour !== "inherit" && colour.startsWith("#"))).toBe(true);
  });

  it("returns plain tokens for an unknown language without throwing", async () => {
    const source = "just some words";
    const lines = await highlightCode(source, "text");
    expect(textOf(lines)).toBe(source);
  });
});
