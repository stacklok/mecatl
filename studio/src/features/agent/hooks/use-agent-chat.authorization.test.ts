import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  AUTHORIZATION_CHECK_IN_FLIGHT_NOTICE,
  AUTHORIZATION_LINK_COPIED_NOTICE,
  AUTHORIZATION_POLL_INTERVAL_MS,
  AUTHORIZATION_POPUP_BLOCKED_NOTICE,
} from "../mcp-authorization-phase";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * The mid-run MCP browser-authorization phase in the chat hook: a prompt
 * stream that ENDS on `authorization.required` parks the run behind the
 * takeover card, and once the sign-in page is opened (or its link copied)
 * the hook re-checks every 3 s — the TUI's poll — so a finished sign-in
 * resumes the run without a click. Pins the guards the polling relies on:
 * one control stream at a time (only one adopts the continuation run), the
 * loop stops when the request resolves from ANY stream or is cancelled, and
 * Cancel takes over from a poll in flight instead of being dropped. The
 * daemon client is mocked at the module boundary — the wire adapters have
 * their own tests (lib/harness/mcp-authorization.test.ts).
 */

const {
  cancelHarnessRun,
  cancelMcpAuthorization,
  createHarnessSession,
  fetchMcpAuthorizationUrl,
  recheckMcpAuthorization,
  streamHarnessPrompt,
} = vi.hoisted(() => ({
  cancelHarnessRun: vi.fn(async () => undefined),
  cancelMcpAuthorization: vi.fn(),
  createHarnessSession: vi.fn(async () => "s1"),
  fetchMcpAuthorizationUrl: vi.fn(async () => "https://idp.example/authorize"),
  recheckMcpAuthorization: vi.fn(),
  streamHarnessPrompt: vi.fn(),
}));

vi.mock("@/lib/harness/client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/client")>()),
  cancelHarnessRun,
  cancelMcpAuthorization,
  createHarnessSession,
  fetchMcpAuthorizationUrl,
  recheckMcpAuthorization,
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

type Control = (
  sessionId: string,
  authorizationId: string,
  onEvent: (event: StreamEvent) => void,
  signal?: AbortSignal,
) => Promise<{ status: string; sawResult: boolean }>;

/** The live stream's event sink — a watch's `authorization_resolved` lands
 *  through the same handler, so a test can inject one after the park. */
let emitStreamEvent: ((event: StreamEvent) => void) | null = null;

const required: StreamEvent = {
  type: "authorization",
  authorizationId: "auth-1",
  callId: "call-1",
  displayName: "GitHub MCP",
  status: "pending",
  runId: "run-1",
};

const resolved = (status: string, runId?: string): StreamEvent => ({
  type: "authorization_resolved",
  authorizationId: "auth-1",
  displayName: "GitHub MCP",
  status,
  ...(runId ? { runId } : {}),
});

/** A prompt stream the daemon closes on `authorization.required` — parked,
 *  not truncated: it RESOLVES without a result. */
function parkedStream(...args: StreamArgs): Promise<void> {
  const [, , , onEvent, , hooks] = args;
  emitStreamEvent = onEvent;
  hooks?.onRunStarted?.("run-1");
  onEvent({ type: "token", text: "Signing in… ", runId: "run-1" });
  onEvent(required);
  return Promise.resolve();
}

/** A recheck that finds the sign-in still incomplete: the lone status frame. */
const stillPending: Control = async (_sessionId, _authorizationId, onEvent) => {
  onEvent({ ...required, runId: undefined });
  return { status: "pending", sawResult: false };
};

/** A recheck that finds the sign-in done: the terminal status, then the
 *  continuation run through its result. */
const granted: Control = async (_sessionId, _authorizationId, onEvent) => {
  onEvent(resolved("granted", "run-2"));
  onEvent({ type: "token", text: "resumed.", runId: "run-2" });
  onEvent({
    type: "run_result",
    stop: "end_turn",
    text: "resumed.",
    errorText: "",
    permanent: false,
    runId: "run-2",
  });
  return { status: "granted", sawResult: true };
};

/** A recheck that hangs until its signal aborts (a slow or dropped stream). */
const hanging: Control = (_sessionId, _authorizationId, _onEvent, signal) =>
  new Promise((_, reject) => {
    signal?.addEventListener(
      "abort",
      () =>
        reject(new DOMException("The operation was aborted.", "AbortError")),
      { once: true },
    );
  });

const popup = () =>
  ({
    closed: false,
    opener: {} as unknown,
    location: { href: "" },
    close: vi.fn(),
  }) as unknown as Window;

const tick = (ms = AUTHORIZATION_POLL_INTERVAL_MS) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

describe("useAgentChat MCP authorization polling", () => {
  const writeText = vi.fn(async () => undefined);

  beforeEach(() => {
    streamHarnessPrompt.mockImplementation(parkedStream);
    recheckMcpAuthorization.mockImplementation(stillPending);
    cancelMcpAuthorization.mockImplementation((async (
      _sessionId,
      _authorizationId,
      onEvent,
    ) => {
      onEvent(resolved("cancelled"));
      return { status: "cancelled", sawResult: false };
    }) satisfies Control);
    fetchMcpAuthorizationUrl.mockResolvedValue("https://idp.example/authorize");
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    emitStreamEvent = null;
    delete (navigator as { clipboard?: unknown }).clipboard;
  });

  /** Sends a prompt on a fresh draft and waits for it to park on the sign-in. */
  async function park() {
    const rendered = renderHook(() => useAgentChat(null));
    await act(async () => {
      await rendered.result.current.sendMessage("use github");
    });
    await waitFor(() =>
      expect(rendered.result.current.pendingAuthorization).toMatchObject({
        authorizationId: "auth-1",
        sessionId: "s1",
        displayName: "GitHub MCP",
      }),
    );
    expect(rendered.result.current.status).toBe("waiting_authorization");
    expect(rendered.result.current.pendingAuthorization?.polling).toBeFalsy();
    return rendered.result;
  }

  it("re-checks every 3 s once the sign-in page is open, stays quiet while pending, and stops when the daemon resolves it", async () => {
    const result = await park();
    const window_ = popup();
    vi.spyOn(window, "open").mockReturnValue(window_);
    vi.useFakeTimers();

    await act(async () => {
      await result.current.openAuthorization();
    });
    expect(fetchMcpAuthorizationUrl).toHaveBeenCalledWith("s1", "auth-1");
    expect(window_.location.href).toBe("https://idp.example/authorize");
    expect(result.current.pendingAuthorization?.polling).toBe(true);
    expect(recheckMcpAuthorization).not.toHaveBeenCalled();

    await tick();
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);
    expect(recheckMcpAuthorization.mock.calls[0]?.slice(0, 2)).toEqual([
      "s1",
      "auth-1",
    ]);
    // A poll tick that finds nothing new leaves the card as it was: still
    // polling, no "not signed in yet" nag, still parked.
    expect(result.current.pendingAuthorization).toMatchObject({
      authorizationId: "auth-1",
      polling: true,
    });
    expect(result.current.pendingAuthorization?.notice).toBeUndefined();
    expect(result.current.status).toBe("waiting_authorization");

    await tick();
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(2);

    // The third tick finds the sign-in done: the control stream resolves the
    // request and carries the continuation run into the parked turn.
    recheckMcpAuthorization.mockImplementation(granted);
    await tick();
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(3);
    expect(result.current.pendingAuthorization).toBeNull();
    expect(result.current.status).toBe("idle");
    const assistant = result.current.messages.at(-1);
    expect(assistant?.role).toBe("assistant");
    expect(assistant?.content).toContain("resumed.");
    expect(assistant?.notices).toContain(
      "Browser authorization for GitHub MCP: signed in",
    );

    // The loop died with the request.
    await tick(3 * AUTHORIZATION_POLL_INTERVAL_MS);
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(3);
  });

  it("polls after Copy link too, never overlaps checks, and tells a clicker a check is already running", async () => {
    const result = await park();
    recheckMcpAuthorization.mockImplementation(hanging);
    vi.useFakeTimers();

    await act(async () => {
      await result.current.copyAuthorizationLink();
    });
    expect(writeText).toHaveBeenCalledWith("https://idp.example/authorize");
    expect(result.current.pendingAuthorization).toMatchObject({
      polling: true,
      notice: AUTHORIZATION_LINK_COPIED_NOTICE,
    });

    await tick();
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);
    // The first check is still in flight: later ticks are dropped, not
    // stacked — only one stream may adopt the continuation run.
    await tick(3 * AUTHORIZATION_POLL_INTERVAL_MS);
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);
    // The copied-link notice survives the silent ticks.
    expect(result.current.pendingAuthorization?.notice).toBe(
      AUTHORIZATION_LINK_COPIED_NOTICE,
    );

    // A manual re-check during the in-flight one is not silently ignored.
    await act(async () => {
      await result.current.recheckAuthorization();
    });
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);
    expect(result.current.pendingAuthorization?.notice).toBe(
      AUTHORIZATION_CHECK_IN_FLIGHT_NOTICE,
    );
  });

  it("Cancel stops the polling and takes over from a poll in flight instead of being dropped", async () => {
    const result = await park();
    vi.spyOn(window, "open").mockReturnValue(popup());
    recheckMcpAuthorization.mockImplementation(hanging);
    vi.useFakeTimers();

    await act(async () => {
      await result.current.openAuthorization();
    });
    await tick();
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);

    await act(async () => {
      await result.current.cancelAuthorization();
    });
    expect(cancelMcpAuthorization).toHaveBeenCalledTimes(1);
    expect(cancelMcpAuthorization.mock.calls[0]?.slice(0, 2)).toEqual([
      "s1",
      "auth-1",
    ]);
    expect(result.current.pendingAuthorization).toBeNull();
    expect(result.current.status).toBe("idle");
    expect(result.current.messages.at(-1)?.notices).toContain(
      "Browser authorization for GitHub MCP: cancelled",
    );
    // A cancelled run's own cancel control is never used for a parked run.
    expect(cancelHarnessRun).not.toHaveBeenCalled();

    await tick(3 * AUTHORIZATION_POLL_INTERVAL_MS);
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);
  });

  it("stops polling when the MAIN stream reports the authorization resolved", async () => {
    const result = await park();
    vi.spyOn(window, "open").mockReturnValue(popup());
    vi.useFakeTimers();

    await act(async () => {
      await result.current.openAuthorization();
    });
    await tick();
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);

    // Another client finished the sign-in; the durable watch relays the
    // daemon's terminal status through the same handler.
    act(() => {
      emitStreamEvent?.(resolved("granted"));
    });
    expect(result.current.pendingAuthorization).toBeNull();
    await tick(3 * AUTHORIZATION_POLL_INTERVAL_MS);
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);
  });

  it("does not poll after a blocked pop-up — the operator has not seen the page", async () => {
    const result = await park();
    vi.spyOn(window, "open").mockReturnValue(null);
    vi.useFakeTimers();

    await act(async () => {
      await result.current.openAuthorization();
    });
    expect(result.current.pendingAuthorization).toMatchObject({
      notice: AUTHORIZATION_POPUP_BLOCKED_NOTICE,
    });
    expect(result.current.pendingAuthorization?.polling).toBeFalsy();
    await tick(3 * AUTHORIZATION_POLL_INTERVAL_MS);
    expect(recheckMcpAuthorization).not.toHaveBeenCalled();
  });

  it("surfaces a failed poll on the card and keeps polling so a transient fault recovers", async () => {
    const result = await park();
    vi.spyOn(window, "open").mockReturnValue(popup());
    recheckMcpAuthorization.mockRejectedValueOnce(
      new Error(
        "Mecatl did not answer the authorization check within 10 seconds.",
      ),
    );
    vi.useFakeTimers();

    await act(async () => {
      await result.current.openAuthorization();
    });
    await tick();
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(1);
    expect(result.current.pendingAuthorization).toMatchObject({
      polling: true,
      error: "Mecatl did not answer the authorization check within 10 seconds.",
    });

    // The next tick runs (the guard was released) and clears the error.
    await tick();
    expect(recheckMcpAuthorization).toHaveBeenCalledTimes(2);
    expect(result.current.pendingAuthorization?.error).toBeUndefined();
  });
});
