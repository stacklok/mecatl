import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionTranscript } from "@/lib/protocol";
import type { AgentMessage, StreamEvent } from "../types";
import { reduceWatchEvent, useAgentChat } from "./use-agent-chat";

/**
 * Run-end stop reasons the daemon never delivers to this tab: a client-side
 * Cancel aborts the tab's own stream before the `cancelled` terminal can
 * arrive, so the hook stamps the trailing turn itself — unless the daemon
 * says the run had already ended (a stale refusal), when the turn's real
 * terminal owns the label. A rehydrated `cancelled` session (the transcript
 * carries no stop) gets the same chip. The daemon client is mocked at the
 * module boundary, as in the sibling hook tests.
 */

const mocks = vi.hoisted(() => ({
  createHarnessSession: vi.fn(async () => "s1"),
  streamHarnessPrompt: vi.fn(),
  fetchSessionTranscriptMessages: vi.fn(),
  fetchHarnessSessionDetail: vi.fn(async () => ({
    resolvedModel: null,
    capabilities: {},
    tokenUsage: null,
  })),
  cancelHarnessRun: vi.fn(async (): Promise<string> => "cancelled"),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  ...mocks,
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => ({
    connected: true,
    features: new Set<string>(),
    serverCapabilities: {},
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

type PromptArgs = [
  string,
  string,
  unknown[],
  (event: StreamEvent) => void,
  AbortSignal | undefined,
  { onRunStarted?: (runId: string) => void } | undefined,
];

/** A prompt stream that names its run, streams one token, then hangs until
 *  the tab aborts it — the shape of a live run being cancelled. */
function hangingStream(...args: PromptArgs): Promise<void> {
  const [, , , onEvent, signal, hooks] = args;
  hooks?.onRunStarted?.("run-1");
  onEvent({ type: "token", text: "partial" });
  return new Promise<void>((_, reject) => {
    signal?.addEventListener("abort", () =>
      reject(new DOMException("The operation was aborted.", "AbortError")),
    );
  });
}

const emptyTranscript = (): SessionTranscript => ({
  sessionId: "s1",
  complete: true,
  messages: [],
});

async function startRun() {
  mocks.streamHarnessPrompt.mockImplementation(hangingStream);
  const { result } = renderHook(() => useAgentChat(null));
  await act(async () => {
    void result.current.sendMessage("go");
  });
  await waitFor(() => expect(result.current.isStreaming).toBe(true));
  await waitFor(() =>
    expect(result.current.messages.at(-1)?.content).toBe("partial"),
  );
  return result;
}

describe("useAgentChat cancel stop reason", () => {
  beforeEach(() => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue(emptyTranscript());
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("stamps `cancelled` on the trailing turn and names it on the status line when the daemon accepts the cancel", async () => {
    mocks.cancelHarnessRun.mockResolvedValue("cancelled");
    const result = await startRun();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(mocks.cancelHarnessRun).toHaveBeenCalledWith("s1", "run-1");
    expect(result.current.messages.at(-1)).toMatchObject({
      role: "assistant",
      content: "partial",
      stopReason: "cancelled",
    });
    expect(result.current.messages.at(-1)?.failed).toBeUndefined();
    expect(result.current.statusMessage).toEqual({
      text: "cancelled",
      tone: "muted",
      kind: "stop",
    });
  });

  it("does not label a turn cancelled when the run had already ended (stale refusal)", async () => {
    mocks.cancelHarnessRun.mockResolvedValue("stale");
    const result = await startRun();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(result.current.messages.at(-1)?.stopReason).toBeUndefined();
    expect(result.current.statusMessage).toBeNull();
  });

  it("still stamps when the cancel's outcome is unknown — the tab aborted before any terminal frame", async () => {
    mocks.cancelHarnessRun.mockResolvedValue("unknown");
    const result = await startRun();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(result.current.messages.at(-1)?.stopReason).toBe("cancelled");
  });
});

describe("useAgentChat rehydrated cancelled session", () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it("stamps the transcript's last turn `cancelled` when the inventory state is cancelled", async () => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue({
      sessionId: "s1",
      complete: true,
      messages: [
        { role: "user", text: "do the thing", toolCalls: [] },
        { role: "assistant", text: "Starting…", toolCalls: [] },
      ],
    } satisfies SessionTranscript);
    const { result } = renderHook(() =>
      useAgentChat("s1", { sessionState: "cancelled" }),
    );
    await waitFor(() =>
      expect(result.current.messages.at(-1)?.stopReason).toBe("cancelled"),
    );
    expect(result.current.messages).toHaveLength(2);
    expect(result.current.messages.at(-1)).toMatchObject({
      role: "assistant",
      content: "Starting…",
    });
  });

  it("opens an empty cancelled bubble when the cancelled run recorded no assistant turn", async () => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue({
      sessionId: "s1",
      complete: true,
      messages: [{ role: "user", text: "do the thing", toolCalls: [] }],
    } satisfies SessionTranscript);
    const { result } = renderHook(() =>
      useAgentChat("s1", { sessionState: "cancelled" }),
    );
    await waitFor(() => expect(result.current.messages).toHaveLength(2));
    expect(result.current.messages.at(-1)).toMatchObject({
      role: "assistant",
      content: "",
      stopReason: "cancelled",
    });
  });

  it("leaves a completed session's transcript unstamped", async () => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue({
      sessionId: "s1",
      complete: true,
      messages: [
        { role: "user", text: "do the thing", toolCalls: [] },
        { role: "assistant", text: "Done.", toolCalls: [] },
      ],
    } satisfies SessionTranscript);
    const { result } = renderHook(() =>
      useAgentChat("s1", { sessionState: "completed" }),
    );
    await waitFor(() => expect(result.current.messages).toHaveLength(2));
    expect(result.current.messages.at(-1)?.stopReason).toBeUndefined();
  });
});

describe("reduceWatchEvent terminal stop", () => {
  it("marks an error terminal failed and never stamps a stop-reason chip", () => {
    let serial = 0;
    const nextId = () => `id-${++serial}`;
    const before: AgentMessage[] = [
      { id: "a1", role: "assistant", content: "trying", timestamp: 0 },
    ];
    const after = reduceWatchEvent(
      before,
      {
        type: "run_result",
        stop: "error",
        text: "",
        errorText: "boom",
        permanent: false,
      },
      nextId,
    );
    expect(after[0]).toMatchObject({ failed: true, failureDetail: "boom" });
    expect(after[0].stopReason).toBeUndefined();
  });

  it("stamps a cancelled terminal delivered by the watch (another client stopped the run)", () => {
    let serial = 0;
    const nextId = () => `id-${++serial}`;
    const after = reduceWatchEvent(
      [{ id: "a1", role: "assistant", content: "partial", timestamp: 0 }],
      {
        type: "run_result",
        stop: "cancelled",
        text: "",
        errorText: "",
        permanent: false,
      },
      nextId,
    );
    expect(after[0]).toMatchObject({
      content: "partial",
      stopReason: "cancelled",
    });
  });
});
