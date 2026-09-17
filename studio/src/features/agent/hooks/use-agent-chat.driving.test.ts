import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionTranscript } from "@/lib/protocol";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * `drivingRun` — the leave guard's second arm: true exactly while THIS tab's
 * prompt or retry stream is driving a run (a `POST /prompt` stream ends its
 * run on client disconnect, so closing the tab would end it), false once the
 * stream settles, and never set by a watched run driven elsewhere. Mirrors
 * the hook's `drivingRef` at the same sites. The daemon client is mocked at
 * the module boundary, as in the sibling hook tests.
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
type RetryArgs = [
  string,
  (event: StreamEvent) => void,
  AbortSignal | undefined,
  Hooks,
];

type RunResult = Extract<StreamEvent, { type: "run_result" }>;

const cleanResult = (text: string): StreamEvent => ({
  type: "run_result",
  stop: "end_turn",
  text,
  errorText: "",
  permanent: false,
});

const failedVisible = (): StreamEvent =>
  ({
    type: "run_result",
    stop: "error",
    text: "",
    errorText: "upstream 503",
    permanent: false,
    retryDisposition: "retryable",
    streamProgress: "visible",
  }) satisfies RunResult;

/** A stream that emits its frames, then stays OPEN until `release` runs. */
function heldStream<Args extends unknown[]>(
  pick: (args: Args) => {
    onEvent: (event: StreamEvent) => void;
    hooks: Hooks;
  },
  runId: string,
  events: StreamEvent[],
  tail: StreamEvent,
) {
  let release: () => void = () => undefined;
  let emit: (event: StreamEvent) => void = () => undefined;
  const impl = async (...args: Args) => {
    const { onEvent, hooks } = pick(args);
    emit = onEvent;
    hooks?.onRunStarted?.(runId);
    for (const event of events) onEvent(event);
    await new Promise<void>((resolve) => {
      release = () => {
        onEvent(tail);
        resolve();
      };
    });
  };
  return { impl, release: () => release(), emit: (e: StreamEvent) => emit(e) };
}

describe("useAgentChat drivingRun", () => {
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

  it("is false before any run", () => {
    const { result } = renderHook(() => useAgentChat(null));
    expect(result.current.drivingRun).toBe(false);
    expect(result.current.isStreaming).toBe(false);
  });

  it("is true while this tab's prompt stream is open, and false once it settles", async () => {
    const held = heldStream<PromptArgs>(
      ([, , , onEvent, , hooks]) => ({ onEvent, hooks }),
      "run-prompt",
      [{ type: "token", text: "Working…" }],
      cleanResult("Working… done."),
    );
    mocks.streamHarnessPrompt.mockImplementation(held.impl);
    const { result } = renderHook(() => useAgentChat(null));

    let send: Promise<void> = Promise.resolve();
    act(() => {
      send = result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.drivingRun).toBe(true));
    expect(result.current.isStreaming).toBe(true);

    await act(async () => {
      held.release();
      await send;
    });
    expect(result.current.drivingRun).toBe(false);
    await waitFor(() => expect(result.current.status).toBe("idle"));
  });

  it("is true again while a failed-step retry this tab drives is open", async () => {
    // A retryable failure with visible output is left to the user (no
    // automatic retry), so `retryLast` is the second drive site.
    mocks.streamHarnessPrompt.mockImplementation(
      async (...args: PromptArgs) => {
        const [, , , onEvent, , hooks] = args;
        hooks?.onRunStarted?.("run-prompt");
        onEvent({ type: "token", text: "partial" });
        onEvent(failedVisible());
      },
    );
    const held = heldStream<RetryArgs>(
      ([, onEvent, , hooks]) => ({ onEvent, hooks }),
      "run-retry",
      [{ type: "token", text: "again" }],
      cleanResult("again, done."),
    );
    mocks.retryHarnessRun.mockImplementation(held.impl);
    const { result } = renderHook(() => useAgentChat(null));

    await act(async () => {
      await result.current.sendMessage("go");
    });
    expect(result.current.drivingRun).toBe(false);
    expect(mocks.retryHarnessRun).not.toHaveBeenCalled();

    let retry: Promise<void> = Promise.resolve();
    act(() => {
      retry = result.current.retryLast();
    });
    await waitFor(() => expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(result.current.drivingRun).toBe(true));

    await act(async () => {
      held.release();
      await retry;
    });
    expect(result.current.drivingRun).toBe(false);
  });

  it("drops to false when the run is cancelled from this tab", async () => {
    const held = heldStream<PromptArgs>(
      ([, , , onEvent, , hooks]) => ({ onEvent, hooks }),
      "run-prompt",
      [{ type: "token", text: "Working…" }],
      cleanResult("cut short"),
    );
    // The abort signal ends the held stream the way a real fetch would.
    mocks.streamHarnessPrompt.mockImplementation(
      async (...args: PromptArgs) => {
        const signal = args[4];
        signal?.addEventListener("abort", () => held.release());
        await held.impl(...args);
      },
    );
    const { result } = renderHook(() => useAgentChat(null));
    let send: Promise<void> = Promise.resolve();
    act(() => {
      send = result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.drivingRun).toBe(true));

    await act(async () => {
      await result.current.cancelChat();
      await send;
    });
    expect(mocks.cancelHarnessRun).toHaveBeenCalledTimes(1);
    expect(result.current.drivingRun).toBe(false);
    expect(result.current.isStreaming).toBe(false);
  });
});
