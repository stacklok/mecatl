import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { FilePreview, previewKind } from "./file-preview";

const PNG_DATA_URI =
  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";

/**
 * FilePreview routes on the shared file-kind classification: images and PDFs
 * render from a url/data-URI source, markdown as prose, recognised source
 * files through the code viewer, and only a file with nothing displayable
 * hits the "No preview available" floor.
 */
describe("previewKind", () => {
  it("routes each kind to its renderer", () => {
    expect(previewKind("photo.png", undefined, PNG_DATA_URI)).toBe("image");
    expect(
      previewKind("chart.svg", undefined, "data:image/svg+xml;utf8,x"),
    ).toBe("image");
    expect(
      previewKind("report.pdf", undefined, "data:application/pdf;base64,x"),
    ).toBe("pdf");
    expect(previewKind("README.md", "# Hello")).toBe("markdown");
    expect(previewKind("logger.ts", "const x = 1;")).toBe("code");
    expect(previewKind("notes.log", "plain text")).toBe("text");
  });

  it("accepts a data-URI riding content for binary kinds", () => {
    expect(previewKind("photo.png", PNG_DATA_URI)).toBe("image");
  });

  it("floors to none only when nothing is displayable", () => {
    expect(previewKind("photo.png")).toBe("none");
    expect(previewKind("report.pdf")).toBe("none");
    expect(previewKind("anything.txt")).toBe("none");
    // An image whose content is NOT displayable text keeps the text fallback
    // instead of a broken <img>.
    expect(previewKind("photo.png", "not a data uri")).toBe("text");
  });
});

describe("FilePreview", () => {
  it("renders an image from its url", () => {
    render(<FilePreview name="photo.png" url={PNG_DATA_URI} />);
    const img = screen.getByAltText("photo.png");
    expect(img).toBeInTheDocument();
    expect(img).toHaveAttribute("src", PNG_DATA_URI);
  });

  it("renders a PDF in an iframe behind a blob: URL", () => {
    // data:application/pdf iframes render blank in Chrome/Safari, so the
    // frame must swap the same bytes onto a blob: URL.
    const src = "data:application/pdf;base64,JVBERi0=";
    render(<FilePreview name="report.pdf" url={src} />);
    const frame = screen.getByTitle("report.pdf");
    expect(frame.tagName).toBe("IFRAME");
    expect(frame.getAttribute("src")).toMatch(/^blob:/);
  });

  it("renders markdown as prose, not raw source", () => {
    render(<FilePreview name="README.md" content={"# Title\n\nBody"} />);
    expect(screen.getByRole("heading", { name: "Title" })).toBeInTheDocument();
  });

  it("renders recognised source files through the code viewer", async () => {
    const { container } = render(
      <FilePreview name="logger.ts" content={"const x = 1;\nexport { x };"} />,
    );
    await waitFor(() =>
      expect(container.querySelector("[data-highlighted='true']")).toBeTruthy(),
    );
    expect(container.querySelectorAll("tbody tr").length).toBe(2);
    expect(container.textContent).toContain("const x = 1;");
  });

  it("shows the no-preview floor when nothing is displayable", () => {
    render(<FilePreview name="mystery.bin" />);
    expect(
      screen.getByText("No preview available for this file"),
    ).toBeInTheDocument();
  });
});
