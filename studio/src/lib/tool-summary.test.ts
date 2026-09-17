import { describe, expect, it } from "vitest";
import type { AgentMessage, ToolCallInfo } from "@/features/agent/types";
import {
  changedFileFromToolCall,
  changedFilesFromMessages,
  clampChars,
  friendlyToolName,
  resourceLinkLabel,
  safeImageDataUrl,
  safeResourceLinkHref,
  summarizeArgs,
  summarizeResult,
  toolRawArgs,
} from "./tool-summary";

/**
 * The compact tier of a tool card: the args summary clamps every value,
 * rolls hidden keys up, and never inlines a whole file body; the result
 * summary names a JSON payload's shape instead of dumping it.
 */
describe("summarizeArgs", () => {
  it("renders key: value pairs joined by a middle dot", () => {
    expect(summarizeArgs('{"command":"ls -la","timeout_ms":5000}')).toBe(
      "command: ls -la · timeout_ms: 5000",
    );
  });

  it("clamps a long string value with an ellipsis", () => {
    const long = "x".repeat(80);
    expect(summarizeArgs(JSON.stringify({ q: long }))).toBe(
      `q: ${"x".repeat(59)}…`,
    );
    expect(
      summarizeArgs(JSON.stringify({ q: long }), { maxValueChars: 10 }),
    ).toBe(`q: ${"x".repeat(9)}…`);
  });

  it("flattens line breaks inside a value onto one line", () => {
    expect(summarizeArgs(JSON.stringify({ text: "a\n  b\n\nc" }))).toBe(
      "text: a b c",
    );
  });

  it("caps the keys shown and reports the rest as +N more", () => {
    expect(
      summarizeArgs(JSON.stringify({ a: 1, b: 2, c: 3, d: 4, e: 5, f: 6 })),
    ).toBe("a: 1 · b: 2 · c: 3 · d: 4 · +2 more");
    expect(summarizeArgs(JSON.stringify({ a: 1, b: 2 }), { maxKeys: 1 })).toBe(
      "a: 1 · +1 more",
    );
  });

  it("renders file bodies as a line count, never inline", () => {
    expect(
      summarizeArgs(
        JSON.stringify({ path: "a.ts", content: "one\ntwo\nthree" }),
      ),
    ).toBe("path: a.ts · content: <3 lines>");
    expect(
      summarizeArgs(
        JSON.stringify({ path: "a.ts", old_string: "x", new_string: "" }),
      ),
    ).toBe("path: a.ts · old_string: <1 line> · new_string: <0 lines>");
  });

  it("renders nested values as compact JSON", () => {
    expect(summarizeArgs(JSON.stringify({ tags: ["a", "b"], on: true }))).toBe(
      'tags: ["a","b"] · on: true',
    );
  });

  it("falls back to a flattened clamp for non-object or non-JSON args", () => {
    expect(summarizeArgs("not json\nat all")).toBe("not json at all");
    expect(summarizeArgs("[1,2,3]")).toBe("[1,2,3]");
    expect(summarizeArgs("")).toBe("");
    expect(summarizeArgs(undefined)).toBe("");
  });
});

describe("summarizeResult", () => {
  it("names a JSON object's keys with a rollup", () => {
    expect(summarizeResult('{"a":1,"b":2,"c":3,"d":4,"e":5}')).toBe(
      "keys: a, b, c (+2)",
    );
    expect(summarizeResult('{"a":1}')).toBe("keys: a");
    expect(summarizeResult("{}")).toBe("empty object");
  });

  it("counts a JSON array's items", () => {
    expect(summarizeResult("[1, 2, 3]")).toBe("3 items");
    expect(summarizeResult("[1]")).toBe("1 item");
  });

  it("flattens and clamps plain text", () => {
    expect(summarizeResult("line one\nline two")).toBe("line one line two");
    const long = "y".repeat(200);
    expect(summarizeResult(long)).toBe(`${"y".repeat(119)}…`);
    expect(summarizeResult(long, { maxChars: 20 })).toBe(`${"y".repeat(19)}…`);
  });

  it("treats text that merely starts with a brace as text", () => {
    expect(summarizeResult("{not json")).toBe("{not json");
  });

  it("renders nothing for an empty output", () => {
    expect(summarizeResult(undefined)).toBe("");
    expect(summarizeResult("  \n ")).toBe("");
  });
});

describe("friendlyToolName", () => {
  it("titles an MCP tool Server · Tool (the TUI's humanized head) and keeps the raw name", () => {
    expect(friendlyToolName("mcp__github__list_issues")).toEqual({
      display: "GitHub · List issues",
      raw: "mcp__github__list_issues",
      mcp: true,
      server: "github",
      tool: "list_issues",
    });
  });

  it("splits on the first double underscore and keeps the rest as the tool", () => {
    const head = friendlyToolName("mcp__fetch__fetch_url__v2");
    expect(head.server).toBe("fetch");
    expect(head.tool).toBe("fetch_url__v2");
    expect(head.display.startsWith("Fetch · Fetch url")).toBe(true);
  });

  it("passes a plain tool name through", () => {
    expect(friendlyToolName("Read")).toEqual({
      display: "Read",
      raw: "Read",
      mcp: false,
    });
  });
});

describe("changedFileFromToolCall", () => {
  it("returns the path for Edit and Write", () => {
    expect(
      changedFileFromToolCall(
        "Edit",
        JSON.stringify({ path: "a.ts", old_string: "x", new_string: "y" }),
      ),
    ).toBe("a.ts");
    expect(
      changedFileFromToolCall(
        "Write",
        JSON.stringify({ file_path: "b.ts", content: "" }),
      ),
    ).toBe("b.ts");
  });

  it("returns undefined for a tool that changes nothing, or unreadable args", () => {
    expect(
      changedFileFromToolCall("Read", JSON.stringify({ path: "a.ts" })),
    ).toBeUndefined();
    expect(changedFileFromToolCall("Edit", undefined)).toBeUndefined();
    expect(changedFileFromToolCall("Edit", "not json")).toBeUndefined();
    expect(changedFileFromToolCall("Edit", '{"path":""}')).toBeUndefined();
  });
});

describe("changedFilesFromMessages", () => {
  const call = (
    name: string,
    changedPath: string,
    extra: Partial<ToolCallInfo> = {},
  ): ToolCallInfo => ({
    callId: `${name}-${changedPath}-${Math.random()}`,
    name,
    input: "",
    changedPath,
    status: "completed",
    ...extra,
  });
  const turn = (
    ...toolCalls: ToolCallInfo[]
  ): Pick<AgentMessage, "toolCalls"> => ({
    toolCalls,
  });

  it("keeps first-seen order across the whole transcript and counts edits and writes", () => {
    const files = changedFilesFromMessages([
      turn(call("Edit", "b.ts"), call("Write", "a.ts")),
      // A Read never gets a changedPath stamped by the producers; even a
      // stray one on a non-mutating tool would count, so the fixture omits it.
      turn(call("Edit", "a.ts"), call("Edit", "b.ts"), {
        ...call("Read", "c.ts"),
        changedPath: undefined,
      }),
    ]);
    expect(files.map((f) => f.path)).toEqual(["b.ts", "a.ts"]);
    expect(files[0]).toMatchObject({ edits: 2, writes: 0 });
    expect(files[1]).toMatchObject({ edits: 1, writes: 1 });
  });

  it("attaches the LAST Write's previewable content", () => {
    const first = { path: "a.ts", name: "a.ts", content: "v1" };
    const second = { path: "a.ts", name: "a.ts", content: "v2" };
    const files = changedFilesFromMessages([
      turn(call("Write", "a.ts", { file: first })),
      turn(call("Write", "a.ts", { file: second })),
    ]);
    expect(files).toHaveLength(1);
    expect(files[0].lastWrite).toEqual(second);
  });

  it("ignores calls with no changed path and turns with no tool calls", () => {
    expect(
      changedFilesFromMessages([
        {},
        turn({ ...call("Read", ""), changedPath: undefined }),
      ]),
    ).toEqual([]);
  });
});

describe("toolRawArgs", () => {
  it("prefers the verbatim rawArgs, else a hydrated JSON input", () => {
    expect(toolRawArgs({ rawArgs: '{"a":1}', input: "a: 1" })).toBe('{"a":1}');
    expect(toolRawArgs({ input: '{"a":1}' })).toBe('{"a":1}');
  });

  it("never mistakes a flattened preview for JSON", () => {
    expect(toolRawArgs({ input: "command: ls · timeout: 5" })).toBeUndefined();
    expect(toolRawArgs({ input: { a: 1 } })).toBeUndefined();
  });
});

describe("untrusted result parts", () => {
  it("allows only http(s) links", () => {
    expect(safeResourceLinkHref("https://example.com/x")).toBe(
      "https://example.com/x",
    );
    expect(safeResourceLinkHref("http://example.com")).toBe(
      "http://example.com/",
    );
    expect(safeResourceLinkHref("javascript:alert(1)")).toBeNull();
    expect(safeResourceLinkHref("data:text/html,<b>x</b>")).toBeNull();
    expect(safeResourceLinkHref("file:///etc/passwd")).toBeNull();
    expect(safeResourceLinkHref("relative/path")).toBeNull();
  });

  it("allows only raster image types with base64 payloads", () => {
    expect(
      safeImageDataUrl({ kind: "image", mimeType: "image/png", data: "AAAA" }),
    ).toBe("data:image/png;base64,AAAA");
    expect(
      safeImageDataUrl({ kind: "image", mimeType: "IMAGE/JPEG", data: "AA==" }),
    ).toBe("data:image/jpeg;base64,AA==");
    expect(
      safeImageDataUrl({
        kind: "image",
        mimeType: "image/svg+xml",
        data: "AAAA",
      }),
    ).toBeNull();
    expect(
      safeImageDataUrl({ kind: "image", mimeType: "text/html", data: "AAAA" }),
    ).toBeNull();
    expect(
      safeImageDataUrl({ kind: "image", mimeType: "image/png", data: "" }),
    ).toBeNull();
    expect(
      safeImageDataUrl({
        kind: "image",
        mimeType: "image/png",
        data: "not base64!",
      }),
    ).toBeNull();
  });

  it("labels a link by title, then name, then url", () => {
    expect(
      resourceLinkLabel({
        kind: "resource_link",
        url: "https://a",
        name: "n",
        title: "t",
      }),
    ).toBe("t");
    expect(
      resourceLinkLabel({ kind: "resource_link", url: "https://a", name: "n" }),
    ).toBe("n");
    expect(
      resourceLinkLabel({ kind: "resource_link", url: "https://a", name: "" }),
    ).toBe("https://a");
  });
});

describe("clampChars", () => {
  it("counts code points so a surrogate pair is never split", () => {
    expect(clampChars("😀😀😀", 2)).toBe("😀…");
    expect(clampChars("abc", 3)).toBe("abc");
  });
});
