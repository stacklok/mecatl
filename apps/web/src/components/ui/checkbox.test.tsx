// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Checkbox } from "./checkbox";
import { Label } from "./label";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

describe("Checkbox", () => {
  it("toggles checked state on click and via its label", async () => {
    const user = userEvent.setup();
    const onCheckedChange = vi.fn();
    render(
      <>
        <Checkbox id="notify" onCheckedChange={onCheckedChange} />
        <Label htmlFor="notify">Notify me</Label>
      </>,
    );
    const box = screen.getByRole("checkbox", { name: "Notify me" });
    expect(box.getAttribute("data-slot")).toBe("checkbox");
    expect(box.getAttribute("aria-checked")).toBe("false");
    await user.click(box);
    expect(box.getAttribute("aria-checked")).toBe("true");
    expect(box.getAttribute("data-state")).toBe("checked");
    expect(onCheckedChange).toHaveBeenLastCalledWith(true);
    await user.click(screen.getByText("Notify me"));
    expect(box.getAttribute("aria-checked")).toBe("false");
    expect(onCheckedChange).toHaveBeenLastCalledWith(false);
  });

  it("ignores clicks when disabled", async () => {
    const user = userEvent.setup();
    const onCheckedChange = vi.fn();
    render(<Checkbox aria-label="Locked" disabled onCheckedChange={onCheckedChange} />);
    await user.click(screen.getByRole("checkbox", { name: "Locked" }));
    expect(onCheckedChange).not.toHaveBeenCalled();
  });
});
