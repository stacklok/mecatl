import { render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AboutStudioCard } from "./about-studio-card";

/**
 * The About Studio card: one build-time version row that never waits on the
 * agent, and three links — the documentation site, the support page, and
 * the in-app shortcuts reference.
 */

beforeEach(() => {
  vi.stubEnv("NEXT_PUBLIC_STUDIO_VERSION", "0.1.0");
});

afterEach(() => {
  vi.unstubAllEnvs();
});

describe("AboutStudioCard", () => {
  it("renders the inlined Studio version and nothing more technical", () => {
    render(<AboutStudioCard />);
    expect(screen.getByText("About Studio")).toBeInTheDocument();
    expect(screen.getByText("Version")).toBeInTheDocument();
    expect(screen.getByTestId("about-studio-version")).toHaveTextContent(
      "0.1.0",
    );
    // The build stamp and the SDK version are not for this audience.
    expect(screen.queryByText("Build")).toBeNull();
    expect(screen.queryByText("SDK version")).toBeNull();
  });

  it("reads 'unknown' rather than an empty cell when the version was not inlined", () => {
    vi.stubEnv("NEXT_PUBLIC_STUDIO_VERSION", "   ");
    render(<AboutStudioCard />);
    expect(screen.getByTestId("about-studio-version")).toHaveTextContent(
      "unknown",
    );
  });

  it("links to the documentation, the support page (new tab) and the shortcuts reference", () => {
    render(<AboutStudioCard />);
    const docs = screen.getByRole("link", { name: /Documentation/ });
    expect(docs).toHaveAttribute("href", "https://mecatl.dev/docs/");
    expect(docs).toHaveAttribute("target", "_blank");
    expect(docs).toHaveAttribute("rel", "noreferrer");

    const support = screen.getByRole("link", { name: /Report a problem/ });
    expect(support).toHaveAttribute(
      "href",
      "https://github.com/stacklok/mecatl/issues",
    );
    expect(support).toHaveAttribute("target", "_blank");
    expect(support).toHaveAttribute("rel", "noreferrer");

    const shortcuts = screen.getByRole("link", { name: /Keyboard shortcuts/ });
    expect(shortcuts).toHaveAttribute("href", "/workspace/shortcuts");
    expect(shortcuts).not.toHaveAttribute("target");
  });
});
