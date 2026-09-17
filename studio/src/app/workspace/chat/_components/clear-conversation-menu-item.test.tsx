import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { CLEAR_CONVERSATION_LABEL } from "./clear-conversation";
import {
  ClearConversationMenuItem,
  ClearConversationSheetItem,
} from "./clear-conversation-menu-item";

/**
 * Pins the two chat-menu entry points for Clear conversation: each renders
 * the shared label and asks the owner to run the handoff; a disabled reason
 * disables the control and shows the daemon's plain words; the sheet variant
 * closes its sheet afterwards.
 */

describe("ClearConversationMenuItem", () => {
  it("asks the owner to clear from the desktop dropdown", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(
      <DropdownMenu open>
        <DropdownMenuTrigger>menu</DropdownMenuTrigger>
        <DropdownMenuContent>
          <ClearConversationMenuItem onSelect={onSelect} />
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    await user.click(
      screen.getByRole("menuitem", { name: CLEAR_CONVERSATION_LABEL }),
    );
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("renders disabled with the daemon's reason when one is given", () => {
    render(
      <DropdownMenu open>
        <DropdownMenuTrigger>menu</DropdownMenuTrigger>
        <DropdownMenuContent>
          <ClearConversationMenuItem
            onSelect={() => {}}
            disabledReason="Running in another client — stop it there first"
          />
        </DropdownMenuContent>
      </DropdownMenu>,
    );
    const item = screen.getByRole("menuitem", {
      name: /Clear conversation/,
    });
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(
      screen.getByText("Running in another client — stop it there first"),
    ).toBeInTheDocument();
  });
});

describe("ClearConversationSheetItem", () => {
  it("clears and then closes the sheet", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const onDone = vi.fn();
    render(<ClearConversationSheetItem onSelect={onSelect} onDone={onDone} />);
    await user.click(
      screen.getByRole("button", { name: CLEAR_CONVERSATION_LABEL }),
    );
    expect(onSelect).toHaveBeenCalledTimes(1);
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("is a disabled button carrying the reason", () => {
    render(
      <ClearConversationSheetItem
        onSelect={() => {}}
        disabledReason="The agent did not say whether this chat can be cleared"
      />,
    );
    const button = screen.getByRole("button", { name: /Clear conversation/ });
    expect(button).toBeDisabled();
    expect(button).toHaveTextContent(
      "The agent did not say whether this chat can be cleared",
    );
  });
});
