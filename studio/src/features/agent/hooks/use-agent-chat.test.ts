import { describe, expect, it } from "vitest";
import { formatVerdictNotice } from "../approval-queue";
import { MAX_TRACE_ENTRIES } from "../delegation-fleet";
import type { AgentMessage, StreamEvent } from "../types";
import {
  applyDelegationEvent,
  attachmentsFromSteerParts,
  reduceWatchEvent,
  splitPendingSteersOnWatermark,
} from "./use-agent-chat";

const pending = [
  { id: "steer-1", text: "first" },
  { id: "steer-2", text: "second" },
  { id: "steer-3", text: "third" },
];

describe("splitPendingSteersOnWatermark", () => {
  it("drops every pending steer up to and including the watermark, keeping the tail", () => {
    expect(splitPendingSteersOnWatermark(pending, "steer-2")).toEqual([
      { id: "steer-3", text: "third" },
    ]);
  });

  it("clears the whole list when the watermark is the last pending steer", () => {
    expect(splitPendingSteersOnWatermark(pending, "steer-3")).toEqual([]);
  });

  it("clears the whole list on an empty watermark — the daemon's FIFO is authoritative", () => {
    expect(splitPendingSteersOnWatermark(pending, "")).toEqual([]);
  });

  it("clears the whole list on an unmatched watermark rather than text-matching", () => {
    expect(splitPendingSteersOnWatermark(pending, "steer-unknown")).toEqual([]);
  });

  it("leaves an empty list empty", () => {
    expect(splitPendingSteersOnWatermark([], "steer-1")).toEqual([]);
  });
});

// ── reduceWatchEvent ─────────────────────────────────────────────────────────

/**
 * Pins the watch transcript reducer (ADR 0250): replayed durable-log events
 * rebuild the same message shape the live prompt path produces.
 */
describe("reduceWatchEvent", () => {
  let serial = 0;
  const nextId = () => `id-${++serial}`;
  const run = (events: StreamEvent[]): AgentMessage[] =>
    events.reduce<AgentMessage[]>(
      (messages, event) => reduceWatchEvent(messages, event, nextId),
      [],
    );

  it("stamps per-turn stats onto the trailing assistant across two turn_end frames (replay path)", () => {
    const turnEnd = (
      inputTokens: number,
      outputTokens: number,
      durationMs: number,
      cacheReadTokens = 0,
    ): StreamEvent => ({
      type: "turn_end",
      durationMs,
      inputTokens,
      outputTokens,
      cacheReadTokens,
      cacheWriteTokens: 0,
      reasoningTokens: 0,
    });
    const messages = run([
      { type: "user_prompt", text: "summarise the repo" },
      { type: "token", text: "Looking…" },
      turnEnd(1000, 50, 1200, 400),
      { type: "token", text: " Done." },
      turnEnd(1500, 300, 2000),
    ]);
    expect(messages).toHaveLength(2);
    expect(messages[1]).toMatchObject({
      role: "assistant",
      content: "Looking… Done.",
      turnStats: {
        turns: 2,
        inputTokens: 2500,
        outputTokens: 350,
        cacheReadTokens: 400,
        cacheWriteTokens: 0,
        durationMs: 3200,
        // The latest turn's input is the context occupancy — assigned, not summed.
        lastInputTokens: 1500,
      },
    });
    // A turn_end with no assistant bubble yet opens one to carry the stats.
    const opened = run([
      { type: "user_prompt", text: "hi" },
      turnEnd(20, 5, 300),
    ]);
    expect(opened).toHaveLength(2);
    expect(opened[1].role).toBe("assistant");
    expect(opened[1].turnStats?.turns).toBe(1);
  });

  it("rebuilds a user → assistant exchange with tool activity", () => {
    const messages = run([
      { type: "user_prompt", text: "list the files" },
      { type: "token", text: "Sure — " },
      {
        type: "tool_call",
        callId: "c1",
        name: "Bash",
        input: "command: ls",
      },
      { type: "tool_result", callId: "c1", output: "a.txt", isError: false },
      { type: "token", text: "done." },
    ]);
    expect(messages).toHaveLength(2);
    expect(messages[0]).toMatchObject({
      role: "user",
      content: "list the files",
    });
    expect(messages[1]).toMatchObject({
      role: "assistant",
      content: "Sure — done.",
      toolCalls: [
        {
          callId: "c1",
          name: "Bash",
          output: "a.txt",
          status: "completed",
        },
      ],
    });
  });

  it("opens a fresh assistant bubble after each user-authored record", () => {
    const messages = run([
      { type: "user_prompt", text: "first" },
      { type: "token", text: "answer one" },
      { type: "user_prompt", text: "second" },
      { type: "token", text: "answer two" },
    ]);
    expect(messages.map((m) => [m.role, m.content])).toEqual([
      ["user", "first"],
      ["assistant", "answer one"],
      ["user", "second"],
      ["assistant", "answer two"],
    ]);
  });

  it("renders a steer echo as a user message, like the committed record it is", () => {
    const messages = run([
      { type: "token", text: "working" },
      { type: "steer", text: "focus on tests", messageId: "s-1" },
      { type: "token", text: "ok" },
    ]);
    expect(messages.map((m) => [m.role, m.content])).toEqual([
      ["assistant", "working"],
      ["user", "focus on tests"],
      ["assistant", "ok"],
    ]);
  });

  it("renders an approval verdict as a quiet notice line", () => {
    const messages = run([
      {
        type: "approval_verdict",
        approvalId: "a1",
        toolName: "Bash",
        verdict: "allow_once",
      },
    ]);
    expect(messages[0].notices).toEqual(["Permission: Bash allowed once"]);
  });

  it("renders the verdict line with the SAME formatter the live respond path records locally", () => {
    const messages = run([
      {
        type: "approval_verdict",
        approvalId: "a2",
        toolName: "Edit",
        verdict: "deny",
      },
    ]);
    expect(messages[0].notices).toEqual([formatVerdictNotice("Edit", "deny")]);
    expect(formatVerdictNotice("Edit", "deny")).toBe("Permission: Edit denied");
  });

  it("marks a failed terminal on the trailing assistant, failing its running calls", () => {
    const messages = run([
      { type: "token", text: "trying" },
      { type: "tool_call", callId: "c9", name: "Edit", input: "" },
      {
        type: "run_result",
        stop: "error",
        text: "",
        errorText: "boom",
        permanent: true,
      },
    ]);
    expect(messages[0]).toMatchObject({
      failed: true,
      failureDetail:
        "boom (permanent — retrying the identical request cannot succeed)",
      toolCalls: [{ callId: "c9", status: "failed" }],
    });
  });

  it("fills an empty assistant bubble from a clean terminal's final text", () => {
    const messages = run([
      { type: "tool_call", callId: "c2", name: "Read", input: "" },
      {
        type: "run_result",
        stop: "end_turn",
        text: "final",
        errorText: "",
        permanent: false,
      },
    ]);
    expect(messages[0].content).toBe("final");
  });

  it("stamps a non-error stop worth naming (budget) on the trailing assistant, and never a clean end_turn", () => {
    const stopped = run([
      { type: "token", text: "partial" },
      {
        type: "run_result",
        stop: "budget",
        text: "",
        errorText: "",
        permanent: false,
      },
    ]);
    expect(stopped[0]).toMatchObject({
      content: "partial",
      stopReason: "budget",
    });
    expect(stopped[0].failed).toBeUndefined();

    const clean = run([
      { type: "token", text: "done" },
      {
        type: "run_result",
        stop: "end_turn",
        text: "",
        errorText: "",
        permanent: false,
      },
    ]);
    expect(clean[0].stopReason).toBeUndefined();
  });

  it("opens a bubble for a limit stop with no assistant activity — a stopped turn never vanishes", () => {
    const messages = run([
      { type: "user_prompt", text: "do the thing" },
      {
        type: "run_result",
        stop: "max_turns",
        text: "",
        errorText: "",
        permanent: false,
      },
    ]);
    expect(messages.at(-1)).toMatchObject({
      role: "assistant",
      content: "",
      stopReason: "max_turns",
    });
  });

  it("ignores the transient status event — a replay must not resurrect a status line", () => {
    const before: AgentMessage[] = [
      { id: "m1", role: "assistant", content: "hi", timestamp: 0 },
    ];
    expect(
      reduceWatchEvent(
        before,
        {
          type: "status",
          text: "no progress after continuation attempts; ending run",
          tone: "warn",
          kind: "no_progress",
        },
        nextId,
      ),
    ).toBe(before);
    expect(before[0].notices).toBeUndefined();
  });

  it("leaves the transcript untouched for hook-state kinds (asks, usage)", () => {
    const before: AgentMessage[] = [
      { id: "m1", role: "assistant", content: "hi", timestamp: 0 },
    ];
    expect(
      reduceWatchEvent(
        before,
        {
          type: "approval",
          approvalId: "a1",
          sessionId: "s",
          toolName: "Bash",
          description: "",
          details: "",
        },
        nextId,
      ),
    ).toBe(before);
  });

  it("stamps the downstream provider route on the trailing assistant and a later token keeps it", () => {
    // A LIVE watch of a run driven elsewhere: the route marks THIS turn's
    // bubble; the next user record opens a fresh bubble with no route (the
    // daemon never logs provider.route, so replay never carries it).
    const messages = run([
      { type: "token", text: "Hel" },
      { type: "provider_route", label: "Google" },
      { type: "token", text: "lo" },
      { type: "user_prompt", text: "and now?" },
      { type: "token", text: "Next" },
    ]);
    expect(messages).toHaveLength(3);
    expect(messages[0]).toMatchObject({
      role: "assistant",
      content: "Hello",
      route: "Google",
    });
    expect(messages[2]).toMatchObject({ role: "assistant", content: "Next" });
    expect(messages[2].route).toBeUndefined();
  });

  it("opens an assistant bubble for a route that arrives before any token", () => {
    const messages = run([{ type: "provider_route", label: "azure" }]);
    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatchObject({ role: "assistant", route: "azure" });
  });
});

// ── delegation cards (D1) ────────────────────────────────────────────────────

describe("applyDelegationEvent", () => {
  const base = (): AgentMessage[] => [
    { id: "u1", role: "user", content: "go", timestamp: 0 },
    {
      id: "a1",
      role: "assistant",
      content: "",
      timestamp: 0,
      delegations: [
        {
          kind: "subagent",
          label: "explore the repo",
          detail: "",
          childId: "subagent-abc",
        },
      ],
    },
  ];

  it("ticks the running counters on delegation_progress, keyed by childId", () => {
    let messages = applyDelegationEvent(base(), {
      type: "delegation_progress",
      childId: "subagent-abc",
      toolCount: 3,
      inputTokens: 1200,
      outputTokens: 80,
      toolName: "Read",
    });
    messages = applyDelegationEvent(messages, {
      type: "delegation_progress",
      childId: "subagent-abc",
      toolCount: 4,
      toolName: "Grep",
    });
    expect(messages[1].delegations?.[0]).toMatchObject({
      childId: "subagent-abc",
      toolCount: 4,
      inputTokens: 1200,
      outputTokens: 80,
      lastTool: "Grep",
    });
    // The card is still running: no stop yet.
    expect(messages[1].delegations?.[0].stop).toBeUndefined();
  });

  it("stamps stop, duration, and the failure cause on delegation_end — a failed child never vanishes", () => {
    const messages = applyDelegationEvent(base(), {
      type: "delegation_end",
      childId: "subagent-abc",
      stop: "error",
      toolCount: 7,
      durationMs: 4200,
      cause: "provider rejected the request",
    });
    expect(messages[1].delegations?.[0]).toMatchObject({
      stop: "error",
      toolCount: 7,
      durationMs: 4200,
      cause: "provider rejected the request",
    });
  });

  it("returns the SAME array when no card carries the child (progress without a start)", () => {
    const before = base();
    expect(
      applyDelegationEvent(before, {
        type: "delegation_progress",
        childId: "subagent-unknown",
        toolCount: 1,
      }),
    ).toBe(before);
  });

  it("accumulates a bounded tool-chip trace: tool.call pends, tool.result resolves", () => {
    let messages = applyDelegationEvent(base(), {
      type: "delegation_progress",
      childId: "subagent-abc",
      innerKind: "tool.call",
      toolName: "Read",
      detail: "path: a.go",
    });
    messages = applyDelegationEvent(messages, {
      type: "delegation_progress",
      childId: "subagent-abc",
      innerKind: "tool.result",
      toolName: "Read",
      isError: true,
      detail: "no such file",
    });
    expect(messages[1].delegations?.[0]).toMatchObject({
      lastTool: "Read",
      lastToolError: true,
      trace: [
        {
          kind: "tool",
          name: "Read",
          detail: "no such file",
          isError: true,
          pending: false,
        },
      ],
    });
    for (let index = 0; index < MAX_TRACE_ENTRIES + 3; index += 1) {
      messages = applyDelegationEvent(messages, {
        type: "delegation_progress",
        childId: "subagent-abc",
        innerKind: "tool.call",
        toolName: `T${index}`,
      });
    }
    expect(messages[1].delegations?.[0].trace).toHaveLength(MAX_TRACE_ENTRIES);
  });

  it("matches a parallel branch by (parentCallId, branchIndex) when the frame carries no childId", () => {
    const start: AgentMessage[] = [
      {
        id: "a1",
        role: "assistant",
        content: "",
        timestamp: 0,
        toolCalls: [
          { callId: "call-p", name: "Parallel", input: "", status: "running" },
        ],
      },
    ];
    let messages = applyDelegationEvent(start, {
      type: "delegation",
      kind: "parallel",
      label: "branch 2",
      detail: "",
      childId: "parallel-call-p-1",
      parentCallId: "call-p",
      branchIndex: 1,
    });
    messages = applyDelegationEvent(messages, {
      type: "delegation_progress",
      parentCallId: "call-p",
      branchIndex: 1,
      toolCount: 2,
      innerKind: "tool.call",
      toolName: "Bash",
    });
    expect(messages[0].delegations?.[0]).toMatchObject({
      childId: "parallel-call-p-1",
      toolCount: 2,
      lastTool: "Bash",
    });
    messages = applyDelegationEvent(messages, {
      type: "delegation_end",
      childId: "parallel-call-p-1",
      parentCallId: "call-p",
      branchIndex: 1,
      stop: "error",
      failed: true,
    });
    expect(messages[0].delegations?.[0]).toMatchObject({
      stop: "error",
      failed: true,
    });
    messages = applyDelegationEvent(messages, {
      type: "parallel_end",
      parentCallId: "call-p",
      join: "first",
      branchCount: 2,
      winner: 1,
      stop: "end_turn",
    });
    expect(messages[0].delegations?.[0].winner).toBe(true);
    expect(messages[0].delegationGroups?.["call-p"]).toMatchObject({
      kind: "parallel",
      winner: 1,
      done: true,
    });
  });

  it("matches a team member by name and backfills its childId from team_member; team_end lands dispositions", () => {
    const start: AgentMessage[] = [
      {
        id: "a1",
        role: "assistant",
        content: "",
        timestamp: 0,
        toolCalls: [
          { callId: "call-t", name: "Team", input: "", status: "running" },
        ],
      },
    ];
    let messages = applyDelegationEvent(start, {
      type: "delegation",
      kind: "team",
      label: "reviewer (lead)",
      detail: "big-1",
      teamId: "team-1",
      parentCallId: "call-t",
      memberName: "reviewer",
      lead: true,
    });
    messages = applyDelegationEvent(messages, {
      type: "team_member",
      teamId: "team-1",
      parentCallId: "call-t",
      member: "reviewer",
      memberSessionId: "team-team-1-reviewer",
      innerKind: "turn.end",
      isError: false,
      inputTokens: 3000,
      outputTokens: 100,
      contextUsed: 3000,
      contextWindow: 200000,
    });
    expect(messages[0].delegations?.[0]).toMatchObject({
      memberName: "reviewer",
      childId: "team-team-1-reviewer",
      inputTokens: 3000,
      contextUsed: 3000,
      contextWindow: 200000,
      idle: false,
    });
    messages = applyDelegationEvent(messages, {
      type: "team_end",
      teamId: "team-1",
      parentCallId: "call-t",
      rounds: 3,
      stop: "end_turn",
      tasks: [],
      findings: [],
      dispositions: [
        { name: "reviewer", stopped: true, errorRounds: 2, reason: "error" },
      ],
    });
    expect(messages[0].delegations?.[0]).toMatchObject({
      stopped: true,
      stopReason: "error",
      errorRounds: 2,
      stop: "error",
    });
    expect(messages[0].delegationGroups?.["call-t"]).toMatchObject({
      kind: "team",
      teamId: "team-1",
      done: true,
      rounds: 3,
      stoppedCount: 1,
    });
  });
});

describe("reduceWatchEvent delegation frames", () => {
  let serial = 0;
  const nextId = () => `d-${++serial}`;

  it("leaves messages unchanged for team_tasks / team_findings (panel-only)", () => {
    const before: AgentMessage[] = [
      { id: "m1", role: "assistant", content: "hi", timestamp: 0 },
    ];
    expect(
      reduceWatchEvent(
        before,
        {
          type: "team_tasks",
          teamId: "team-1",
          parentCallId: "call-t",
          tasks: [],
        },
        nextId,
      ),
    ).toBe(before);
    expect(
      reduceWatchEvent(
        before,
        {
          type: "team_findings",
          teamId: "team-1",
          parentCallId: "call-t",
          findings: [],
        },
        nextId,
      ),
    ).toBe(before);
  });

  it("opens an assistant bubble for a delegation start with no owning turn, like other activity", () => {
    const messages = reduceWatchEvent(
      [{ id: "u1", role: "user", content: "go", timestamp: 0 }],
      {
        type: "delegation",
        kind: "subagent",
        label: "explore",
        detail: "",
        childId: "subagent-1",
      },
      nextId,
    );
    expect(messages).toHaveLength(2);
    expect(messages[1]).toMatchObject({
      role: "assistant",
      delegations: [{ childId: "subagent-1" }],
    });
  });
});

// ── steer echo attachments (ADR 0251 / C2.2) ─────────────────────────────────

describe("attachmentsFromSteerParts", () => {
  it("renders inline bytes as data: URLs and passes url parts through", () => {
    expect(
      attachmentsFromSteerParts([
        { kind: "image", mimeType: "image/png", data: "aGk=" },
        { kind: "audio", mimeType: "audio/wav", url: "mecatl://a" },
      ]),
    ).toEqual([
      {
        name: "image-1.png",
        type: "image/png",
        url: "data:image/png;base64,aGk=",
      },
      { name: "audio-2.wav", type: "audio/wav", url: "mecatl://a" },
    ]);
  });

  it("returns undefined for an empty bundle", () => {
    expect(attachmentsFromSteerParts(undefined)).toBeUndefined();
    expect(attachmentsFromSteerParts([])).toBeUndefined();
  });
});

describe("reduceWatchEvent steer parts", () => {
  it("keeps the committed steer's media on the rebuilt user bubble", () => {
    let serial = 100;
    const messages = reduceWatchEvent(
      [],
      {
        type: "steer",
        text: "look at this",
        messageId: "m-1",
        parts: [{ kind: "image", mimeType: "image/png", data: "aGk=" }],
      },
      () => `id-${++serial}`,
    );
    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatchObject({
      role: "user",
      content: "look at this",
      attachments: [
        {
          name: "image-1.png",
          type: "image/png",
          url: "data:image/png;base64,aGk=",
        },
      ],
    });
  });
});
