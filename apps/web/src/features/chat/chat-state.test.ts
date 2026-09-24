// SPDX-License-Identifier: Apache-2.0

import type { RunStreamEvent, SessionUsageResponse } from "@mecatl-studio/contracts";
import { describe, expect, it } from "vitest";
import {
  applyRunDelivery,
  enqueueApproval,
  errorMessage,
  failureFromResult,
  finalRunFailure,
  initialRunDeliveryState,
  messagesFromTranscript,
  payloadImages,
  payloadText,
  permissionAsk,
  type RunDeliveryState,
  retractApproval,
  startsNewRun,
  toolCall,
  toolResult,
} from "./chat-state";

const first = { args: "{}", askId: "ask-1", reason: "", tool: "Read" };
const second = { args: "{}", askId: "ask-2", reason: "", tool: "Write" };

describe("chat state", () => {
  it("keeps permission asks in FIFO order and ignores duplicate deliveries", () => {
    const queued = enqueueApproval(enqueueApproval([first], second), {
      ...first,
      reason: "redelivered",
    });
    expect(queued).toEqual([first, second]);
  });

  it("retracts only the matching permission ask", () => {
    expect(retractApproval([first, second], "ask-1")).toEqual([second]);
  });

  it("recognizes terminal failures without treating cancellation as failure", () => {
    expect(failureFromResult({ error: "Provider unavailable", permanent: true }, "hello")).toEqual({
      message: "Provider unavailable",
      permanent: true,
      prompt: "hello",
    });
    expect(failureFromResult({ stop: "cancelled" }, "hello")).toBeUndefined();
  });
});

describe("payload parsing", () => {
  it("reads the text field off a payload only when it is a string", () => {
    expect(payloadText({ text: "hello" })).toBe("hello");
    expect(payloadText({ text: 5 })).toBe("");
    expect(payloadText("not an object")).toBe("");
  });

  it("restores inline and URL images from replay payloads", () => {
    expect(
      payloadImages({
        parts: [
          {
            data: { data: "aGVsbG8=", encoding: "base64" },
            kind: 1,
            mimeType: "image/png",
            url: "",
          },
          {
            data: { data: "", encoding: "base64" },
            kind: 1,
            mimeType: "image/webp",
            url: "https://example.com/image.webp",
          },
        ],
      }),
    ).toEqual([
      {
        data: "aGVsbG8=",
        id: "replay-image-0",
        mimeType: "image/png",
        name: "Image 1",
      },
      {
        id: "replay-image-1",
        mimeType: "image/webp",
        name: "Image 2",
        url: "https://example.com/image.webp",
      },
    ]);
  });

  it("extracts a tool call only once it has an id", () => {
    expect(toolCall({ args: "{}", id: "call-1", name: "Read" })).toEqual({
      args: "{}",
      id: "call-1",
      name: "Read",
    });
    expect(toolCall({ args: "{}", name: "Read" })).toBeUndefined();
  });

  it("defaults an unnamed tool call to 'Tool'", () => {
    expect(toolCall({ id: "call-1" })).toEqual({ args: "", id: "call-1", name: "Tool" });
  });

  it("extracts a tool result only once it has a callId", () => {
    expect(toolResult({ callId: "call-1", content: "done", isError: true })).toEqual({
      callId: "call-1",
      content: "done",
      isError: true,
    });
    expect(toolResult({ content: "done" })).toBeUndefined();
  });

  it("extracts a permission ask only once it has an askId", () => {
    expect(permissionAsk({ askId: "ask-1", tool: "Shell" })).toEqual({
      args: "",
      askId: "ask-1",
      reason: "",
      tool: "Shell",
    });
    expect(permissionAsk({ tool: "Shell" })).toBeUndefined();
  });

  it("reads an RFC 9457 detail before falling back to Error#message", () => {
    expect(errorMessage({ detail: "Session not found" })).toBe("Session not found");
    expect(errorMessage(new Error("network down"))).toBe("network down");
    expect(errorMessage("anything else")).toBe("The request could not be completed.");
  });
});

describe("messagesFromTranscript", () => {
  it("attaches a tool result to the assistant message that made the matching call", () => {
    const messages = messagesFromTranscript([
      { role: "user", text: "go", toolCalls: [] },
      {
        role: "assistant",
        text: "",
        toolCalls: [{ args: "{}", id: "call-1", name: "Read" }],
      },
      {
        role: "tool",
        text: "",
        toolCalls: [],
        toolResult: { callId: "call-1", content: "file contents", isError: false },
      },
    ]);
    expect(messages).toHaveLength(2);
    expect(messages[1]?.tools?.[0]).toEqual({
      args: "{}",
      id: "call-1",
      isError: false,
      name: "Read",
      output: "file contents",
    });
  });

  it("drops a tool result with no matching call rather than throwing", () => {
    const messages = messagesFromTranscript([
      {
        role: "tool",
        text: "",
        toolCalls: [],
        toolResult: { callId: "missing", content: "orphan", isError: false },
      },
    ]);
    expect(messages).toEqual([]);
  });

  it("restores persisted transcript images", () => {
    const images = [{ data: "aGVsbG8=", mimeType: "image/png", name: "Image 1" }];
    expect(messagesFromTranscript([{ images, role: "user", text: "Look", toolCalls: [] }])).toEqual(
      [
        {
          content: "Look",
          id: "transcript-0",
          images: [{ ...images[0], id: "transcript-0-image-0" }],
          role: "user",
        },
      ],
    );
  });
});

function usage(overrides: Partial<SessionUsageResponse> = {}): SessionUsageResponse {
  return {
    cacheReadTokens: "0",
    cacheWriteTokens: "0",
    inputTokens: "0",
    outputTokens: "0",
    reasoningTokens: "0",
    ...overrides,
  };
}

function delivered(kind: string, extra: Record<string, unknown> = {}): RunStreamEvent {
  return {
    event: {
      kind,
      payload: undefined,
      runId: "run-1",
      seq: "1",
      text: "",
      turn: 0,
      unknown: false,
      ...extra,
    },
    type: "run.event",
  };
}

const options = {
  newId: (() => {
    let n = 0;
    return () => `generated-${n++}`;
  })(),
  now: 1_000,
  replay: false,
};

describe("applyRunDelivery", () => {
  it("stamps the run id and clears a prior failure on run.started", () => {
    const state: RunDeliveryState = {
      ...initialRunDeliveryState("assistant-1", "hello"),
      failure: { message: "old failure", permanent: false, prompt: "hello" },
    };
    const next = applyRunDelivery(
      state,
      { runId: "run-42", sessionId: "s1", type: "run.started" },
      options,
    );
    expect(next.runId).toBe("run-42");
    expect(next.failure).toBeUndefined();
    expect(next.sawResult).toBe(false);
  });

  it("mints a fresh assistant id and clears the active prompt when replaying run.started", () => {
    const state = initialRunDeliveryState("assistant-1", "hello");
    const next = applyRunDelivery(
      state,
      { runId: "run-42", sessionId: "s1", type: "run.started" },
      { ...options, replay: true },
    );
    expect(next.activeAssistantId).not.toBe("assistant-1");
    expect(next.activePrompt).toBe("");
  });

  it("a repeated run.started for the followed run keeps the active assistant", () => {
    const started = { runId: "run-1", sessionId: "s1", type: "run.started" } as const;
    let state = applyRunDelivery(initialRunDeliveryState("assistant-1", "hello"), started, {
      ...options,
      replay: true,
    });
    state = applyRunDelivery(state, delivered("message.delta", { text: "Hel" }), {
      ...options,
      replay: true,
    });
    const repeated = applyRunDelivery(state, started, { ...options, now: 9_000, replay: true });
    expect(repeated).toBe(state);
    expect(startsNewRun("run-1", "run-1")).toBe(false);
  });

  it("a reattached stream with no leading run.started keeps appending into the current assistant", () => {
    const replay = { ...options, replay: true };
    let state = applyRunDelivery(
      initialRunDeliveryState("assistant-1", ""),
      { runId: "run-1", sessionId: "s1", type: "run.started" },
      replay,
    );
    state = applyRunDelivery(state, delivered("message.delta", { text: "Hel" }), replay);
    const active = state.activeAssistantId;
    // The bounded stream ends here; the resumed one starts at the next durable event.
    state = applyRunDelivery(
      state,
      { cursor: "c-1", reason: "bound", type: "run.truncated" },
      replay,
    );
    state = applyRunDelivery(state, delivered("message.delta", { text: "lo" }), replay);
    expect(state.activeAssistantId).toBe(active);
    expect(state.messages).toEqual([{ content: "Hello", id: active, role: "assistant" }]);
  });

  it("starts a fresh assistant turn for a new run later in a resumed stream", () => {
    const replay = { ...options, replay: true };
    let state = applyRunDelivery(
      initialRunDeliveryState("assistant-1", ""),
      { runId: "run-1", sessionId: "s1", type: "run.started" },
      replay,
    );
    state = applyRunDelivery(state, delivered("message.delta", { text: "first" }), replay);
    const first = state.activeAssistantId;
    state = applyRunDelivery(
      state,
      { runId: "run-2", sessionId: "s1", type: "run.started" },
      replay,
    );
    expect(startsNewRun("run-1", "run-2")).toBe(true);
    expect(state.runId).toBe("run-2");
    expect(state.activeAssistantId).not.toBe(first);
  });

  it("records a run.error as a failure carrying the current prompt", () => {
    const state = initialRunDeliveryState("assistant-1", "hello");
    const next = applyRunDelivery(
      state,
      { code: "internal", message: "boom", type: "run.error" },
      options,
    );
    expect(next.failure).toEqual({ message: "boom", permanent: false, prompt: "hello" });
  });

  it("appends message.delta text, creating the assistant placeholder on first delivery", () => {
    const state = initialRunDeliveryState("assistant-1", "hello");
    let next = applyRunDelivery(state, delivered("message.delta", { text: "Hel" }), options);
    next = applyRunDelivery(next, delivered("message.delta", { text: "lo" }), options);
    expect(next.messages).toEqual([{ content: "Hello", id: "assistant-1", role: "assistant" }]);
  });

  it("accumulates reasoning.delta text separately from the final content", () => {
    const state = initialRunDeliveryState("assistant-1", "hello");
    let next = applyRunDelivery(state, delivered("reasoning.delta", { text: "Think" }), options);
    next = applyRunDelivery(next, delivered("reasoning.delta", { text: "ing…" }), options);
    expect(next.messages[0]).toMatchObject({ content: "", reasoning: "Thinking…" });
  });

  it("appends a tool.call and then fills in its result by callId", () => {
    const state = initialRunDeliveryState("assistant-1", "hello");
    let next = applyRunDelivery(
      state,
      delivered("tool.call", { payload: { args: "{}", id: "call-1", name: "Read" } }),
      options,
    );
    next = applyRunDelivery(
      next,
      delivered("tool.result", { payload: { callId: "call-1", content: "ok", isError: false } }),
      options,
    );
    expect(next.messages[0]?.tools).toEqual([
      { args: "{}", id: "call-1", isError: false, name: "Read", output: "ok" },
    ]);
  });

  it("enqueues a permission.ask and clears it on permission.retract or approval", () => {
    const state = initialRunDeliveryState("assistant-1", "hello");
    const asked = applyRunDelivery(
      state,
      delivered("permission.ask", { payload: { askId: "ask-1", tool: "Shell" } }),
      options,
    );
    expect(asked.approvals).toHaveLength(1);

    const retracted = applyRunDelivery(
      asked,
      delivered("permission.retract", { payload: { askId: "ask-1" } }),
      options,
    );
    expect(retracted.approvals).toEqual([]);

    const approved = applyRunDelivery(
      asked,
      delivered("approval", { payload: { askId: "ask-1" } }),
      options,
    );
    expect(approved.approvals).toEqual([]);
  });

  it("marks sawResult and folds a failing result into the assistant message", () => {
    const state = initialRunDeliveryState("assistant-1", "hello");
    const next = applyRunDelivery(
      state,
      delivered("result", { payload: { error: "Provider unavailable", permanent: true } }),
      options,
    );
    expect(next.sawResult).toBe(true);
    expect(next.messages[0]?.failure).toEqual({
      detail: JSON.stringify({ error: "Provider unavailable", permanent: true }, null, 2),
      message: "Provider unavailable",
      permanent: true,
    });
  });

  it("only records a turn stat for a live (non-replay) result carrying usage", () => {
    const state = initialRunDeliveryState("assistant-1", "hello");
    const withUsage = delivered("result", { text: "done", usage: usage({ inputTokens: "5000" }) });
    const live = applyRunDelivery(state, withUsage, { ...options, now: 5_000 });
    expect(live.messages[0]?.turnStat).toBeTruthy();

    const replay = applyRunDelivery(state, withUsage, { ...options, now: 5_000, replay: true });
    expect(replay.messages[0]?.turnStat).toBeUndefined();
  });

  it("only turns a user_prompt event into a message during replay", () => {
    const state = initialRunDeliveryState("assistant-1", "");
    const live = applyRunDelivery(state, delivered("user_prompt", { text: "hi" }), options);
    expect(live.messages).toEqual([]);

    const replay = applyRunDelivery(
      state,
      delivered("user_prompt", {
        payload: {
          parts: [
            {
              data: { data: "aGVsbG8=", encoding: "base64" },
              kind: 1,
              mimeType: "image/png",
            },
          ],
          text: "hi",
        },
        text: "hi",
      }),
      {
        ...options,
        replay: true,
      },
    );
    expect(replay.messages).toEqual([
      {
        content: "hi",
        id: expect.any(String),
        images: [
          {
            data: "aGVsbG8=",
            id: "replay-image-0",
            mimeType: "image/png",
            name: "Image 1",
          },
        ],
        role: "user",
      },
    ]);
    expect(replay.activePrompt).toBe("hi");
  });
});

describe("finalRunFailure", () => {
  it("preserves an explicit failure", () => {
    const failure = { message: "boom", permanent: false, prompt: "hello" };
    expect(finalRunFailure({ failure, sawResult: false })).toEqual(failure);
  });

  it("does not turn an unexplained stream close into a retryable failure", () => {
    expect(finalRunFailure({ failure: undefined, sawResult: false })).toBeUndefined();
  });

  it("reports no failure once a result was seen and nothing failed", () => {
    expect(finalRunFailure({ failure: undefined, sawResult: true })).toBeUndefined();
  });
});
