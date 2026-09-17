import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WatchDelivery, WatchOptions } from "@/lib/harness/watch";
import type { SessionTranscript } from "@/lib/protocol";
import { onRunFinished, type RunFinishedSignal } from "../run-signals";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * The page-wide run-finished signal (`run-signals.ts`, mecatui's
 * "re-list learned-skill receipts on every ResultMsg" trigger): the hook
 * emits it exactly once per LIVE run terminal — the prompt stream's
 * `run_result` and the durable watch's live `run_result` — and never for a
 * replayed history frame, which is a rebuild, not a run ending now. The
 * daemon client and the watch transport are mocked at the module boundary,
 * as in the sibling hook tests.
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
  watchSessionEvents: vi.fn(),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  createHarnessSession: mocks.createHarnessSession,
  streamHarnessPrompt: mocks.streamHarnessPrompt,
  retryHarnessRun: mocks.retryHarnessRun,
  fetchSessionTranscriptMessages: mocks.fetchSessionTranscriptMessages,
  fetchHarnessSessionDetail: mocks.fetchHarnessSessionDetail,
  cancelHarnessRun: mocks.cancelHarnessRun,
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => ({
    connected: true,
    features: new Set<string>(["watch_session_events"]),
    serverCapabilities: {},
  }),
}));

vi.mock("../composer-capabilities", () => ({
  refreshSlashCommands: vi.fn(async () => undefined),
}));

vi.mock("@/lib/harness/watch", () => ({
  watchSessionEvents: mocks.watchSessionEvents,
}));

vi.mock("@/lib/attachment-store", () => ({
  loadSentAttachments: vi.fn(async () => []),
  saveSentAttachments: vi.fn(async () => undefined),
}));

/** One captured `watchSessionEvents` call the test drives by hand. */
interface WatchCall {
  sessionId: string;
  deliver: (delivery: WatchDelivery) => void;
  aborted: () => boolean;
}

/** Every watch attaches, records its sink, and stays open until aborted. */
function capturingWatches(): WatchCall[] {
  const calls: WatchCall[] = [];
  mocks.watchSessionEvents.mockImplementation(
    (
      sessionId: string,
      onDelivery: (delivery: WatchDelivery) => void,
      options?: WatchOptions,
    ) => {
      const signal = options?.signal;
      calls.push({
        sessionId,
        deliver: onDelivery,
        aborted: () => signal?.aborted === true,
      });
      return new Promise<void>((resolve) => {
        if (signal?.aborted) resolve();
        signal?.addEventListener("abort", () => resolve());
      });
    },
  );
  return calls;
}

const result = (stop: string, text = "done"): StreamEvent => ({
  type: "run_result",
  stop,
  text,
  errorText: "",
  permanent: false,
});

const boundary: WatchDelivery = {
  phase: "live",
  cursor: "cur-live",
  event: null,
};
const replay = (event: StreamEvent, cursor = "cur-r"): WatchDelivery => ({
  phase: "replay",
  cursor,
  event,
});
const live = (event: StreamEvent, cursor = "cur-l"): WatchDelivery => ({
  phase: "live",
  cursor,
  event,
});

type StreamArgs = [
  string,
  string,
  unknown[],
  (event: StreamEvent) => void,
  AbortSignal | undefined,
  { onRunStarted?: (runId: string) => void } | undefined,
];

/** A prompt stream that names its run, streams one token and ends cleanly. */
async function finishingStream(...args: StreamArgs): Promise<void> {
  const [, , , onEvent, , hooks] = args;
  hooks?.onRunStarted?.("run-own");
  onEvent({ type: "token", text: "hi" });
  onEvent(result("end_turn"));
}

const emptyTranscript = (): SessionTranscript => ({
  sessionId: "s1",
  complete: true,
  messages: [],
});

describe("useAgentChat run-finished signal", () => {
  let heard: RunFinishedSignal[];
  let stop: () => void;

  beforeEach(() => {
    mocks.fetchSessionTranscriptMessages.mockResolvedValue(emptyTranscript());
    heard = [];
    stop = onRunFinished((signal) => heard.push(signal));
  });

  afterEach(() => {
    stop();
    vi.clearAllMocks();
  });

  it("emits once when this tab's own prompt stream reaches its result", async () => {
    // The idle chat's metadata watch attaches too; keep it inert.
    capturingWatches();
    mocks.streamHarnessPrompt.mockImplementation(finishingStream);
    const { result: hook } = renderHook(() =>
      useAgentChat("s1", { sessionState: "idle" }),
    );
    await act(async () => {
      await hook.current.sendMessage("go");
    });
    await waitFor(() => expect(hook.current.isStreaming).toBe(false));
    expect(heard).toEqual([{ sessionId: "s1", stop: "end_turn" }]);
  });

  it("emits once for the durable watch's LIVE result and never for a replayed one", async () => {
    const watches = capturingWatches();
    renderHook(() => useAgentChat("s1", { sessionState: "running" }));
    await waitFor(() => expect(watches).toHaveLength(1));
    expect(watches[0].sessionId).toBe("s1");

    act(() => {
      // History: an earlier run's terminal, rebuilt — not a run ending now.
      watches[0].deliver(replay({ type: "user_prompt", text: "earlier" }));
      watches[0].deliver(replay(result("end_turn", "earlier answer")));
      watches[0].deliver(boundary);
    });
    expect(heard).toEqual([]);

    // Async act: the terminal arm re-reads the session detail, and that
    // settles a microtask later.
    await act(async () => {
      watches[0].deliver(live({ type: "token", text: "now", runId: "run-2" }));
      watches[0].deliver(live(result("max_turns", "now answer")));
    });
    await waitFor(() =>
      expect(mocks.fetchHarnessSessionDetail).toHaveBeenCalled(),
    );
    expect(heard).toEqual([{ sessionId: "s1", stop: "max_turns" }]);
  });
});
