import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { PLAN_APPROVED_PROCEED_TEXT } from "@/features/agent/plan-ask";
import type { AgentMessage } from "@/features/agent/types";
import { HarnessNote } from "./harness-note";
import { MessageBubble } from "./message-bubble";

/**
 * The plan-approved proceed prompt is a recorded user turn the HARNESS
 * authored: it renders as a muted note with a "harness" chip, never as the
 * user's own bubble — whether this tab flagged it at send time or the same
 * text came back from the transcript unflagged.
 */

const user = (partial: Partial<AgentMessage> = {}): AgentMessage => ({
  id: "u1",
  role: "user",
  content: "hello",
  timestamp: 1_755_000_000_000,
  ...partial,
});

describe("HarnessNote", () => {
  it("shows the note text with the harness chip", () => {
    render(
      <HarnessNote
        message={user({ content: PLAN_APPROVED_PROCEED_TEXT, synthetic: true })}
      />,
    );
    expect(screen.getByTestId("harness-note")).toBeInTheDocument();
    expect(screen.getByText("harness")).toBeInTheDocument();
    expect(screen.getByText(PLAN_APPROVED_PROCEED_TEXT)).toBeInTheDocument();
  });
});

describe("MessageBubble harness note variant", () => {
  it("renders a synthetic user message as the harness note, not a user bubble", () => {
    render(
      <MessageBubble
        message={user({ content: PLAN_APPROVED_PROCEED_TEXT, synthetic: true })}
      />,
    );
    expect(screen.getByTestId("harness-note")).toBeInTheDocument();
    expect(screen.queryByText("You")).toBeNull();
  });

  it("recognises the replayed proceed text without the flag (a rehydrated transcript)", () => {
    render(
      <MessageBubble message={user({ content: PLAN_APPROVED_PROCEED_TEXT })} />,
    );
    expect(screen.getByTestId("harness-note")).toBeInTheDocument();
  });

  it("leaves an ordinary user message as the user's bubble", () => {
    render(<MessageBubble message={user({ content: "please proceed" })} />);
    expect(screen.queryByTestId("harness-note")).toBeNull();
    expect(screen.getByText("You")).toBeInTheDocument();
    expect(screen.getByText("please proceed")).toBeInTheDocument();
  });
});
