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

  it("points the active descendant at the automatically selected option", async () => {
    const user = userEvent.setup();
    render(<Palette onSelect={() => {}} />);
    const input = screen.getByPlaceholderText("Type a command");
    const listbox = screen.getByRole("listbox");
    const chat = screen.getByRole("option", { name: "Chat" });
    expect(chat.getAttribute("aria-selected")).toBe("true");
    expect(input.getAttribute("aria-activedescendant")).toBe(chat.id);
    expect(listbox.getAttribute("aria-activedescendant")).toBe(chat.id);
    await user.type(input, "mem");
    const memory = screen.getByRole("option", { name: "Memory" });
    expect(input.getAttribute("aria-activedescendant")).toBe(memory.id);
    expect(listbox.getAttribute("aria-activedescendant")).toBe(memory.id);
    await user.type(input, "zzz");
    expect(input.hasAttribute("aria-activedescendant")).toBe(false);
    expect(listbox.hasAttribute("aria-activedescendant")).toBe(false);
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

  it("keeps the title inside the dialog and can drop the description", async () => {
    const { rerender } = render(
      <CommandDialog description={null} open={false} title="Go to">
        <CommandInput placeholder="Search pages" />
      </CommandDialog>,
    );
    // A closed palette leaves no title behind in the page.
    expect(screen.queryByText("Go to")).toBeNull();
    rerender(
      <CommandDialog description={null} open title="Go to">
        <CommandInput placeholder="Search pages" />
      </CommandDialog>,
    );
    const dialog = await screen.findByRole("dialog", { name: "Go to" });
    expect(dialog.contains(screen.getByText("Go to"))).toBe(true);
    expect(dialog.hasAttribute("aria-describedby")).toBe(false);
    expect(screen.queryByText("Search for a command to run...")).toBeNull();
  });

  it("forwards cmdk and content props and renders input slots", async () => {
    const onOpenAutoFocus = vi.fn();
    render(
      <CommandDialog
        commandProps={{ label: "Pages", vimBindings: false }}
        contentProps={{ onOpenAutoFocus }}
        open
        title="Go to"
      >
        <CommandInput
          icon={<span data-testid="leading" />}
          placeholder="Search pages"
          trailing={<button type="button">Close pages</button>}
        />
        <CommandList>
          <CommandItem value="chat">Chat</CommandItem>
          <CommandItem value="memory">Memory</CommandItem>
        </CommandList>
      </CommandDialog>,
    );
    const input = await screen.findByPlaceholderText("Search pages");
    expect(onOpenAutoFocus).toHaveBeenCalled();
    expect(screen.getByTestId("leading").parentElement?.dataset.slot).toBe("command-input-wrapper");
    expect(screen.getByRole("button", { name: "Close pages" })).toBeTruthy();
    const labelId = input.getAttribute("aria-labelledby");
    expect(labelId && document.getElementById(labelId)?.textContent).toBe("Pages");
    const user = userEvent.setup();
    await user.click(input);
    await user.keyboard("{ArrowDown}");
    expect(screen.getByRole("option", { name: "Memory" }).getAttribute("aria-selected")).toBe(
      "true",
    );
    // With vim bindings off, Ctrl+K stays free for the app's own shortcut.
    await user.keyboard("{Control>}k{/Control}");
    expect(screen.getByRole("option", { name: "Memory" }).getAttribute("aria-selected")).toBe(
      "true",
    );
  });
});
