// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ScrollArea, ScrollBar } from "./scroll-area";

afterEach(cleanup);

describe("ScrollArea", () => {
  it("wraps its children in a focusable-ring viewport and merges caller classes", () => {
    const { container } = render(
      <ScrollArea className="h-40">
        <p>Scrolled content</p>
      </ScrollArea>,
    );
    const root = container.querySelector('[data-slot="scroll-area"]') as HTMLElement;
    expect(root.className).toContain("relative");
    expect(root.className).toContain("h-40");
    const viewport = root.querySelector('[data-slot="scroll-area-viewport"]') as HTMLElement;
    expect(viewport.contains(screen.getByText("Scrolled content"))).toBe(true);
    expect(viewport.className).toContain("focus-visible:ring-[3px]");
  });

  it("renders vertical and horizontal scrollbars with their own sizing", () => {
    const { container } = render(
      <ScrollArea type="always">
        <p>Wide content</p>
        <ScrollBar orientation="horizontal" />
      </ScrollArea>,
    );
    const bars = [...container.querySelectorAll('[data-slot="scroll-area-scrollbar"]')];
    const vertical = bars.find((bar) => bar.getAttribute("data-orientation") === "vertical");
    const horizontal = bars.find((bar) => bar.getAttribute("data-orientation") === "horizontal");
    expect(vertical?.className).toContain("w-2.5");
    expect(horizontal?.className).toContain("h-2.5");
    expect(horizontal?.className).toContain("flex-col");
  });
});
