// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from "vitest";
import { extensionOf, fileKindMeta } from "./file-meta";

describe("extensionOf", () => {
  it("lower-cases the extension after the last dot", () => {
    expect(extensionOf("report.PDF")).toBe("pdf");
    expect(extensionOf("archive.tar.gz")).toBe("gz");
  });

  it("is empty for a name with no extension", () => {
    expect(extensionOf("README")).toBe("");
  });
});

describe("fileKindMeta", () => {
  it("classifies images by MIME type or extension", () => {
    expect(fileKindMeta("photo.png", "image/png").label).toBe("Image");
    expect(fileKindMeta("photo.png").label).toBe("Image");
    expect(fileKindMeta("capture", "image/webp").label).toBe("Image");
  });

  it("classifies PDFs, Markdown, and plain text as documents", () => {
    expect(fileKindMeta("report.pdf").label).toBe("PDF");
    expect(fileKindMeta("notes.md").label).toBe("Markdown");
    expect(fileKindMeta("server.log").label).toBe("Text");
    expect(fileKindMeta("notes", "text/plain").label).toBe("Text");
  });

  it("classifies recognised source files as code", () => {
    expect(fileKindMeta("index.ts").label).toBe("Code");
    expect(fileKindMeta("main.go", "").label).toBe("Code");
  });

  it("falls back to a generic file for anything else", () => {
    expect(fileKindMeta("data.bin").label).toBe("File");
    expect(fileKindMeta("README").label).toBe("File");
  });
});
