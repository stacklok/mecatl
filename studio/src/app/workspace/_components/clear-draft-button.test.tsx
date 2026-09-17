import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import {
  CLEAR_DRAFT_LABEL,
  CLEAR_DRAFT_TITLE,
  ClearDraftButton,
} from "./clear-draft-button";

/**
 * The composer's × (the TUI's ctrl+u ClearPrompt as a button): present only
 * while something is staged, labelled for assistive tech, hinting the chord
 * on hover, and inert while the composer is read-only.
 */
describe("ClearDraftButton", () => {
  it("renders nothing while the composer is empty", () => {
    const { container } = render(
      <ClearDraftButton hasDraft={false} onClear={() => {}} />,
    );
    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("is a labelled button that fires onClear once per click", async () => {
    const onClear = vi.fn();
    render(<ClearDraftButton hasDraft onClear={onClear} />);
    const button = screen.getByRole("button", { name: CLEAR_DRAFT_LABEL });
    // The hover hint names the chord that does the same thing.
    expect(button).toHaveAttribute("title", CLEAR_DRAFT_TITLE);
    expect(CLEAR_DRAFT_TITLE).toBe("Clear draft (⌘⇧U)");
    // Never a submit button — the composer sits inside no form, but a
    // future one must not send on ×.
    expect(button).toHaveAttribute("type", "button");
    await userEvent.click(button);
    expect(onClear).toHaveBeenCalledTimes(1);
  });

  it("is reachable and operable from the keyboard", async () => {
    const onClear = vi.fn();
    render(<ClearDraftButton hasDraft onClear={onClear} />);
    await userEvent.tab();
    expect(
      screen.getByRole("button", { name: CLEAR_DRAFT_LABEL }),
    ).toHaveFocus();
    await userEvent.keyboard("{Enter}");
    expect(onClear).toHaveBeenCalledTimes(1);
  });

  it("is disabled with the composer", async () => {
    const onClear = vi.fn();
    render(<ClearDraftButton hasDraft disabled onClear={onClear} />);
    const button = screen.getByRole("button", { name: CLEAR_DRAFT_LABEL });
    expect(button).toBeDisabled();
    await userEvent.click(button);
    expect(onClear).not.toHaveBeenCalled();
  });
});
