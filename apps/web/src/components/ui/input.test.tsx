// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { Input } from "./input";

afterEach(cleanup);

function classes(element: HTMLElement) {
  return element.className.split(/\s+/u);
}

describe("Input", () => {
  it("keeps Studio's contrast-hardened border and focus ring", () => {
    render(<Input aria-label="Name" />);
    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input.getAttribute("data-slot")).toBe("input");
    const list = classes(input);
    for (const token of [
      "border-control-border",
      "focus-visible:border-ring",
      "focus-visible:ring-2",
      "focus-visible:ring-ring",
    ]) {
      expect(list).toContain(token);
    }
    for (const token of [
      "border-input",
      "focus-visible:ring-ring/50",
      "focus-visible:ring-[3px]",
    ]) {
      expect(list).not.toContain(token);
    }
  });

  it("carries the prototype's invalid, file, and selection states", () => {
    render(<Input aria-invalid aria-label="Name" />);
    const input = screen.getByRole("textbox", { name: "Name" });
    expect(input.getAttribute("aria-invalid")).toBe("true");
    const list = classes(input);
    for (const token of [
      "aria-invalid:border-destructive",
      "aria-invalid:ring-destructive",
      "file:inline-flex",
      "file:h-7",
      "file:border-0",
      "file:bg-transparent",
      "file:text-sm",
      "file:font-medium",
      "file:text-foreground",
      "selection:bg-primary",
      "selection:text-primary-foreground",
      "disabled:pointer-events-none",
      "disabled:cursor-not-allowed",
    ]) {
      expect(list).toContain(token);
    }
  });

  it("passes type, value, and caller classes through", async () => {
    const user = userEvent.setup();
    const { container } = render(<Input className="h-11" placeholder="Search" type="search" />);
    const input = container.querySelector("input") as HTMLInputElement;
    expect(input.type).toBe("search");
    expect(classes(input)).toContain("h-11");
    expect(classes(input)).not.toContain("h-9");
    await user.type(input, "skills");
    expect(input.value).toBe("skills");
  });
});
