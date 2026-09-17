import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { formatMemoryFootprint, MemoryFootprint } from "./memory-footprint";

/**
 * The summary line above the facts table: the count only — the byte size
 * and digest the agent reports never reach the page.
 */
describe("formatMemoryFootprint", () => {
  it("counts the facts and nothing else", () => {
    expect(
      formatMemoryFootprint({
        count: 3,
        sizeBytes: 64,
        sha256: "abcdef0123456789abcdef",
      }),
    ).toBe("3 facts remembered");
  });

  it("singularises one fact", () => {
    expect(formatMemoryFootprint({ count: 1, sizeBytes: 12, sha256: "" })).toBe(
      "1 fact remembered",
    );
  });

  it("is empty when there is nothing to count, whatever else was reported", () => {
    expect(
      formatMemoryFootprint({ count: 0, sizeBytes: 64, sha256: "ff" }),
    ).toBe("");
  });
});

describe("MemoryFootprint", () => {
  it("shows the count line without the digest or size", () => {
    render(
      <MemoryFootprint
        store={{ count: 1, sizeBytes: 64, sha256: "f".repeat(64) }}
      />,
    );
    const line = screen.getByTestId("memory-footprint");
    expect(line).toHaveTextContent("1 fact remembered");
    expect(line).not.toHaveAttribute("title");
    expect(line.textContent).not.toMatch(/sha256|bytes/);
  });

  it("renders nothing when there is nothing to report", () => {
    render(<MemoryFootprint store={{ count: 0, sizeBytes: 0, sha256: "" }} />);
    expect(screen.queryByTestId("memory-footprint")).toBeNull();
  });
});
