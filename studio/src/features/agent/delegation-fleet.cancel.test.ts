import { describe, expect, it } from "vitest";
import {
  applyDelegationEvent,
  type DelegationFleet,
  emptyFleet,
  markCancelling,
  markCardCancelling,
  reduceDelegationFleet,
} from "./delegation-fleet";
import type { AgentMessage, StreamEvent } from "./types";

/**
 * The optimistic `cancelling…` flag a per-child cancel sets is cleared by the
 * child's OWN terminal, never by a guess: `delegation_end` for a subagent or
 * a parallel branch, the team's `team_end` disposition for a member. Both
 * the session fleet (Agents panel) and the turn's card row agree.
 */

const fold = (events: StreamEvent[], from = emptyFleet()): DelegationFleet =>
  events.reduce(reduceDelegationFleet, from);

const turn = (): AgentMessage[] => [
  { id: "u1", role: "user", content: "go", timestamp: 1 },
  { id: "a1", role: "assistant", content: "", timestamp: 2 },
];

const subagentStart: StreamEvent = {
  type: "delegation",
  kind: "subagent",
  label: "explore",
  detail: "",
  childId: "subagent-1",
  parentCallId: "call-s",
};

const branchStart: StreamEvent = {
  type: "delegation",
  kind: "parallel",
  label: "fast",
  detail: "",
  childId: "parallel-2",
  parentCallId: "call-p",
  branchIndex: 1,
};

const memberStart: StreamEvent = {
  type: "delegation",
  kind: "team",
  label: "alice (lead)",
  detail: "",
  parentCallId: "call-team",
  teamId: "team-1",
  memberName: "alice",
  lead: true,
};

const memberFrame: StreamEvent = {
  type: "team_member",
  teamId: "team-1",
  parentCallId: "call-team",
  member: "alice",
  memberSessionId: "team-team-1-alice",
  innerKind: "tool.call",
  toolName: "Read",
  isError: false,
  contextUsed: 0,
  contextWindow: 0,
};

const teamEnd: StreamEvent = {
  type: "team_end",
  teamId: "team-1",
  parentCallId: "call-team",
  rounds: 2,
  stop: "end_turn",
  tasks: [],
  findings: [],
  dispositions: [
    { name: "alice", stopped: true, errorRounds: 0, reason: "cancelled" },
  ],
};

describe("cancelling flag lifecycle — fleet", () => {
  it("a subagent's delegation_end clears its optimistic cancel", () => {
    const marked = markCancelling(fold([subagentStart]), "subagent-1", true);
    expect(marked.subagents[0].cancelling).toBe(true);
    const ended = reduceDelegationFleet(marked, {
      type: "delegation_end",
      childId: "subagent-1",
      stop: "cancelled",
    });
    expect(ended.subagents[0]).toMatchObject({ stop: "cancelled" });
    expect(ended.subagents[0].cancelling).toBeUndefined();
  });

  it("a parallel branch's branch_end clears the flag on that branch only", () => {
    const fleet = fold([
      {
        type: "parallel_start",
        parentCallId: "call-p",
        join: "first",
        branchCount: 2,
      },
      {
        type: "delegation",
        kind: "parallel",
        label: "slow",
        detail: "",
        childId: "parallel-1",
        parentCallId: "call-p",
        branchIndex: 0,
      },
      branchStart,
    ]);
    const marked = markCancelling(
      markCancelling(fleet, "parallel-1", true),
      "parallel-2",
      true,
    );
    const ended = reduceDelegationFleet(marked, {
      type: "delegation_end",
      childId: "parallel-2",
      parentCallId: "call-p",
      branchIndex: 1,
      stop: "cancelled",
    });
    const branches = ended.parallelGroups[0].branches;
    expect(branches.find((c) => c.childId === "parallel-1")?.cancelling).toBe(
      true,
    );
    expect(
      branches.find((c) => c.childId === "parallel-2")?.cancelling,
    ).toBeUndefined();
  });

  it("a team member is addressable once team.member backfills its session id, and team_end's disposition clears the flag", () => {
    const beforeId = fold([memberStart]);
    // No session id yet → nothing to mark (the UI shows the control disabled).
    expect(markCancelling(beforeId, "team-team-1-alice", true)).toBe(beforeId);

    const fleet = fold([memberFrame], beforeId);
    const marked = markCancelling(fleet, "team-team-1-alice", true);
    expect(marked.teams[0].lanes[0]).toMatchObject({
      childId: "team-team-1-alice",
      cancelling: true,
    });
    const ended = reduceDelegationFleet(marked, teamEnd);
    expect(ended.teams[0].lanes[0]).toMatchObject({
      stopped: true,
      stopReason: "cancelled",
    });
    expect(ended.teams[0].lanes[0].cancelling).toBeUndefined();
  });
});

describe("cancelling flag lifecycle — turn cards", () => {
  it("a subagent card's delegation_end clears its optimistic cancel", () => {
    const messages = applyDelegationEvent(turn(), subagentStart, {
      assistantId: "a1",
    });
    const marked = markCardCancelling(messages, "subagent-1", true);
    expect(marked[1].delegations?.[0].cancelling).toBe(true);
    const ended = applyDelegationEvent(marked, {
      type: "delegation_end",
      childId: "subagent-1",
      stop: "cancelled",
    });
    expect(ended[1].delegations?.[0]).toMatchObject({ stop: "cancelled" });
    expect(ended[1].delegations?.[0].cancelling).toBeUndefined();
  });

  it("a team member card follows the same backfill-then-disposition path", () => {
    let messages = applyDelegationEvent(turn(), memberStart, {
      assistantId: "a1",
    });
    messages = applyDelegationEvent(messages, memberFrame);
    const marked = markCardCancelling(messages, "team-team-1-alice", true);
    expect(marked[1].delegations?.[0]).toMatchObject({
      childId: "team-team-1-alice",
      cancelling: true,
    });
    const ended = applyDelegationEvent(marked, teamEnd);
    expect(ended[1].delegations?.[0]).toMatchObject({
      stopped: true,
      stopReason: "cancelled",
    });
    expect(ended[1].delegations?.[0].cancelling).toBeUndefined();
  });
});
