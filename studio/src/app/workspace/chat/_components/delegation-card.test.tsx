import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type {
  AgentMessage,
  DelegationGroupInfo,
  DelegationInfo,
} from "@/features/agent";
import { DelegationCard, DelegationCardRow } from "./delegation-card";
import { MessageBubble } from "./message-bubble";

/**
 * The inline delegation cards are live for all three families: a team lane
 * shows `[lead]`, its supervisor state and a read-write cue; a parallel
 * branch pulses without a child id, marks the ★ winner and renders a failed
 * branch's cause; a subagent settles on its compact stop label. Sections get
 * group headers, a large team folds behind `+N more`, and with `onOpen` /
 * `onCancel` the cards are buttons a keyboard can reach.
 */

const card = (partial: Partial<DelegationInfo> = {}): DelegationInfo => ({
  kind: "subagent",
  label: "explore",
  detail: "",
  ...partial,
});

const assistant = (partial: Partial<AgentMessage> = {}): AgentMessage => ({
  id: "m1",
  role: "assistant",
  content: "",
  timestamp: 1_755_000_000_000,
  ...partial,
});

describe("DelegationCard", () => {
  it("renders a team lane with [lead], its idle state, and the read-write cue", () => {
    render(
      <DelegationCard
        delegation={card({
          kind: "team",
          label: "reviewer",
          lead: true,
          mutating: true,
          idle: true,
          toolCount: 2,
        })}
        group={{ kind: "team", teamId: "t1" }}
      />,
    );
    expect(
      screen.getByText("team: reviewer [lead] · idle · 2 tools"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("read-write")).toBeInTheDocument();
    // An idle lane is live but quiet: a static dot, not the running pulse.
    expect(screen.getByRole("status", { name: "idle" })).not.toHaveClass(
      "animate-pulse",
    );
  });

  it("shows a working team lane's current tool with a heartbeat", () => {
    render(
      <DelegationCard
        delegation={card({ kind: "team", label: "coder", lastTool: "Edit" })}
      />,
    );
    expect(screen.getByText("team: coder · Edit…")).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "running" })).toHaveClass(
      "animate-pulse",
    );
  });

  it("marks a benched team member as failed with its cause once the team ended", () => {
    render(
      <DelegationCard
        delegation={card({
          kind: "team",
          label: "tester",
          stopped: true,
          stopReason: "error",
          stop: "error",
          cause: "provider returned 500",
        })}
        group={{ kind: "team", done: true }}
      />,
    );
    expect(
      screen.getByText("team: tester · stopped — error"),
    ).toBeInTheDocument();
    expect(screen.getByText("provider returned 500")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("pulses a parallel branch that has no child id yet", () => {
    render(
      <DelegationCard
        delegation={card({
          kind: "parallel",
          label: "branch 1",
          parentCallId: "p1",
          branchIndex: 0,
          lastTool: "Read",
        })}
      />,
    );
    expect(screen.getByRole("status", { name: "running" })).toBeInTheDocument();
    expect(screen.getByText("parallel: branch 1 · Read")).toBeInTheDocument();
  });

  it("shows a running subagent's current MCP tool as Server · Tool", () => {
    render(
      <DelegationCard
        delegation={card({
          childId: "subagent-1",
          label: "triage",
          lastTool: "mcp__github__issue_write",
        })}
      />,
    );
    expect(
      screen.getByText("subagent: triage · GitHub · Issue write"),
    ).toBeInTheDocument();
  });

  it("stars the winning branch and reads its stop label", () => {
    render(
      <DelegationCard
        delegation={card({
          kind: "parallel",
          label: "fast",
          winner: true,
          stop: "end_turn",
          durationMs: 3_000,
          toolCount: 1,
        })}
        group={{ kind: "parallel", done: true, winner: 0 }}
      />,
    );
    expect(
      screen.getByText("★ parallel: fast · 1 tool · done in 3s"),
    ).toBeInTheDocument();
  });

  it("renders a failed branch with its cause line", () => {
    render(
      <DelegationCard
        delegation={card({
          kind: "parallel",
          label: "slow",
          failed: true,
          stop: "error",
          cause: "child failed: context window exceeded",
        })}
      />,
    );
    expect(screen.getByText("parallel: slow · failed")).toBeInTheDocument();
    expect(
      screen.getByText("child failed: context window exceeded"),
    ).toBeInTheDocument();
  });

  it("labels a capped subagent with the compact stop label", () => {
    render(
      <DelegationCard
        delegation={card({
          childId: "subagent-abcdef123456",
          stop: "max_turns",
          toolCount: 4,
          inputTokens: 1_200,
          outputTokens: 300,
        })}
      />,
    );
    const badge = screen.getByText(
      "subagent: explore · 4 tools · 1.5k tok · done (max-turns)",
    );
    expect(badge).toBeInTheDocument();
    // The tooltip carries the hash and the split token counters.
    expect(badge.closest("[title]")).toHaveAttribute(
      "title",
      expect.stringContaining("#123456"),
    );
    expect(badge.closest("[title]")).toHaveAttribute(
      "title",
      expect.stringContaining("↑1.2k ↓300"),
    );
  });

  it("becomes a button that opens the card when onOpen is given", () => {
    const onOpen = vi.fn();
    const group: DelegationGroupInfo = { kind: "team", teamId: "t1" };
    const lane = card({ kind: "team", label: "reviewer", lead: true });
    render(<DelegationCard delegation={lane} group={group} onOpen={onOpen} />);
    fireEvent.click(screen.getByRole("button", { name: "Open team reviewer" }));
    expect(onOpen).toHaveBeenCalledWith(lane, group);
  });

  it("offers a cancel control for a live child when onCancel is given", () => {
    const onCancel = vi.fn();
    render(
      <DelegationCard
        delegation={card({ childId: "child-1" })}
        onCancel={onCancel}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Cancel subagent explore" }),
    );
    expect(onCancel).toHaveBeenCalledWith("child-1");
  });

  it("hides the cancel control once the child ended or a cancel is in flight", () => {
    const onCancel = vi.fn();
    const { rerender } = render(
      <DelegationCard
        delegation={card({ childId: "child-1", cancelling: true })}
        onCancel={onCancel}
      />,
    );
    expect(screen.queryByRole("button", { name: /Cancel/ })).toBeNull();
    expect(screen.getByText(/cancelling…/)).toBeInTheDocument();
    rerender(
      <DelegationCard
        delegation={card({ childId: "child-1", stop: "end_turn" })}
        onCancel={onCancel}
      />,
    );
    expect(screen.queryByRole("button", { name: /Cancel/ })).toBeNull();
  });
});

describe("DelegationCardRow", () => {
  it("renders a team header above lead-first lanes and a parallel header above branches", () => {
    render(
      <DelegationCardRow
        delegations={[
          card({
            kind: "team",
            label: "coder",
            parentCallId: "t-call",
            teamId: "team-1",
          }),
          card({
            kind: "team",
            label: "lead",
            parentCallId: "t-call",
            teamId: "team-1",
            lead: true,
          }),
          card({
            kind: "parallel",
            label: "fast",
            parentCallId: "p-call",
            branchIndex: 0,
            stop: "end_turn",
            winner: true,
          }),
        ]}
        groups={{
          "t-call": { kind: "team", teamId: "team-1" },
          "p-call": {
            kind: "parallel",
            join: "first",
            branchCount: 2,
            winner: 0,
            stop: "end_turn",
            done: true,
          },
        }}
      />,
    );
    expect(screen.getByText("team team-1 · 2 members")).toBeInTheDocument();
    expect(
      screen.getByText("parallel · first · 1/2 done · ★ fast · run stop: done"),
    ).toBeInTheDocument();
    const labels = screen
      .getAllByText(/^(★ )?(team|parallel): /)
      .map((el) => el.textContent);
    expect(labels).toEqual([
      "team: lead [lead] · working…",
      "team: coder · working…",
      "★ parallel: fast · done",
    ]);
  });

  it("folds a large team behind a +N more badge that opens the group", () => {
    const onOpen = vi.fn();
    const lanes = Array.from({ length: 8 }, (_, i) =>
      card({
        kind: "team",
        label: `member-${i}`,
        parentCallId: "t-call",
        teamId: "team-1",
      }),
    );
    const group: DelegationGroupInfo = { kind: "team", teamId: "team-1" };
    render(
      <DelegationCardRow
        delegations={lanes}
        groups={{ "t-call": group }}
        onOpen={onOpen}
      />,
    );
    expect(
      screen.getAllByRole("button", { name: /^Open team member-/ }),
    ).toHaveLength(6);
    expect(screen.queryByText(/member-7/)).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Open team members (2 more)" }),
    );
    expect(onOpen).toHaveBeenCalledWith(
      expect.objectContaining({ label: "member-6" }),
      group,
    );
  });
});

describe("MessageBubble delegation row", () => {
  it("renders a turn that only delegated (no text, no tool cards)", () => {
    render(
      <MessageBubble
        message={assistant({
          delegations: [card({ childId: "c1", toolCount: 1 })],
        })}
      />,
    );
    expect(screen.getByText("subagent: explore · 1 tool")).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "running" })).toBeInTheDocument();
  });

  it("threads onCancelDelegation to a running card's cancel control and offers none on a done card", () => {
    const onCancel = vi.fn();
    render(
      <MessageBubble
        message={assistant({
          delegations: [
            card({ childId: "c-live" }),
            card({
              childId: "c-done",
              label: "finished",
              stop: "end_turn",
              durationMs: 1000,
            }),
          ],
        })}
        onCancelDelegation={onCancel}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Cancel subagent explore" }),
    );
    expect(onCancel).toHaveBeenCalledWith("c-live");
    expect(
      screen.queryByRole("button", { name: "Cancel subagent finished" }),
    ).toBeNull();
  });

  it("threads onOpenDelegation and the message's groups to the cards", () => {
    const onOpen = vi.fn();
    const group: DelegationGroupInfo = {
      kind: "parallel",
      join: "all",
      branchCount: 1,
    };
    const branch = card({
      kind: "parallel",
      label: "only",
      parentCallId: "p-call",
      branchIndex: 0,
    });
    render(
      <MessageBubble
        message={assistant({
          delegations: [branch],
          delegationGroups: { "p-call": group },
        })}
        onOpenDelegation={onOpen}
      />,
    );
    expect(screen.getByText("parallel · all · 0/1 done")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open parallel only" }));
    expect(onOpen).toHaveBeenCalledWith(branch, group);
  });

  it("offers Inspect on a card with a child session id and threads it from the bubble", () => {
    const onInspect = vi.fn();
    render(
      <MessageBubble
        message={assistant({
          delegations: [
            card({ childId: "subagent-1", stop: "end_turn" }),
            // A branch before its branch_end has no session yet: no Inspect.
            card({ kind: "parallel", label: "pending", branchIndex: 0 }),
          ],
        })}
        onInspectDelegation={onInspect}
      />,
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Inspect subagent explore" }),
    );
    expect(onInspect).toHaveBeenCalledWith("subagent-1", "subagent: explore");
    expect(
      screen.queryByRole("button", { name: "Inspect parallel pending" }),
    ).toBeNull();
  });
});
