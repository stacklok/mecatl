import type { EventUsage, Event as SdkEvent } from "@stacklok-oss/mecatl-sdk";
import { describe, expect, it } from "vitest";
import { translateEvent } from "./events";

/**
 * Builds one SDK-decoded event. The SDK owns the wire decode (protojson,
 * bigint counters, numeric enums); these tests feed the decoded shape and pin
 * the rendering rules that turn it into StreamEvents.
 */
function sdkEvent(
  kind: string,
  payload?: unknown,
  common: Partial<{
    runId: string;
    seq: bigint;
    text: string;
    wireKind: string;
  }> = {},
): SdkEvent {
  return {
    kind,
    payload,
    runId: common.runId ?? "",
    seq: common.seq ?? BigInt(0),
    text: common.text ?? "",
    turn: 0,
    usage: undefined,
    ...(kind === "unknown"
      ? { rawData: null, transport: "http", wireKind: common.wireKind ?? "" }
      : {}),
  } as unknown as SdkEvent;
}

const usage = (
  partial: Partial<Record<keyof EventUsage, bigint>>,
): EventUsage => ({
  inputTokens: BigInt(0),
  outputTokens: BigInt(0),
  cacheReadTokens: BigInt(0),
  cacheWriteTokens: BigInt(0),
  reasoningTokens: BigInt(0),
  ...partial,
});

const subagent = (partial: Record<string, unknown>) => ({
  background: false,
  cause: "",
  childId: "",
  detail: "",
  durationMs: BigInt(0),
  goal: "",
  innerKind: "",
  isError: false,
  model: "",
  parentCallId: "",
  routedCategory: "",
  routedModel: "",
  routingReason: "",
  stop: "",
  text: "",
  toolCount: 0,
  toolName: "",
  usage: undefined,
  ...partial,
});

const result = (partial: Record<string, unknown>) => ({
  error: "",
  permanent: false,
  stop: "",
  text: "",
  ...partial,
});

const translate = (event: SdkEvent) => translateEvent(event, "session-1");

describe("translateEvent", () => {
  it("maps deltas to tokens and reasoning", () => {
    expect(
      translate(sdkEvent("message.delta", undefined, { text: "abc" })),
    ).toEqual([{ type: "token", text: "abc" }]);
    expect(
      translate(sdkEvent("reasoning.delta", undefined, { text: "hm" })),
    ).toEqual([{ type: "reasoning", text: "hm" }]);
    // An empty delta renders nothing.
    expect(translate(sdkEvent("message.delta"))).toEqual([]);
  });

  it("renders a failed result as a run_result with the failure, never a success", () => {
    expect(
      translate(
        sdkEvent(
          "result",
          result({ stop: "error", error: "boom", permanent: false }),
        ),
      ),
    ).toEqual([
      {
        type: "run_result",
        stop: "error",
        text: "",
        errorText: "boom",
        permanent: false,
      },
    ]);
  });

  it("emits all five usage token fields from the terminal frame, bigint → number", () => {
    const events = translate(
      sdkEvent(
        "result",
        result({
          stop: "end_turn",
          text: "done",
          usage: usage({
            inputTokens: BigInt(100),
            outputTokens: BigInt(50),
            cacheReadTokens: BigInt(30),
            cacheWriteTokens: BigInt(10),
            reasoningTokens: BigInt(5),
          }),
        }),
      ),
    );
    expect(events[0]).toEqual({
      type: "usage",
      inputTokens: 100,
      outputTokens: 50,
      cacheReadTokens: 30,
      cacheWriteTokens: 10,
      reasoningTokens: 5,
      estimatedCost: null,
    });
    expect(events[1]).toMatchObject({ type: "run_result", stop: "end_turn" });
  });

  it("surfaces a permission ask and its retraction", () => {
    expect(
      translate(
        sdkEvent("permission.ask", {
          askId: "a1",
          tool: "bash",
          reason: "runs a command",
          args: "",
        }),
      ),
    ).toEqual([
      {
        type: "approval",
        approvalId: "a1",
        sessionId: "session-1",
        toolName: "bash",
        description: "bash needs your approval.",
        details: "runs a command",
      },
    ]);
    expect(
      translate(
        sdkEvent("permission.retract", {
          askId: "a1",
          tool: "",
          reason: "",
          args: "",
        }),
      ),
    ).toEqual([{ type: "retract", approvalId: "a1" }]);
  });

  it("renders delegation badges for subagent, team roster, and parallel branch starts", () => {
    expect(
      translate(
        sdkEvent(
          "subagent.start",
          subagent({
            goal: "explore the repo",
            routedCategory: "explore",
            routedModel: "small-1",
          }),
        ),
      ),
    ).toEqual([
      {
        type: "delegation",
        kind: "subagent",
        label: "explore the repo",
        detail: "explore → small-1",
        childId: undefined,
        background: undefined,
        routingReason: undefined,
      },
    ]);
    const member = (partial: Record<string, unknown>) => ({
      lead: false,
      model: "",
      mutating: false,
      name: "",
      role: "",
      routedCategory: "",
      routedModel: "",
      routingReason: "",
      ...partial,
    });
    expect(
      translate(
        sdkEvent("team.start", {
          roster: [
            member({ name: "reviewer", role: "lead", model: "big-1" }),
            member({ name: "tester" }),
          ],
        }),
      ),
    ).toEqual([
      {
        type: "delegation",
        kind: "team",
        label: "reviewer (lead)",
        detail: "big-1",
      },
      { type: "delegation", kind: "team", label: "tester", detail: "" },
    ]);
    const parallel = (partial: Record<string, unknown>) => ({
      branchCount: 0,
      branchIndex: 0,
      branchLabel: "",
      kind: "",
      model: "",
      routedCategory: "",
      routedModel: "",
      ...partial,
    });
    expect(
      translate(
        sdkEvent(
          "parallel.branch",
          parallel({ kind: "branch_start", branchIndex: 1 }),
        ),
      ),
    ).toEqual([
      { type: "delegation", kind: "parallel", label: "branch 2", detail: "" },
    ]);
    // Only a branch START is a badge; other branch lifecycle frames are silent.
    expect(
      translate(sdkEvent("parallel.branch", parallel({ kind: "branch_end" }))),
    ).toEqual([]);
  });

  it("translates a steer drain echo to the steer arm with its watermark id", () => {
    expect(
      translate(
        sdkEvent("steer", {
          text: "focus on the failing test",
          messageId: "steer-7-2",
          parts: [],
        }),
      ),
    ).toEqual([
      {
        type: "steer",
        text: "focus on the failing test",
        messageId: "steer-7-2",
        parts: undefined,
      },
    ]);
  });

  it("surfaces an unknown event kind as a notice naming the wire kind, never a silent drop", () => {
    expect(
      translate(sdkEvent("unknown", undefined, { wireKind: "something.new" })),
    ).toEqual([
      {
        type: "notice",
        text: "Mecatl sent an event this Studio version does not render yet: something.new",
      },
    ]);
    // A kind the SDK types but Studio has no surface for reads the same way.
    expect(translate(sdkEvent("session.title", {}))).toEqual([
      {
        type: "notice",
        text: "Mecatl sent an event this Studio version does not render yet: session.title",
      },
    ]);
  });

  it("passes advisory text through as a notice and keeps lifecycle markers silent", () => {
    expect(
      translate(
        sdkEvent("recover_notice", undefined, {
          text: "resumed after a retry",
        }),
      ),
    ).toEqual([{ type: "notice", text: "resumed after a retry" }]);
    expect(translate(sdkEvent("turn.start"))).toEqual([]);
    expect(translate(sdkEvent("session.init"))).toEqual([]);
    // Routing is an implementation detail, not something shown per turn.
    expect(
      translate(
        sdkEvent("provider.route", undefined, { text: "routed to small-1" }),
      ),
    ).toEqual([]);
  });

  it("renders a durable-log user_prompt as a user message event", () => {
    expect(
      translate(sdkEvent("user_prompt", { text: "fix the bug", parts: [] })),
    ).toEqual([{ type: "user_prompt", text: "fix the bug" }]);
    // A media-only prompt (no text) stays quiet rather than an empty bubble.
    expect(translate(sdkEvent("user_prompt", { text: "", parts: [] }))).toEqual(
      [],
    );
  });

  it("renders a durable-log approval as a verdict event — tool name and verdict, never args", () => {
    expect(
      translate(
        sdkEvent("approval", {
          askId: "a1",
          tool: "Bash",
          verdict: "allow_once",
          allowAlways: false,
          callId: "c1",
        }),
      ),
    ).toEqual([
      {
        type: "approval_verdict",
        approvalId: "a1",
        toolName: "Bash",
        verdict: "allow_once",
      },
    ]);
  });

  it("keeps the compaction archive silent — audit history, not transcript", () => {
    expect(translate(sdkEvent("compaction.archive", { replaced: [] }))).toEqual(
      [],
    );
  });

  it("stamps runId onto every translated event (ADR 0249)", () => {
    expect(
      translate(
        sdkEvent("message.delta", undefined, { text: "x", runId: "run-7" }),
      ),
    ).toEqual([{ type: "token", text: "x", runId: "run-7" }]);
    const results = translate(
      sdkEvent(
        "result",
        result({
          stop: "end_turn",
          text: "done",
          usage: usage({ inputTokens: BigInt(1) }),
        }),
        { runId: "run-7" },
      ),
    );
    expect(results).toHaveLength(2);
    expect(results.every((event) => event.runId === "run-7")).toBe(true);
    // Empty is meaningful — a session-scoped event stays unstamped.
    expect(
      translate(sdkEvent("message.delta", undefined, { text: "x" })),
    ).toEqual([{ type: "token", text: "x" }]);
  });

  it("decodes the typed retry disposition and stream progress off a failed terminal (ADR 0239)", () => {
    const [failed] = translate(
      sdkEvent(
        "result",
        result({
          stop: "error",
          error: "stream died",
          retryDisposition: 2,
          streamProgress: 2,
        }),
      ),
    );
    expect(failed).toMatchObject({
      type: "run_result",
      stop: "error",
      retryDisposition: "retryable",
      streamProgress: "precommit",
    });
    // 3 = permanent, 4 = complete.
    const [permanent] = translate(
      sdkEvent(
        "result",
        result({
          stop: "error",
          error: "bad request",
          permanent: true,
          retryDisposition: 3,
          streamProgress: 4,
        }),
      ),
    );
    expect(permanent).toMatchObject({
      retryDisposition: "permanent",
      streamProgress: "complete",
      permanent: true,
    });
    const [visible] = translate(
      sdkEvent(
        "result",
        result({ stop: "error", retryDisposition: 1, streamProgress: 3 }),
      ),
    );
    expect(visible).toMatchObject({
      retryDisposition: "unknown",
      streamProgress: "visible",
    });
  });

  it("keeps the retry fields ABSENT against an old daemon, and degrades unrecognized values to unknown", () => {
    // Presence-aware: an old server sends neither field.
    const [legacy] = translate(
      sdkEvent("result", result({ stop: "error", error: "boom" })),
    );
    expect(legacy).toMatchObject({ type: "run_result" });
    expect(
      (legacy as { retryDisposition?: string }).retryDisposition,
    ).toBeUndefined();
    expect(
      (legacy as { streamProgress?: string }).streamProgress,
    ).toBeUndefined();
    // A present-but-unrecognized value must never read as retryable.
    const [odd] = translate(
      sdkEvent(
        "result",
        result({ stop: "error", retryDisposition: 99, streamProgress: 99 }),
      ),
    );
    expect(odd).toMatchObject({
      retryDisposition: "unknown",
      streamProgress: "unknown",
    });
  });

  it("renders model.retry as the quiet retrying line (B2.1)", () => {
    expect(
      translate(
        sdkEvent("model.retry", { retryDisposition: 2, streamProgress: 2 }),
      ),
    ).toEqual([{ type: "notice", text: "Retrying the failed step…" }]);
  });

  it("decodes the steer echo's committed media parts to base64 (ADR 0251)", () => {
    const [echo] = translate(
      sdkEvent("steer", {
        text: "look at this",
        messageId: "steer-3",
        parts: [
          {
            kind: 1,
            mimeType: "image/png",
            data: new Uint8Array([104, 105]),
            url: "",
          },
          {
            kind: 2,
            mimeType: "audio/wav",
            data: new Uint8Array(),
            url: "mecatl://a",
          },
          {
            kind: 0,
            mimeType: "application/x-unknown",
            data: new Uint8Array([1]),
            url: "",
          },
        ],
      }),
    );
    expect(echo).toEqual({
      type: "steer",
      text: "look at this",
      messageId: "steer-3",
      parts: [
        { kind: "image", mimeType: "image/png", data: "aGk=", url: undefined },
        {
          kind: "audio",
          mimeType: "audio/wav",
          data: undefined,
          url: "mecatl://a",
        },
      ],
    });
    // A part-less echo stays part-less (no empty array invented).
    const [plain] = translate(
      sdkEvent("steer", { text: "just text", messageId: "steer-4", parts: [] }),
    );
    expect((plain as { parts?: unknown }).parts).toBeUndefined();
  });

  it("translates subagent.tool into live delegation progress (D1)", () => {
    expect(
      translate(
        sdkEvent(
          "subagent.tool",
          subagent({
            parentCallId: "call-1",
            childId: "subagent-abc",
            toolName: "Read",
            toolCount: 4,
            usage: usage({
              inputTokens: BigInt(1200),
              outputTokens: BigInt(300),
            }),
          }),
        ),
      ),
    ).toEqual([
      {
        type: "delegation_progress",
        childId: "subagent-abc",
        toolCount: 4,
        inputTokens: 1200,
        outputTokens: 300,
        toolName: "Read",
      },
    ]);
    // A frame with no child id has nothing to key on and stays quiet.
    expect(translate(sdkEvent("subagent.tool", subagent({})))).toEqual([]);
  });

  it("translates subagent.end into the terminal card update — a failed child carries its cause (D1)", () => {
    expect(
      translate(
        sdkEvent(
          "subagent.end",
          subagent({
            childId: "subagent-abc",
            stop: "error",
            toolCount: 7,
            durationMs: BigInt(4200),
            cause: "provider rejected the request",
            usage: usage({
              inputTokens: BigInt(9000),
              outputTokens: BigInt(1500),
            }),
          }),
        ),
      ),
    ).toEqual([
      {
        type: "delegation_end",
        childId: "subagent-abc",
        stop: "error",
        toolCount: 7,
        inputTokens: 9000,
        outputTokens: 1500,
        durationMs: 4200,
        cause: "provider rejected the request",
      },
    ]);
  });

  it("carries childId, background, and routingReason on the subagent start badge (D1/D2.1)", () => {
    const [start] = translate(
      sdkEvent(
        "subagent.start",
        subagent({
          childId: "subagent-abc",
          goal: "explore the repo",
          background: true,
          routingReason: "pinned-model",
        }),
      ),
    );
    expect(start).toMatchObject({
      type: "delegation",
      kind: "subagent",
      label: "explore the repo",
      childId: "subagent-abc",
      background: true,
      routingReason: "pinned-model",
    });
  });

  it("pretty-prints tool args on the call card and relays the result", () => {
    expect(
      translate(
        sdkEvent("tool.call", {
          id: "c1",
          name: "bash",
          args: JSON.stringify({ command: "ls", timeout: 5 }),
        }),
      ),
    ).toEqual([
      {
        type: "tool_call",
        callId: "c1",
        name: "bash",
        input: "command: ls · timeout: 5",
        file: undefined,
      },
    ]);
    expect(
      translate(
        sdkEvent("tool.result", {
          blocks: [],
          callId: "c1",
          content: "ok",
          isError: false,
          structuredContent: "",
        }),
      ),
    ).toEqual([
      { type: "tool_result", callId: "c1", output: "ok", isError: false },
    ]);
  });
});
