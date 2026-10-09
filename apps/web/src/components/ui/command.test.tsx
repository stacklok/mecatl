// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "./command";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

function Palette({ onSelect }: { onSelect: (value: string) => void }) {
  return (
    <Command label="Commands">
      <CommandInput placeholder="Type a command" />
      <CommandList>
        <CommandEmpty>No results.</CommandEmpty>
        <CommandGroup heading="Pages">
          <CommandItem onSelect={onSelect} value="chat">
            Chat
          </CommandItem>
          <CommandItem onSelect={onSelect} value="memory">
            Memory
          </CommandItem>
          <CommandItem onSelect={onSelect} value="schedules">
            Schedules
          </CommandItem>
        </CommandGroup>
      </CommandList>
    </Command>
  );
}

function optionNames() {
  return screen.queryAllByRole("option").map((option) => option.textContent);
}

describe("Command", () => {
  it("filters items as the user types and shows the empty state", async () => {
    const user = userEvent.setup();
    render(<Palette onSelect={() => {}} />);
    expect(optionNames()).toEqual(["Chat", "Memory", "Schedules"]);
    const input = screen.getByPlaceholderText("Type a command");
    expect(input.getAttribute("data-slot")).toBe("command-input");
    await user.type(input, "mem");
    expect(optionNames()).toEqual(["Memory"]);
    await user.clear(input);
    await user.type(input, "zzz");
    expect(optionNames()).toEqual([]);
    expect(screen.getByText("No results.")).toBeTruthy();
  });

  it("selects the highlighted item with the keyboard and by click", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Palette onSelect={onSelect} />);
    await user.type(screen.getByPlaceholderText("Type a command"), "sched{Enter}");
    expect(onSelect).toHaveBeenLastCalledWith("schedules");
    await user.clear(screen.getByPlaceholderText("Type a command"));
    await user.click(screen.getByRole("option", { name: "Chat" }));
    expect(onSelect).toHaveBeenLastCalledWith("chat");
  });

  it("renders inside a titled dialog", async () => {
    render(
      <CommandDialog open title="Go to">
        <CommandInput placeholder="Search pages" />
        <CommandList>
          <CommandItem>Chat</CommandItem>
        </CommandList>
      </CommandDialog>,
    );
    const dialog = await screen.findByRole("dialog", { name: "Go to" });
    expect(dialog.querySelector('[data-slot="command"]')).not.toBeNull();
    expect(screen.getByRole("option", { name: "Chat" })).toBeTruthy();
  });
});
