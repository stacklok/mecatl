// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
  SelectValue,
} from "./select";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

function Picker({ onValueChange }: { onValueChange: (value: string) => void }) {
  const [value, setValue] = useState("apple");
  return (
    <Select
      onValueChange={(next) => {
        setValue(next);
        onValueChange(next);
      }}
      value={value}
    >
      <SelectTrigger aria-label="Fruit">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          <SelectLabel>Fruit</SelectLabel>
          <SelectItem value="apple">Apple</SelectItem>
          <SelectItem value="pear">Pear</SelectItem>
          <SelectSeparator />
          <SelectItem disabled value="durian">
            Durian (unavailable)
          </SelectItem>
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}

afterEach(cleanup);

describe("Select", () => {
  it("renders the trigger with its accessible name and current value", () => {
    render(<Picker onValueChange={() => {}} />);
    const trigger = screen.getByRole("combobox", { name: "Fruit" });
    expect(trigger.getAttribute("data-slot")).toBe("select-trigger");
    expect(trigger.getAttribute("data-size")).toBe("default");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(trigger.textContent).toContain("Apple");
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("opens to show the items with the selected one checked", async () => {
    const user = userEvent.setup();
    render(<Picker onValueChange={() => {}} />);
    await user.click(screen.getByRole("combobox", { name: "Fruit" }));
    const listbox = await screen.findByRole("listbox");
    expect(listbox.getAttribute("data-slot")).toBe("select-content");
    const names = screen.getAllByRole("option").map((option) => option.textContent);
    expect(names).toEqual(["Apple", "Pear", "Durian (unavailable)"]);
    const apple = screen.getByRole("option", { name: "Apple" });
    expect(apple.getAttribute("aria-selected")).toBe("true");
    expect(apple.getAttribute("data-state")).toBe("checked");
  });

  it("does not select a disabled item", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<Picker onValueChange={onValueChange} />);
    await user.click(screen.getByRole("combobox", { name: "Fruit" }));
    const durian = await screen.findByRole("option", { name: "Durian (unavailable)" });
    expect(durian.getAttribute("aria-disabled")).toBe("true");
    expect(durian.hasAttribute("data-disabled")).toBe(true);
    await user.click(durian);
    expect(onValueChange).not.toHaveBeenCalled();
    expect(screen.getByRole("listbox")).toBeTruthy();
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.getByRole("combobox", { name: "Fruit" }).textContent).toContain("Apple");
  });

  it("calls onValueChange and shows the new value after a selection", async () => {
    const user = userEvent.setup();
    const onValueChange = vi.fn();
    render(<Picker onValueChange={onValueChange} />);
    await user.click(screen.getByRole("combobox", { name: "Fruit" }));
    await user.click(await screen.findByRole("option", { name: "Pear" }));
    expect(onValueChange).toHaveBeenCalledWith("pear");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.getByRole("combobox", { name: "Fruit" }).textContent).toContain("Pear");
  });
});
