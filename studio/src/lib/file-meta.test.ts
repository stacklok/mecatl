import { describe, expect, it } from "vitest";
import { fileFromToolCall } from "./file-meta";

describe("fileFromToolCall", () => {
  it("derives the file from a Write call's daemon-shaped args", () => {
    expect(
      fileFromToolCall(
        "Write",
        JSON.stringify({ path: "content/mock/tv.md", content: "# hi" }),
      ),
    ).toEqual({ path: "content/mock/tv.md", name: "tv.md", content: "# hi" });
  });
  it("accepts the file_path variant", () => {
    expect(
      fileFromToolCall(
        "Write",
        JSON.stringify({ file_path: "a/b.ts", content: "x" }),
      )?.name,
    ).toBe("b.ts");
  });
  it("ignores non-Write tools, malformed JSON, and pathless args", () => {
    expect(fileFromToolCall("Edit", '{"path":"a.md"}')).toBeUndefined();
    expect(fileFromToolCall("Write", "not json")).toBeUndefined();
    expect(fileFromToolCall("Write", '{"content":"x"}')).toBeUndefined();
  });
});
