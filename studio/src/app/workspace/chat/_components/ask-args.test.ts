import { describe, expect, it } from "vitest";
import {
  askArgsSource,
  askToolName,
  clampAskText,
  describeAskArgs,
  formatTimeout,
} from "./ask-args";

describe("describeAskArgs", () => {
  it("decodes a Shell ask to the bare command and its timeout", () => {
    expect(
      describeAskArgs(
        "Shell",
        JSON.stringify({ command: "go test ./...", timeout_ms: 120000 }),
      ),
    ).toEqual({ kind: "shell", command: "go test ./...", timeoutMs: 120000 });
    // The legacy/alias tool name decodes the same way, case-insensitively.
    expect(describeAskArgs("bash", '{"command":"ls -la"}')).toEqual({
      kind: "shell",
      command: "ls -la",
      timeoutMs: undefined,
    });
  });

  it("decodes an Edit ask to path + old/new text + replace_all", () => {
    expect(
      describeAskArgs(
        "Edit",
        JSON.stringify({
          path: "main.go",
          old_string: 'fmt.Println("hi")',
          new_string: 'fmt.Println("hello")',
        }),
      ),
    ).toEqual({
      kind: "edit",
      path: "main.go",
      oldText: 'fmt.Println("hi")',
      newText: 'fmt.Println("hello")',
      replaceAll: false,
    });
    expect(
      describeAskArgs(
        "Edit",
        JSON.stringify({
          path: "a.txt",
          old_string: "x",
          new_string: "",
          replace_all: true,
        }),
      ),
    ).toMatchObject({ kind: "edit", newText: "", replaceAll: true });
  });

  it("decodes a Write ask to path + content", () => {
    expect(
      describeAskArgs(
        "Write",
        JSON.stringify({ path: "notes/todo.txt", content: "buy milk\n" }),
      ),
    ).toEqual({ kind: "write", path: "notes/todo.txt", content: "buy milk\n" });
  });

  it("indents any other tool's JSON object", () => {
    expect(
      describeAskArgs("WebFetch", '{"url":"https://x.test","max":2}'),
    ).toEqual({
      kind: "json",
      pretty: '{\n  "url": "https://x.test",\n  "max": 2\n}',
    });
  });

  it("falls back to indented JSON when a known tool's fields are missing", () => {
    // A Shell ask without a string `command` is still shown, as JSON.
    expect(describeAskArgs("Shell", '{"cmd":"ls"}')).toEqual({
      kind: "json",
      pretty: '{\n  "cmd": "ls"\n}',
    });
    expect(describeAskArgs("Edit", '{"path":"a"}')).toMatchObject({
      kind: "json",
    });
  });

  it("returns malformed or empty args verbatim", () => {
    expect(describeAskArgs("Shell", "not json")).toEqual({
      kind: "raw",
      text: "not json",
    });
    expect(describeAskArgs("Shell", "")).toEqual({ kind: "raw", text: "" });
    expect(describeAskArgs("Shell", undefined)).toEqual({
      kind: "raw",
      text: "",
    });
  });
});

describe("askArgsSource", () => {
  it("prefers the raw tier when the ask carries it", () => {
    expect(
      askArgsSource({
        reason: "runs a command",
        args: '{"command":"ls"}',
        details: "runs a command\n\ncommand: ls",
      }),
    ).toEqual({ reason: "runs a command", args: '{"command":"ls"}' });
  });

  it("falls back to the joined details for an ask without the raw tier", () => {
    expect(askArgsSource({ details: "ls -la" })).toEqual({
      reason: "",
      args: "ls -la",
    });
  });
});

describe("askToolName", () => {
  it("names the tool, or the description's subject, or Tool", () => {
    expect(askToolName({ toolName: "Shell", description: "" })).toBe("Shell");
    expect(
      askToolName({ toolName: "", description: "Edit needs your approval." }),
    ).toBe("Edit");
    expect(askToolName({ toolName: "", description: "" })).toBe("Tool");
  });
});

describe("clampAskText", () => {
  it("passes short text through and bounds long text with a trailer", () => {
    expect(clampAskText("abc", 10)).toBe("abc");
    const clamped = clampAskText("a".repeat(20), 10);
    expect(clamped.startsWith("a".repeat(10))).toBe(true);
    expect(clamped).toContain("10 more characters not shown");
  });

  it("never splits a surrogate pair at the cut", () => {
    const text = `${"a".repeat(9)}😀zz`;
    const clamped = clampAskText(text, 10);
    // The cut backs off to before the pair's lead unit.
    expect(clamped.startsWith("a".repeat(9))).toBe(true);
    expect(clamped.charCodeAt(9)).not.toBe(text.charCodeAt(9));
  });
});

describe("formatTimeout", () => {
  it("renders seconds, trimming a .0", () => {
    expect(formatTimeout(120000)).toBe("timeout 120s");
    expect(formatTimeout(1500)).toBe("timeout 1.5s");
    expect(formatTimeout(undefined)).toBe("");
  });
});
