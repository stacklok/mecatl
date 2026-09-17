import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { StatusLine } from "./status-line";

/**
 * The transient status line: a warning-toned advisory (no-progress nudge,
 * recover notice, a limit stop) reads as a live status region, a muted cue
 * (cancelled) stays quiet, and daemon-influenced text is clamped to one line.
 */
describe("StatusLine", () => {
  it("renders a warning advisory as a live status region", () => {
    render(
      <StatusLine
        status={{
          text: "no progress after continuation attempts; ending run",
          tone: "warn",
          kind: "no_progress",
        }}
      />,
    );
    const line = screen.getByRole("status");
    expect(line).toHaveAttribute("data-status-kind", "no_progress");
    expect(line).toHaveAttribute("data-status-tone", "warn");
    expect(
      screen.getByText("no progress after continuation attempts; ending run"),
    ).toHaveClass("text-warning");
  });

  it("renders a muted stop cue without the warning colour", () => {
    render(
      <StatusLine
        status={{ text: "cancelled", tone: "muted", kind: "stop" }}
      />,
    );
    expect(screen.getByText("cancelled")).not.toHaveClass("text-warning");
    expect(screen.getByRole("status")).toHaveAttribute(
      "data-status-kind",
      "stop",
    );
  });

  it("folds a multi-line daemon advisory onto one line", () => {
    render(
      <StatusLine
        status={{
          text: "recovered from a permanent failure\nretrying may fail again",
          tone: "warn",
          kind: "recover_notice",
        }}
      />,
    );
    expect(
      screen.getByText(
        "recovered from a permanent failure retrying may fail again",
      ),
    ).toBeInTheDocument();
  });

  it("renders nothing for an advisory with no visible text", () => {
    const { container } = render(
      <StatusLine
        status={{ text: "   ", tone: "warn", kind: "no_progress" }}
      />,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
