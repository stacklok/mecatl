// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ContentPreviewPanel } from "./content-preview-panel";
import { imagePreview, readLocalFile } from "./local-file-preview";

afterEach(() => vi.unstubAllGlobals());

describe("content preview panel", () => {
  it("renders file and tool previews safely under the shipped csp", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const common = { canvas: "", onCanvasChange: vi.fn(), onClose: vi.fn() };
    const remote = imagePreview(
      { mimeType: "image/png", name: "Remote diagram", url: "https://example.com/a.png" },
      true,
    );
    const { container, rerender } = render(
      <ContentPreviewPanel {...common} preview={{ file: remote, kind: "file" }} />,
    );
    expect(screen.getByRole("complementary", { name: "Remote diagram" })).toBeTruthy();
    expect(screen.queryByRole("img")).toBeNull();
    expect(screen.getByRole("link", { name: /open remote image/i }).getAttribute("href")).toBe(
      "https://example.com/a.png",
    );
    expect(fetch).not.toHaveBeenCalled();

    rerender(
      <ContentPreviewPanel
        {...common}
        preview={{
          file: {
            dataUrl: "data:application/pdf;base64,JVBERg==",
            kind: "pdf",
            name: "report.pdf",
            size: 5,
            type: "application/pdf",
          },
          kind: "file",
        }}
      />,
    );
    expect(screen.queryByTitle("report.pdf")).toBeNull();
    expect(screen.getByRole("link", { name: /download pdf/i }).getAttribute("download")).toBe(
      "report.pdf",
    );

    rerender(
      <ContentPreviewPanel
        {...common}
        preview={{
          file: {
            content:
              "# Notes\n![remote](https://example.com/tracker.png)\n<script>alert(1)</script>",
            kind: "markdown",
            name: "notes.md",
            size: 70,
            type: "text/markdown",
          },
          kind: "file",
        }}
      />,
    );
    expect(screen.getByRole("heading", { name: "Notes" })).toBeTruthy();
    expect(screen.queryByRole("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    expect(fetch).not.toHaveBeenCalled();

    rerender(
      <ContentPreviewPanel
        {...common}
        preview={{
          kind: "tool",
          tool: { args: "<script>x</script>", id: "t1", name: "Read", output: "hello" },
        }}
      />,
    );
    expect(screen.getByRole("complementary", { name: "Read result" })).toBeTruthy();
    expect(screen.getByText("Input")).toBeTruthy();
    expect(screen.getByText("Output")).toBeTruthy();
    expect(container.querySelector("script")).toBeNull();

    const unsupported = await readLocalFile(
      new File(["archive"], "data.zip", { type: "application/zip" }),
    );
    rerender(<ContentPreviewPanel {...common} preview={{ file: unsupported, kind: "file" }} />);
    expect(screen.getByText(/file type is not supported/i)).toBeTruthy();

    const oversized = await readLocalFile(
      new File([new Uint8Array(5 * 1024 * 1024 + 1)], "too-big.txt", { type: "text/plain" }),
    );
    rerender(<ContentPreviewPanel {...common} preview={{ file: oversized, kind: "file" }} />);
    expect(screen.getByText(/larger than the 5 MB preview limit/i)).toBeTruthy();
    expect(fetch).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Close panel" }));
    expect(common.onClose).toHaveBeenCalledTimes(1);
  });
});
