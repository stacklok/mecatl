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
});
