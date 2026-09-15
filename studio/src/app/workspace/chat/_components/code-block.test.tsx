import { render, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CodeBlock } from "./code-block";

/**
 * `CodeBlock` renders a synchronous plain fallback and then swaps in Shiki's
 * highlighted tokens once the grammar loads. Each test waits for that swap
 * (`data-highlighted="true"`) so the async state update settles inside `act`.
 * Assertions stay engine-agnostic — row count and preserved text hold in both
 * the fallback and highlighted states.
 */
describe("CodeBlock", () => {
  it("renders one numbered row per line and preserves the code text", async () => {
    const code = "const x = 1;\nfunction f() {\n  return x;\n}";
    const { container } = render(<CodeBlock code={code} lang="typescript" />);
    await waitFor(() =>
      expect(container.querySelector("[data-highlighted='true']")).toBeTruthy(),
    );
    const rows = container.querySelectorAll("tbody tr");
    expect(rows.length).toBe(4);
    // Text is preserved (gutter digits are interleaved but fragments survive).
    expect(container.textContent).toContain("const x = 1;");
    expect(container.textContent).toContain("return x;");
  });

  it("drops a single trailing newline so there's no phantom last line", async () => {
    const { container } = render(<CodeBlock code={"a\nb\n"} lang="text" />);
    await waitFor(() =>
      expect(container.querySelector("[data-highlighted='true']")).toBeTruthy(),
    );
    expect(container.querySelectorAll("tbody tr").length).toBe(2);
  });
});
