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

const parallelPayload = (partial: Record<string, unknown>) => ({
  branchCount: 0,
  branchIndex: 0,
  branchLabel: "",
  childId: "",
  detail: "",
  durationMs: BigInt(0),
  failed: false,
  goal: "",
  innerKind: "",
  isError: false,
  join: "",
  kind: "",
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
  winner: -1,
  ...partial,
});

const teamPayload = (partial: Record<string, unknown>) => ({
  cause: "",
  contextUsed: BigInt(0),
  contextWindow: BigInt(0),
  detail: "",
  dispositions: [],
  findings: [],
  innerKind: "",
  isError: false,
  member: "",
  memberSessionId: "",
  parentCallId: "call-t",
  roster: [],
  rounds: 0,
  stop: "",
  tasks: [],
  teamId: "team-1",
  text: "",
  toolName: "",
  usage: undefined,
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

  it("carries an ask's reason and raw args verbatim alongside the flattened details", () => {
    const args = JSON.stringify({ command: "go test ./...", timeout_ms: 5000 });
    expect(
      translate(
        sdkEvent("permission.ask", {
          askId: "a2",
          tool: "Shell",
          reason: "runs a command",
          args,
        }),
      ),
    ).toEqual([
      {
        type: "approval",
        approvalId: "a2",
        sessionId: "session-1",
        toolName: "Shell",
        description: "Shell needs your approval.",
        details: "runs a command\n\ncommand: go test ./... · timeout_ms: 5000",
        reason: "runs a command",
        args,
      },
    ]);
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
        reason: "runs a command",
        args: "",
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
          teamId: "team-1",
          parentCallId: "call-t",
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
        teamId: "team-1",
        parentCallId: "call-t",
        memberName: "reviewer",
        model: "big-1",
      },
      {
        type: "delegation",
        kind: "team",
        label: "tester",
        detail: "",
        teamId: "team-1",
        parentCallId: "call-t",
        memberName: "tester",
      },
    ]);
    expect(
      translate(
        sdkEvent(
          "parallel.branch",
          parallelPayload({
            kind: "branch_start",
            branchIndex: 1,
            parentCallId: "call-p",
            childId: "parallel-call-p-1",
          }),
        ),
      ),
    ).toEqual([
      {
        type: "delegation",
        kind: "parallel",
        label: "branch 2",
        detail: "",
        childId: "parallel-call-p-1",
        parentCallId: "call-p",
        branchIndex: 1,
      },
    ]);
    // A branch END is the terminal card update — never silent.
    expect(
      translate(
        sdkEvent(
          "parallel.branch",
          parallelPayload({
            kind: "branch_end",
            branchIndex: 1,
            parentCallId: "call-p",
            childId: "parallel-call-p-1",
            stop: "error",
            failed: true,
            toolCount: 3,
            durationMs: BigInt(900),
          }),
        ),
      ),
    ).toEqual([
      {
        type: "delegation_end",
        childId: "parallel-call-p-1",
        parentCallId: "call-p",
        branchIndex: 1,
        stop: "error",
        failed: true,
        toolCount: 3,
        durationMs: 900,
      },
    ]);
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
  });

  it("maps session.title to a title metadata event (never a transcript notice)", () => {
    expect(
      translate(
        sdkEvent("session.title", {
          title: "Fix flake",
          provenance: "first-prompt",
          revision: BigInt(2),
          generationState: "completed",
        }),
      ),
    ).toEqual([
      {
        type: "title",
        title: "Fix flake",
        provenance: "first-prompt",
        revision: 2,
      },
    ]);
    // A pending-generation event carries no title yet and may carry no
    // revision on an older daemon: both cross as their honest empties.
    expect(translate(sdkEvent("session.title", {}))).toEqual([
      { type: "title", title: "", provenance: "", revision: null },
    ]);
  });

  it("passes durable advisory text through as a notice and keeps lifecycle markers silent", () => {
    expect(
      translate(
        sdkEvent("tool.progress", undefined, { text: "read 3 of 9 files" }),
      ),
    ).toEqual([{ type: "notice", text: "read 3 of 9 files" }]);
    expect(
      translate(
        sdkEvent("compaction", undefined, {
          text: "[conversation compacted] older turns summarised",
        }),
      ),
    ).toEqual([
      {
        type: "notice",
        text: "[conversation compacted] older turns summarised",
      },
    ]);
    expect(translate(sdkEvent("turn.start"))).toEqual([]);
    expect(translate(sdkEvent("session.init"))).toEqual([]);
  });

  it("routes the no-progress nudge and the recover notice to the TRANSIENT status line, not the transcript", () => {
    expect(
      translate(
        sdkEvent("recover_notice", undefined, {
          text: "resumed after a retry",
        }),
      ),
    ).toEqual([
      {
        type: "status",
        text: "resumed after a retry",
        tone: "warn",
        kind: "recover_notice",
      },
    ]);
    expect(
      translate(
        sdkEvent("no_progress", undefined, {
          text: "no progress after continuation attempts; ending run",
        }),
      ),
    ).toEqual([
      {
        type: "status",
        text: "no progress after continuation attempts; ending run",
        tone: "warn",
        kind: "no_progress",
      },
    ]);
    // A text-less advisory has nothing to say on the status line either.
    expect(translate(sdkEvent("no_progress"))).toEqual([]);
  });

  it("translates turn.end into a per-turn stat event (bigint → number), never silent", () => {
    expect(
      translate(
        sdkEvent("turn.end", {
          durationMs: BigInt(4100),
          usage: usage({
            inputTokens: BigInt(1200),
            outputTokens: BigInt(340),
            cacheReadTokens: BigInt(420),
            cacheWriteTokens: BigInt(7),
          }),
        }),
      ),
    ).toEqual([
      {
        type: "turn_end",
        durationMs: 4100,
        inputTokens: 1200,
        outputTokens: 340,
        cacheReadTokens: 420,
        cacheWriteTokens: 7,
        reasoningTokens: 0,
      },
    ]);
    // A turn.end without usage still reports its elapsed model-call time.
    expect(
      translate(sdkEvent("turn.end", { durationMs: BigInt(250) })),
    ).toEqual([
      {
        type: "turn_end",
        durationMs: 250,
        inputTokens: 0,
        outputTokens: 0,
        cacheReadTokens: 0,
        cacheWriteTokens: 0,
        reasoningTokens: 0,
      },
    ]);
  });

  it("translates provider.route into status-strip metadata, never a transcript line", () => {
    // The Go loop emits the route on `text` with no payload (ADR 0210); the
    // strip's model segment renders it as `model/route`.
    expect(
      translate(
        sdkEvent("provider.route", undefined, { text: "openai/fast-lane" }),
      ),
    ).toEqual([{ type: "provider_route", label: "openai/fast-lane" }]);
    // A cache hit strips the metadata upstream: an empty text is nothing to
    // show — no fabricated route, no "not rendered yet" notice.
    expect(translate(sdkEvent("provider.route", undefined))).toEqual([]);
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
        parentCallId: "call-1",
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
        rawArgs: '{"command":"ls","timeout":5}',
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

  it("stamps the changed path on an Edit/Write call and none on a read", () => {
    const [edit] = translate(
      sdkEvent("tool.call", {
        id: "c2",
        name: "Edit",
        args: JSON.stringify({
          path: "a.ts",
          old_string: "x",
          new_string: "y",
        }),
      }),
    );
    expect(edit).toMatchObject({
      type: "tool_call",
      name: "Edit",
      changedPath: "a.ts",
      rawArgs: '{"path":"a.ts","old_string":"x","new_string":"y"}',
    });
    // Edit names no final content, so it is a changed path but not a file.
    expect(edit.type === "tool_call" && edit.file).toBeUndefined();

    const [write] = translate(
      sdkEvent("tool.call", {
        id: "c3",
        name: "Write",
        args: JSON.stringify({ path: "b.ts", content: "hi" }),
      }),
    );
    expect(write).toMatchObject({
      changedPath: "b.ts",
      file: { path: "b.ts", name: "b.ts", content: "hi" },
    });

    const [read] = translate(
      sdkEvent("tool.call", {
        id: "c4",
        name: "Read",
        args: JSON.stringify({ path: "c.ts" }),
      }),
    );
    expect(read.type === "tool_call" && read.changedPath).toBeUndefined();
  });

  it("decodes a result's image and resource-link blocks into parts, ignoring text and unknown kinds", () => {
    const [result] = translate(
      sdkEvent("tool.result", {
        callId: "c1",
        content: "ok",
        isError: false,
        structuredContent: "",
        blocks: [
          { kind: 1, text: "ok" },
          {
            kind: 2,
            mimeType: "image/png",
            data: new Uint8Array([137, 80, 78, 71]),
          },
          {
            kind: 4,
            url: "https://example.com/report.html",
            name: "report.html",
            title: "Report",
          },
          { kind: 4, url: "", name: "no-url" },
          { kind: 2, mimeType: "image/png", data: new Uint8Array() },
          { kind: 5, text: "embedded" },
        ],
      }),
    );
    expect(result).toEqual({
      type: "tool_result",
      callId: "c1",
      output: "ok",
      isError: false,
      parts: [
        { kind: "image", mimeType: "image/png", data: "iVBORw==" },
        {
          kind: "resource_link",
          url: "https://example.com/report.html",
          name: "report.html",
          title: "Report",
        },
      ],
    });
    // Text-only blocks yield no parts at all (absent, not an empty list).
    const [plain] = translate(
      sdkEvent("tool.result", {
        callId: "c1",
        content: "ok",
        isError: false,
        structuredContent: "",
        blocks: [{ kind: 1, text: "ok" }],
      }),
    );
    expect(plain.type === "tool_result" && plain.parts).toBeUndefined();
  });

  // ── delegation protocol (parallel.* / team.* / subagent.tool previews) ────

  it("carries the child-activity preview on subagent.tool (inner kind, error, detail, text)", () => {
    expect(
      translate(
        sdkEvent(
          "subagent.tool",
          subagent({
            parentCallId: "call-1",
            childId: "subagent-abc",
            innerKind: "tool.result",
            toolName: "Read",
            isError: true,
            detail: "no such file",
            text: "",
            toolCount: 2,
          }),
        ),
      ),
    ).toEqual([
      {
        type: "delegation_progress",
        childId: "subagent-abc",
        parentCallId: "call-1",
        toolCount: 2,
        toolName: "Read",
        innerKind: "tool.result",
        isError: true,
        detail: "no such file",
      },
    ]);
    // subagent.start / subagent.end carry the call id and model too.
    const [start] = translate(
      sdkEvent(
        "subagent.start",
        subagent({ childId: "c", parentCallId: "call-1", model: "small-1" }),
      ),
    );
    expect(start).toMatchObject({ parentCallId: "call-1", model: "small-1" });
    const [end] = translate(
      sdkEvent(
        "subagent.end",
        subagent({ childId: "c", parentCallId: "call-1", stop: "end_turn" }),
      ),
    );
    expect(end).toMatchObject({
      type: "delegation_end",
      parentCallId: "call-1",
    });
  });

  it("orders a team roster LEAD FIRST and carries the member handles", () => {
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
    const events = translate(
      sdkEvent(
        "team.start",
        teamPayload({
          roster: [
            member({ name: "tester", mutating: true, routingReason: "pinned" }),
            member({ name: "coordinator", role: "lead", lead: true }),
          ],
        }),
      ),
    );
    expect(events.map((event) => (event as { label: string }).label)).toEqual([
      "coordinator (lead)",
      "tester",
    ]);
    expect(events[0]).toMatchObject({
      memberName: "coordinator",
      lead: true,
      teamId: "team-1",
      parentCallId: "call-t",
    });
    expect(events[1]).toMatchObject({
      memberName: "tester",
      mutating: true,
      routingReason: "pinned",
    });
    expect((events[1] as { lead?: boolean }).lead).toBeUndefined();
  });

  it("translates parallel.start / parallel.end into the group header and terminal", () => {
    expect(
      translate(
        sdkEvent(
          "parallel.start",
          parallelPayload({
            parentCallId: "call-p",
            join: "first",
            branchCount: 3,
          }),
        ),
      ),
    ).toEqual([
      {
        type: "parallel_start",
        parentCallId: "call-p",
        join: "first",
        branchCount: 3,
      },
    ]);
    expect(
      translate(
        sdkEvent(
          "parallel.end",
          parallelPayload({
            parentCallId: "call-p",
            join: "first",
            branchCount: 3,
            winner: 1,
            stop: "end_turn",
            usage: usage({
              inputTokens: BigInt(500),
              outputTokens: BigInt(50),
            }),
          }),
        ),
      ),
    ).toEqual([
      {
        type: "parallel_end",
        parentCallId: "call-p",
        join: "first",
        branchCount: 3,
        winner: 1,
        stop: "end_turn",
        inputTokens: 500,
        outputTokens: 50,
      },
    ]);
    // join=all / none succeeded: winner −1 passes through verbatim.
    const [none] = translate(
      sdkEvent(
        "parallel.end",
        parallelPayload({ parentCallId: "call-p", join: "all", winner: -1 }),
      ),
    );
    expect(none).toMatchObject({ winner: -1, join: "all" });
  });

  it("keys a parallel branch_tool by (parentCallId, branchIndex) — it carries no child id", () => {
    expect(
      translate(
        sdkEvent(
          "parallel.branch",
          parallelPayload({
            kind: "branch_tool",
            parentCallId: "call-p",
            branchIndex: 2,
            toolCount: 5,
            innerKind: "tool.call",
            toolName: "Bash",
            detail: "command: go test",
            usage: usage({ inputTokens: BigInt(10) }),
          }),
        ),
      ),
    ).toEqual([
      {
        type: "delegation_progress",
        parentCallId: "call-p",
        branchIndex: 2,
        toolCount: 5,
        inputTokens: 10,
        outputTokens: 0,
        toolName: "Bash",
        innerKind: "tool.call",
        detail: "command: go test",
      },
    ]);
    // An unrecognized branch kind is quiet (the per-branch kinds are closed).
    expect(
      translate(
        sdkEvent("parallel.branch", parallelPayload({ kind: "branch_wat" })),
      ),
    ).toEqual([]);
  });

  it("translates team.member with its inner kind, usage, context meter (bigint → number), and cause", () => {
    expect(
      translate(
        sdkEvent(
          "team.member",
          teamPayload({
            member: "tester",
            memberSessionId: "team-team-1-tester",
            innerKind: "turn.end",
            usage: usage({
              inputTokens: BigInt(4000),
              outputTokens: BigInt(200),
            }),
            contextUsed: BigInt(4000),
            contextWindow: BigInt(128000),
          }),
        ),
      ),
    ).toEqual([
      {
        type: "team_member",
        teamId: "team-1",
        parentCallId: "call-t",
        member: "tester",
        memberSessionId: "team-team-1-tester",
        innerKind: "turn.end",
        isError: false,
        inputTokens: 4000,
        outputTokens: 200,
        contextUsed: 4000,
        contextWindow: 128000,
      },
    ]);
    const [call] = translate(
      sdkEvent(
        "team.member",
        teamPayload({
          member: "tester",
          innerKind: "tool.call",
          toolName: "Edit",
          detail: "path: x.go",
        }),
      ),
    );
    expect(call).toMatchObject({
      innerKind: "tool.call",
      toolName: "Edit",
      detail: "path: x.go",
      contextUsed: 0,
      contextWindow: 0,
    });
    const [failed] = translate(
      sdkEvent(
        "team.member",
        teamPayload({
          member: "tester",
          innerKind: "result",
          text: "gave up",
          cause: "provider outage",
        }),
      ),
    );
    expect(failed).toMatchObject({
      innerKind: "result",
      text: "gave up",
      cause: "provider outage",
    });
  });

  it("snapshots team.tasks and team.findings", () => {
    expect(
      translate(
        sdkEvent(
          "team.tasks",
          teamPayload({
            tasks: [
              {
                id: "t1",
                state: "in_progress",
                assignee: "tester",
                deps: ["t0"],
                description: "run the suite",
              },
            ],
          }),
        ),
      ),
    ).toEqual([
      {
        type: "team_tasks",
        teamId: "team-1",
        parentCallId: "call-t",
        tasks: [
          {
            id: "t1",
            state: "in_progress",
            assignee: "tester",
            deps: ["t0"],
            description: "run the suite",
          },
        ],
      },
    ]);
    expect(
      translate(
        sdkEvent(
          "team.findings",
          teamPayload({
            findings: [{ member: "tester", body: "suite is green" }],
          }),
        ),
      ),
    ).toEqual([
      {
        type: "team_findings",
        teamId: "team-1",
        parentCallId: "call-t",
        findings: [{ member: "tester", body: "suite is green" }],
      },
    ]);
  });

  it("translates team.end with dispositions, mapping the stop reason enum {1:error, 2:cancelled, 3:budget, 0:''}", () => {
    expect(
      translate(
        sdkEvent(
          "team.end",
          teamPayload({
            rounds: 4,
            stop: "end_turn",
            usage: usage({
              inputTokens: BigInt(20000),
              outputTokens: BigInt(1500),
            }),
            dispositions: [
              { name: "lead", stopped: false, errorRounds: 1, reason: 0 },
              { name: "a", stopped: true, errorRounds: 2, reason: 1 },
              { name: "b", stopped: true, errorRounds: 0, reason: 2 },
              { name: "c", stopped: true, errorRounds: 0, reason: 3 },
              { name: "d", stopped: true, errorRounds: 0, reason: 42 },
            ],
          }),
        ),
      ),
    ).toEqual([
      {
        type: "team_end",
        teamId: "team-1",
        parentCallId: "call-t",
        rounds: 4,
        stop: "end_turn",
        inputTokens: 20000,
        outputTokens: 1500,
        tasks: [],
        findings: [],
        dispositions: [
          { name: "lead", stopped: false, errorRounds: 1, reason: "" },
          { name: "a", stopped: true, errorRounds: 2, reason: "error" },
          { name: "b", stopped: true, errorRounds: 0, reason: "cancelled" },
          { name: "c", stopped: true, errorRounds: 0, reason: "budget" },
          { name: "d", stopped: true, errorRounds: 0, reason: "" },
        ],
      },
    ]);
  });

  it("renders none of the delegation kinds as silent or as a not-rendered notice", () => {
    const frames: [string, unknown][] = [
      ["team.member", teamPayload({ member: "m", innerKind: "message.delta" })],
      ["team.tasks", teamPayload({})],
      ["team.findings", teamPayload({})],
      ["team.end", teamPayload({})],
      ["parallel.start", parallelPayload({ parentCallId: "call-p" })],
      ["parallel.end", parallelPayload({ parentCallId: "call-p" })],
      [
        "parallel.branch",
        parallelPayload({ kind: "branch_end", parentCallId: "call-p" }),
      ],
    ];
    for (const [kind, payload] of frames) {
      const events = translate(sdkEvent(kind, payload));
      expect(events, kind).not.toEqual([]);
      expect(
        events.some((event) => event.type === "notice"),
        kind,
      ).toBe(false);
    }
  });
});

// ── MCP browser authorization ────────────────────────────────────────────────

/**
 * The per-tool MCP authorization lifecycle: both kinds translate to typed
 * events (never the "not rendered yet" notice), the protobuf Timestamp expiry
 * becomes epoch milliseconds, and no URL rides an event — the client fetches
 * it from the presentation control when the operator opens it.
 */
describe("translateEvent — authorization", () => {
  const authorization = (
    status: string,
    extra: Record<string, unknown> = {},
  ) => ({
    authorizationId: "auth-1",
    callId: "call-7",
    displayName: "GitHub MCP",
    expiresAt: undefined,
    status,
    ...extra,
  });

  it("translates authorization.required into the parked phase with the expiry in ms, stamped with the run", () => {
    expect(
      translate(
        sdkEvent(
          "authorization.required",
          authorization("pending", {
            expiresAt: { seconds: BigInt(1_800_000_000), nanos: 500_000_000 },
          }),
          { runId: "run-1" },
        ),
      ),
    ).toEqual([
      {
        type: "authorization",
        authorizationId: "auth-1",
        callId: "call-7",
        displayName: "GitHub MCP",
        status: "pending",
        expiresAt: 1_800_000_000_500,
        runId: "run-1",
      },
    ]);
  });

  it("reads an unset expiry as absent, never as 1970", () => {
    const [event] = translate(
      sdkEvent("authorization.required", authorization("pending")),
    );
    expect(event).toMatchObject({ type: "authorization", status: "pending" });
    expect(event).not.toHaveProperty("expiresAt", expect.any(Number));
  });

  it("translates authorization.resolved with the terminal status", () => {
    expect(
      translate(
        sdkEvent("authorization.resolved", authorization("granted"), {
          runId: "run-2",
        }),
      ),
    ).toEqual([
      {
        type: "authorization_resolved",
        authorizationId: "auth-1",
        displayName: "GitHub MCP",
        status: "granted",
        runId: "run-2",
      },
    ]);
  });

  it("renders neither kind as a not-rendered notice", () => {
    for (const kind of ["authorization.required", "authorization.resolved"]) {
      const events = translate(sdkEvent(kind, authorization("cancelled")));
      expect(events, kind).toHaveLength(1);
      expect(
        events.some((event) => event.type === "notice"),
        kind,
      ).toBe(false);
    }
  });
});
