import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { WatchDelivery, WatchOptions } from "@/lib/harness/watch";
import type { SessionTranscript } from "@/lib/protocol";
import type { AgentMessage, StreamEvent } from "../types";
import { reduceWatchEvent, useAgentChat } from "./use-agent-chat";

/**
 * Live session titles and the always-armed metadata watch (mecatui's
 * `armLiveFeed` + `onSessionTitle`):
 *
 *  - a `title` stream event is session metadata, never a bubble — the
 *    reducer leaves the transcript alone and the hook hands it to the host
 *    (`onTitle`) from every path it can arrive on: the full durable watch of
 *    a run driven elsewhere, and the metadata watch below;
 *  - an OPEN chat with no run to render (idle per inventory) still attaches a
 *    watch when the daemon supports it: replay is skimmed, titles are adopted
 *    in either phase (the host's revision guard orders them), and a LIVE
 *    run-bearing frame this tab is not driving fires `onExternalRunDetected`
 *    once per run id so the host re-walks the inventory and the full watch
 *    takes over;
 *  - the metadata watch is torn down the moment the row flips to running.
 *
 * The daemon client and the watch transport are mocked at the module
 * boundary, as in the sibling hook tests.
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
  runtime: {
    features: new Set<string>(["watch_session_events"]),
  },
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
    features: mocks.runtime.features,
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
  options: WatchOptions | undefined;
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
        options,
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

const titleEvent = (
  title: string,
  revision: number | null,
  provenance = "first-prompt",
): StreamEvent => ({ type: "title", title, provenance, revision });

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

/** A prompt stream that names its run, then hangs until aborted. */
function hangingStream(...args: StreamArgs): Promise<void> {
  const [, , , , signal, hooks] = args;
  hooks?.onRunStarted?.("run-own");
  return new Promise<void>((_, reject) => {
    signal?.addEventListener("abort", () =>
      reject(new DOMException("The operation was aborted.", "AbortError")),
    );
  });
}

describe("reduceWatchEvent and title events", () => {
  it("leaves the transcript untouched — a title is metadata, not a bubble", () => {
    const before: AgentMessage[] = [
      { id: "m1", role: "assistant", content: "hi", timestamp: 1 },
    ];
    expect(
      reduceWatchEvent(before, titleEvent("Fix flake", 2), () => "x"),
    ).toBe(before);
    expect(reduceWatchEvent([], titleEvent("Fix flake", 2), () => "x")).toEqual(
      [],
    );
  });
});

describe("useAgentChat metadata watch", () => {
  beforeEach(() => {
    mocks.runtime.features = new Set(["watch_session_events"]);
    mocks.fetchSessionTranscriptMessages.mockResolvedValue({
      sessionId: "s1",
      complete: true,
      messages: [],
    } satisfies SessionTranscript);
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("arms on an idle chat from the beginning and adopts titles in replay and live phases", async () => {
    const watches = capturingWatches();
    const onTitle = vi.fn();
    const { result } = renderHook(() =>
      useAgentChat("s1", { sessionState: "idle", onTitle }),
    );
    await waitFor(() => expect(watches).toHaveLength(1));
    expect(watches[0].sessionId).toBe("s1");
    // The metadata watch names its resume position (empty = from the start),
    // unlike the full watch, which always rebuilds from the beginning.
    expect(watches[0].options?.cursor).toBe("");

    act(() => {
      watches[0].deliver(replay(titleEvent("Fix the flake", 1)));
      watches[0].deliver(boundary);
      watches[0].deliver(live(titleEvent("Fix the scheduler flake", 2)));
    });
    expect(onTitle).toHaveBeenNthCalledWith(1, "s1", {
      title: "Fix the flake",
      provenance: "first-prompt",
      revision: 1,
    });
    expect(onTitle).toHaveBeenNthCalledWith(2, "s1", {
      title: "Fix the scheduler flake",
      provenance: "first-prompt",
      revision: 2,
    });
    // Nothing reached the transcript or the streaming state.
    expect(result.current.messages).toEqual([]);
    expect(result.current.isStreaming).toBe(false);
  });

  it("fires onExternalRunDetected once per LIVE run id, never for replayed history", async () => {
    const watches = capturingWatches();
    const onExternalRunDetected = vi.fn();
    renderHook(() =>
      useAgentChat("s1", { sessionState: "idle", onExternalRunDetected }),
    );
    await waitFor(() => expect(watches).toHaveLength(1));

    act(() => {
      // A long history replays every earlier run: none of it is "a run
      // started elsewhere", so none of it may storm the inventory.
      watches[0].deliver(
        replay({ type: "token", text: "old", runId: "run-old" }),
      );
      watches[0].deliver(
        replay({
          type: "run_result",
          stop: "end_turn",
          text: "",
          errorText: "",
          permanent: false,
          runId: "run-old",
        }),
      );
      watches[0].deliver(boundary);
    });
    expect(onExternalRunDetected).not.toHaveBeenCalled();

    act(() => {
      watches[0].deliver(
        live({ type: "user_prompt", text: "go", runId: "run-1" }),
      );
      watches[0].deliver(live({ type: "token", text: "…", runId: "run-1" }));
    });
    expect(onExternalRunDetected).toHaveBeenCalledTimes(1);
    expect(onExternalRunDetected).toHaveBeenCalledWith("s1");

    act(() => {
      watches[0].deliver(live({ type: "token", text: "next", runId: "run-2" }));
      // A frame with no run id (a title, a notice) is not a run.
      watches[0].deliver(live({ type: "notice", text: "compacted" }));
    });
    expect(onExternalRunDetected).toHaveBeenCalledTimes(2);
  });

  it("does not report this tab's own run as external", async () => {
    const watches = capturingWatches();
    mocks.streamHarnessPrompt.mockImplementation(hangingStream);
    const onExternalRunDetected = vi.fn();
    const { result } = renderHook(() =>
      useAgentChat("s1", { sessionState: "idle", onExternalRunDetected }),
    );
    await waitFor(() => expect(watches).toHaveLength(1));
    act(() => watches[0].deliver(boundary));

    await act(async () => {
      void result.current.sendMessage("hello");
    });
    await waitFor(() => expect(mocks.streamHarnessPrompt).toHaveBeenCalled());

    // The daemon fans this tab's own run out on the durable feed too.
    act(() => {
      watches[0].deliver(live({ type: "token", text: "hi", runId: "run-own" }));
    });
    expect(onExternalRunDetected).not.toHaveBeenCalled();
  });

  it("hands over to the full watch when the row flips to running, which also relays titles", async () => {
    const watches = capturingWatches();
    const onTitle = vi.fn();
    const { rerender, result } = renderHook(
      ({ state }: { state: string }) =>
        useAgentChat("s1", { sessionState: state, onTitle }),
      { initialProps: { state: "idle" } },
    );
    await waitFor(() => expect(watches).toHaveLength(1));
    act(() => watches[0].deliver(boundary));

    rerender({ state: "running" });
    await waitFor(() => expect(watches).toHaveLength(2));
    // The metadata watch is gone; the full watch rebuilds from the start.
    expect(watches[0].aborted()).toBe(true);
    expect(watches[1].options?.cursor).toBeUndefined();

    act(() => {
      watches[1].deliver(boundary);
      watches[1].deliver(live(titleEvent("Renamed elsewhere", 3, "operator")));
      watches[1].deliver(live({ type: "token", text: "hi", runId: "run-1" }));
    });
    expect(onTitle).toHaveBeenCalledWith("s1", {
      title: "Renamed elsewhere",
      provenance: "operator",
      revision: 3,
    });
    // The title added no bubble of its own: the token opened the only one.
    expect(result.current.messages.map((m) => m.role)).toEqual(["assistant"]);
    expect(result.current.messages[0].content).toBe("hi");
  });

  it("resumes a re-armed metadata watch from the last cursor it saw", async () => {
    const watches = capturingWatches();
    const { rerender } = renderHook(
      ({ state }: { state: string }) =>
        useAgentChat("s1", { sessionState: state }),
      { initialProps: { state: "idle" } },
    );
    await waitFor(() => expect(watches).toHaveLength(1));
    act(() => {
      watches[0].deliver(boundary);
      watches[0].deliver(live({ type: "notice", text: "x" }, "cur-42"));
    });
    // running → the full watch; idle again → the metadata watch re-arms
    // after the resume position rather than replaying the whole log.
    rerender({ state: "running" });
    await waitFor(() => expect(watches).toHaveLength(2));
    rerender({ state: "idle" });
    await waitFor(() => expect(watches).toHaveLength(3));
    expect(watches[2].options?.cursor).toBe("cur-42");
  });

  it("does not arm without the daemon's watch feature", async () => {
    mocks.runtime.features = new Set();
    const watches = capturingWatches();
    renderHook(() => useAgentChat("s1", { sessionState: "idle" }));
    await waitFor(() =>
      expect(mocks.fetchSessionTranscriptMessages).toHaveBeenCalled(),
    );
    expect(watches).toHaveLength(0);
  });
});
