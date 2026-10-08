// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ApprovalRequest } from "./approval-panel";
import type { ChatMessage } from "./chat-state";
import { ChatTranscript } from "./chat-transcript";

afterEach(cleanup);

const message: ChatMessage = {
  content: "I prepared the deployment plan.",
  id: "plan-turn",
  role: "assistant",
  tools: [{ args: '{"plan":"Ship safely"}', id: "call-plan", name: "PresentPlan", runId: "run-1" }],
};

function plan(args: string): ApprovalRequest {
  return {
    args,
    askId: "ask-1",
    callId: "call-plan",
    controlTarget: { askId: "ask-1", runId: "run-1", sessionId: "chat-1" },
    reason: "Review this plan",
    tool: "PresentPlan",
  };
}

describe("plan review", () => {
  it("renders the presented plan and three distinct verdicts", () => {
    const respond = vi.fn();
    render(
      <ChatTranscript
        approvals={[plan('{"plan":"Ship safely","note":"Check the rollout"}')]}
        messages={[message]}
        onRespondToPlan={respond}
        showToolCalls
      />,
    );
    const card = screen.getByRole("region", { name: "Plan review" });
    expect(within(card).getByText("Ship safely")).toBeTruthy();
    expect(within(card).getByText("Check the rollout")).toBeTruthy();
    expect(screen.getByText("I prepared the deployment plan.")).toBeTruthy();
    for (const [label, verdict] of [
      ["Approve & run", "approve"],
      ["Auto-accept edits", "accept_edits"],
      ["Iterate", "iterate"],
    ] as const) {
      fireEvent.click(within(card).getByRole("button", { name: label }));
      expect(respond).toHaveBeenLastCalledWith(
        expect.objectContaining({ askId: "ask-1" }),
        verdict,
      );
    }
    fireEvent.click(within(card).getByText("Raw arguments"));
    expect(
      within(card).getByText('{"plan":"Ship safely","note":"Check the rollout"}'),
    ).toBeTruthy();
  });

  it("allows iteration when the plan text is malformed", () => {
    const respond = vi.fn();
    render(
      <ChatTranscript
        approvals={[plan('{"plan":42,"note":"Still reviewable"}')]}
        messages={[message]}
        onRespondToPlan={respond}
        showToolCalls
      />,
    );
    const card = screen.getByRole("region", { name: "Plan review" });
    expect(within(card).getByText("Still reviewable")).toBeTruthy();
    expect(
      within(card).getByRole("button", { name: "Approve & run" }).hasAttribute("disabled"),
    ).toBe(true);
    expect(
      within(card).getByRole("button", { name: "Auto-accept edits" }).hasAttribute("disabled"),
    ).toBe(true);
    fireEvent.click(within(card).getByRole("button", { name: "Iterate" }));
    expect(respond).toHaveBeenCalledWith(expect.objectContaining({ askId: "ask-1" }), "iterate");
  });
});
