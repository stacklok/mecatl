import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AgentMessage } from "@/features/agent/types";
import {
  FailedTurnCard,
  PERMANENT_FAILURE_SUFFIX,
  summarizeFailure,
} from "./failed-turn-card";
import { MessageBubble } from "./message-bubble";

/**
 * The failed-turn card (the TUI's one-line `✗ …: retrying won't help` block):
 * a first-line summary capped at 120 characters, the permanent suffix, and a
 * Show details toggle that reveals the full text under `raw payload:`.
 */

describe("summarizeFailure", () => {
  it("takes the first non-empty line", () => {
    expect(summarizeFailure('\n  400 Bad Request\n{"error":{}}', false)).toBe(
      "400 Bad Request",
    );
  });

  it("clamps a long line to 120 characters with an ellipsis", () => {
    const long = "x".repeat(300);
    const summary = summarizeFailure(long, false);
    expect([...summary]).toHaveLength(120);
    expect(summary.endsWith("…")).toBe(true);
  });

  it("appends the permanent suffix and drops the hook's inline annotation", () => {
    expect(
      summarizeFailure(
        "context window exceeded (permanent — retrying the identical request cannot succeed)",
        true,
      ),
    ).toBe(`context window exceeded${PERMANENT_FAILURE_SUFFIX}`);
    expect(PERMANENT_FAILURE_SUFFIX).toContain("retrying won't help");
  });

  it("falls back to a generic line when the detail is blank", () => {
    expect(summarizeFailure(undefined, false)).toBe(
      "The run failed without a specific error.",
    );
    expect(summarizeFailure("   ", true)).toBe(
      `Permanent provider error${PERMANENT_FAILURE_SUFFIX}`,
    );
  });
});

describe("FailedTurnCard", () => {
  const raw =
    'POST "https://api.example/v1/responses": 400 Bad Request\n{"error":{"message":"context window exceeded"}}';

  it("renders a permanent failure as a one-line summary and reveals the raw payload on Show details", () => {
    render(<FailedTurnCard detail={raw} permanent />);
    expect(
      screen.getByText("This turn failed permanently"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        `POST "https://api.example/v1/responses": 400 Bad Request${PERMANENT_FAILURE_SUFFIX}`,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText("raw payload:")).not.toBeInTheDocument();

    const toggle = screen.getByRole("button", { name: "Show details" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("raw payload:")).toBeInTheDocument();
    // The full text, line breaks intact (getByText would collapse them).
    expect(document.querySelector("pre")?.textContent).toBe(raw);
    expect(
      screen.getByRole("button", { name: "Hide details" }),
    ).toBeInTheDocument();
  });

  it("offers no toggle when the summary already shows the whole detail", () => {
    render(<FailedTurnCard detail="upstream 503" />);
    expect(screen.getByText("This turn failed")).toBeInTheDocument();
    expect(screen.getByText("upstream 503")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });

  it("renders through MessageBubble for a permanent failed turn", () => {
    const message: AgentMessage = {
      id: "a1",
      role: "assistant",
      content: "",
      timestamp: 0,
      failed: true,
      failurePermanent: true,
      failureDetail:
        "context window exceeded (permanent — retrying the identical request cannot succeed)",
    };
    render(<MessageBubble message={message} />);
    expect(
      screen.getByText("This turn failed permanently"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(`context window exceeded${PERMANENT_FAILURE_SUFFIX}`),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Show details" }),
    ).toBeInTheDocument();
  });
});
