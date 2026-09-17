import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { SessionTranscript } from "@/lib/protocol";
import type { AgentMessage, StreamEvent } from "../types";
import { reduceWatchEvent, useAgentChat } from "./use-agent-chat";

/**
 * The tool-card fields ride every path a call reaches the transcript by:
 * the live/watch reducer keeps `changedPath` off the call and `parts` off
 * the result, and a rehydrated transcript stamps `rawArgs`, `changedPath`
 * and the result's `parts` so the cards render the same after a reload.
 */

const mocks = vi.hoisted(() => ({
  createHarnessSession: vi.fn(async () => "s1"),
  streamHarnessPrompt: vi.fn(),
  retryHarnessRun: vi.fn(),
  fetchSessionTranscriptMessages: vi.fn(),
  fetchHarnessSessionDetail: vi.fn(async () => ({
    resolvedModel: null,
    capabilities: {},
    tokenUsage: null,
  })),
  cancelHarnessRun: vi.fn(async () => undefined),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  ...mocks,
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => ({
    connected: true,
    features: new Set(["http_steer"]),
    serverCapabilities: { image: true },
  }),
}));

vi.mock("../composer-capabilities", () => ({
  refreshSlashCommands: vi.fn(async () => undefined),
}));

vi.mock("@/lib/harness/watch", () => ({
  watchSessionEvents: vi.fn(),
}));

vi.mock("@/lib/attachment-store", () => ({
  loadSentAttachments: vi.fn(async () => []),
  saveSentAttachments: vi.fn(async () => undefined),
}));

const editArgs = JSON.stringify({
  path: "src/a.ts",
  old_string: "one",
  new_string: "two",
});

describe("reduceWatchEvent tool-card fields", () => {
  let serial = 0;
  const nextId = () => `id-${++serial}`;
  const run = (events: StreamEvent[]): AgentMessage[] =>
    events.reduce<AgentMessage[]>(
      (messages, event) => reduceWatchEvent(messages, event, nextId),
      [],
    );

  it("keeps changedPath on the call and parts on the result", () => {
    const messages = run([
      { type: "user_prompt", text: "edit the file" },
      {
        type: "tool_call",
        callId: "c1",
        name: "Edit",
        input: "path: src/a.ts",
        rawArgs: editArgs,
        changedPath: "src/a.ts",
      },
      {
        type: "tool_result",
        callId: "c1",
        output: "ok",
        isError: false,
        parts: [
          { kind: "resource_link", url: "https://example.com/r", name: "r" },
        ],
      },
    ]);
    expect(messages[1].toolCalls?.[0]).toMatchObject({
      callId: "c1",
      name: "Edit",
      rawArgs: editArgs,
      changedPath: "src/a.ts",
      output: "ok",
      status: "completed",
      parts: [
        { kind: "resource_link", url: "https://example.com/r", name: "r" },
      ],
    });
  });
});

describe("transcript rehydration tool-card fields", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("stamps rawArgs, changedPath and result parts on a hydrated call", async () => {
    const transcript: SessionTranscript = {
      sessionId: "s1",
      complete: true,
      messages: [
        { role: "user", text: "edit the file", toolCalls: [] },
        {
          role: "assistant",
          text: "",
          toolCalls: [{ id: "c1", name: "Edit", args: editArgs }],
        },
        {
          role: "tool",
          text: "",
          toolCalls: [],
          toolResult: {
            callId: "c1",
            content: "ok",
            isError: false,
            parts: [{ kind: "image", mimeType: "image/png", data: "iVBORw==" }],
          },
        },
        { role: "assistant", text: "done", toolCalls: [] },
      ],
    };
    mocks.fetchSessionTranscriptMessages.mockResolvedValue(transcript);
    const { result } = renderHook(() =>
      useAgentChat("s1", { sessionState: "idle" }),
    );
    await waitFor(() => expect(result.current.messages).toHaveLength(3));
    expect(result.current.messages[1].toolCalls?.[0]).toMatchObject({
      callId: "c1",
      name: "Edit",
      input: editArgs,
      rawArgs: editArgs,
      changedPath: "src/a.ts",
      output: "ok",
      status: "completed",
      parts: [{ kind: "image", mimeType: "image/png", data: "iVBORw==" }],
    });
  });
});
