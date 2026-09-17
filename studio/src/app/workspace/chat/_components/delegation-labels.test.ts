import { describe, expect, it } from "vitest";
import type { DelegationGroupInfo, DelegationInfo } from "@/features/agent";
import {
  childHash,
  delegationFailed,
  delegationRunning,
  delegationStopLabel,
  formatChildDuration,
  groupDelegations,
  stopFamily,
  teamMemberStateLabel,
} from "./delegation-labels";

/**
 * The card-row label helpers mirror the TUI (`subagentStopLabel`,
 * `teamLaneState`, the parallel roster line) so a Studio user reads the same
 * words: stop tokens map to compact labels (unknown ones pass through), a
 * team lane's state follows the supervisor, liveness differs per family, and
 * a turn's cards split into headed team/parallel sections plus a flat
 * subagent row.
 */

const card = (partial: Partial<DelegationInfo> = {}): DelegationInfo => ({
  kind: "subagent",
  label: "explore",
  detail: "",
  ...partial,
});

describe("delegationStopLabel", () => {
  it.each([
    ["end_turn", "done"],
    ["", "done"],
    [undefined, "done"],
    ["max_tool_calls", "max-tools"],
    ["max_turns", "max-turns"],
    ["max_consecutive_failures", "max-failures"],
    ["cancelled", "cancelled"],
    ["error", "error"],
    ["budget", "budget"],
    ["structured_output", "schema"],
    ["no_progress", "no-progress"],
  ])("maps %j to %j", (stop, label) => {
    expect(delegationStopLabel(stop)).toBe(label);
  });

  it("passes an unknown stop reason through verbatim so it is never hidden", () => {
    expect(delegationStopLabel("some_new_reason")).toBe("some_new_reason");
  });
});

describe("stopFamily", () => {
  it("treats error and cancelled as bad", () => {
    expect(stopFamily("error")).toBe("bad");
    expect(stopFamily("cancelled")).toBe("bad");
  });
  it("treats clean ends and the cap family as ok", () => {
    for (const stop of [
      "end_turn",
      "",
      undefined,
      "max_turns",
      "max_tool_calls",
      "budget",
      "no_progress",
      "structured_output",
    ]) {
      expect(stopFamily(stop)).toBe("ok");
    }
  });
});

describe("childHash", () => {
  it("takes the last six characters of the child id", () => {
    expect(childHash("subagent-0123456789abcdef")).toBe("abcdef");
  });
  it("keeps a short id whole", () => {
    expect(childHash("abc")).toBe("abc");
  });
});

describe("formatChildDuration", () => {
  it("renders milliseconds under a second", () => {
    expect(formatChildDuration(850)).toBe("850ms");
  });
  it("renders whole seconds under a minute", () => {
    expect(formatChildDuration(12_400)).toBe("12s");
  });
  it("renders minutes and seconds", () => {
    expect(formatChildDuration(200_000)).toBe("3m 20s");
  });
  it("renders nothing for a missing or zero duration", () => {
    expect(formatChildDuration(0)).toBe("");
    expect(formatChildDuration(Number.NaN)).toBe("");
  });
});

describe("teamMemberStateLabel", () => {
  const lane = (partial: Partial<DelegationInfo> = {}) =>
    card({ kind: "team", label: "reviewer", ...partial });

  it("names a benched member's reason once the team ended", () => {
    expect(
      teamMemberStateLabel(lane({ stopped: true, stopReason: "error" }), true),
    ).toBe("stopped — error");
    expect(
      teamMemberStateLabel(
        lane({ stopped: true, stopReason: "cancelled" }),
        true,
      ),
    ).toBe("stopped — cancelled");
  });
  it("reads a bare stopped when the reason is unspecified", () => {
    expect(
      teamMemberStateLabel(lane({ stopped: true, stopReason: "" }), true),
    ).toBe("stopped");
  });
  it("does not contradict the supervisor about a retried member", () => {
    expect(teamMemberStateLabel(lane({ errorRounds: 1 }), true)).toBe(
      "done (retried)",
    );
  });
  it("reads done for a cleanly finished member", () => {
    expect(teamMemberStateLabel(lane(), true)).toBe("done");
  });
  it("reads idle between rounds", () => {
    expect(teamMemberStateLabel(lane({ idle: true }), false)).toBe("idle");
  });
  it("shows the running tool with a heartbeat, else working", () => {
    expect(teamMemberStateLabel(lane({ lastTool: "Grep" }), false)).toBe(
      "Grep…",
    );
    expect(teamMemberStateLabel(lane(), false)).toBe("working…");
  });
  it("lets the team end win over idle", () => {
    expect(teamMemberStateLabel(lane({ idle: true }), true)).toBe("done");
  });
});

describe("delegationRunning", () => {
  it("subagent: live once it has a child id and no stop", () => {
    expect(delegationRunning(card({ childId: "c1" }))).toBe(true);
    expect(delegationRunning(card({ childId: "c1", background: true }))).toBe(
      true,
    );
    expect(delegationRunning(card({ childId: "c1", stop: "end_turn" }))).toBe(
      false,
    );
    expect(delegationRunning(card())).toBe(false);
  });

  it("parallel: live without a child id until the branch or group ends", () => {
    const branch = card({ kind: "parallel", branchIndex: 0 });
    expect(delegationRunning(branch)).toBe(true);
    expect(delegationRunning({ ...branch, stop: "end_turn" })).toBe(false);
    expect(delegationRunning({ ...branch, failed: true })).toBe(false);
    expect(delegationRunning(branch, { kind: "parallel", done: true })).toBe(
      false,
    );
  });

  it("team: live until the team ends", () => {
    const lane = card({ kind: "team", idle: true });
    expect(delegationRunning(lane)).toBe(true);
    expect(delegationRunning(lane, { kind: "team", done: false })).toBe(true);
    expect(delegationRunning(lane, { kind: "team", done: true })).toBe(false);
    expect(delegationRunning({ ...lane, stop: "end_turn" })).toBe(false);
  });
});

describe("delegationFailed", () => {
  it("flags an error stop, a failed branch, and an error-benched member", () => {
    expect(delegationFailed(card({ stop: "error" }))).toBe(true);
    expect(delegationFailed(card({ kind: "parallel", failed: true }))).toBe(
      true,
    );
    expect(
      delegationFailed(
        card({ kind: "team", stopped: true, stopReason: "error" }),
      ),
    ).toBe(true);
  });
  it("does not flag a budget-benched member or a capped child", () => {
    expect(
      delegationFailed(
        card({ kind: "team", stopped: true, stopReason: "budget" }),
      ),
    ).toBe(false);
    expect(delegationFailed(card({ stop: "max_turns" }))).toBe(false);
  });
});

describe("groupDelegations", () => {
  const lanes: DelegationInfo[] = [
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
  ];

  it("orders a team's lanes lead-first under a members header", () => {
    const [section] = groupDelegations(lanes, {
      "t-call": { kind: "team", teamId: "team-1" },
    });
    expect(section.kind).toBe("team");
    expect(section.cards.map((c) => c.label)).toEqual(["lead", "coder"]);
    expect(section.header).toBe("team team-1 · 2 members");
  });

  it("adds rounds, the stop label and the stopped count once the team ended", () => {
    const groups: Record<string, DelegationGroupInfo> = {
      "t-call": {
        kind: "team",
        teamId: "team-1",
        done: true,
        rounds: 3,
        stop: "end_turn",
        stoppedCount: 1,
      },
    };
    expect(groupDelegations(lanes, groups)[0].header).toBe(
      "team team-1 · 2 members · 3 rounds · done · 1 stopped",
    );
  });

  it("headers a parallel group with join, tally, ★ winner and run stop", () => {
    const branches: DelegationInfo[] = [
      card({
        kind: "parallel",
        label: "slow",
        parentCallId: "p-call",
        branchIndex: 1,
        stop: "end_turn",
      }),
      card({
        kind: "parallel",
        label: "fast",
        parentCallId: "p-call",
        branchIndex: 0,
        stop: "end_turn",
      }),
      card({
        kind: "parallel",
        label: "third",
        parentCallId: "p-call",
        branchIndex: 2,
      }),
    ];
    const [section] = groupDelegations(branches, {
      "p-call": {
        kind: "parallel",
        join: "first",
        branchCount: 3,
        winner: 0,
        stop: "end_turn",
        done: true,
      },
    });
    expect(section.cards.map((c) => c.label)).toEqual([
      "fast",
      "slow",
      "third",
    ]);
    expect(section.header).toBe(
      "parallel · first · 2/3 done · ★ fast · run stop: done",
    );
  });

  it("reads join=all and no winner while a parallel group is still running", () => {
    const branches: DelegationInfo[] = [
      card({
        kind: "parallel",
        label: "a",
        parentCallId: "p-call",
        branchIndex: 0,
      }),
      card({
        kind: "parallel",
        label: "b",
        parentCallId: "p-call",
        branchIndex: 1,
        failed: true,
        stop: "error",
      }),
    ];
    const [section] = groupDelegations(branches, {
      "p-call": { kind: "parallel", branchCount: 2, winner: -1 },
    });
    expect(section.header).toBe("parallel · all · 1/2 done");
  });

  it("falls back to branch-N for a winner whose start card never landed", () => {
    const [section] = groupDelegations(
      [
        card({
          kind: "parallel",
          label: "a",
          parentCallId: "p-call",
          branchIndex: 0,
        }),
      ],
      { "p-call": { kind: "parallel", branchCount: 2, winner: 1, done: true } },
    );
    expect(section.header).toContain("★ branch-2");
  });

  it("keeps subagents in one flat, header-less section in arrival order", () => {
    const cards: DelegationInfo[] = [
      card({ label: "first", childId: "c1" }),
      card({
        kind: "parallel",
        label: "b0",
        parentCallId: "p-call",
        branchIndex: 0,
      }),
      card({ label: "second", childId: "c2" }),
    ];
    const sections = groupDelegations(cards);
    expect(sections.map((s) => s.key)).toEqual([
      "subagents",
      "parallel:p-call",
    ]);
    expect(sections[0].header).toBeUndefined();
    expect(sections[0].cards.map((c) => c.label)).toEqual(["first", "second"]);
    expect(sections[1].header).toBe("parallel · all · 0/1 done");
  });

  it("preserves a caller's extra fields on the grouped cards", () => {
    const withIds = [{ ...card({ childId: "c1" }), id: "row-1" }];
    expect(groupDelegations(withIds)[0].cards[0].id).toBe("row-1");
  });

  it("finds a team's group by team id when the lanes carry no call id", () => {
    const [section] = groupDelegations(
      [card({ kind: "team", label: "coder", teamId: "team-9" })],
      {
        "call-x": {
          kind: "team",
          teamId: "team-9",
          done: true,
          rounds: 1,
          stop: "budget",
        },
      },
    );
    expect(section.header).toBe("team team-9 · 1 member · 1 round · budget");
  });
});
