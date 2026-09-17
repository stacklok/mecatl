import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AgentMessage } from "@/features/agent/types";
import { MessageBubble } from "./message-bubble";
import { REASONING_CAVEAT } from "./reasoning-disclosure";

const assistant = (partial: Partial<AgentMessage> = {}): AgentMessage => ({
  id: "m1",
  role: "assistant",
  content: "",
  timestamp: 1_755_000_000_000,
  ...partial,
});

/**
 * The assistant bubble surfaces the model's reasoning: a collapsed
 * "Reasoning summary · N lines" disclosure above the turn's text, a live
 * "Reasoning…" line while the trailing turn streams with nothing else to
 * show, and a bubble for a reasoning-only turn (it never vanishes). The
 * stop-reason chip keeps an empty cancelled turn visible too.
 */
describe("MessageBubble reasoning", () => {
  it("renders the collapsed disclosure above the text and expands to the caveat", () => {
    render(
      <MessageBubble
        message={assistant({
          content: "The answer is 42.",
          reasoning: "Consider the question.\nCheck the book.\nAnswer.",
        })}
      />,
    );
    const toggle = screen.getByRole("button", {
      name: "Reasoning summary · 3 lines",
    });
    expect(screen.getByText("The answer is 42.")).toBeInTheDocument();
    expect(screen.queryByText(REASONING_CAVEAT)).toBeNull();
    fireEvent.click(toggle);
    expect(screen.getByText(REASONING_CAVEAT)).toBeInTheDocument();
    expect(screen.getByText(/Check the book\./)).toBeInTheDocument();
    // The disclosure sits above the content in document order.
    const disclosure = screen.getByTestId("reasoning-disclosure");
    const content = screen.getByText("The answer is 42.");
    expect(
      disclosure.compareDocumentPosition(content) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("renders a reasoning-only turn instead of dropping it", () => {
    render(
      <MessageBubble message={assistant({ reasoning: "Weighing options." })} />,
    );
    expect(
      screen.getByRole("button", { name: "Reasoning summary · 1 line" }),
    ).toBeInTheDocument();
  });

  it("shows the live Reasoning… line while the trailing turn streams with no text yet", () => {
    render(
      <MessageBubble
        message={assistant({ reasoning: "First step.\nSecond step." })}
        streaming
      />,
    );
    expect(screen.getByTestId("reasoning-live")).toBeInTheDocument();
    expect(screen.getByText("Reasoning…")).toBeInTheDocument();
    expect(screen.getByText("Second step.")).toBeInTheDocument();
    expect(screen.queryByTestId("reasoning-disclosure")).toBeNull();
  });

  it("collapses to the summary once text streams, and while a tool is running", () => {
    render(
      <MessageBubble
        message={assistant({ reasoning: "Plan.", content: "Doing it…" })}
        streaming
      />,
    );
    expect(screen.queryByTestId("reasoning-live")).toBeNull();
    expect(screen.getByTestId("reasoning-disclosure")).toBeInTheDocument();
  });

  it("collapses to the summary while a tool call is running", () => {
    render(
      <MessageBubble
        message={assistant({
          id: "m2",
          reasoning: "Plan.",
          toolCalls: [
            { callId: "c1", name: "Read", input: null, status: "running" },
          ],
        })}
        streaming
      />,
    );
    expect(screen.queryByTestId("reasoning-live")).toBeNull();
    expect(screen.getByTestId("reasoning-disclosure")).toBeInTheDocument();
  });

  it("renders no disclosure for a user message or a turn with no reasoning", () => {
    const { container } = render(
      <MessageBubble
        message={{
          id: "u1",
          role: "user",
          content: "hi",
          timestamp: 0,
          reasoning: "never mine",
        }}
      />,
    );
    expect(screen.queryByTestId("reasoning-disclosure")).toBeNull();
    expect(container).not.toBeEmptyDOMElement();
    render(<MessageBubble message={assistant({ content: "plain" })} />);
    expect(screen.queryByTestId("reasoning-disclosure")).toBeNull();
  });
});

describe("MessageBubble stop-reason chip on an empty cancelled turn", () => {
  it("renders the cancelled chip for a turn with no text (the turn never vanishes)", () => {
    const { container } = render(
      <MessageBubble message={assistant({ stopReason: "cancelled" })} />,
    );
    expect(container).not.toBeEmptyDOMElement();
    expect(screen.getByTestId("stop-reason-chip")).toHaveTextContent(
      "cancelled",
    );
  });

  it("renders the token-budget chip as a warning alongside the turn's text", () => {
    render(
      <MessageBubble
        message={assistant({ content: "Partial.", stopReason: "budget" })}
      />,
    );
    expect(screen.getByTestId("stop-reason-chip")).toHaveTextContent(
      "stopped · token budget",
    );
    expect(screen.getByText("Partial.")).toBeInTheDocument();
  });
});
