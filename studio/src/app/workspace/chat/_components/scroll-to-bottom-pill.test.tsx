import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ScrollToBottomPill } from "./scroll-to-bottom-pill";

describe("ScrollToBottomPill", () => {
  it("shows the TUI-style ↑ NN% position cue", () => {
    render(<ScrollToBottomPill percent={43} onClick={() => {}} />);
    expect(screen.getByText("↑ 43%")).toBeInTheDocument();
  });

  it("names the control with its position for assistive tech", () => {
    render(<ScrollToBottomPill percent={43} onClick={() => {}} />);
    expect(
      screen.getByRole("button", {
        name: "Scroll to bottom — 43% through the conversation",
      }),
    ).toBeInTheDocument();
  });

  it("jumps to the bottom on click", () => {
    const onClick = vi.fn();
    render(<ScrollToBottomPill percent={12} onClick={onClick} />);
    fireEvent.click(screen.getByRole("button"));
    expect(onClick).toHaveBeenCalledTimes(1);
  });
});
