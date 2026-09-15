import { describe, expect, it } from "vitest";
import { highlightCode, langForExtension } from "./code-highlighter";

describe("langForExtension", () => {
  it("maps known extensions to Shiki language ids", () => {
    expect(langForExtension("ts")).toBe("typescript");
    expect(langForExtension("tsx")).toBe("tsx");
    expect(langForExtension("py")).toBe("python");
    expect(langForExtension("sh")).toBe("bash");
    expect(langForExtension("rs")).toBe("rust");
  });

  it("falls back to plain text for unknown extensions", () => {
    expect(langForExtension("")).toBe("text");
    expect(langForExtension("xyz")).toBe("text");
  });
});

describe("highlightCode", () => {
  function textOf(lines: readonly (readonly { content: string }[])[]): string {
    return lines.map((line) => line.map((t) => t.content).join("")).join("\n");
  }

  it("splits into one token line per source line and preserves the text", async () => {
    const code = "const x = 1;\nconst y = 2;";
    const lines = await highlightCode(code, "typescript");
    expect(lines.length).toBe(2);
    expect(textOf(lines)).toBe(code);
  });

  it("assigns real colours to a known language", async () => {
    const lines = await highlightCode("const x = 1;", "typescript");
    const colours = lines.flat().map((t) => t.light);
    // At least one token is coloured rather than inheriting the foreground.
    expect(colours.some((c) => c !== "inherit" && c.startsWith("#"))).toBe(
      true,
    );
  });

  it("returns plain tokens for an unknown language without throwing", async () => {
    const code = "just some words";
    const lines = await highlightCode(code, "text");
    expect(textOf(lines)).toBe(code);
  });
});
