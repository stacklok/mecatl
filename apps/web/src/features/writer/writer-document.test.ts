// SPDX-License-Identifier: Apache-2.0
import { describe, expect, it, vi } from "vitest";
import { readWriterDocument } from "./writer-document";

describe("local Writer document import", () => {
  it("decodes UTF-8, strips its BOM, and accepts an empty document", async () => {
    expect(
      await readWriterDocument(
        new File(["\ufeff# café\r\n"], "draft.markdown", { type: "text/markdown" }),
      ),
    ).toBe("# café\r\n");
    expect(await readWriterDocument(new File([], "empty.txt"))).toBe("");
  });

  it("rejects unsupported types, invalid UTF-8, controls, and both size ceilings", async () => {
    await expect(
      readWriterDocument(new File(["x"], "draft.pdf", { type: "text/plain" })),
    ).rejects.toThrow(/Choose/);
    await expect(
      readWriterDocument(new File(["x"], "draft.md", { type: "application/pdf" })),
    ).rejects.toThrow(/Choose/);
    await expect(
      readWriterDocument(new File([new Uint8Array([0xff])], "draft.txt")),
    ).rejects.toThrow(/UTF-8/);
    await expect(readWriterDocument(new File(["a\u0000b"], "draft.txt"))).rejects.toThrow(
      /control/,
    );
    await expect(readWriterDocument(new File(["a\u0085b"], "draft.txt"))).rejects.toThrow(
      /control/,
    );
    await expect(readWriterDocument(new File(["a\u009fb"], "draft.txt"))).rejects.toThrow(
      /control/,
    );
    expect(await readWriterDocument(new File(["雪\té\n"], "draft.txt"))).toBe("雪\té\n");
    await expect(readWriterDocument(new File(["a".repeat(100_001)], "draft.md"))).rejects.toThrow(
      /characters/,
    );
    await expect(readWriterDocument(new File(["é".repeat(200_001)], "draft.md"))).rejects.toThrow(
      /bytes/,
    );
    const misleading = new File(["ok"], "draft.md");
    vi.spyOn(misleading, "arrayBuffer").mockResolvedValue(new Uint8Array(400_001).buffer);
    await expect(readWriterDocument(misleading)).rejects.toThrow(/bytes/);
  });
});
