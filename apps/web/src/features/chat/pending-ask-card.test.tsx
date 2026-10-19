// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ApprovalRequest } from "./approval-panel";
import { PendingAskCard, pendingAskKey } from "./pending-ask-card";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

function ask(tool: string, runId = "run-1", withTarget = true): ApprovalRequest {
  return {
    args: '{"plan":"Ship safely"}',
    askId: "ask-1",
    controlTarget: withTarget ? { askId: "ask-1", runId, sessionId: "chat-1" } : undefined,
    reason: "Needs a decision",
    tool,
  };
}

describe("PendingAskCard", () => {
  it("renders PresentPlan as the plan review and anything else as the approval panel", () => {
    const controls = { onRespondToApproval: vi.fn(), onRespondToPlan: vi.fn() };
    const { rerender } = render(
      <PendingAskCard approval={ask("PresentPlan")} controls={controls} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Iterate" }));
    expect(controls.onRespondToPlan).toHaveBeenCalledWith(expect.anything(), "iterate");
    expect(screen.queryByRole("button", { name: "Allow once" })).toBeNull();

    rerender(<PendingAskCard approval={ask("Write")} controls={controls} />);
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    expect(controls.onRespondToApproval).toHaveBeenCalledWith(expect.anything(), "allow_once");
    expect(screen.queryByRole("region", { name: "Plan review" })).toBeNull();
  });

  it("disables an ask that has no control target, whatever the surface predicate says", () => {
    const controls = { approvalDisabled: () => false };
    render(<PendingAskCard approval={ask("Write", "run-1", false)} controls={controls} />);
    expect(screen.getByRole("button", { name: "Allow once" }).hasAttribute("disabled")).toBe(true);
  });

  it("follows the surface predicate when the ask has a control target", () => {
    const { rerender } = render(
      <PendingAskCard approval={ask("Write")} controls={{ approvalDisabled: () => true }} />,
    );
    expect(screen.getByRole("button", { name: "Allow once" }).hasAttribute("disabled")).toBe(true);
    rerender(
      <PendingAskCard approval={ask("Write")} controls={{ approvalDisabled: () => false }} />,
    );
    expect(screen.getByRole("button", { name: "Allow once" }).hasAttribute("disabled")).toBe(false);
  });

  it("keys on run and ask, so a new run's ask remounts its card", () => {
    expect(pendingAskKey(ask("Write", "run-1"))).toBe("run-1:ask-1");
    expect(pendingAskKey(ask("Write", "run-1", false))).toBe(":ask-1");
    const { rerender } = render(<PendingAskCard approval={ask("Write", "run-1")} controls={{}} />);
    const first = screen.getByRole("region");
    rerender(<PendingAskCard approval={ask("Write", "run-2")} controls={{}} />);
    expect(screen.getByRole("region")).not.toBe(first);
  });
});
