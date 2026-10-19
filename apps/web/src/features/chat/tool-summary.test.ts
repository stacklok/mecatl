// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { clampChars, friendlyToolName, summarizeArgs, summarizeResult } from "./tool-summary";

describe("clampChars", () => {
  it("passes short text through unchanged", () => {
    expect(clampChars("abc", 10)).toBe("abc");
  });

  it("clamps with a trailing ellipsis", () => {
    expect(clampChars("abcdef", 4)).toBe("abc…");
  });

  it("counts code points, so a text of astral characters is not cut early", () => {
    const text = "😀".repeat(10); // 20 UTF-16 units, 10 code points
    expect(clampChars(text, 10)).toBe(text);
    expect(clampChars(`${text}x`, 10)).toBe(`${"😀".repeat(9)}…`);
  });

  it("clamps a huge text without splitting a pair at the cut", () => {
    const clamped = clampChars("😀".repeat(2_000_000), 5);
    expect(clamped).toBe(`${"😀".repeat(4)}…`);
  });
});

describe("summarizeArgs", () => {
  it("returns an empty string for missing or blank args", () => {
    expect(summarizeArgs(undefined)).toBe("");
    expect(summarizeArgs("")).toBe("");
    expect(summarizeArgs("   ")).toBe("");
  });

  it("joins key: value pairs and rolls up extra keys", () => {
    const args = JSON.stringify({ a: "1", b: "2", c: "3", d: "4", e: "5" });
    expect(summarizeArgs(args)).toBe("a: 1 · b: 2 · c: 3 · d: 4 · +1 more");
  });

  it("renders a body key as a line count instead of inline text", () => {
    const args = JSON.stringify({ path: "x.ts", content: "a\nb\nc" });
    expect(summarizeArgs(args)).toBe("path: x.ts · content: <3 lines>");
  });

  it("flattens and clamps non-JSON args", () => {
    expect(summarizeArgs("not json", { maxValueChars: 5 })).toBe("not …");
  });

  it("flattens and clamps a non-object JSON value", () => {
    expect(summarizeArgs("42")).toBe("42");
  });
});

describe("summarizeResult", () => {
  it("returns an empty string for missing or blank output", () => {
    expect(summarizeResult(undefined)).toBe("");
    expect(summarizeResult("  ")).toBe("");
  });

  it("names an array's length", () => {
    expect(summarizeResult(JSON.stringify([1, 2, 3]))).toBe("3 items");
  });

  it("names an object's keys with a rollup", () => {
    expect(summarizeResult(JSON.stringify({ a: 1, b: 2, c: 3, d: 4 }))).toBe("keys: a, b, c (+1)");
  });

  it("labels an empty object", () => {
    expect(summarizeResult("{}")).toBe("empty object");
  });

  it("flattens and clamps plain text", () => {
    expect(summarizeResult("line one\nline two", { maxChars: 12 })).toBe("line one li…");
  });
});

describe("previews of huge inputs", () => {
  it("reads only a bounded head of a huge plain-text output", () => {
    const output = `${"word ".repeat(2_000_000)}tail`;
    const summary = summarizeResult(output);
    expect(summary.length).toBeLessThanOrEqual(120);
    expect(summary.endsWith("…")).toBe(true);
    expect(summary.startsWith("word word")).toBe(true);
  });

  it("says a cut text was cut even when what was read was mostly whitespace", () => {
    expect(summarizeResult(`x${" ".repeat(10_000)}y`)).toBe("x…");
  });

  it("summarizes a huge JSON output as text instead of parsing it", () => {
    const output = JSON.stringify(Array.from({ length: 200_000 }, (_, index) => ({ index })));
    expect(output.length).toBeGreaterThan(256 * 1024);
    const summary = summarizeResult(output);
    expect(summary).not.toBe("200000 items");
    expect(summary.length).toBeLessThanOrEqual(120);
  });

  it("clamps a huge string argument and a huge non-JSON argument", () => {
    const huge = "x".repeat(5_000_000);
    expect(summarizeArgs(JSON.stringify({ path: huge })).length).toBeLessThanOrEqual(
      "path: ".length + 60,
    );
    expect(summarizeArgs(huge).length).toBeLessThanOrEqual(60);
  });

  it("leaves normal-size outputs exactly as before", () => {
    expect(summarizeResult("  a\n\nb   c ")).toBe("a b c");
    expect(summarizeResult("[1,2,3]")).toBe("3 items");
  });
});

describe("friendlyToolName", () => {
  it("passes a built-in tool name through unchanged", () => {
    expect(friendlyToolName("Edit")).toEqual({
      display: "Edit",
      raw: "Edit",
      mcp: false,
    });
  });

  it("renders the Server · Tool display for an MCP tool", () => {
    expect(friendlyToolName("mcp__github__issue_write")).toEqual({
      display: "GitHub · Issue write",
      raw: "mcp__github__issue_write",
      mcp: true,
      server: "github",
      tool: "issue_write",
    });
  });
});
