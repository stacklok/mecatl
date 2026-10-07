// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { describe, expect, it } from "vitest";
import { acceptWriterReference, readWriterReference } from "./writer-reference";

const file = (content: string, name = "notes.md") =>
  new File([content], name, { type: "text/markdown" });

describe("Writer local reference snapshots", () => {
  it("reads exact-limit UTF-8 text without selecting a workspace or sending a request", async () => {
    await expect(readWriterReference(file("a".repeat(8_000)))).resolves.toEqual({
      name: "notes.md",
      content: "a".repeat(8_000),
    });
    await expect(readWriterReference(file("é".repeat(4_001)))).rejects.toThrow("8,000 bytes");
    await expect(readWriterReference(file("\u0000binary"))).rejects.toThrow("binary/control");
    await expect(readWriterReference(file(""))).rejects.toThrow("nonempty");
    await expect(readWriterReference(file("a".repeat(8_001)))).rejects.toThrow("8,000 bytes");
    await expect(
      readWriterReference(new File([new Uint8Array([0xff])], "bad.txt", { type: "text/plain" })),
    ).rejects.toThrow("UTF-8");
    await expect(
      readWriterReference(new File(["data"], "photo.png", { type: "image/png" })),
    ).rejects.toThrow("text");
    await expect(readWriterReference(file("data", "../notes.md"))).rejects.toThrow("text");
  });
  it("enforces count and aggregate bytes", () => {
    const item = { name: "a.txt", content: "a".repeat(8_000) };
    const second = { ...item, name: "b.txt" };
    expect(acceptWriterReference([item], second)).toBeUndefined();
    expect(acceptWriterReference([item], item)).toContain("already attached");
    expect(acceptWriterReference([item, second], { name: "c.txt", content: "b" })).toContain(
      "16,000",
    );
    expect(acceptWriterReference([item, second, { ...item, name: "c.txt" }], item)).toContain(
      "three",
    );
  });
});
