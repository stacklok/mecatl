// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { Skeleton } from "./skeleton";

afterEach(cleanup);

describe("Skeleton", () => {
  it("renders a pulsing placeholder that merges caller classes", () => {
    const { container } = render(<Skeleton aria-hidden className="h-4 w-32 rounded-full" />);
    const skeleton = container.firstElementChild as HTMLElement;
    expect(skeleton.getAttribute("data-slot")).toBe("skeleton");
    expect(skeleton.getAttribute("aria-hidden")).toBe("true");
    expect(skeleton.className).toContain("animate-pulse");
    expect(skeleton.className).toContain("bg-muted");
    expect(skeleton.className).toContain("rounded-full");
    expect(skeleton.className).not.toContain("rounded-md");
  });
});
