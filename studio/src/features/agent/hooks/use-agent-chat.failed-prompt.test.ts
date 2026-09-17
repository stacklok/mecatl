import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type { SessionTranscript } from "@/lib/protocol";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * The esc-to-edit half of a run-entry failure (TUI parity with
 * failStartupRunEntry): a prompt the daemon refused BEFORE any frame arrived
 * — the session mint or POST /prompt itself — is held as `failedPrompt`, and
 * `takeFailedPrompt` hands its text back for the composer, drops the failed
 * exchange it left in the transcript and clears the failure. The daemon
 * client is mocked at the module boundary.
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

type Hooks = { onRunStarted?: (runId: string) => void } | undefined;
type PromptArgs = [
  string,
  string,
  unknown[],
  (event: StreamEvent) => void,
  AbortSignal | undefined,
  Hooks,
];

const leasedElsewhere = () =>
  new HarnessApiError(
    409,
    "session_leased_elsewhere",
    "another client is driving this session",
  );
const draining = () =>
  new HarnessApiError(503, "draining", "the daemon is draining");

/** A prompt stream that names its run, relays the frames, and ends. */
const promptStream =
  (events: StreamEvent[]) =>
  async (...args: PromptArgs) => {
    const [, , , onEvent, , hooks] = args;
    hooks?.onRunStarted?.("run-prompt");
    for (const event of events) onEvent(event);
  };

const cleanResult = (text: string): StreamEvent => ({
  type: "run_result",
  stop: "end_turn",
  text,
  errorText: "",
  permanent: false,
});

describe("useAgentChat failed-prompt restore", () => {
  beforeEach(() => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue({
      sessionId: "s1",
      complete: true,
      messages: [],
    } satisfies SessionTranscript);
  });

  afterEach(() => {
    vi.clearAllMocks();
    mocks.createHarnessSession.mockImplementation(async () => "s1");
  });

  it("holds the prompt a refused session mint dropped, and Edit takes it back", async () => {
    mocks.createHarnessSession.mockRejectedValueOnce(leasedElsewhere());
    const { result } = renderHook(() => useAgentChat(null));

    await act(async () => {
      await result.current.sendMessage("hello");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.error).toBeTruthy();
    expect(result.current.failedPrompt).toBe("hello");
    // The text-only composer recovery still fires alongside.
    expect(result.current.recoverDraft).toEqual({ text: "hello" });
    expect(result.current.messages.map((m) => m.role)).toEqual([
      "user",
      "assistant",
    ]);
    expect(result.current.messages.at(-1)).toMatchObject({ failed: true });
    expect(mocks.streamHarnessPrompt).not.toHaveBeenCalled();

    let taken: string | null = null;
    act(() => {
      taken = result.current.takeFailedPrompt();
    });
    expect(taken).toBe("hello");
    expect(result.current.messages).toEqual([]);
    expect(result.current.status).toBe("idle");
    expect(result.current.error).toBeNull();
    expect(result.current.failedPrompt).toBeNull();
    expect(result.current.recoverDraft).toBeNull();

    // Consumed: a second take has nothing to hand back.
    let again: string | null = "unset";
    act(() => {
      again = result.current.takeFailedPrompt();
    });
    expect(again).toBeNull();
  });

  it("holds a refused prompt that carried a file too — the text comes back even though the composer recovery is text-only", async () => {
    mocks.streamHarnessPrompt.mockRejectedValueOnce(draining());
    const png = new File([new Uint8Array([137, 80, 78, 71])], "shot.png", {
      type: "image/png",
    });
    const { result } = renderHook(() => useAgentChat(null));

    await act(async () => {
      await result.current.sendMessage("what is this", [png]);
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.recoverDraft).toBeNull();
    expect(result.current.failedPrompt).toBe("what is this");

    let taken: string | null = null;
    act(() => {
      taken = result.current.takeFailedPrompt();
    });
    expect(taken).toBe("what is this");
    expect(result.current.messages).toEqual([]);
    expect(result.current.status).toBe("idle");
  });

  it("keeps the failed exchange when Edit is not taken, and a fresh send clears the held prompt", async () => {
    mocks.streamHarnessPrompt
      .mockRejectedValueOnce(leasedElsewhere())
      .mockImplementation(promptStream([cleanResult("done")]));
    const { result } = renderHook(() => useAgentChat(null));

    await act(async () => {
      await result.current.sendMessage("first");
    });
    await waitFor(() => expect(result.current.failedPrompt).toBe("first"));

    await act(async () => {
      await result.current.sendMessage("second");
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(result.current.failedPrompt).toBeNull();
    // Untouched by the new send: the failed exchange stays as a record.
    expect(result.current.messages.map((m) => m.content)).toEqual([
      "first",
      "",
      "second",
      "done",
    ]);
  });

  it("a verbatim Retry consumes the held prompt the same way", async () => {
    // The MINT is refused, so the chat is still a draft (no daemon id):
    // Retry has no failed step to ask the daemon about and re-sends the held
    // prompt directly, replacing the failed exchange.
    mocks.createHarnessSession.mockRejectedValueOnce(leasedElsewhere());
    mocks.streamHarnessPrompt.mockImplementation(
      promptStream([cleanResult("done")]),
    );
    const { result } = renderHook(() => useAgentChat(null));

    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.failedPrompt).toBe("go"));
    expect(mocks.streamHarnessPrompt).not.toHaveBeenCalled();

    await act(async () => {
      await result.current.retryLast();
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(result.current.failedPrompt).toBeNull();
    expect(mocks.retryHarnessRun).not.toHaveBeenCalled();
    expect(mocks.streamHarnessPrompt).toHaveBeenCalledTimes(1);
    expect(result.current.messages.map((m) => m.content)).toEqual([
      "go",
      "done",
    ]);
  });

  it("never holds a prompt the daemon accepted — a mid-stream failure is the retry endpoint's", async () => {
    mocks.streamHarnessPrompt.mockImplementation(
      promptStream([
        {
          type: "run_result",
          stop: "error",
          text: "",
          errorText: "upstream 503",
          permanent: false,
        },
      ]),
    );
    const { result } = renderHook(() => useAgentChat(null));

    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.failedPrompt).toBeNull();
    expect(result.current.takeFailedPrompt()).toBeNull();
  });

  it("drops the held prompt when the chat changes", async () => {
    mocks.streamHarnessPrompt.mockRejectedValueOnce(leasedElsewhere());
    const { result, rerender } = renderHook(
      ({ id }: { id: string | null }) => useAgentChat(id),
      { initialProps: { id: null as string | null } },
    );

    await act(async () => {
      await result.current.sendMessage("hello");
    });
    await waitFor(() => expect(result.current.failedPrompt).toBe("hello"));

    rerender({ id: "s2" });
    await waitFor(() => expect(result.current.failedPrompt).toBeNull());
    expect(result.current.status).toBe("idle");
    expect(result.current.takeFailedPrompt()).toBeNull();
  });
});
