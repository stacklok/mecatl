// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Toggle } from "./toggle";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

describe("Toggle", () => {
  it("flips aria-pressed on click", async () => {
    const user = userEvent.setup();
    const onPressedChange = vi.fn();
    render(
      <Toggle onPressedChange={onPressedChange} size="sm" variant="outline">
        Bold
      </Toggle>,
    );
    const toggle = screen.getByRole("button", { name: "Bold" });
    expect(toggle.getAttribute("data-slot")).toBe("toggle");
    expect(toggle.className).toContain("border");
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
    await user.click(toggle);
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(toggle.getAttribute("data-state")).toBe("on");
    expect(onPressedChange).toHaveBeenLastCalledWith(true);
    await user.click(toggle);
    expect(toggle.getAttribute("aria-pressed")).toBe("false");
  });

  it("keeps the ghost variant unfilled while pressed, like the ghost Button", async () => {
    const user = userEvent.setup();
    render(<Toggle variant="ghost">Raw</Toggle>);
    const toggle = screen.getByRole("button", { name: "Raw" });
    for (const pressed of ["false", "true"]) {
      expect(toggle.getAttribute("aria-pressed")).toBe(pressed);
      expect(toggle.className).toContain("rounded-full");
      expect(toggle.className).toContain("hover:bg-accent");
      expect(toggle.className).toContain("focus-visible:ring-2");
      expect(toggle.className).not.toContain("data-[state=on]:bg-accent");
      expect(toggle.className).not.toContain("hover:bg-muted");
      await user.click(toggle);
    }
  });

  it("fills the default and outline variants while pressed", () => {
    render(
      <>
        <Toggle>Bold</Toggle>
        <Toggle variant="outline">Italic</Toggle>
      </>,
    );
    for (const name of ["Bold", "Italic"]) {
      expect(screen.getByRole("button", { name }).className).toContain("data-[state=on]:bg-accent");
    }
  });
});
