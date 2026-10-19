// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { CodeBlock } from "./code-block";

afterEach(() => cleanup());

describe("CodeBlock", () => {
  it("renders one numbered row per line, dropping a phantom trailing line", async () => {
    render(<CodeBlock code={"const a = 1;\nconst b = 2;\n"} lang="text" />);
    expect(screen.getByText("1")).toBeTruthy();
    expect(screen.getByText("2")).toBeTruthy();
    expect(screen.queryByText("3")).toBeNull();
    expect(screen.getByText("const a = 1;")).toBeTruthy();
    expect(screen.getByText("const b = 2;")).toBeTruthy();
    await waitFor(() =>
      expect(
        screen.getByRole("table").closest("[data-highlighted]")?.getAttribute("data-highlighted"),
      ).toBe("true"),
    );
  });

  it("keeps a gutter row for an empty line", () => {
    render(<CodeBlock code={"a\n\nb"} />);
    expect(screen.getByText("1")).toBeTruthy();
    expect(screen.getByText("2")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
    expect(screen.getAllByRole("row")).toHaveLength(3);
  });
});
