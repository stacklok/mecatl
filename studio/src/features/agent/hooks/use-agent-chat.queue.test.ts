import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StreamEvent } from "../types";
import {
  mergeQueued,
  pauseReasonFor,
  shouldDrainQueue,
  useAgentChat,
} from "./use-agent-chat";

/**
 * The paused queue (the TUI's "⏸ n queued · paused: <reason>"): a non-clean
 * stop holds the client-side queue instead of auto-firing the next message;
 * resume sends the whole queue as one prompt; Retry re-drives the failed turn
 * WITHOUT lifting the pause; a retracted steer bundle comes back for
 * recomposition. The daemon client is mocked at the module boundary — the
 * hook's own wire adapters are covered by their own tests.
 */

const {
  cancelHarnessRun,
  cancelHarnessSteer,
  createHarnessSession,
  steerHarnessRun,
  streamHarnessPrompt,
} = vi.hoisted(() => ({
  cancelHarnessRun: vi.fn(async () => undefined),
  cancelHarnessSteer: vi.fn(async (): Promise<"retracted" | "none_pending"> => {
    return "retracted";
  }),
  createHarnessSession: vi.fn(async () => "s1"),
  steerHarnessRun: vi.fn(async () => ({
    outcome: "accepted" as const,
    messageId: "m1",
  })),
  streamHarnessPrompt: vi.fn(),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  cancelHarnessRun,
  cancelHarnessSteer,
  createHarnessSession,
  steerHarnessRun,
  streamHarnessPrompt,
  fetchSessionTranscriptMessages: vi.fn(async () => []),
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => ({
    connected: true,
    features: new Set(["http_steer"]),
    serverCapabilities: {},
  }),
}));

vi.mock("../composer-capabilities", () => ({
  refreshSlashCommands: vi.fn(async () => undefined),
}));

vi.mock("@/lib/harness/watch", () => ({
  watchSessionEvents: vi.fn(),
}));

type StreamArgs = [
  string,
  string,
  unknown[],
  (event: StreamEvent) => void,
  AbortSignal | undefined,
  { onRunStarted?: (runId: string) => void } | undefined,
];

/** The live stream's event sink, so a test can inject daemon frames (the
 *  steer drain echo) into the run the hook is driving. */
let emitStreamEvent: ((event: StreamEvent) => void) | null = null;

/** A prompt stream that names its run, then hangs until the signal aborts —
 *  the shape of a live run the user cancels. */
function hangingStream(...args: StreamArgs): Promise<void> {
  const [, , , onEvent, signal, hooks] = args;
  emitStreamEvent = onEvent;
  hooks?.onRunStarted?.("run-1");
  return new Promise<void>((_, reject) => {
    signal?.addEventListener("abort", () =>
      reject(new DOMException("The operation was aborted.", "AbortError")),
    );
  });
}

describe("pauseReasonFor", () => {
  it("maps every non-clean stop to its documented reason", () => {
    expect(pauseReasonFor("cancelled")).toBe("cancelled");
    expect(pauseReasonFor("error")).toBe("the last turn failed");
    expect(pauseReasonFor("transport")).toBe("connection lost");
  });
});

describe("mergeQueued", () => {
  it("joins texts with a blank line and concatenates files in order", () => {
    const a = new File(["a"], "a.png", { type: "image/png" });
    const b = new File(["b"], "b.png", { type: "image/png" });
    expect(
      mergeQueued([
        { text: "first", files: [a] },
        { text: "second" },
        { text: "third", files: [b] },
      ]),
    ).toEqual({ text: "first\n\nsecond\n\nthird", files: [a, b] });
  });

  it("skips empty texts and omits files when none are staged", () => {
    expect(mergeQueued([{ text: "" }, { text: "only" }])).toEqual({
      text: "only",
      files: undefined,
    });
  });

  it("returns null for an empty queue", () => {
    expect(mergeQueued([])).toBeNull();
  });
});

describe("shouldDrainQueue", () => {
  const ready = {
    status: "idle",
    connected: true,
    queued: 2,
    pendingSteers: 0,
    paused: false,
    flushing: false,
  };

  it("drains a clean, connected idle with something queued", () => {
    expect(shouldDrainQueue(ready)).toBe(true);
  });

  it("holds while paused even when idle, and drains again once resumed", () => {
    expect(shouldDrainQueue({ ...ready, paused: true })).toBe(false);
    expect(shouldDrainQueue({ ...ready, paused: false })).toBe(true);
  });

  it("holds while streaming, errored, disconnected, empty, steering, or flushing", () => {
    expect(shouldDrainQueue({ ...ready, status: "streaming" })).toBe(false);
    expect(shouldDrainQueue({ ...ready, status: "error" })).toBe(false);
    expect(shouldDrainQueue({ ...ready, connected: false })).toBe(false);
    expect(shouldDrainQueue({ ...ready, queued: 0 })).toBe(false);
    expect(shouldDrainQueue({ ...ready, pendingSteers: 1 })).toBe(false);
    expect(shouldDrainQueue({ ...ready, flushing: true })).toBe(false);
  });
});

describe("useAgentChat paused queue", () => {
  beforeEach(() => {
    streamHarnessPrompt.mockImplementation(hangingStream);
    cancelHarnessSteer.mockResolvedValue("retracted");
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  /** Starts a run on a fresh draft and queues two follow-ups behind it. */
  async function runWithTwoQueued() {
    const rendered = renderHook(() => useAgentChat(null));
    await act(async () => {
      void rendered.result.current.sendMessage("go");
    });
    await waitFor(() => expect(rendered.result.current.isStreaming).toBe(true));
    act(() => {
      rendered.result.current.queueMessage("first");
      rendered.result.current.queueMessage("second");
    });
    expect(rendered.result.current.queuedMessages).toHaveLength(2);
    return rendered;
  }

  it("cancelChat leaves both queued rows in place and pauses the queue as cancelled", async () => {
    const { result } = await runWithTwoQueued();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(result.current.queuePaused).toEqual({ reason: "cancelled" });
    expect(result.current.queuedMessages.map((m) => m.text)).toEqual([
      "first",
      "second",
    ]);
    // Nothing auto-fired: the only prompt on the wire is the cancelled one.
    expect(streamHarnessPrompt).toHaveBeenCalledTimes(1);
  });

  it("resumeQueue sends the held queue as ONE merged prompt and lifts the pause", async () => {
    const { result } = await runWithTwoQueued();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.queuePaused).not.toBeNull());
    await act(async () => {
      result.current.resumeQueue();
    });
    await waitFor(() => expect(streamHarnessPrompt).toHaveBeenCalledTimes(2));
    expect(streamHarnessPrompt.mock.calls[1][1]).toBe("first\n\nsecond");
    expect(result.current.queuedMessages).toEqual([]);
    expect(result.current.queuePaused).toBeNull();
  });

  it("Retry re-drives the failed turn but keeps the queue paused", async () => {
    const { result } = await runWithTwoQueued();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    await act(async () => {
      void result.current.retryLast();
    });
    await waitFor(() => expect(streamHarnessPrompt).toHaveBeenCalledTimes(2));
    expect(streamHarnessPrompt.mock.calls[1][1]).toBe("go");
    expect(result.current.queuePaused).toEqual({ reason: "cancelled" });
    expect(result.current.queuedMessages).toHaveLength(2);
  });

  it("a fresh composer send lifts the pause so the queue follows that run", async () => {
    const { result } = await runWithTwoQueued();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.queuePaused).not.toBeNull());
    await act(async () => {
      void result.current.sendMessage("something new");
    });
    await waitFor(() => expect(result.current.queuePaused).toBeNull());
    expect(result.current.queuedMessages).toHaveLength(2);
  });

  it("takeAllQueued hands the merged draft back, empties the queue, and the stale pause clears itself", async () => {
    const { result } = await runWithTwoQueued();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.queuePaused).not.toBeNull());
    let taken: { text: string; files?: File[] } | null = null;
    act(() => {
      taken = result.current.takeAllQueued();
    });
    expect(taken).toEqual({ text: "first\n\nsecond", files: undefined });
    expect(result.current.queuedMessages).toEqual([]);
    // Nothing held → nothing to pause.
    await waitFor(() => expect(result.current.queuePaused).toBeNull());
    // Nothing was sent on the user's behalf.
    expect(streamHarnessPrompt).toHaveBeenCalledTimes(1);
  });

  it("clearQueue drops every held row and lifts the pause without sending anything", async () => {
    const { result } = await runWithTwoQueued();
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.queuePaused).not.toBeNull());
    act(() => {
      result.current.clearQueue();
    });
    expect(result.current.queuedMessages).toEqual([]);
    expect(result.current.queuePaused).toBeNull();
    expect(streamHarnessPrompt).toHaveBeenCalledTimes(1);
  });

  it("takeQueued returns the row's text AND its staged files", async () => {
    const { result } = await runWithTwoQueued();
    const file = new File(["x"], "shot.png", { type: "image/png" });
    act(() => {
      result.current.queueMessage("with a picture", [file]);
    });
    const row = result.current.queuedMessages[2];
    let taken: { text: string; files?: File[] } | null = null;
    act(() => {
      taken = result.current.takeQueued(row.id);
    });
    expect(taken).toEqual({ text: "with a picture", files: [file] });
    expect(result.current.queuedMessages).toHaveLength(2);
  });

  it("cancelPendingSteers returns the retracted bundle and removes its optimistic bubbles", async () => {
    const rendered = renderHook(() => useAgentChat(null));
    const { result } = rendered;
    await act(async () => {
      void result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(true));
    await act(async () => {
      await result.current.steerMessage("focus on tests");
    });
    expect(result.current.pendingSteers).toHaveLength(1);
    expect(
      result.current.messages.some((m) => m.content === "focus on tests"),
    ).toBe(true);

    let retracted: { text: string }[] = [];
    await act(async () => {
      retracted = await result.current.cancelPendingSteers();
    });
    expect(retracted.map((s) => s.text)).toEqual(["focus on tests"]);
    expect(result.current.pendingSteers).toEqual([]);
    expect(
      result.current.messages.some((m) => m.content === "focus on tests"),
    ).toBe(false);
  });

  it("the drain echo stamps the applied steer's bubble as steered and clears it from pending", async () => {
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      void result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(true));
    await act(async () => {
      await result.current.steerMessage("focus on tests");
    });
    const steerId = result.current.pendingSteers[0]?.id;
    expect(steerId).toBeTruthy();
    const before = result.current.messages.find(
      (m) => m.content === "focus on tests",
    );
    expect(before?.steered).toBeUndefined();

    act(() => {
      emitStreamEvent?.({
        type: "steer",
        text: "focus on tests",
        messageId: steerId,
      });
    });
    expect(result.current.pendingSteers).toEqual([]);
    const after = result.current.messages.find(
      (m) => m.content === "focus on tests",
    );
    expect(after).toMatchObject({ role: "user", steered: true });
    // No duplicate echo bubble: the optimistic one was moved, not copied.
    expect(
      result.current.messages.filter((m) => m.content === "focus on tests"),
    ).toHaveLength(1);
  });

  it("cancelPendingSteers returns nothing on none_pending — the bundle already drained", async () => {
    cancelHarnessSteer.mockResolvedValue("none_pending");
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      void result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(true));
    await act(async () => {
      await result.current.steerMessage("focus on tests");
    });
    let retracted: { text: string }[] = [];
    await act(async () => {
      retracted = await result.current.cancelPendingSteers();
    });
    expect(retracted).toEqual([]);
    expect(result.current.pendingSteers).toEqual([]);
    // The echo owns the bubble now; the optimistic one stays put.
    expect(
      result.current.messages.some((m) => m.content === "focus on tests"),
    ).toBe(true);
  });
});
