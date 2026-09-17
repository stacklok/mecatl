import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AgentMessage } from "@/features/agent";
import { MessageBubble } from "./message-bubble";

/**
 * The per-turn downstream provider route (`provider.route`, ADR 0210): an
 * assistant turn the gateway reported a route for carries a muted
 * `via <route>` marker next to its timestamp — the TUI's "via <route>"
 * footer. A turn with no reported route (a prompt-cache hit, an older
 * daemon, a reload — the daemon never logs the event) shows nothing, and
 * a user message never shows one.
 */
describe("MessageBubble provider route marker", () => {
  const assistant: AgentMessage = {
    id: "a1",
    role: "assistant",
    content: "Here is the answer.",
    timestamp: 0,
  };

  it("labels an assistant turn with the route the gateway reported", () => {
    render(<MessageBubble message={{ ...assistant, route: "Google" }} />);
    const marker = screen.getByText("via Google");
    expect(marker).toBeInTheDocument();
    expect(marker).toHaveAttribute(
      "title",
      "Downstream provider reported by the model gateway for this turn",
    );
    expect(screen.getByText("Here is the answer.")).toBeInTheDocument();
  });

  it("shows no marker when no route was reported", () => {
    render(<MessageBubble message={assistant} />);
    expect(screen.queryByText(/^via /)).toBeNull();
  });

  it("never labels a user message", () => {
    render(
      <MessageBubble
        message={{
          id: "u1",
          role: "user",
          content: "hello",
          timestamp: 0,
          route: "Google",
        }}
      />,
    );
    expect(screen.queryByText("via Google")).toBeNull();
  });
});
