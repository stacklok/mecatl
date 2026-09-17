import { describe, expect, it } from "vitest";
import { formatToolInput, toolPanelInput } from "./tool-call-panel";

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

/**
 * During a live run the reducer stores the FLATTENED `key: value · …`
 * preview as `input`; the verbatim JSON rides `rawArgs`, so the drill-down
 * must prefer it — otherwise the maximized view shows the one-liner.
 */
describe("toolPanelInput", () => {
  it("prefers the raw args JSON over the flattened preview", () => {
    expect(
      toolPanelInput({
        input: "command: ls · timeout_ms: 5",
        rawArgs: '{"command":"ls","timeout_ms":5}',
      }),
    ).toBe('{\n  "command": "ls",\n  "timeout_ms": 5\n}');
  });

  it("falls back to input when no raw args were carried (hydrated history)", () => {
    expect(toolPanelInput({ input: '{"path":"/a"}' })).toBe(
      '{\n  "path": "/a"\n}',
    );
    expect(toolPanelInput({ input: "ls -la", rawArgs: "" })).toBe("ls -la");
  });
});
