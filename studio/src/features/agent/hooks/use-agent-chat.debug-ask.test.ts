import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import { debugAskResolvedNotice } from "../debug-ask";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * The developer tools' FAKE permission ask (the TUI's `/debug-ask`) and the
 * steer correlation trace. The fake ask is local: answering it makes NO
 * daemon request, puts back EXACTLY the status it displaced (idle stays
 * idle, a live run stays streaming — the composer's Enter behaviour depends
 * on it), a genuine ask arriving meanwhile displaces it (a fake ask must
 * never hide the daemon's real one), and a pending ask refuses a second one.
 * The daemon client is mocked at the module boundary, so "no request" is
 * "the approval client was never called".
 */

const {
  cancelHarnessRun,
  createHarnessSession,
  respondToHarnessApproval,
  steerHarnessRun,
  streamHarnessPrompt,
} = vi.hoisted(() => ({
  cancelHarnessRun: vi.fn(async () => undefined),
  createHarnessSession: vi.fn(async () => "s1"),
  respondToHarnessApproval: vi.fn(async () => undefined),
  steerHarnessRun: vi.fn(),
  streamHarnessPrompt: vi.fn(),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  cancelHarnessRun,
  createHarnessSession,
  respondToHarnessApproval,
  steerHarnessRun,
  streamHarnessPrompt,
  fetchSessionTranscriptMessages: vi.fn(async () => []),
  fetchHarnessSessionDetail: vi.fn(async () => {
    throw new Error("offline");
  }),
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => ({
    connected: true,
    features: new Set<string>(["http_steer"]),
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

let emitStreamEvent: ((event: StreamEvent) => void) | null = null;

/** A prompt stream that names its run, then hangs until the signal aborts. */
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

const MAIN_ASK = "s1:1:c1:r1";

const approval = (approvalId: string, toolName: string): StreamEvent => ({
  type: "approval",
  approvalId,
  sessionId: "s1",
  toolName,
  description: `${toolName} needs your approval.`,
  details: "",
});

const emit = (...events: StreamEvent[]) => {
  act(() => {
    for (const event of events) emitStreamEvent?.(event);
  });
};

describe("useAgentChat developer tools", () => {
  beforeEach(() => {
    streamHarnessPrompt.mockImplementation(hangingStream);
    steerHarnessRun.mockImplementation(
      async (_session: string, _text: string, messageId: string) => ({
        outcome: "accepted",
        messageId,
      }),
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
    emitStreamEvent = null;
  });

  /** Starts a run on a fresh draft (the daemon session it mints is "s1"). */
  async function startRun() {
    const rendered = renderHook(() => useAgentChat(null));
    await act(async () => {
      void rendered.result.current.sendMessage("go");
    });
    await waitFor(() => expect(rendered.result.current.isStreaming).toBe(true));
    expect(emitStreamEvent).not.toBeNull();
    return rendered;
  }

  it("parks a synthetic Shell ask mid-run; answering it sends nothing and returns to streaming exactly", async () => {
    const { result } = await startRun();
    expect(result.current.status).toBe("streaming");

    let injected = false;
    act(() => {
      injected = result.current.injectDebugApproval();
    });
    expect(injected).toBe(true);
    expect(result.current.status).toBe("waiting_approval");
    expect(result.current.isStreaming).toBe(true);
    expect(result.current.pendingApproval).toMatchObject({
      toolName: "Shell",
      synthetic: true,
      child: false,
      sessionId: "s1",
    });
    expect(result.current.approvalQueueLength).toBe(1);

    await act(async () => {
      await result.current.respondToApproval("deny");
    });
    expect(respondToHarnessApproval).not.toHaveBeenCalled();
    expect(result.current.pendingApproval).toBeNull();
    expect(result.current.approvalQueueLength).toBe(0);
    // Back to the status the injection displaced — streaming, never idle
    // (an idle here would drain the queue against the still-live run).
    expect(result.current.status).toBe("streaming");
    const last = result.current.messages.at(-1);
    expect(last?.role).toBe("assistant");
    expect(last?.notices).toContain(debugAskResolvedNotice("deny"));
  });

  it("on an idle chat the fake ask parks and its verdict returns the status to idle, with no request", async () => {
    // A run that ends at once: the chat is idle with a finished turn.
    streamHarnessPrompt.mockImplementation((...args: StreamArgs) => {
      const [, , , onEvent, , hooks] = args;
      hooks?.onRunStarted?.("run-1");
      onEvent({
        type: "run_result",
        stop: "end_turn",
        text: "done",
        errorText: "",
        permanent: false,
      });
      return Promise.resolve();
    });
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("idle"));
    expect(result.current.messages.at(-1)?.role).toBe("assistant");

    act(() => {
      result.current.injectDebugApproval();
    });
    expect(result.current.status).toBe("waiting_approval");
    await act(async () => {
      await result.current.respondToApproval("once");
    });
    expect(result.current.status).toBe("idle");
    expect(result.current.isStreaming).toBe(false);
    expect(respondToHarnessApproval).not.toHaveBeenCalled();
    expect(result.current.messages.at(-1)?.notices).toContain(
      debugAskResolvedNotice("once"),
    );
  });

  it("a genuine ask displaces the fake one, and only the genuine verdict reaches the daemon", async () => {
    const { result } = await startRun();
    act(() => {
      result.current.injectDebugApproval();
    });
    expect(result.current.pendingApproval?.synthetic).toBe(true);

    emit(approval(MAIN_ASK, "Bash"));
    expect(result.current.approvalQueueLength).toBe(1);
    expect(result.current.pendingApproval).toMatchObject({
      approvalId: MAIN_ASK,
      toolName: "Bash",
    });
    expect(result.current.pendingApproval?.synthetic).toBeUndefined();

    await act(async () => {
      await result.current.respondToApproval("once");
    });
    expect(respondToHarnessApproval).toHaveBeenCalledTimes(1);
    expect(respondToHarnessApproval).toHaveBeenCalledWith(
      "s1",
      MAIN_ASK,
      "allow_once",
      "run-1",
    );
  });

  it("refuses a second ask while one is pending — real or fake — and rotates the payload across injections", async () => {
    const { result } = await startRun();
    emit(approval(MAIN_ASK, "Bash"));
    let injected = true;
    act(() => {
      injected = result.current.injectDebugApproval();
    });
    expect(injected).toBe(false);
    expect(result.current.approvalQueueLength).toBe(1);
    expect(result.current.pendingApproval?.approvalId).toBe(MAIN_ASK);

    await act(async () => {
      await result.current.respondToApproval("deny");
    });
    act(() => {
      injected = result.current.injectDebugApproval();
    });
    expect(injected).toBe(true);
    const first = result.current.pendingApproval;
    act(() => {
      injected = result.current.injectDebugApproval();
    });
    expect(injected).toBe(false);
    expect(result.current.approvalQueueLength).toBe(1);

    await act(async () => {
      await result.current.respondToApproval("once");
    });
    act(() => {
      result.current.injectDebugApproval();
    });
    const second = result.current.pendingApproval;
    expect(second?.approvalId).not.toBe(first?.approvalId);
    expect(second?.args).not.toBe(first?.args);
  });

  it("traces every steer: accepted → drained at the echo's watermark; too_late on a dead run", async () => {
    const { result } = await startRun();
    expect(result.current.steerSupported).toBe(true);
    expect(result.current.steerTrace).toEqual([]);

    await act(async () => {
      await result.current.steerMessage("go left");
    });
    expect(steerHarnessRun).toHaveBeenCalledTimes(1);
    const steerId = result.current.pendingSteers[0]?.id ?? "";
    expect(steerId).toMatch(/^steer-/);
    expect(result.current.steerTrace).toEqual([
      expect.objectContaining({
        id: steerId,
        text: "go left",
        decision: "accepted",
      }),
    ]);

    emit({ type: "steer", text: "go left", messageId: steerId });
    expect(result.current.pendingSteers).toEqual([]);
    expect(result.current.steerTrace).toHaveLength(2);
    expect(result.current.steerTrace[1]).toMatchObject({
      id: steerId,
      decision: "drained",
      watermark: steerId,
    });

    steerHarnessRun.mockImplementationOnce(async () => {
      throw new HarnessApiError(409, "stale_run_control", "run ended");
    });
    await act(async () => {
      await result.current.steerMessage("too slow");
    });
    expect(result.current.steerTrace.at(-1)).toMatchObject({
      text: "too slow",
      decision: "too_late",
    });
    // The text was requeued, never lost — the trace only observes.
    expect(result.current.queuedMessages.map((m) => m.text)).toEqual([
      "too slow",
    ]);
  });
});
