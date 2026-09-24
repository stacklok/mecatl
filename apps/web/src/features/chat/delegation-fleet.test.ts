// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent } from "@mecatl-studio/contracts";
import { describe, expect, it } from "vitest";
import {
  applyDelegationDelivery,
  createDelegationFleet,
  markDelegationHistoryIncomplete,
  markDelegationRunUnfollowed,
} from "./delegation-fleet";

function event(
  kind: string,
  seq: string,
  payload: unknown,
  runId = "run-a",
  unknown = false,
): RunStreamEvent {
  return {
    event: { kind, payload, runId, seq, text: "", turn: 1, unknown },
    type: "run.event",
  };
}

function fold(...deliveries: RunStreamEvent[]) {
  return deliveries.reduce(applyDelegationDelivery, createDelegationFleet("session-a"));
}

describe("delegation fleet", () => {
  it("separates interleaved delegation families and deduplicates replay by run and sequence", () => {
    const subagent = event("subagent.start", "9007199254740993", {
      childId: "child-a",
      goal: "Review",
      parentCallId: "call-a",
    });
    const tool = event("subagent.tool", "9007199254740994", {
      childId: "child-a",
      innerKind: "message.delta",
      parentCallId: "call-a",
      text: "hello",
      toolCount: 2,
      toolName: "Read",
    });
    const fleet = fold(
      { runId: "run-a", sessionId: "session-a", type: "run.started" },
      subagent,
      event(
        "team.start",
        "1",
        {
          parentCallId: "call-t",
          roster: [{ lead: true, name: "lead", role: "Coordinator" }],
          teamId: "team-a",
        },
        "run-b",
      ),
      tool,
      event("parallel.start", "9007199254740995", {
        branchCount: 2,
        join: "first",
        parentCallId: "call-p",
      }),
      event(
        "team.tasks",
        "2",
        {
          parentCallId: "call-t",
          tasks: [
            { assignee: "lead", deps: [], description: "Check", id: "task-1", state: "pending" },
          ],
          teamId: "team-a",
        },
        "run-b",
      ),
      { runId: "run-a", sessionId: "session-a", type: "run.started" },
      subagent,
      tool,
      event("team.tasks", "2", { parentCallId: "call-t", tasks: [], teamId: "team-a" }, "run-b"),
      event("subagent.end", "9007199254740994", {
        childId: "child-a",
        parentCallId: "call-a",
        stop: "error",
      }),
      event(
        "subagent.start",
        "9007199254740996",
        {
          childId: "future",
          parentCallId: "call-z",
        },
        "run-a",
        true,
      ),
      event("subagent.future", "9007199254740997", {
        childId: "future",
        parentCallId: "call-z",
      }),
      event("subagent.start", "9007199254740998", { childId: "malformed" }),
      event("result", "9007199254740999", { stop: "end_turn" }, "run-a", true),
      event("parallel.start", "bad-seq", { parentCallId: "invalid" }),
    );

    expect(fleet.subagents).toHaveLength(1);
    expect(fleet.parallelGroups).toHaveLength(1);
    expect(fleet.teams).toHaveLength(1);
    expect(fleet.subagents[0]).toMatchObject({
      childId: "child-a",
      state: "running",
      toolCount: 2,
    });
    expect(fleet.subagents[0]?.trace.entries).toHaveLength(1);
    expect(fleet.teams[0]?.tasks).toHaveLength(1);
    expect(fleet.subagents[0]?.key).not.toBe(fleet.parallelGroups[0]?.key);
  });

  it("keeps partial activity honest across bounded replay and gaps", () => {
    const partial = fold(
      event("subagent.tool", "5", {
        childId: "child-a",
        innerKind: "tool.call",
        parentCallId: "call-a",
        toolCount: 1,
        toolName: "Read",
      }),
      { cursor: "5", reason: "bound", type: "run.truncated" },
      event("subagent.tool", "6", {
        childId: "child-a",
        innerKind: "tool.result",
        parentCallId: "call-a",
        toolCount: 1,
        toolName: "Read",
      }),
    );
    expect(partial.subagents[0]).toMatchObject({
      historyIncomplete: true,
      startObserved: false,
      state: "running",
      toolCount: 1,
    });
    expect(partial.subagents[0]?.goal).toBeUndefined();
    expect(partial.subagents[0]?.trace.entries).toHaveLength(2);
    expect(partial.incompleteHistory).toBe(false);

    const gap = applyDelegationDelivery(partial, {
      cursor: "",
      reason: "gap",
      type: "run.truncated",
    });
    expect(gap.incompleteHistory).toBe(true);
    expect(gap.subagents[0]?.state).toBe("unknown");
    expect(gap.subagents[0]?.stop).toBeUndefined();

    const exhausted = markDelegationHistoryIncomplete(partial);
    expect(exhausted.incompleteHistory).toBe(true);
    expect(exhausted.subagents[0]?.state).toBe("unknown");

    const unfollowed = markDelegationRunUnfollowed(partial, "run-a");
    expect(unfollowed.subagents[0]?.state).toBe("unknown");
    expect(unfollowed.incompleteHistory).toBe(true);

    const noEnd = applyDelegationDelivery(partial, event("result", "7", { stop: "end_turn" }));
    expect(noEnd.subagents[0]?.state).toBe("unknown");
    expect(noEnd.subagents[0]?.stop).toBeUndefined();

    const nextSession = createDelegationFleet("session-b");
    expect(nextSession.subagents).toEqual([]);
    expect(
      applyDelegationDelivery(nextSession, {
        runId: "run-a",
        sessionId: "session-a",
        type: "run.started",
      }),
    ).toEqual(nextSession);
  });

  it("preserves subagent tool counts and terminal stops across replay", () => {
    const long = "🧪".repeat(300);
    let fleet = fold(
      event("subagent.start", "1", {
        background: true,
        childId: "child-a",
        goal: "Audit",
        parentCallId: "call-a",
      }),
      event("subagent.tool", "2", {
        childId: "child-a",
        innerKind: "message.delta",
        parentCallId: "call-a",
        text: "🧪".repeat(150),
        toolCount: 3,
        toolName: "Read",
      }),
      event("tool.result", "3", { callId: "call-a", content: "started" }),
    );
    expect(fleet.subagents[0]).toMatchObject({
      background: true,
      currentTool: "Read",
      goal: "Audit",
      state: "running",
      toolCount: 3,
    });
    fleet = applyDelegationDelivery(
      fleet,
      event("subagent.tool", "4", {
        childId: "child-a",
        innerKind: "message.delta",
        parentCallId: "call-a",
        text: "🔎".repeat(150),
        toolCount: 3,
      }),
    );
    expect(Array.from(fleet.subagents[0]?.trace.entries[0]?.text ?? "")).toHaveLength(201);
    expect(fleet.subagents[0]?.trace.entries[0]?.text?.endsWith("…")).toBe(true);

    for (let seq = 5; seq <= 18; seq += 1) {
      fleet = applyDelegationDelivery(
        fleet,
        event("subagent.tool", String(seq), {
          childId: "child-a",
          detail: long,
          innerKind: "tool.result",
          parentCallId: "call-a",
          toolCount: 4,
          toolName: "Read",
        }),
      );
    }
    expect(fleet.subagents[0]?.trace.entries).toHaveLength(12);
    expect(fleet.subagents[0]?.trace.omitted).toBe(3);
    expect(Array.from(fleet.subagents[0]?.trace.entries[0]?.detail ?? "")).toHaveLength(201);

    fleet = applyDelegationDelivery(
      fleet,
      event("subagent.end", "19", {
        cause: "Provider unavailable",
        childId: "child-a",
        durationMs: "1200",
        parentCallId: "call-a",
        stop: "error",
        toolCount: 4,
      }),
    );
    fleet = applyDelegationDelivery(
      fleet,
      event("subagent.end", "19", {
        childId: "child-a",
        parentCallId: "call-a",
        stop: "end_turn",
      }),
    );
    fleet = applyDelegationDelivery(fleet, event("result", "20", { stop: "end_turn" }));
    expect(fleet.subagents[0]).toMatchObject({
      cause: "Provider unavailable",
      durationMs: "1200",
      state: "finished",
      stop: "error",
      toolCount: 4,
    });
    fleet = applyDelegationDelivery(
      fleet,
      event("subagent.start", "21", {
        childId: "child-a",
        goal: "Follow up",
        parentCallId: "call-b",
      }),
    );
    expect(fleet.subagents).toHaveLength(2);
    expect(fleet.subagents[1]?.key).not.toBe(fleet.subagents[0]?.key);
  });

  it("keeps parallel branches separate and selects only a declared winner", () => {
    let fleet = fold(
      event("parallel.start", "1", { branchCount: 2, join: "first", parentCallId: "call-p" }),
      event("parallel.branch", "2", {
        branchIndex: 0,
        branchLabel: "branch-1",
        goal: "Try A",
        kind: "branch_start",
        parentCallId: "call-p",
      }),
      event("parallel.branch", "3", {
        branchIndex: 1,
        branchLabel: "branch-2",
        goal: "Try B",
        kind: "branch_start",
        parentCallId: "call-p",
      }),
      event("parallel.branch", "4", {
        branchIndex: 0,
        detail: "A result",
        innerKind: "tool.result",
        kind: "branch_tool",
        parentCallId: "call-p",
        toolCount: 3,
        toolName: "Read",
      }),
      event("parallel.branch", "5", {
        branchIndex: 1,
        failed: true,
        kind: "branch_end",
        parentCallId: "call-p",
        stop: "error",
        toolCount: 1,
      }),
    );
    expect(fleet.parallelGroups[0]).toMatchObject({ branchCount: 2, join: "first" });
    expect(fleet.parallelGroups[0]?.winner).toBeUndefined();
    expect(fleet.parallelGroups[0]?.branches[0]).toMatchObject({
      branchIndex: 0,
      currentTool: "Read",
      goal: "Try A",
      state: "running",
      toolCount: 3,
    });
    expect(fleet.parallelGroups[0]?.branches[1]).toMatchObject({
      branchIndex: 1,
      failed: true,
      goal: "Try B",
      state: "finished",
      stop: "error",
    });
    expect(fleet.subagents).toEqual([]);
    fleet = applyDelegationDelivery(
      fleet,
      event("parallel.end", "6", {
        branchCount: 2,
        join: "first",
        parentCallId: "call-p",
        winner: 0,
      }),
    );
    expect(fleet.parallelGroups[0]?.winner).toBe(0);
    expect(fleet.parallelGroups[0]?.branches[0]?.state).toBe("unknown");
    expect(fleet.parallelGroups[0]?.branches[1]?.state).toBe("finished");

    const all = fold(
      event("parallel.start", "1", { branchCount: 2, join: "all", parentCallId: "all" }),
      event("parallel.end", "2", { branchCount: 2, join: "all", parentCallId: "all", winner: 0 }),
    );
    expect(all.parallelGroups[0]?.winner).toBeUndefined();
    expect(
      fold(event("parallel.end", "1", { join: "first", parentCallId: "none", winner: -1 }))
        .parallelGroups[0]?.winner,
    ).toBeUndefined();
  });

  it("applies latest team task finding and terminal disposition snapshots", () => {
    const longSnapshot = "🧪".repeat(300);
    let fleet = fold(
      event("team.start", "1", {
        parentCallId: "call-t",
        roster: [
          { lead: true, name: "lead", role: "Coordinator" },
          { lead: false, name: "worker", role: "Researcher" },
        ],
        teamId: "team-a",
      }),
      event("team.member", "2", {
        cause: "Transient failure",
        innerKind: "result",
        member: "worker",
        parentCallId: "call-t",
        teamId: "team-a",
        text: "Failed round",
      }),
      event("team.tasks", "3", {
        parentCallId: "call-t",
        tasks: [
          {
            assignee: "worker",
            deps: ["task-0"],
            description: "Research",
            id: "task-1",
            state: "in_progress",
          },
        ],
        teamId: "team-a",
      }),
      event("team.findings", "4", {
        findings: [{ body: "First", member: "worker" }],
        parentCallId: "call-t",
        teamId: "team-a",
      }),
      event("team.tasks", "5", { parentCallId: "call-t", tasks: [], teamId: "team-a" }),
      event("team.findings", "6", { findings: [], parentCallId: "call-t", teamId: "team-a" }),
    );
    expect(fleet.teams[0]).toMatchObject({ state: "running", tasks: [], findings: [] });
    expect(fleet.teams[0]?.members.find((member) => member.name === "worker")).toMatchObject({
      cause: "Transient failure",
      role: "Researcher",
      roundState: "failed",
    });
    expect(
      fleet.teams[0]?.members.find((member) => member.name === "lead")?.roundState,
    ).toBeUndefined();

    fleet = applyDelegationDelivery(
      fleet,
      event("team.member", "7", {
        innerKind: "tool.call",
        member: "worker",
        parentCallId: "call-t",
        teamId: "team-a",
        toolName: "Read",
      }),
    );
    const workerAfterTool = fleet.teams[0]?.members.find((member) => member.name === "worker");
    expect(workerAfterTool).toMatchObject({ currentTool: "Read", roundState: "running" });
    expect(workerAfterTool?.trace.entries).toEqual([
      { kind: "result", text: "Failed round", cause: "Transient failure" },
      { kind: "tool.call", toolName: "Read" },
    ]);
    fleet = applyDelegationDelivery(
      fleet,
      event("team.end", "8", {
        dispositions: [
          { errorRounds: 1, name: "worker", reason: 0, stopped: false },
          { errorRounds: 0, name: "lead", reason: 2, stopped: true },
        ],
        findings: [{ body: longSnapshot, member: "worker" }],
        parentCallId: "call-t",
        rounds: 3,
        stop: "end_turn",
        tasks: [
          {
            assignee: "worker",
            deps: [],
            description: longSnapshot,
            id: "task-1",
            state: "completed",
          },
        ],
        teamId: "team-a",
      }),
    );
    expect(fleet.teams[0]).toMatchObject({ rounds: 3, state: "finished", stop: "end_turn" });
    expect(fleet.teams[0]?.tasks[0]).toMatchObject({
      description: longSnapshot,
      state: "completed",
    });
    expect(fleet.teams[0]?.findings).toEqual([{ body: longSnapshot, member: "worker" }]);
    expect(fleet.teams[0]?.members.find((member) => member.name === "worker")).toMatchObject({
      disposition: "done",
      errorRounds: 1,
      reason: undefined,
    });
    expect(fleet.teams[0]?.members.find((member) => member.name === "lead")).toMatchObject({
      disposition: "stopped",
      reason: "cancelled",
    });
    for (const [reason, expected] of [
      [1, "error"],
      [2, "cancelled"],
      [3, "budget"],
    ] as const) {
      const snapshot = fold(
        event("team.end", "1", {
          dispositions: [{ errorRounds: 1, name: "worker", reason, stopped: true }],
          findings: [],
          parentCallId: "call-t",
          tasks: [],
          teamId: "team-a",
        }),
      );
      expect(snapshot.teams[0]?.members[0]?.reason).toBe(expected);
    }
  });
});
