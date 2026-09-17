import { describe, expect, it } from "vitest";
import {
  applyDelegationEvent,
  type DelegationFleet,
  delegationKey,
  emptyFleet,
  fleetCounts,
  isDelegationEvent,
  MAX_TRACE_ENTRIES,
  markCancelling,
  markCardCancelling,
  matchesDelegation,
  reduceDelegationFleet,
  routeTraceEvent,
} from "./delegation-fleet";
import type { AgentMessage, DelegationInfo, StreamEvent } from "./types";

const fold = (events: StreamEvent[], from = emptyFleet()): DelegationFleet =>
  events.reduce(reduceDelegationFleet, from);

const subagentStart = (
  childId: string,
  extra: Partial<Extract<StreamEvent, { type: "delegation" }>> = {},
): StreamEvent => ({
  type: "delegation",
  kind: "subagent",
  label: `goal for ${childId}`,
  detail: "explore → small-1",
  childId,
  parentCallId: `call-${childId}`,
  ...extra,
});

const teamMember = (
  member: string,
  innerKind: string,
  extra: Partial<Extract<StreamEvent, { type: "team_member" }>> = {},
): StreamEvent => ({
  type: "team_member",
  teamId: "team-1",
  parentCallId: "call-team",
  member,
  memberSessionId: `team-team-1-${member}`,
  innerKind,
  isError: false,
  contextUsed: 0,
  contextWindow: 0,
  ...extra,
});

// ── keys ─────────────────────────────────────────────────────────────────────

describe("matchesDelegation", () => {
  const branch: DelegationInfo = {
    kind: "parallel",
    label: "branch 1",
    detail: "",
    parentCallId: "call-p",
    branchIndex: 0,
  };
  const lane: DelegationInfo = {
    kind: "team",
    label: "alice (lead)",
    detail: "",
    parentCallId: "call-t",
    teamId: "team-9",
    memberName: "alice",
  };

  it("matches on child id when both sides carry one", () => {
    const card: DelegationInfo = {
      kind: "subagent",
      label: "x",
      detail: "",
      childId: "subagent-1",
    };
    expect(matchesDelegation(card, { childId: "subagent-1" })).toBe(true);
    expect(matchesDelegation(card, { childId: "subagent-2" })).toBe(false);
    // A card with an id never matches a ref that lacks one on kind alone.
    expect(matchesDelegation(card, { parentCallId: "call-x" })).toBe(false);
  });

  it("matches a parallel branch by (parentCallId, branchIndex) — a branch_tool has no child id", () => {
    expect(
      matchesDelegation(branch, { parentCallId: "call-p", branchIndex: 0 }),
    ).toBe(true);
    expect(
      matchesDelegation(branch, { parentCallId: "call-p", branchIndex: 1 }),
    ).toBe(false);
    expect(
      matchesDelegation(branch, { parentCallId: "call-q", branchIndex: 0 }),
    ).toBe(false);
    // Two undefined call ids are NOT a match.
    expect(matchesDelegation(branch, { branchIndex: 0 })).toBe(false);
  });

  it("matches a team lane by member name within the same call or team", () => {
    expect(
      matchesDelegation(lane, { parentCallId: "call-t", member: "alice" }),
    ).toBe(true);
    expect(matchesDelegation(lane, { teamId: "team-9", member: "alice" })).toBe(
      true,
    );
    expect(
      matchesDelegation(lane, { parentCallId: "call-t", member: "bob" }),
    ).toBe(false);
    expect(
      matchesDelegation(lane, { parentCallId: "call-u", member: "alice" }),
    ).toBe(false);
  });

  it("derives a stable key per family", () => {
    expect(
      delegationKey({ kind: "subagent", label: "", detail: "", childId: "c" }),
    ).toBe("child:c");
    expect(delegationKey(branch)).toBe("branch:call-p:0");
    expect(delegationKey(lane)).toBe("member:call-t:alice");
  });
});

// ── trace ────────────────────────────────────────────────────────────────────

describe("routeTraceEvent", () => {
  const card: DelegationInfo = { kind: "subagent", label: "x", detail: "" };

  it("appends a pending chip on tool.call and resolves it on tool.result", () => {
    let next = routeTraceEvent(card, {
      innerKind: "tool.call",
      toolName: "Read",
      detail: "path: a.go",
    });
    expect(next.lastTool).toBe("Read");
    expect(next.trace).toEqual([
      { kind: "tool", name: "Read", detail: "path: a.go", pending: true },
    ]);
    next = routeTraceEvent(next, {
      innerKind: "tool.result",
      toolName: "Read",
      detail: "42 lines",
      isError: true,
    });
    expect(next.trace).toEqual([
      {
        kind: "tool",
        name: "Read",
        detail: "42 lines",
        pending: false,
        isError: true,
      },
    ]);
    expect(next.lastToolError).toBe(true);
  });

  it("appends a resolved chip when a result has no pending chip — never dropped", () => {
    const next = routeTraceEvent(card, {
      innerKind: "tool.result",
      toolName: "Grep",
    });
    expect(next.trace).toEqual([
      { kind: "tool", name: "Grep", pending: false },
    ]);
  });

  it("coalesces consecutive message deltas onto one line and starts a new one after a chip", () => {
    let next = routeTraceEvent(card, {
      innerKind: "message.delta",
      text: "Hel",
    });
    next = routeTraceEvent(next, { innerKind: "message.delta", text: "lo" });
    next = routeTraceEvent(next, { innerKind: "tool.call", toolName: "Bash" });
    next = routeTraceEvent(next, { innerKind: "result", text: "done" });
    expect(next.trace).toEqual([
      { kind: "message", text: "Hello" },
      { kind: "tool", name: "Bash", pending: true },
      { kind: "message", text: "done" },
    ]);
  });

  it("treats a kind-less frame with a tool name as a tool.call (older daemon)", () => {
    const next = routeTraceEvent(card, { toolName: "Glob" });
    expect(next.lastTool).toBe("Glob");
    expect(next.trace?.[0]).toMatchObject({ name: "Glob", pending: true });
  });

  it(`bounds the trace at ${MAX_TRACE_ENTRIES}, dropping the oldest`, () => {
    let next = card;
    for (let index = 0; index < MAX_TRACE_ENTRIES + 5; index += 1) {
      next = routeTraceEvent(next, {
        innerKind: "tool.call",
        toolName: `T${index}`,
      });
    }
    expect(next.trace).toHaveLength(MAX_TRACE_ENTRIES);
    expect(next.trace?.[0].name).toBe("T5");
    expect(next.trace?.at(-1)?.name).toBe(`T${MAX_TRACE_ENTRIES + 4}`);
  });

  it("returns the same card for a message frame with no text", () => {
    expect(routeTraceEvent(card, { innerKind: "message.delta" })).toBe(card);
  });
});

// ── fleet: subagents ─────────────────────────────────────────────────────────

describe("reduceDelegationFleet — subagents", () => {
  it("returns the SAME fleet for a non-delegation event", () => {
    const fleet = emptyFleet();
    expect(reduceDelegationFleet(fleet, { type: "token", text: "x" })).toBe(
      fleet,
    );
    expect(isDelegationEvent({ type: "token", text: "x" })).toBe(false);
    expect(
      isDelegationEvent({
        type: "parallel_start",
        parentCallId: "c",
        join: "all",
        branchCount: 2,
      }),
    ).toBe(true);
  });

  it("tracks a subagent through start → progress → end, background counted running until its end", () => {
    let fleet = fold([
      subagentStart("subagent-a"),
      subagentStart("subagent-b", { background: true }),
      {
        type: "delegation_progress",
        childId: "subagent-a",
        toolCount: 2,
        inputTokens: 100,
        outputTokens: 10,
        innerKind: "tool.call",
        toolName: "Read",
      },
    ]);
    expect(fleetCounts(fleet).subagents).toEqual({ running: 2, done: 0 });
    expect(fleet.subagents[0]).toMatchObject({
      childId: "subagent-a",
      toolCount: 2,
      inputTokens: 100,
      lastTool: "Read",
    });
    fleet = fold(
      [
        {
          type: "delegation_end",
          childId: "subagent-a",
          stop: "end_turn",
          toolCount: 3,
          durationMs: 1200,
        },
      ],
      fleet,
    );
    expect(fleetCounts(fleet).subagents).toEqual({ running: 1, done: 1 });
    expect(fleet.subagents[0]).toMatchObject({
      stop: "end_turn",
      toolCount: 3,
      durationMs: 1200,
    });
    expect(fleet.subagents[1].background).toBe(true);
    expect(fleet.subagents[1].stop).toBeUndefined();
  });

  it("backfills a lane for progress whose start was never seen", () => {
    const fleet = fold([
      {
        type: "delegation_progress",
        childId: "subagent-late",
        parentCallId: "call-late",
        toolCount: 1,
        toolName: "Grep",
      },
    ]);
    expect(fleet.subagents).toHaveLength(1);
    expect(fleet.subagents[0]).toMatchObject({
      kind: "subagent",
      childId: "subagent-late",
      parentCallId: "call-late",
      label: "subagent-late",
      toolCount: 1,
      lastTool: "Grep",
    });
  });

  it("refreshes a re-started child in place (resume) instead of duplicating it, clearing its terminal", () => {
    const fleet = fold([
      subagentStart("subagent-a"),
      {
        type: "delegation_end",
        childId: "subagent-a",
        stop: "error",
        cause: "boom",
      },
      subagentStart("subagent-a", { label: "resumed goal" }),
    ]);
    expect(fleet.subagents).toHaveLength(1);
    expect(fleet.subagents[0]).toMatchObject({ label: "resumed goal" });
    expect(fleet.subagents[0].stop).toBeUndefined();
    expect(fleet.subagents[0].cause).toBeUndefined();
  });

  it("marks and clears an optimistic cancel by child id", () => {
    const fleet = fold([subagentStart("subagent-a")]);
    const marked = markCancelling(fleet, "subagent-a", true);
    expect(marked.subagents[0].cancelling).toBe(true);
    expect(markCancelling(marked, "subagent-a", true)).toBe(marked);
    expect(markCancelling(fleet, "subagent-zzz", true)).toBe(fleet);
    const cleared = markCancelling(marked, "subagent-a", false);
    expect(cleared.subagents[0].cancelling).toBeUndefined();
  });
});

// ── fleet: parallel ──────────────────────────────────────────────────────────

describe("reduceDelegationFleet — parallel groups", () => {
  const branchStart = (index: number): StreamEvent => ({
    type: "delegation",
    kind: "parallel",
    label: `branch ${index + 1}`,
    detail: "",
    childId: `parallel-call-p-${index}`,
    parentCallId: "call-p",
    branchIndex: index,
  });

  it("groups start → 3 branches → a failed branch_end → parallel_end with the winner", () => {
    let fleet = fold([
      {
        type: "parallel_start",
        parentCallId: "call-p",
        join: "first",
        branchCount: 3,
      },
      branchStart(0),
      branchStart(1),
      branchStart(2),
      // branch_tool: keyed by (parentCallId, branchIndex), no child id.
      {
        type: "delegation_progress",
        parentCallId: "call-p",
        branchIndex: 1,
        toolCount: 4,
        innerKind: "tool.call",
        toolName: "Bash",
        detail: "command: go test",
      },
    ]);
    expect(fleet.parallelGroups).toHaveLength(1);
    const [group] = fleet.parallelGroups;
    expect(group).toMatchObject({
      parentCallId: "call-p",
      join: "first",
      branchCount: 3,
      done: false,
    });
    expect(group.branches.map((b) => b.branchIndex)).toEqual([0, 1, 2]);
    expect(group.branches[1]).toMatchObject({ toolCount: 4, lastTool: "Bash" });
    expect(fleetCounts(fleet).parallel).toEqual({ running: 1, done: 0 });

    fleet = fold(
      [
        {
          type: "delegation_end",
          childId: "parallel-call-p-2",
          parentCallId: "call-p",
          branchIndex: 2,
          stop: "error",
          failed: true,
          cause: "provider rejected",
        },
        {
          type: "parallel_end",
          parentCallId: "call-p",
          join: "first",
          branchCount: 3,
          winner: 1,
          stop: "end_turn",
          inputTokens: 900,
          outputTokens: 90,
        },
      ],
      fleet,
    );
    const [ended] = fleet.parallelGroups;
    expect(ended).toMatchObject({
      done: true,
      winner: 1,
      stop: "end_turn",
      inputTokens: 900,
      outputTokens: 90,
    });
    expect(ended.branches[2]).toMatchObject({ stop: "error", failed: true });
    expect(ended.branches[1].winner).toBe(true);
    expect(ended.branches[0].winner).toBeUndefined();
    expect(fleetCounts(fleet).parallel).toEqual({ running: 0, done: 1 });
  });

  it("creates the group and branch defensively when a branch_tool arrives before any start", () => {
    const fleet = fold([
      {
        type: "delegation_progress",
        parentCallId: "call-p",
        branchIndex: 2,
        toolCount: 1,
        toolName: "Read",
      },
    ]);
    expect(fleet.parallelGroups[0].branches).toEqual([
      expect.objectContaining({
        kind: "parallel",
        label: "branch 3",
        parentCallId: "call-p",
        branchIndex: 2,
        toolCount: 1,
      }),
    ]);
    // Winner −1 (join=all / none) stamps nobody.
    const ended = fold(
      [
        {
          type: "parallel_end",
          parentCallId: "call-p",
          join: "all",
          branchCount: 3,
          winner: -1,
          stop: "end_turn",
        },
      ],
      fleet,
    );
    expect(ended.parallelGroups[0].done).toBe(true);
    expect(ended.parallelGroups[0].winner).toBe(-1);
    expect(ended.parallelGroups[0].branches[0].winner).toBeUndefined();
  });
});

// ── fleet: teams ─────────────────────────────────────────────────────────────

describe("reduceDelegationFleet — teams", () => {
  const roster: StreamEvent[] = [
    {
      type: "delegation",
      kind: "team",
      label: "lead (coordinator)",
      detail: "big-1",
      teamId: "team-1",
      parentCallId: "call-team",
      memberName: "lead",
      lead: true,
      model: "big-1",
    },
    {
      type: "delegation",
      kind: "team",
      label: "tester",
      detail: "",
      teamId: "team-1",
      parentCallId: "call-team",
      memberName: "tester",
      mutating: true,
    },
  ];

  it("builds the board: roster → member activity → tasks/findings → team_end dispositions", () => {
    let fleet = fold([
      ...roster,
      teamMember("tester", "tool.call", {
        toolName: "Edit",
        detail: "path: x.go",
      }),
      teamMember("tester", "tool.result", { toolName: "Edit", isError: false }),
      teamMember("tester", "turn.end", {
        inputTokens: 4000,
        outputTokens: 200,
        contextUsed: 4000,
        contextWindow: 128000,
      }),
      teamMember("lead", "message.delta", { text: "Planning" }),
    ]);
    expect(fleet.teams).toHaveLength(1);
    let [team] = fleet.teams;
    expect(team).toMatchObject({
      teamId: "team-1",
      parentCallId: "call-team",
      done: false,
    });
    // Lead first; member session id backfilled from team.member.
    expect(team.lanes.map((lane) => lane.memberName)).toEqual([
      "lead",
      "tester",
    ]);
    expect(team.lanes[1]).toMatchObject({
      childId: "team-team-1-tester",
      toolCount: 1,
      lastTool: "Edit",
      inputTokens: 4000,
      outputTokens: 200,
      contextUsed: 4000,
      contextWindow: 128000,
      idle: false,
      mutating: true,
    });
    expect(team.lanes[1].trace).toEqual([
      { kind: "tool", name: "Edit", detail: "path: x.go", pending: false },
    ]);
    expect(team.lanes[0].trace).toEqual([
      { kind: "message", text: "Planning" },
    ]);
    expect(fleetCounts(fleet).team).toEqual({
      live: true,
      working: 2,
      total: 2,
      teamId: "team-1",
    });

    // A result frame parks the member idle; a later turn.end with no window
    // keeps the sticky denominator; usage SUMS across turns.
    fleet = fold(
      [
        teamMember("tester", "result", { inputTokens: 500, outputTokens: 50 }),
        teamMember("tester", "turn.end", {
          inputTokens: 4500,
          outputTokens: 100,
          contextUsed: 4500,
        }),
      ],
      fleet,
    );
    [team] = fleet.teams;
    expect(team.lanes[1]).toMatchObject({
      idle: false,
      inputTokens: 9000,
      outputTokens: 350,
      contextUsed: 4500,
      contextWindow: 128000,
    });
    fleet = fold([teamMember("tester", "result", {})], fleet);
    expect(fleet.teams[0].lanes[1].idle).toBe(true);
    expect(fleetCounts(fleet).team.working).toBe(1);

    fleet = fold(
      [
        {
          type: "team_tasks",
          teamId: "team-1",
          parentCallId: "call-team",
          tasks: [
            {
              id: "t1",
              state: "in_progress",
              assignee: "tester",
              deps: [],
              description: "run the suite",
            },
          ],
        },
        {
          type: "team_findings",
          teamId: "team-1",
          parentCallId: "call-team",
          findings: [{ member: "tester", body: "suite is green" }],
        },
      ],
      fleet,
    );
    expect(fleet.teams[0].tasks).toEqual([
      expect.objectContaining({ id: "t1", state: "in_progress" }),
    ]);
    expect(fleet.teams[0].findings).toEqual([
      { member: "tester", body: "suite is green" },
    ]);

    fleet = fold(
      [
        teamMember("tester", "result", { cause: "provider outage" }),
        {
          type: "team_end",
          teamId: "team-1",
          parentCallId: "call-team",
          rounds: 4,
          stop: "end_turn",
          inputTokens: 20000,
          outputTokens: 1500,
          tasks: [],
          findings: [],
          dispositions: [
            { name: "lead", stopped: false, errorRounds: 1, reason: "" },
            { name: "tester", stopped: true, errorRounds: 2, reason: "error" },
          ],
        },
      ],
      fleet,
    );
    [team] = fleet.teams;
    expect(team).toMatchObject({
      done: true,
      rounds: 4,
      stop: "end_turn",
      inputTokens: 20000,
      outputTokens: 1500,
    });
    // Snapshots survive an empty terminal snapshot.
    expect(team.tasks).toHaveLength(1);
    expect(team.findings).toHaveLength(1);
    // "done (retried)": not stopped, but retried once.
    expect(team.lanes[0]).toMatchObject({
      stopped: false,
      errorRounds: 1,
      stop: "end_turn",
    });
    // Benched with the error reason, cause kept from its last result.
    expect(team.lanes[1]).toMatchObject({
      stopped: true,
      stopReason: "error",
      errorRounds: 2,
      stop: "error",
      cause: "provider outage",
    });
    expect(fleetCounts(fleet).team).toEqual({
      live: false,
      working: 0,
      total: 2,
      teamId: "team-1",
    });
  });

  it("backfills the team and lane when a team.member arrives before team.start", () => {
    const fleet = fold([
      teamMember("alice", "tool.call", { toolName: "Read" }),
      ...roster,
    ]);
    expect(fleet.teams).toHaveLength(1);
    expect(fleet.teams[0].lanes.map((lane) => lane.memberName)).toEqual([
      "lead",
      "alice",
      "tester",
    ]);
    expect(fleet.teams[0].lanes[1]).toMatchObject({
      childId: "team-team-1-alice",
      lastTool: "Read",
    });
  });

  it("adds a lane for a disposition the roster never named", () => {
    const fleet = fold([
      ...roster,
      {
        type: "team_end",
        teamId: "team-1",
        parentCallId: "call-team",
        rounds: 1,
        stop: "budget",
        tasks: [],
        findings: [],
        dispositions: [
          { name: "ghost", stopped: true, errorRounds: 0, reason: "budget" },
        ],
      },
    ]);
    expect(fleet.teams[0].lanes.map((lane) => lane.memberName)).toEqual([
      "lead",
      "tester",
      "ghost",
    ]);
    expect(fleet.teams[0].lanes[2]).toMatchObject({
      stopped: true,
      stopReason: "budget",
      stop: "budget",
    });
    // Unbenched lanes stop on the team's stop.
    expect(fleet.teams[0].lanes[0].stop).toBe("budget");
  });

  it("reports empty counts for an empty fleet", () => {
    expect(fleetCounts(emptyFleet())).toEqual({
      subagents: { running: 0, done: 0 },
      parallel: { running: 0, done: 0 },
      team: { live: false, working: 0, total: 0, teamId: "" },
    });
  });
});

// ── transcript ───────────────────────────────────────────────────────────────

describe("applyDelegationEvent", () => {
  const turn = (): AgentMessage[] => [
    { id: "u1", role: "user", content: "go", timestamp: 0 },
    {
      id: "a1",
      role: "assistant",
      content: "",
      timestamp: 0,
      toolCalls: [
        { callId: "call-p", name: "Parallel", input: "", status: "running" },
      ],
    },
    // A steer the user sent while the run streams: the trailing bubble is
    // NOT the assistant being driven.
    { id: "u2", role: "user", content: "also…", timestamp: 1 },
  ];

  it("anchors a start on the turn whose tool call it belongs to, not the trailing bubble", () => {
    const messages = applyDelegationEvent(turn(), {
      type: "delegation",
      kind: "parallel",
      label: "branch 1",
      detail: "",
      childId: "parallel-call-p-0",
      parentCallId: "call-p",
      branchIndex: 0,
    });
    expect(messages[1].delegations).toHaveLength(1);
    expect(messages[2].delegations).toBeUndefined();
    // A parallel start implies its group header even before parallel_start.
    expect(messages[1].delegationGroups).toEqual({
      "call-p": { kind: "parallel" },
    });
  });

  it("falls back to the anchored assistant bubble, then opens one when asked", () => {
    const start: StreamEvent = {
      type: "delegation",
      kind: "subagent",
      label: "explore",
      detail: "",
      childId: "subagent-1",
    };
    const anchored = applyDelegationEvent(turn(), start, { assistantId: "a1" });
    expect(anchored[1].delegations).toHaveLength(1);
    const opened = applyDelegationEvent(
      [{ id: "u1", role: "user", content: "go", timestamp: 0 }],
      start,
      {
        openAssistant: () => ({
          id: "fresh",
          role: "assistant",
          content: "",
          timestamp: 0,
        }),
      },
    );
    expect(opened).toHaveLength(2);
    expect(opened[1]).toMatchObject({
      id: "fresh",
      delegations: [
        { kind: "subagent", label: "explore", childId: "subagent-1" },
      ],
    });
    // With nothing to anchor on, the transcript is left unchanged.
    const before = [
      { id: "u1", role: "user" as const, content: "", timestamp: 0 },
    ];
    expect(applyDelegationEvent(before, start)).toBe(before);
  });

  it("lands parallel_start/parallel_end on the group and stamps the winner branch", () => {
    let messages = applyDelegationEvent(turn(), {
      type: "parallel_start",
      parentCallId: "call-p",
      join: "judge",
      branchCount: 2,
    });
    for (const index of [0, 1]) {
      messages = applyDelegationEvent(messages, {
        type: "delegation",
        kind: "parallel",
        label: `branch ${index + 1}`,
        detail: "",
        childId: `parallel-call-p-${index}`,
        parentCallId: "call-p",
        branchIndex: index,
      });
    }
    messages = applyDelegationEvent(messages, {
      type: "parallel_end",
      parentCallId: "call-p",
      join: "judge",
      branchCount: 2,
      winner: 1,
      stop: "end_turn",
      inputTokens: 10,
      outputTokens: 2,
    });
    expect(messages[1].delegationGroups?.["call-p"]).toEqual({
      kind: "parallel",
      join: "judge",
      branchCount: 2,
      winner: 1,
      stop: "end_turn",
      done: true,
      inputTokens: 10,
      outputTokens: 2,
    });
    expect(messages[1].delegations?.map((d) => d.winner)).toEqual([
      undefined,
      true,
    ]);
    // A parallel_end with no owning turn changes nothing.
    const before = turn();
    expect(
      applyDelegationEvent(before, {
        type: "parallel_end",
        parentCallId: "call-unknown",
        join: "all",
        branchCount: 1,
        winner: -1,
        stop: "end_turn",
      }),
    ).toBe(before);
  });

  it("creates the team group on the first team card and updates lanes from team_member/team_end", () => {
    const base: AgentMessage[] = [
      {
        id: "a1",
        role: "assistant",
        content: "",
        timestamp: 0,
        toolCalls: [
          { callId: "call-team", name: "Team", input: "", status: "running" },
        ],
      },
    ];
    let messages = applyDelegationEvent(base, {
      type: "delegation",
      kind: "team",
      label: "alice (lead)",
      detail: "",
      teamId: "team-1",
      parentCallId: "call-team",
      memberName: "alice",
      lead: true,
    });
    expect(messages[0].delegationGroups).toEqual({
      "call-team": { kind: "team", teamId: "team-1" },
    });
    messages = applyDelegationEvent(
      messages,
      teamMember("alice", "tool.call", { toolName: "Read" }) as Extract<
        StreamEvent,
        { type: "team_member" }
      >,
    );
    // Backfilled session id + activity on the roster card.
    expect(messages[0].delegations?.[0]).toMatchObject({
      memberName: "alice",
      childId: "team-team-1-alice",
      lastTool: "Read",
      toolCount: 1,
    });
    // A member the roster never listed gets a lane after the lead.
    messages = applyDelegationEvent(
      messages,
      teamMember("bob", "message.delta", { text: "hi" }) as Extract<
        StreamEvent,
        { type: "team_member" }
      >,
    );
    expect(messages[0].delegations?.map((d) => d.memberName)).toEqual([
      "alice",
      "bob",
    ]);
    messages = applyDelegationEvent(messages, {
      type: "team_end",
      teamId: "team-1",
      parentCallId: "call-team",
      rounds: 2,
      stop: "end_turn",
      tasks: [],
      findings: [],
      dispositions: [
        { name: "alice", stopped: false, errorRounds: 0, reason: "" },
        { name: "bob", stopped: true, errorRounds: 1, reason: "cancelled" },
      ],
    });
    expect(messages[0].delegationGroups?.["call-team"]).toMatchObject({
      kind: "team",
      done: true,
      rounds: 2,
      stop: "end_turn",
      stoppedCount: 1,
    });
    expect(messages[0].delegations?.[1]).toMatchObject({
      stopped: true,
      stopReason: "cancelled",
      stop: "cancelled",
      errorRounds: 1,
    });
    expect(messages[0].delegations?.[0].stop).toBe("end_turn");
  });

  it("leaves the transcript untouched for task/findings snapshots (panel-only)", () => {
    const before = turn();
    expect(
      applyDelegationEvent(before, {
        type: "team_tasks",
        teamId: "team-1",
        parentCallId: "call-team",
        tasks: [],
      }),
    ).toBe(before);
    expect(
      applyDelegationEvent(before, {
        type: "team_findings",
        teamId: "team-1",
        parentCallId: "call-team",
        findings: [],
      }),
    ).toBe(before);
  });

  it("marks and clears an optimistic cancel on a card", () => {
    const messages = applyDelegationEvent(
      turn(),
      {
        type: "delegation",
        kind: "subagent",
        label: "x",
        detail: "",
        childId: "subagent-1",
      },
      { assistantId: "a1" },
    );
    const marked = markCardCancelling(messages, "subagent-1", true);
    expect(marked[1].delegations?.[0].cancelling).toBe(true);
    expect(markCardCancelling(marked, "subagent-1", true)).toBe(marked);
    expect(markCardCancelling(messages, "nope", true)).toBe(messages);
    expect(
      markCardCancelling(marked, "subagent-1", false)[1].delegations?.[0]
        .cancelling,
    ).toBeUndefined();
  });
});
