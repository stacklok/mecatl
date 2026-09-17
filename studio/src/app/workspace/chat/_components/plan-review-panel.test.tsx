import { fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ApprovalRequest } from "@/features/agent";
import { memoryStorage } from "@/test/memory-storage";
import { ApprovalDetailPanel } from "./approval-detail-panel";
import { ApprovalPanel } from "./approval-panel";
import {
  PLAN_VERDICTS,
  PlanReviewCard,
  PlanReviewDetailPanel,
} from "./plan-review-panel";

const planAsk: ApprovalRequest = {
  approvalId: "s1:1:c1:r1",
  sessionId: "s1",
  toolName: "PresentPlan",
  description: "PresentPlan needs your approval.",
  reason: "The agent presented a plan.",
  args: JSON.stringify({
    plan: "# Rollout plan\n\n1. Ship the flag\n2. Watch the dashboards",
    note: "two steps",
  }),
  details: "The agent presented a plan.\n\nplan: # Rollout plan …",
};

// The side panel frame persists its width in localStorage; this vitest
// environment's storage shim is method-less, so a real in-memory Storage is
// stubbed per test (the global afterEach unstubs it).
beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
});

describe("PLAN_VERDICTS", () => {
  it("maps the three plan outcomes onto the ordinary verdict choices, in order", () => {
    expect(PLAN_VERDICTS.map((verdict) => verdict.choice)).toEqual([
      "once",
      "always",
      "deny",
    ]);
    expect(PLAN_VERDICTS.map((verdict) => verdict.label)).toEqual([
      "Approve & run",
      "Auto-accept edits",
      "Iterate",
    ]);
  });
});

describe("PlanReviewCard", () => {
  it("renders the plan as markdown under a Plan review header with the reason", () => {
    render(<PlanReviewCard approval={planAsk} onRespond={() => {}} />);
    expect(screen.getByText("Plan review")).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: "Rollout plan" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Ship the flag")).toBeInTheDocument();
    expect(screen.getByText("The agent presented a plan.")).toBeInTheDocument();
    // Not a permission ask: no shield copy, no tool-name pill.
    expect(screen.queryByText("Permission required")).toBeNull();
    expect(screen.queryByText("PresentPlan")).toBeNull();
  });

  it("routes each plan verdict to its ordinary choice and explains what each does", () => {
    const onRespond = vi.fn();
    render(<PlanReviewCard approval={planAsk} onRespond={onRespond} />);
    fireEvent.click(screen.getByRole("button", { name: "Approve & run" }));
    fireEvent.click(screen.getByRole("button", { name: "Auto-accept edits" }));
    fireEvent.click(screen.getByRole("button", { name: "Iterate" }));
    expect(onRespond.mock.calls.map(([choice]) => choice)).toEqual([
      "once",
      "always",
      "deny",
    ]);
    expect(
      screen.getByRole("button", { name: "Approve & run" }),
    ).toHaveAttribute("title", "Switch to Manual mode and start executing");
    expect(
      screen.getByRole("button", { name: "Auto-accept edits" }),
    ).toHaveAttribute(
      "title",
      "Approve and switch to Accept edits for the execution run",
    );
    expect(screen.getByRole("button", { name: "Iterate" })).toHaveAttribute(
      "title",
      "Send the plan back for another pass",
    );
  });

  it("falls back to the note when the plan is empty, and to the verbatim args when neither decodes", () => {
    render(
      <PlanReviewCard
        approval={{
          ...planAsk,
          args: JSON.stringify({ plan: "", note: "just a note" }),
        }}
        onRespond={() => {}}
      />,
    );
    expect(screen.getByText("just a note")).toBeInTheDocument();

    render(
      <PlanReviewCard
        approval={{ ...planAsk, reason: undefined, args: "{not json" }}
        onRespond={() => {}}
      />,
    );
    expect(screen.getByText("{not json")).toBeInTheDocument();
    expect(
      screen.getByText(
        "The agent presented a plan and is waiting for your review.",
      ),
    ).toBeInTheDocument();
  });

  it("shows the queue position and the Expand button when offered", () => {
    const onExpand = vi.fn();
    render(
      <PlanReviewCard
        approval={planAsk}
        onRespond={() => {}}
        queuePosition={{ index: 1, total: 2 }}
        onExpand={onExpand}
      />,
    );
    expect(screen.getByText("1 of 2")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Expand plan review" }));
    expect(onExpand).toHaveBeenCalledWith(planAsk);
  });
});

describe("PlanReviewDetailPanel", () => {
  it("fills the side panel with the rendered plan and answers-then-closes on a verdict", () => {
    const onRespond = vi.fn();
    const onClose = vi.fn();
    render(
      <PlanReviewDetailPanel
        approval={planAsk}
        onRespond={onRespond}
        onClose={onClose}
        maximized={false}
        onToggleMaximize={() => {}}
      />,
    );
    expect(
      screen.getByRole("heading", { name: "Rollout plan" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Close plan review" }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Iterate" }));
    expect(onRespond).toHaveBeenCalledWith("deny");
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});

describe("plan asks route through the generic approval surfaces", () => {
  it("ApprovalPanel renders a PresentPlan ask as the plan review card", () => {
    const onRespond = vi.fn();
    render(<ApprovalPanel approval={planAsk} onRespond={onRespond} />);
    expect(screen.getByText("Plan review")).toBeInTheDocument();
    expect(screen.queryByText("Permission required")).toBeNull();
    expect(screen.queryByRole("button", { name: "Allow once" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Approve & run" }));
    expect(onRespond).toHaveBeenCalledWith("once");
  });

  it("ApprovalPanel still renders an ordinary ask as a permission ask", () => {
    render(
      <ApprovalPanel
        approval={{ ...planAsk, toolName: "Bash", args: '{"command":"ls"}' }}
        onRespond={() => {}}
      />,
    );
    expect(screen.getByText("Permission required")).toBeInTheDocument();
    expect(screen.queryByText("Plan review")).toBeNull();
  });

  it("ApprovalDetailPanel opens a PresentPlan ask as the full-height plan review", () => {
    render(
      <ApprovalDetailPanel
        approval={planAsk}
        onRespond={() => {}}
        onClose={() => {}}
        maximized={false}
        onToggleMaximize={() => {}}
      />,
    );
    expect(
      screen.getByRole("button", { name: "Close plan review" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("PresentPlan — permission ask")).toBeNull();
    expect(
      screen.getByRole("button", { name: "Auto-accept edits" }),
    ).toBeInTheDocument();
  });
});
