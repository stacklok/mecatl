import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { DraftGreeting, STARTER_PROMPTS } from "./draft-greeting";

/**
 * The draft greeting has two pieces: the greeting heading (always) and the
 * starter-prompt chips (hideable — the `--no-banner` analogue). Pins the
 * prop's effect and the seed callback.
 */
describe("DraftGreeting", () => {
  function renderGreeting(
    overrides: Partial<React.ComponentProps<typeof DraftGreeting>> = {},
  ) {
    const onPickSeed = vi.fn();
    render(
      <DraftGreeting
        showStarterPrompts
        onPickSeed={onPickSeed}
        {...overrides}
      />,
    );
    return { onPickSeed };
  }

  it("renders the greeting heading", () => {
    renderGreeting();
    expect(
      screen.getByRole("heading", { name: "What can I help you with?" }),
    ).toBeInTheDocument();
  });

  it("hides every starter chip when showStarterPrompts is false", () => {
    renderGreeting({ showStarterPrompts: false });
    expect(
      screen.getByRole("heading", { name: "What can I help you with?" }),
    ).toBeInTheDocument();
    expect(screen.queryAllByRole("button")).toHaveLength(0);
  });

  it("seeds the composer with the clicked starter prompt", async () => {
    const { onPickSeed } = renderGreeting();
    const chips = screen.getAllByRole("button");
    expect(chips.map((c) => c.textContent)).toEqual([...STARTER_PROMPTS]);
    await userEvent.click(
      screen.getByRole("button", { name: STARTER_PROMPTS[1] }),
    );
    expect(onPickSeed).toHaveBeenCalledWith(STARTER_PROMPTS[1]);
  });
});
