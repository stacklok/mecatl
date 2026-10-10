// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Separator } from "./separator";

afterEach(cleanup);

describe("Separator", () => {
  it("renders a decorative horizontal rule hidden from assistive tech by default", () => {
    const { container } = render(<Separator className="my-4" />);
    const separator = container.firstElementChild as HTMLElement;
    expect(separator.getAttribute("role")).toBe("none");
    expect(separator.getAttribute("data-orientation")).toBe("horizontal");
    expect(separator.className).toContain("bg-border");
    expect(separator.className).toContain("my-4");
    expect(screen.queryByRole("separator")).toBeNull();
  });

  it("exposes a semantic vertical separator when not decorative", () => {
    render(<Separator decorative={false} orientation="vertical" />);
    const separator = screen.getByRole("separator");
    expect(separator.getAttribute("aria-orientation")).toBe("vertical");
    expect(separator.getAttribute("data-orientation")).toBe("vertical");
  });
});
