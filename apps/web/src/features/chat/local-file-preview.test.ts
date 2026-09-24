// SPDX-License-Identifier: Apache-2.0
// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  acceptImageAttachments,
  chatImageDisplay,
  imagePreview,
  imageSource,
  localFileKind,
  maxImageAttachmentBytes,
  maxImagePromptBytes,
  readImageAttachment,
} from "./local-file-preview";

afterEach(() => vi.unstubAllGlobals());

describe("local file previews", () => {
  it("classifies supported browser previews", () => {
    expect(localFileKind("photo.png", "image/png")).toBe("image");
    expect(localFileKind("README.md", "text/markdown")).toBe("markdown");
    expect(localFileKind("worker.ts", "")).toBe("code");
    expect(localFileKind("report.pdf", "application/pdf")).toBe("pdf");
    expect(localFileKind("archive.zip", "application/zip")).toBe("unsupported");
  });

  it("builds image sources and estimates persisted image sizes", () => {
    const inline = { data: "aGVsbG8=", mimeType: "image/png", name: "Image 1" };
    expect(imageSource(inline)).toBe("data:image/png;base64,aGVsbG8=");
    expect(imagePreview(inline, true)).toMatchObject({ sent: true, size: 5 });
    expect(
      imageSource({
        mimeType: "image/webp",
        name: "Image 2",
        url: "https://example.com/image.webp",
      }),
    ).toBe("https://example.com/image.webp");
  });

  it("renders inline image data as an image and a URL-only image as an external link", () => {
    expect(chatImageDisplay({ data: "aGVsbG8=", mimeType: "image/png", name: "Image 1" })).toEqual({
      kind: "inline",
      src: "data:image/png;base64,aGVsbG8=",
    });
    expect(
      chatImageDisplay({
        data: "aGVsbG8=",
        mimeType: "image/png",
        name: "Image 1",
        url: "https://example.com/image.png",
      }).kind,
    ).toBe("inline");
    expect(
      chatImageDisplay({
        mimeType: "image/webp",
        name: "Image 2",
        url: "https://example.com/image.webp",
      }),
    ).toEqual({ href: "https://example.com/image.webp", kind: "link" });
  });

  it("does not link a URL-only image whose address is not http or https", () => {
    for (const url of ["javascript:alert(1)", "data:image/png;base64,aGVsbG8=", "/relative.png"]) {
      expect(chatImageDisplay({ mimeType: "image/png", name: "Image 3", url })).toEqual({
        kind: "name",
      });
    }
  });

  it("rejects unsupported, empty, oversize, and unreadable image selections", async () => {
    await expect(
      readImageAttachment(new File(["text"], "notes.txt", { type: "text/plain" })),
    ).rejects.toThrow("is not an image");
    await expect(
      readImageAttachment(new File([], "empty.png", { type: "image/png" })),
    ).rejects.toThrow("is empty");
    await expect(
      readImageAttachment(
        new File([new Uint8Array(maxImageAttachmentBytes + 1)], "huge.png", {
          type: "image/png",
        }),
      ),
    ).rejects.toThrow("10 MB image limit");

    class BrokenFileReader {
      error = new Error("File read failed");
      onerror: (() => void) | null = null;
      readAsDataURL() {
        this.onerror?.();
      }
    }
    vi.stubGlobal("FileReader", BrokenFileReader);
    await expect(
      readImageAttachment(new File(["data"], "broken.png", { type: "image/png" })),
    ).rejects.toThrow("File read failed");
  });

  it("accepts exactly twenty MiB of images and explains an aggregate overflow", () => {
    const image = (id: string, size: number) => ({
      data: "YQ==",
      id,
      mimeType: "image/png",
      name: `${id}.png`,
      size,
    });
    const selected = acceptImageAttachments(
      [image("first", maxImagePromptBytes / 2)],
      [image("second", maxImagePromptBytes / 2), image("overflow", 1)],
    );
    expect(selected.accepted.map((item) => item.id)).toEqual(["second"]);
    expect(selected.errors).toEqual(["overflow.png exceeds the 20 MB total image limit."]);
  });
});
