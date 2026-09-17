import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AgentMessage } from "@/features/agent";
import { MessageBubble } from "./message-bubble";

/**
 * A user message the daemon applied as a mid-run steer (the drain echo
 * landed) carries a small "steered" marker next to its timestamp; an
 * ordinary prompt does not.
 */
describe("MessageBubble steered marker", () => {
  const base: AgentMessage = {
    id: "u1",
    role: "user",
    content: "focus on the tests",
    timestamp: 0,
  };

  it("labels a steered user message", () => {
    render(<MessageBubble message={{ ...base, steered: true }} />);
    expect(screen.getByText("steered")).toBeInTheDocument();
    expect(screen.getByText("focus on the tests")).toBeInTheDocument();
  });

  it("shows no marker on an ordinary prompt", () => {
    render(<MessageBubble message={base} />);
    expect(screen.queryByText("steered")).toBeNull();
  });
});
