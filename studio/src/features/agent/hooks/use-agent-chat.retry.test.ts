import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type { SessionTranscript } from "@/lib/protocol";
import type { StreamEvent } from "../types";
import {
  AUTO_RETRY_NOTICE,
  REHYDRATED_FAILURE_DETAIL,
  REHYDRATED_FAILURE_ERROR,
  RETRY_INELIGIBLE_NO_PROMPT,
  RETRY_PRECOMMIT_EXPLANATION,
} from "./failed-step-retry";
import { useAgentChat } from "./use-agent-chat";

/**
 * Failed-step retry in the chat hook (ADR 0239, TUI parity): the ONE
 * automatic prompt-free retry on retryable + precommit, the daemon asked
 * first for every non-permanent disposition, a permanent failure withholding
 * Retry, durable availability after a reload, and prompt recovery after a
 * transport fault. The daemon client is mocked at the module boundary.
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
    // The fixture daemon accepts images: the send-time media gate (mirroring
    // the SDK's strict capability check) would otherwise refuse the PNG.
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
type RetryArgs = [
  string,
  (event: StreamEvent) => void,
  AbortSignal | undefined,
  Hooks,
];

type RunResult = Extract<StreamEvent, { type: "run_result" }>;

const failedResult = (over: Partial<RunResult> = {}): StreamEvent => ({
  type: "run_result",
  stop: "error",
  text: "",
  errorText: "upstream 503",
  permanent: false,
  ...over,
});
const cleanResult = (text: string): StreamEvent => ({
  type: "run_result",
  stop: "end_turn",
  text,
  errorText: "",
  permanent: false,
});

/** A prompt stream that names its run, relays the frames, and ends. */
const promptStream =
  (events: StreamEvent[]) =>
  async (...args: PromptArgs) => {
    const [, , , onEvent, , hooks] = args;
    hooks?.onRunStarted?.("run-prompt");
    for (const event of events) onEvent(event);
  };

/** The retry endpoint's stream: same relay, no prompt. */
const retryStream =
  (events: StreamEvent[]) =>
  async (...args: RetryArgs) => {
    const [, onEvent, , hooks] = args;
    hooks?.onRunStarted?.("run-retry");
    for (const event of events) onEvent(event);
  };

const ineligible = () =>
  new HarnessApiError(
    409,
    "failed_step_retry_ineligible",
    "no eligible failed step",
  );

const RETRYABLE_PRECOMMIT: Partial<RunResult> = {
  retryDisposition: "retryable",
  streamProgress: "precommit",
};

describe("useAgentChat automatic failed-step retry", () => {
  beforeEach(() => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue({
      sessionId: "s1",
      complete: true,
      messages: [],
    } satisfies SessionTranscript);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("retries a retryable + precommit failure once, prompt-free, and says so on the retried turn", async () => {
    mocks.streamHarnessPrompt.mockImplementation(
      promptStream([failedResult(RETRYABLE_PRECOMMIT)]),
    );
    mocks.retryHarnessRun.mockImplementation(
      retryStream([cleanResult("Recovered.")]),
    );
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(result.current.status).toBe("idle"));
    // The prompt went out exactly once: the retry re-drove the recorded step.
    expect(mocks.streamHarnessPrompt).toHaveBeenCalledTimes(1);
    expect(result.current.error).toBeNull();
    const last = result.current.messages.at(-1);
    expect(last).toMatchObject({
      role: "assistant",
      content: "Recovered.",
      notices: [AUTO_RETRY_NOTICE],
    });
    expect(last?.failed).toBeUndefined();
  });

  it("never retries twice: a retry that dies precommit again is explained, and a NEW prompt re-arms the budget", async () => {
    mocks.streamHarnessPrompt.mockImplementation(
      promptStream([failedResult(RETRYABLE_PRECOMMIT)]),
    );
    mocks.retryHarnessRun.mockImplementation(
      retryStream([failedResult(RETRYABLE_PRECOMMIT)]),
    );
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(result.current.error).toBe(RETRY_PRECOMMIT_EXPLANATION),
    );
    expect(result.current.status).toBe("error");
    expect(result.current.messages.at(-1)?.failed).toBe(true);
    // Still exactly one retry after everything settled.
    expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1);

    await act(async () => {
      await result.current.sendMessage("again");
    });
    await waitFor(() => expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(2));
    expect(mocks.streamHarnessPrompt).toHaveBeenCalledTimes(2);
  });

  it("leaves a retryable failure that already produced visible output to the user", async () => {
    mocks.streamHarnessPrompt.mockImplementation(
      promptStream([
        { type: "token", text: "partial" },
        failedResult({
          retryDisposition: "retryable",
          streamProgress: "visible",
        }),
      ]),
    );
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(mocks.retryHarnessRun).not.toHaveBeenCalled();
    expect(result.current.error).toBe("upstream 503");
    expect(result.current.lastFailurePermanent).toBe(false);
  });

  it("never retries a permanent failure: it flags the verdict, and Retry re-sends instead of asking the daemon", async () => {
    mocks.streamHarnessPrompt
      .mockImplementationOnce(
        promptStream([
          failedResult({
            errorText: "context window exceeded",
            permanent: true,
            retryDisposition: "permanent",
            streamProgress: "precommit",
          }),
        ]),
      )
      .mockImplementation(promptStream([cleanResult("shorter answer")]));
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.lastFailurePermanent).toBe(true);
    expect(result.current.messages.at(-1)).toMatchObject({
      failed: true,
      failurePermanent: true,
    });
    expect(mocks.retryHarnessRun).not.toHaveBeenCalled();

    await act(async () => {
      await result.current.retryLast();
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(mocks.retryHarnessRun).not.toHaveBeenCalled();
    expect(mocks.streamHarnessPrompt).toHaveBeenCalledTimes(2);
    expect(mocks.streamHarnessPrompt.mock.calls[1][1]).toBe("go");
    expect(result.current.lastFailurePermanent).toBe(false);
  });
});

describe("useAgentChat prompt recovery after a transport fault", () => {
  const TRUNCATED =
    "The connection closed before Mecatl returned a final result.";

  beforeEach(() => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue({
      sessionId: "s1",
      complete: true,
      messages: [],
    } satisfies SessionTranscript);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("hands a text-only prompt back to the composer, and a consumed draft is never re-sent behind the user's back", async () => {
    mocks.streamHarnessPrompt.mockRejectedValueOnce(new Error(TRUNCATED));
    mocks.retryHarnessRun.mockRejectedValue(ineligible());
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.error).toBe(TRUNCATED);
    expect(result.current.recoverDraft).toEqual({ text: "go" });
    expect(result.current.messages.at(-1)).toMatchObject({
      failed: true,
      failureDetail: TRUNCATED,
    });

    act(() => {
      result.current.consumeRecoverDraft();
    });
    expect(result.current.recoverDraft).toBeNull();

    // Untyped failure → the daemon is asked first; its 409 finds no held
    // prompt (the composer owns it now) and says so instead of replaying.
    await act(async () => {
      await result.current.retryLast();
    });
    expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1);
    expect(mocks.streamHarnessPrompt).toHaveBeenCalledTimes(1);
    await waitFor(() =>
      expect(result.current.error).toBe(RETRY_INELIGIBLE_NO_PROMPT),
    );
    expect(result.current.status).toBe("error");
  });

  it("keeps a prompt with an attachment out of the composer, and the resend fallback carries the file", async () => {
    mocks.streamHarnessPrompt
      .mockRejectedValueOnce(new Error(TRUNCATED))
      .mockImplementation(promptStream([cleanResult("looked at it")]));
    mocks.retryHarnessRun.mockRejectedValue(ineligible());
    const png = new File([new Uint8Array([137, 80, 78, 71])], "shot.png", {
      type: "image/png",
    });
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("what is this", [png]);
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.recoverDraft).toBeNull();

    await act(async () => {
      await result.current.retryLast();
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1);
    expect(mocks.streamHarnessPrompt).toHaveBeenCalledTimes(2);
    const [, text, parts] = mocks.streamHarnessPrompt.mock
      .calls[1] as PromptArgs;
    expect(text).toBe("what is this");
    expect(parts).toHaveLength(1);
    expect(parts[0]).toMatchObject({ kind: "image", mime_type: "image/png" });
  });

  it("names a retry that could not start and keeps the failure in place", async () => {
    mocks.streamHarnessPrompt.mockRejectedValueOnce(new Error(TRUNCATED));
    mocks.retryHarnessRun.mockRejectedValue(new Error("socket hang up"));
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    await act(async () => {
      await result.current.retryLast();
    });
    await waitFor(() =>
      expect(result.current.error).toBe(
        "Retry could not start: socket hang up",
      ),
    );
    expect(result.current.status).toBe("error");
    expect(mocks.streamHarnessPrompt).toHaveBeenCalledTimes(1);
    expect(result.current.messages.at(-1)).toMatchObject({
      failed: true,
      failureDetail: "Retry could not start: socket hang up",
    });
  });
});

describe("useAgentChat durable failed-step retry after a reload", () => {
  const failedTranscript: SessionTranscript = {
    sessionId: "s1",
    complete: true,
    messages: [
      { role: "user", text: "do it", toolCalls: [] },
      { role: "assistant", text: "", toolCalls: [] },
    ],
  };

  afterEach(() => {
    vi.clearAllMocks();
  });

  function openFailedChat(transcript: SessionTranscript = failedTranscript) {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue(transcript);
    return renderHook(
      ({ state }: { state: string }) =>
        useAgentChat("s1", { sessionState: state }),
      { initialProps: { state: "failed" } },
    );
  }

  it("marks the trailing turn failed, offers the strip, and Retry asks the daemon first", async () => {
    mocks.retryHarnessRun.mockImplementation(
      retryStream([cleanResult("done")]),
    );
    const { result, rerender } = openFailedChat();
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.error).toBe(REHYDRATED_FAILURE_ERROR);
    expect(result.current.messages).toHaveLength(2);
    expect(result.current.messages[1]).toMatchObject({
      role: "assistant",
      failed: true,
      failureDetail: REHYDRATED_FAILURE_DETAIL,
    });
    expect(result.current.lastFailurePermanent).toBe(false);

    await act(async () => {
      await result.current.retryLast();
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1);
    expect(mocks.streamHarnessPrompt).not.toHaveBeenCalled();
    expect(result.current.messages.at(-1)).toMatchObject({
      role: "assistant",
      content: "done",
    });

    // A stale `failed` from the inventory poll after the local retry must
    // not re-mark the fresh turn or re-park the chat.
    rerender({ state: "failed" });
    await act(async () => {});
    expect(result.current.status).toBe("idle");
    expect(result.current.error).toBeNull();
    expect(result.current.messages.at(-1)?.failed).toBeUndefined();
  });

  it("reverses the marking when the session leaves failed without this tab driving a run", async () => {
    const { result, rerender } = openFailedChat();
    await waitFor(() => expect(result.current.status).toBe("error"));
    rerender({ state: "idle" });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(result.current.error).toBeNull();
    expect(result.current.messages[1].failed).toBeUndefined();
    expect(result.current.messages[1].failureDetail).toBeUndefined();
  });

  it("appends a failed bubble when the transcript ends on the prompt, and the 409 fallback re-sends that prompt", async () => {
    mocks.retryHarnessRun.mockRejectedValue(ineligible());
    mocks.streamHarnessPrompt.mockImplementation(
      promptStream([cleanResult("second time lucky")]),
    );
    const { result } = openFailedChat({
      sessionId: "s1",
      complete: true,
      messages: [{ role: "user", text: "do it", toolCalls: [] }],
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.messages).toHaveLength(2);
    expect(result.current.messages[1]).toMatchObject({
      role: "assistant",
      failed: true,
    });

    await act(async () => {
      await result.current.retryLast();
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1);
    expect(mocks.streamHarnessPrompt).toHaveBeenCalledTimes(1);
    expect(mocks.streamHarnessPrompt.mock.calls[0][0]).toBe("s1");
    expect(mocks.streamHarnessPrompt.mock.calls[0][1]).toBe("do it");
    expect(result.current.messages.map((m) => m.role)).toEqual([
      "user",
      "assistant",
    ]);
    expect(result.current.messages[1].content).toBe("second time lucky");
  });
});
