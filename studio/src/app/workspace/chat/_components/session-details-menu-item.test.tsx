import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SESSION_DETAILS_LABEL,
  SessionDetailsMenuItem,
  SessionDetailsSheetItem,
} from "./session-details-menu-item";

/**
 * Pins the two chat-menu entry points to the session details dialog: each
 * renders the shared label, asks the owner to open the dialog, and the sheet
 * variant also closes its sheet afterwards.
 */

describe("SessionDetailsMenuItem", () => {
  it("asks the owner to open the dialog from the desktop dropdown", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <DropdownMenu open>
        <DropdownMenuTrigger>menu</DropdownMenuTrigger>
        <DropdownMenuContent>
          <SessionDetailsMenuItem onSelect={onSelect} />
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    await user.click(
      screen.getByRole("menuitem", { name: SESSION_DETAILS_LABEL }),
    );
    expect(onSelect).toHaveBeenCalledTimes(1);
  });
});

describe("SessionDetailsSheetItem", () => {
  it("opens the dialog and then closes the sheet", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const onDone = vi.fn();
    render(<SessionDetailsSheetItem onSelect={onSelect} onDone={onDone} />);
    await user.click(
      screen.getByRole("button", { name: SESSION_DETAILS_LABEL }),
    );
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onDone).toHaveBeenCalledTimes(1);
  });
});
