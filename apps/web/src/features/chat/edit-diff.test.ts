// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import {
  computeLineDiff,
  editSizeNote,
  isDiffTool,
  LINE_DIFF_MAX_LINES,
  parseDiffArgs,
  parseEditArgs,
  parseWriteArgs,
  writeSizeNote,
} from "./edit-diff";

describe("computeLineDiff", () => {
  it("finds no changes for identical text", () => {
    const diff = computeLineDiff("a\nb\nc", "a\nb\nc");
    expect(diff).toEqual({
      added: 0,
      coarse: false,
      lines: [
        { kind: "context", text: "a" },
        { kind: "context", text: "b" },
        { kind: "context", text: "c" },
      ],
      removed: 0,
    });
  });

  it("marks a pure addition", () => {
    const diff = computeLineDiff("a", "a\nb");
    expect(diff.added).toBe(1);
    expect(diff.removed).toBe(0);
    expect(diff.lines.map((l) => l.kind)).toEqual(["context", "added"]);
  });

  it("marks a pure deletion", () => {
    const diff = computeLineDiff("a\nb", "a");
    expect(diff.added).toBe(0);
    expect(diff.removed).toBe(1);
    expect(diff.lines.map((l) => l.kind)).toEqual(["context", "removed"]);
  });

  it("trims the common prefix and suffix around a one-line change", () => {
    const diff = computeLineDiff("a\nb\nc\nd", "a\nX\nc\nd");
    expect(diff.lines.map((l) => `${l.kind}:${l.text}`)).toEqual([
      "context:a",
      "removed:b",
      "added:X",
      "context:c",
      "context:d",
    ]);
  });

  it("treats an empty string as no lines, not one blank line", () => {
    expect(computeLineDiff("", "a").lines).toEqual([{ kind: "added", text: "a" }]);
    expect(computeLineDiff("a", "").lines).toEqual([{ kind: "removed", text: "a" }]);
    expect(computeLineDiff("", "")).toEqual({ added: 0, coarse: false, lines: [], removed: 0 });
  });

  it("degrades to a coarse removed/added split above the line cap", () => {
    const big = Array.from({ length: LINE_DIFF_MAX_LINES }, (_, i) => `line-${i}`).join("\n");
    const diff = computeLineDiff(big, `${big}\nextra`);
    expect(diff.coarse).toBe(true);
    expect(diff.added).toBe(LINE_DIFF_MAX_LINES + 1);
    expect(diff.removed).toBe(LINE_DIFF_MAX_LINES);
  });
});

describe("editSizeNote / writeSizeNote", () => {
  it("formats the edit header note, with an optional replace-all suffix", () => {
    const diff = computeLineDiff("a", "a\nb");
    expect(editSizeNote(diff, false)).toBe("-0 +1");
    expect(editSizeNote(diff, true)).toBe("-0 +1 · replace all");
  });

  it("formats the write header note with the overwrite caveat", () => {
    expect(writeSizeNote("a\nb\nc")).toBe("3 lines · overwrites if it exists");
    expect(writeSizeNote("a")).toBe("1 line · overwrites if it exists");
  });
});

describe("parseDiffArgs", () => {
  it("parses Edit args", () => {
    const args = JSON.stringify({
      new_string: "b",
      old_string: "a",
      path: "src/foo.ts",
      replace_all: true,
    });
    expect(parseEditArgs(args)).toEqual({
      kind: "edit",
      newString: "b",
      oldString: "a",
      path: "src/foo.ts",
      replaceAll: true,
    });
    expect(parseDiffArgs("Edit", args)).toEqual(parseEditArgs(args));
  });

  it("parses Write args", () => {
    const args = JSON.stringify({ content: "hello", path: "src/foo.ts" });
    expect(parseWriteArgs(args)).toEqual({ content: "hello", kind: "write", path: "src/foo.ts" });
    expect(parseDiffArgs("write", args)).toEqual(parseWriteArgs(args));
  });

  it("returns null for missing fields, malformed JSON, or a non-diff tool", () => {
    expect(parseEditArgs(JSON.stringify({ path: "x" }))).toBeNull();
    expect(parseEditArgs("not json")).toBeNull();
    expect(parseEditArgs(undefined)).toBeNull();
    expect(parseDiffArgs("Read", JSON.stringify({ path: "x" }))).toBeNull();
  });

  it("recognises Edit and Write case-insensitively via isDiffTool", () => {
    expect(isDiffTool("Edit")).toBe(true);
    expect(isDiffTool("write")).toBe(true);
    expect(isDiffTool("Read")).toBe(false);
  });
});
