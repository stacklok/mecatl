// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ChevronIndicator } from "./chevron-indicator";

afterEach(cleanup);

describe("ChevronIndicator", () => {
  it("points down while closed and up while open", () => {
    const { container, rerender } = render(<ChevronIndicator isOpen={false} />);
    const closed = container.querySelector("svg") as SVGElement;
    expect(closed.getAttribute("class")).toContain("lucide-chevron-down");
    expect(closed.getAttribute("class")).toContain("size-4");
    rerender(<ChevronIndicator isOpen />);
    const open = container.querySelector("svg") as SVGElement;
    expect(open.getAttribute("class")).toContain("lucide-chevron-up");
  });

  it("merges caller classes over the default size", () => {
    const { container } = render(<ChevronIndicator className="size-3" isOpen={false} />);
    const icon = container.querySelector("svg") as SVGElement;
    expect(icon.getAttribute("class")).toContain("size-3");
    expect(icon.getAttribute("class")).not.toContain("size-4");
  });
});
