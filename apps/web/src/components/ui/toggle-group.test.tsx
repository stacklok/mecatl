// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ToggleGroup, ToggleGroupItem } from "./toggle-group";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

describe("ToggleGroup", () => {
  it("keeps a single item on and passes the group variant to items", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(
      <ToggleGroup
        aria-label="View"
        defaultValue="list"
        onValueChange={onValueChange}
        type="single"
        variant="outline"
      >
        <ToggleGroupItem value="list">List</ToggleGroupItem>
        <ToggleGroupItem value="grid">Grid</ToggleGroupItem>
      </ToggleGroup>,
    );
    const group = screen.getByRole("radiogroup", { name: "View" });
    expect(group.getAttribute("data-slot")).toBe("toggle-group");
    const list = screen.getByRole("radio", { name: "List" });
    const grid = screen.getByRole("radio", { name: "Grid" });
    expect(list.getAttribute("data-slot")).toBe("toggle-group-item");
    expect(grid.className).toContain("border");
    expect(list.getAttribute("aria-checked")).toBe("true");
    await user.click(grid);
    expect(onValueChange).toHaveBeenCalledWith("grid");
    expect(grid.getAttribute("aria-checked")).toBe("true");
    expect(list.getAttribute("aria-checked")).toBe("false");
  });
});
