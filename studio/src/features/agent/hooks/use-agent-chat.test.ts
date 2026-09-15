import { describe, expect, it } from "vitest";
import type { AgentMessage, StreamEvent } from "../types";
import {
  applyDelegationUpdate,
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
});

// ── delegation cards (D1) ────────────────────────────────────────────────────

describe("applyDelegationUpdate", () => {
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
    let messages = applyDelegationUpdate(base(), {
      type: "delegation_progress",
      childId: "subagent-abc",
      toolCount: 3,
      inputTokens: 1200,
      outputTokens: 80,
      toolName: "Read",
    });
    messages = applyDelegationUpdate(messages, {
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
    const messages = applyDelegationUpdate(base(), {
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
      applyDelegationUpdate(before, {
        type: "delegation_progress",
        childId: "subagent-unknown",
        toolCount: 1,
      }),
    ).toBe(before);
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
