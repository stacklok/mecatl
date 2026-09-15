import { describe, expect, it } from "vitest";
import { formatToolInput } from "./tool-call-panel";

/**
 * The drill-down panel shows the call's FULL input, pretty-printed. Inputs
 * arrive as decoded objects (the usual case) or as raw strings; either way
 * the formatter must never throw — the panel renders whatever it gets.
 */
describe("formatToolInput", () => {
  it("pretty-prints a decoded args object", () => {
    expect(formatToolInput({ path: "/a", limit: 2 })).toBe(
      '{\n  "path": "/a",\n  "limit": 2\n}',
    );
  });

  it("re-indents a string that is itself JSON", () => {
    expect(formatToolInput('{"a":1}')).toBe('{\n  "a": 1\n}');
  });

  it("passes a non-JSON string through verbatim", () => {
    expect(formatToolInput("ls -la")).toBe("ls -la");
  });

  it("renders nothing for an absent input", () => {
    expect(formatToolInput(undefined)).toBe("");
    expect(formatToolInput(null)).toBe("");
  });

  it("degrades an unserializable input instead of throwing", () => {
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    expect(formatToolInput(cyclic)).toBe("[object Object]");
  });
});
