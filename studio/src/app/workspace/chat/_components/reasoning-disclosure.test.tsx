import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  REASONING_CAVEAT,
  ReasoningDisclosure,
  reasoningLines,
} from "./reasoning-disclosure";

const THREE_LINES = "Look at the tests first.\n\nThen the reducer.\nThen ship.";

/**
 * The reasoning disclosure: a collapsed "Reasoning summary · N lines" header
 * that expands to the text under its caveat, and — while the trailing turn
 * is still streaming with nothing else to show — a pulsing "Reasoning…" line
 * previewing the latest reasoning.
 */
describe("ReasoningDisclosure", () => {
  it("renders the collapsed header with the non-empty line count", () => {
    render(<ReasoningDisclosure reasoning={THREE_LINES} />);
    const toggle = screen.getByRole("button", {
      name: "Reasoning summary · 3 lines",
    });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(REASONING_CAVEAT)).toBeNull();
    expect(screen.queryByText(/Look at the tests first/)).toBeNull();
  });

  it("expands on click to show the caveat and the text, and collapses again", () => {
    render(<ReasoningDisclosure reasoning={THREE_LINES} />);
    const toggle = screen.getByRole("button", {
      name: "Reasoning summary · 3 lines",
    });
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText(REASONING_CAVEAT)).toBeInTheDocument();
    expect(screen.getByText(/Look at the tests first/)).toBeInTheDocument();
    // aria-controls names the revealed body.
    const bodyId = toggle.getAttribute("aria-controls");
    expect(bodyId).toBeTruthy();
    expect(document.getElementById(bodyId ?? "")).not.toBeNull();
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText(REASONING_CAVEAT)).toBeNull();
  });

  it("uses the singular for one line and honours defaultOpen", () => {
    render(<ReasoningDisclosure reasoning="Just one thought." defaultOpen />);
    const toggle = screen.getByRole("button", {
      name: "Reasoning summary · 1 line",
    });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("Just one thought.")).toBeInTheDocument();
  });

  it("renders the pulsing live line previewing the latest reasoning line while live", () => {
    render(<ReasoningDisclosure reasoning={THREE_LINES} live />);
    const live = screen.getByTestId("reasoning-live");
    expect(live).toHaveAttribute("role", "status");
    expect(screen.getByText("Reasoning…")).toBeInTheDocument();
    expect(screen.getByText("Then ship.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("clamps the live preview to one bounded line", () => {
    const long = `first\n${"x".repeat(200)}\tforged[31m`;
    render(<ReasoningDisclosure reasoning={long} live />);
    const preview = screen.getByTestId("reasoning-live").lastElementChild;
    expect(preview?.textContent).toBe(`${"x".repeat(159)}…`);
  });

  it("renders nothing for whitespace-only reasoning", () => {
    const { container } = render(<ReasoningDisclosure reasoning={"  \n \n"} />);
    expect(container).toBeEmptyDOMElement();
  });
});

describe("reasoningLines", () => {
  it("counts non-empty trimmed lines across CRLF and blank lines", () => {
    expect(reasoningLines("a\r\n\r\n  b  \n\nc\n")).toEqual(["a", "b", "c"]);
    expect(reasoningLines("")).toEqual([]);
  });
});
