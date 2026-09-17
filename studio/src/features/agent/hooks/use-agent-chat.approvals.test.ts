import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * The FIFO permission-ask queue (the TUI's concurrent-asks model): a second
 * ask queues behind the one on screen instead of replacing it, a verdict
 * advances to the next, a withdrawn child ask leaves a notice, and a dead
 * run's asks never outlive it. The daemon client is mocked at the module
 * boundary — the wire adapters have their own tests.
 */

const {
  cancelHarnessRun,
  createHarnessSession,
  respondToHarnessApproval,
  streamHarnessPrompt,
} = vi.hoisted(() => ({
  cancelHarnessRun: vi.fn(async () => undefined),
  createHarnessSession: vi.fn(async () => "s1"),
  respondToHarnessApproval: vi.fn(async () => undefined),
  streamHarnessPrompt: vi.fn(),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  cancelHarnessRun,
  createHarnessSession,
  respondToHarnessApproval,
  streamHarnessPrompt,
  fetchSessionTranscriptMessages: vi.fn(async () => []),
  fetchHarnessSessionDetail: vi.fn(async () => {
    throw new Error("offline");
  }),
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

type StreamArgs = [
  string,
  string,
  unknown[],
  (event: StreamEvent) => void,
  AbortSignal | undefined,
  { onRunStarted?: (runId: string) => void } | undefined,
];

/** The live stream's event sink, so a test can inject daemon frames. */
let emitStreamEvent: ((event: StreamEvent) => void) | null = null;

/** A prompt stream that names its run, then hangs until the signal aborts —
 *  the shape of a live run parked on the operator. */
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
const CHILD_ASK = "subagent-x:1:c1:r1";

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

describe("useAgentChat approval queue", () => {
  beforeEach(() => {
    streamHarnessPrompt.mockImplementation(hangingStream);
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

  it("queues a second ask behind the first (FIFO), dedupes a re-surfaced id, and classifies child asks against the daemon session id", async () => {
    const { result } = await startRun();
    emit(approval(MAIN_ASK, "Bash"), approval(CHILD_ASK, "Edit"));
    expect(result.current.status).toBe("waiting_approval");
    expect(result.current.pendingApproval).toMatchObject({
      approvalId: MAIN_ASK,
      toolName: "Bash",
      child: false,
    });
    expect(result.current.approvalQueueLength).toBe(2);

    // The daemon re-delivers a known ask: not a third one.
    emit(approval(MAIN_ASK, "Bash"));
    expect(result.current.approvalQueueLength).toBe(2);
  });

  it("answering the head sends its verdict, records it on the turn, and advances to the next ask", async () => {
    const { result } = await startRun();
    emit(approval(MAIN_ASK, "Bash"), approval(CHILD_ASK, "Edit"));

    await act(async () => {
      await result.current.respondToApproval("once");
    });
    expect(respondToHarnessApproval).toHaveBeenCalledWith(
      "s1",
      MAIN_ASK,
      "allow_once",
      "run-1",
    );
    // Advance-to-next: the child ask takes the screen; still waiting.
    expect(result.current.pendingApproval).toMatchObject({
      approvalId: CHILD_ASK,
      child: true,
    });
    expect(result.current.approvalQueueLength).toBe(1);
    expect(result.current.status).toBe("waiting_approval");
    // The verdict is recorded locally — EvApproval never rides the live
    // stream — in the durable watch's own words.
    expect(result.current.messages.at(-1)?.notices).toContain(
      "Permission: Bash allowed once",
    );

    await act(async () => {
      await result.current.respondToApproval("deny");
    });
    expect(respondToHarnessApproval).toHaveBeenLastCalledWith(
      "s1",
      CHILD_ASK,
      "deny",
      "run-1",
    );
    expect(result.current.pendingApproval).toBeNull();
    expect(result.current.approvalQueueLength).toBe(0);
    expect(result.current.status).toBe("streaming");
    expect(result.current.messages.at(-1)?.notices).toEqual([
      "Permission: Bash allowed once",
      "Permission: Edit denied",
    ]);
  });

  it("a withdrawn child ask leaves a notice, whether queued or on screen; a main ask vanishes quietly", async () => {
    const { result } = await startRun();
    emit(approval(MAIN_ASK, "Bash"), approval(CHILD_ASK, "Edit"));

    // Queued child retracted: the head is untouched, still waiting.
    emit({ type: "retract", approvalId: CHILD_ASK });
    expect(result.current.pendingApproval?.approvalId).toBe(MAIN_ASK);
    expect(result.current.approvalQueueLength).toBe(1);
    expect(result.current.status).toBe("waiting_approval");
    expect(result.current.messages.at(-1)?.notices).toEqual([
      "Queued permission request withdrawn — the subagent that asked was cancelled",
    ]);

    // Main ask retracted: no notice (nothing was cancelled on the user's
    // behalf), and with nothing left the run streams again.
    emit({ type: "retract", approvalId: MAIN_ASK });
    expect(result.current.pendingApproval).toBeNull();
    expect(result.current.status).toBe("streaming");
    expect(result.current.messages.at(-1)?.notices).toHaveLength(1);

    // A child ask on screen when withdrawn gets the head wording.
    emit(approval(CHILD_ASK, "Edit"), {
      type: "retract",
      approvalId: CHILD_ASK,
    });
    expect(result.current.messages.at(-1)?.notices?.at(-1)).toBe(
      "Permission request withdrawn — the subagent that asked was cancelled",
    );
    expect(result.current.status).toBe("streaming");
  });

  it("the run's terminal drops a leftover ask, so a dead ask never stays on screen", async () => {
    const { result } = await startRun();
    emit(approval(MAIN_ASK, "Bash"));
    expect(result.current.approvalQueueLength).toBe(1);
    emit({
      type: "run_result",
      stop: "end_turn",
      text: "",
      errorText: "",
      permanent: false,
    });
    expect(result.current.pendingApproval).toBeNull();
    expect(result.current.approvalQueueLength).toBe(0);
  });

  it("cancelling the run drops every queued ask", async () => {
    const { result } = await startRun();
    emit(approval(MAIN_ASK, "Bash"), approval(CHILD_ASK, "Edit"));
    await act(async () => {
      await result.current.cancelChat();
    });
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(result.current.pendingApproval).toBeNull();
    expect(result.current.approvalQueueLength).toBe(0);
    expect(cancelHarnessRun).toHaveBeenCalledWith("s1", "run-1");
  });
});
