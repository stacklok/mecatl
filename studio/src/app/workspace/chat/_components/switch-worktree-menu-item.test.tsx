import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SWITCH_WORKTREE_LABEL,
  SwitchWorktreeMenuItem,
  SwitchWorktreeSheetItem,
} from "./switch-worktree-menu-item";

/**
 * Pins the two chat-menu entry points to the worktree picker: each renders
 * the shared label, asks the owner to open the dialog, and the sheet variant
 * also closes its sheet afterwards.
 */

describe("SwitchWorktreeMenuItem", () => {
  it("asks the owner to open the picker from the desktop dropdown", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <DropdownMenu open>
        <DropdownMenuTrigger>menu</DropdownMenuTrigger>
        <DropdownMenuContent>
          <SwitchWorktreeMenuItem onSelect={onSelect} />
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    await user.click(
      screen.getByRole("menuitem", { name: SWITCH_WORKTREE_LABEL }),
    );
    expect(onSelect).toHaveBeenCalledTimes(1);
  });
});

describe("SwitchWorktreeSheetItem", () => {
  it("opens the picker and then closes the sheet", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const onDone = vi.fn();
    render(<SwitchWorktreeSheetItem onSelect={onSelect} onDone={onDone} />);
    await user.click(
      screen.getByRole("button", { name: SWITCH_WORKTREE_LABEL }),
    );
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onDone).toHaveBeenCalledTimes(1);
  });
});
