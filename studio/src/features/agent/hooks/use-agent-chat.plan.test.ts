import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PLAN_APPROVED_PROCEED_TEXT } from "../plan-ask";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * Plan-mode auto-proceed (TUI parity, cmd/mecatui/ui/update.go): Studio
 * answers a PresentPlan ask over the interactive resumeApproval control,
 * which ends the plan run on `plan_approved` WITHOUT starting execution —
 * so the tab that approved sends the harness-framed proceed prompt itself
 * once the run settles. Never on iterate, never from a tab that did not
 * answer, and the host learns of every terminal. The daemon client is
 * mocked at the module boundary.
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

/** The CURRENT prompt stream's event sink and its resolver, so a test can
 *  inject daemon frames and then end the stream the way the daemon does
 *  after its terminal `result`. */
let emitStreamEvent: ((event: StreamEvent) => void) | null = null;
let finishStream: (() => void) | null = null;
let runSerial = 0;

function scriptedStream(...args: StreamArgs): Promise<void> {
  const [, , , onEvent, signal, hooks] = args;
  emitStreamEvent = onEvent;
  runSerial += 1;
  hooks?.onRunStarted?.(`run-${runSerial}`);
  return new Promise<void>((resolve, reject) => {
    finishStream = resolve;
    signal?.addEventListener("abort", () =>
      reject(new DOMException("The operation was aborted.", "AbortError")),
    );
  });
}

const PLAN_ASK = "s1:1:c1:r1";

const planAsk: StreamEvent = {
  type: "approval",
  approvalId: PLAN_ASK,
  sessionId: "s1",
  toolName: "PresentPlan",
  description: "PresentPlan needs your approval.",
  details: "",
  reason: "plan approval",
  args: JSON.stringify({ plan: "# Plan\n\n1. do the thing" }),
};

const terminal = (stop: string): StreamEvent => ({
  type: "run_result",
  stop,
  text: "",
  errorText: "",
  permanent: false,
});

const emit = (...events: StreamEvent[]) => {
  act(() => {
    for (const event of events) emitStreamEvent?.(event);
  });
};

/** Ends the current prompt stream (the daemon closes it after `result`). */
const endStream = async () => {
  await act(async () => {
    finishStream?.();
  });
};

describe("useAgentChat plan-approved auto-proceed", () => {
  beforeEach(() => {
    streamHarnessPrompt.mockImplementation(scriptedStream);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    emitStreamEvent = null;
    finishStream = null;
    runSerial = 0;
  });

  /** Starts a plan run on a fresh draft (the daemon session it mints is "s1"). */
  async function startPlanRun(onRunEnded?: (stop: string) => void) {
    const rendered = renderHook(() => useAgentChat(null, { onRunEnded }));
    await act(async () => {
      void rendered.result.current.sendMessage("plan the rollout");
    });
    await waitFor(() => expect(rendered.result.current.isStreaming).toBe(true));
    expect(streamHarnessPrompt).toHaveBeenCalledTimes(1);
    return rendered;
  }

  it("sends the harness-framed proceed prompt after Approve & run, as a synthetic user turn", async () => {
    const onRunEnded = vi.fn();
    const { result } = await startPlanRun(onRunEnded);
    emit(planAsk);
    expect(result.current.pendingApproval?.toolName).toBe("PresentPlan");

    await act(async () => {
      await result.current.respondToApproval("once");
    });
    expect(respondToHarnessApproval).toHaveBeenCalledWith(
      "s1",
      PLAN_ASK,
      "allow_once",
      "run-1",
    );

    emit(terminal("plan_approved"));
    await endStream();

    await waitFor(() => expect(streamHarnessPrompt).toHaveBeenCalledTimes(2));
    const [sessionId, content] = streamHarnessPrompt.mock
      .calls[1] as StreamArgs;
    expect(sessionId).toBe("s1");
    expect(content).toBe(PLAN_APPROVED_PROCEED_TEXT);
    expect(onRunEnded).toHaveBeenCalledWith("plan_approved");

    // The proceed turn is on screen as a harness-authored user message.
    const proceed = result.current.messages.find(
      (message) =>
        message.role === "user" &&
        message.content === PLAN_APPROVED_PROCEED_TEXT,
    );
    expect(proceed?.synthetic).toBe(true);
    // The plan turn carries the durable transcript label (the stop-reason
    // chip); the transient status line is cleared by the execution run the
    // proceed prompt starts, exactly like any next run.
    expect(
      result.current.messages.some(
        (message) =>
          message.role === "assistant" &&
          message.stopReason === "plan_approved",
      ),
    ).toBe(true);
  });

  it("also proceeds after Auto-accept edits (allow_always is an approval too)", async () => {
    const { result } = await startPlanRun();
    emit(planAsk);
    await act(async () => {
      await result.current.respondToApproval("always");
    });
    expect(respondToHarnessApproval).toHaveBeenCalledWith(
      "s1",
      PLAN_ASK,
      "allow_always",
      "run-1",
    );
    emit(terminal("plan_approved"));
    await endStream();
    await waitFor(() => expect(streamHarnessPrompt).toHaveBeenCalledTimes(2));
    expect((streamHarnessPrompt.mock.calls[1] as StreamArgs)[1]).toBe(
      PLAN_APPROVED_PROCEED_TEXT,
    );
  });

  it("never proceeds after Iterate: the run ends cleanly for the operator's next prompt", async () => {
    const onRunEnded = vi.fn();
    const { result } = await startPlanRun(onRunEnded);
    emit(planAsk);
    await act(async () => {
      await result.current.respondToApproval("deny");
    });
    emit(terminal("plan_iterate"));
    await endStream();
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(streamHarnessPrompt).toHaveBeenCalledTimes(1);
    expect(onRunEnded).toHaveBeenCalledWith("plan_iterate");
    expect(result.current.statusMessage?.text).toBe(
      "plan iterate · awaiting your feedback",
    );
    expect(
      result.current.messages.some((message) => message.synthetic === true),
    ).toBe(false);
  });

  it("never proceeds from a plan_approved terminal this tab did not approve (another client answered)", async () => {
    const { result } = await startPlanRun();
    emit(planAsk);
    // The ask is answered elsewhere: the daemon ends the run without this
    // tab's verdict. A second proceed prompt would race the other client's.
    emit(terminal("plan_approved"));
    await endStream();
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(streamHarnessPrompt).toHaveBeenCalledTimes(1);
  });

  it("an ordinary tool approval never arms the proceed, whatever the run's stop", async () => {
    const { result } = await startPlanRun();
    emit({ ...planAsk, toolName: "Bash", approvalId: "s1:1:c9:r1" });
    await act(async () => {
      await result.current.respondToApproval("once");
    });
    emit(terminal("plan_approved"));
    await endStream();
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(streamHarnessPrompt).toHaveBeenCalledTimes(1);
  });

  it("spends the arm on the terminal: a later run's plan_approved does not re-fire it", async () => {
    const { result } = await startPlanRun();
    emit(planAsk);
    await act(async () => {
      await result.current.respondToApproval("once");
    });
    emit(terminal("plan_approved"));
    await endStream();
    await waitFor(() => expect(streamHarnessPrompt).toHaveBeenCalledTimes(2));
    // The execution run (run-2) ends on plan_approved again, say — no ask
    // was approved during it, so nothing proceeds a third time.
    emit(terminal("plan_approved"));
    await endStream();
    await waitFor(() => expect(result.current.isStreaming).toBe(false));
    expect(streamHarnessPrompt).toHaveBeenCalledTimes(2);
  });
});
