import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * Per-child cancel (the TUI's `x` on a roster child): `cancelChild` names ONE
 * running subagent / parallel branch / team member by its session id while
 * the parent run keeps streaming. The lane in the fleet and the card on the
 * turn go `cancelling…` optimistically; the child's own terminal frame clears
 * the flag, and an "already finished" or a refusal clears it here and says
 * so. The daemon client is mocked at the module boundary — the wire call has
 * its own test (cancel-child.test.ts).
 */

const {
  cancelHarnessChild,
  createHarnessSession,
  streamHarnessPrompt,
  toastError,
  toastInfo,
} = vi.hoisted(() => ({
  cancelHarnessChild:
    vi.fn<
      (sessionId: string, childId: string) => Promise<"cancelled" | "not_found">
    >(),
  createHarnessSession: vi.fn(async () => "s1"),
  streamHarnessPrompt: vi.fn(),
  toastError: vi.fn(),
  toastInfo: vi.fn(),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  cancelHarnessChild,
  createHarnessSession,
  streamHarnessPrompt,
  fetchSessionTranscriptMessages: vi.fn(async () => []),
  fetchHarnessSessionDetail: vi.fn(async () => {
    throw new Error("offline");
  }),
}));

vi.mock("sonner", () => ({
  toast: { info: toastInfo, error: toastError, success: vi.fn() },
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
 *  the shape of a live run whose children are still working. */
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

const emit = (...events: StreamEvent[]) => {
  act(() => {
    for (const event of events) emitStreamEvent?.(event);
  });
};

const subagentStart: StreamEvent = {
  type: "delegation",
  kind: "subagent",
  label: "explore auth",
  detail: "explorer → gpt-5",
  childId: "subagent-1",
};

describe("useAgentChat cancelChild", () => {
  beforeEach(() => {
    streamHarnessPrompt.mockImplementation(hangingStream);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    cancelHarnessChild.mockReset();
    toastInfo.mockReset();
    toastError.mockReset();
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

  const laneOf = (result: { current: ReturnType<typeof useAgentChat> }) =>
    result.current.fleet.subagents.find((c) => c.childId === "subagent-1");
  const cardOf = (result: { current: ReturnType<typeof useAgentChat> }) =>
    result.current.messages
      .flatMap((m) => m.delegations ?? [])
      .find((c) => c.childId === "subagent-1");

  it("marks the lane and the card cancelling while the daemon call is in flight, and the child's own end clears both", async () => {
    let settle: ((outcome: "cancelled") => void) | null = null;
    cancelHarnessChild.mockImplementation(
      () =>
        new Promise((resolve) => {
          settle = resolve;
        }),
    );
    const { result } = await startRun();
    emit(subagentStart);
    expect(laneOf(result)?.cancelling).toBeUndefined();

    act(() => {
      void result.current.cancelChild("subagent-1");
    });
    expect(cancelHarnessChild).toHaveBeenCalledWith("s1", "subagent-1");
    expect(laneOf(result)?.cancelling).toBe(true);
    expect(cardOf(result)?.cancelling).toBe(true);
    // The run itself is untouched: still streaming, no whole-run cancel.
    expect(result.current.isStreaming).toBe(true);

    // An accepted cancel keeps the flag: the terminal frame is the truth.
    await act(async () => {
      settle?.("cancelled");
    });
    expect(laneOf(result)?.cancelling).toBe(true);
    expect(toastInfo).not.toHaveBeenCalled();

    emit({ type: "delegation_end", childId: "subagent-1", stop: "cancelled" });
    expect(laneOf(result)).toMatchObject({ stop: "cancelled" });
    expect(laneOf(result)?.cancelling).toBeUndefined();
    expect(cardOf(result)?.cancelling).toBeUndefined();
  });

  it("clears the flag and says so when the child had already finished", async () => {
    cancelHarnessChild.mockResolvedValue("not_found");
    const { result } = await startRun();
    emit(subagentStart);
    await act(async () => {
      await result.current.cancelChild("subagent-1");
    });
    expect(laneOf(result)?.cancelling).toBeUndefined();
    expect(cardOf(result)?.cancelling).toBeUndefined();
    expect(toastInfo).toHaveBeenCalledWith("That agent already finished.");
    expect(toastError).not.toHaveBeenCalled();
  });

  it("clears the flag and reports a refusal in the daemon's own words", async () => {
    cancelHarnessChild.mockRejectedValue(
      new HarnessApiError(409, "no_active_run", "session has no active run"),
    );
    const { result } = await startRun();
    emit(subagentStart);
    await act(async () => {
      await result.current.cancelChild("subagent-1");
    });
    expect(laneOf(result)?.cancelling).toBeUndefined();
    expect(cardOf(result)?.cancelling).toBeUndefined();
    expect(toastError).toHaveBeenCalledWith(
      expect.stringContaining("session has no active run"),
    );
  });

  it("reaches a team member once team.member has named its session, and team_end's disposition clears it", async () => {
    cancelHarnessChild.mockResolvedValue("cancelled");
    const { result } = await startRun();
    emit(
      {
        type: "delegation",
        kind: "team",
        label: "alice (lead)",
        detail: "",
        parentCallId: "call-team",
        teamId: "team-1",
        memberName: "alice",
        lead: true,
      },
      {
        type: "team_member",
        teamId: "team-1",
        parentCallId: "call-team",
        member: "alice",
        memberSessionId: "team-team-1-alice",
        innerKind: "tool.call",
        toolName: "Read",
        isError: false,
        contextUsed: 0,
        contextWindow: 0,
      },
    );
    await act(async () => {
      await result.current.cancelChild("team-team-1-alice");
    });
    expect(cancelHarnessChild).toHaveBeenCalledWith("s1", "team-team-1-alice");
    const lane = () => result.current.fleet.teams[0]?.lanes[0];
    expect(lane()).toMatchObject({
      childId: "team-team-1-alice",
      cancelling: true,
    });
    emit({
      type: "team_end",
      teamId: "team-1",
      parentCallId: "call-team",
      rounds: 1,
      stop: "end_turn",
      tasks: [],
      findings: [],
      dispositions: [
        { name: "alice", stopped: true, errorRounds: 0, reason: "cancelled" },
      ],
    });
    expect(lane()).toMatchObject({ stopped: true, stopReason: "cancelled" });
    expect(lane()?.cancelling).toBeUndefined();
  });

  it("does nothing without a daemon session to address", async () => {
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.cancelChild("subagent-1");
    });
    expect(cancelHarnessChild).not.toHaveBeenCalled();
  });
});
