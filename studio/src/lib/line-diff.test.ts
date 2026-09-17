import { describe, expect, it } from "vitest";
import { diffLines, LINE_DIFF_MAX_LINES } from "./line-diff";

describe("diffLines", () => {
  it("reports identical texts as all-same lines", () => {
    expect(diffLines("a\nb\nc", "a\nb\nc")).toEqual([
      { type: "same", text: "a" },
      { type: "same", text: "b" },
      { type: "same", text: "c" },
    ]);
  });

  it("marks an inserted line as add", () => {
    expect(diffLines("a\nc", "a\nb\nc")).toEqual([
      { type: "same", text: "a" },
      { type: "add", text: "b" },
      { type: "same", text: "c" },
    ]);
  });

  it("marks a removed line as del", () => {
    expect(diffLines("a\nb\nc", "a\nc")).toEqual([
      { type: "same", text: "a" },
      { type: "del", text: "b" },
      { type: "same", text: "c" },
    ]);
  });

  it("renders a replacement as del then add", () => {
    expect(diffLines('fmt.Println("hi")', 'fmt.Println("hello")')).toEqual([
      { type: "del", text: 'fmt.Println("hi")' },
      { type: "add", text: 'fmt.Println("hello")' },
    ]);
  });

  it("treats the empty text as zero lines, so deleting everything is all del", () => {
    expect(diffLines("a\nb", "")).toEqual([
      { type: "del", text: "a" },
      { type: "del", text: "b" },
    ]);
    expect(diffLines("", "x")).toEqual([{ type: "add", text: "x" }]);
    expect(diffLines("", "")).toEqual([]);
  });

  it("aligns around a change in the middle of a long common context", () => {
    const before = ["one", "two", "three", "four", "five"].join("\n");
    const after = ["one", "two", "THREE", "four", "five"].join("\n");
    expect(diffLines(before, after)).toEqual([
      { type: "same", text: "one" },
      { type: "same", text: "two" },
      { type: "del", text: "three" },
      { type: "add", text: "THREE" },
      { type: "same", text: "four" },
      { type: "same", text: "five" },
    ]);
  });

  it("gives up (null) above the line bound instead of running the alignment", () => {
    const half = Math.ceil(LINE_DIFF_MAX_LINES / 2) + 1;
    const before = Array.from({ length: half }, (_, i) => `a${i}`).join("\n");
    const after = Array.from({ length: half }, (_, i) => `b${i}`).join("\n");
    expect(diffLines(before, after)).toBeNull();
    // Right at the bound it still diffs.
    const atBound = Array.from(
      { length: LINE_DIFF_MAX_LINES / 2 },
      (_, i) => `x${i}`,
    ).join("\n");
    expect(diffLines(atBound, atBound)).toHaveLength(LINE_DIFF_MAX_LINES / 2);
  });
});
