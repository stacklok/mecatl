import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ContinueLatestChip } from "./continue-latest-chip";

/**
 * The Continue chip is the no-configuration path to the most recent chat:
 * present exactly when there is a pick, named by the chat's title and age,
 * and a single activation hands the id back.
 */
describe("ContinueLatestChip", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("renders nothing without a pick", () => {
    const { container } = render(
      <ContinueLatestChip latest={null} onContinue={() => {}} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("names the chat and its age, and continues it on click", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    vi.setSystemTime(new Date("2026-09-17T12:00:00Z"));
    const onContinue = vi.fn();
    render(
      <ContinueLatestChip
        latest={{
          id: "chat-1",
          title: "Fix the flaky scheduler test",
          updatedAt: Date.now() - 2 * 3_600_000,
        }}
        onContinue={onContinue}
      />,
    );
    const chip = screen.getByRole("button", { name: /^Continue/ });
    expect(chip).toHaveTextContent(
      "Continue “Fix the flaky scheduler test” · 2h",
    );
    await userEvent.click(chip);
    expect(onContinue).toHaveBeenCalledWith("chat-1");
  });

  it("falls back to Untitled chat and omits the age it cannot state", () => {
    render(
      <ContinueLatestChip
        latest={{ id: "chat-2", title: "   ", updatedAt: 0 }}
        onContinue={() => {}}
      />,
    );
    const chip = screen.getByRole("button", { name: /^Continue/ });
    expect(chip).toHaveTextContent("Continue “Untitled chat”");
    expect(chip).not.toHaveTextContent("·");
  });
});
