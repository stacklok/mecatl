import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type { SessionTranscript } from "@/lib/protocol";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * A send or retry that fails because Studio's credential was refused (a
 * 401, or the proxy's oidc_* refusal) is named by its cause class on the
 * error strip — the same title + remedy the auth-recovery banner shows — and
 * re-probes the runtime at once, so the banner appears without waiting for
 * the 5-second poll. Every other failure keeps its own wording and never
 * touches the runtime.
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
  refreshRuntime: vi.fn(async () => undefined),
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
    features: new Set(["http_steer"]),
    serverCapabilities: { image: true },
    refresh: mocks.refreshRuntime,
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

const failedResult: StreamEvent = {
  type: "run_result",
  stop: "error",
  text: "",
  errorText: "upstream 503",
  permanent: false,
};

const promptStream =
  (events: StreamEvent[]) =>
  async (...args: PromptArgs) => {
    const [, , , onEvent, , hooks] = args;
    hooks?.onRunStarted?.("run-prompt");
    for (const event of events) onEvent(event);
  };

describe("useAgentChat on a refused credential", () => {
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

  it("names an expired OIDC session on the strip and re-probes the runtime at once", async () => {
    mocks.streamHarnessPrompt.mockRejectedValue(
      new HarnessApiError(
        401,
        "oidc_session_expired",
        "The OIDC session expired — sign in again from Settings to keep using this deployment.",
      ),
    );
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.error).toMatch(/^Sign-in expired — /);
    expect(result.current.error).toMatch(/Sign in again to reconnect/);
    expect(mocks.refreshRuntime).toHaveBeenCalledTimes(1);
    // The failed exchange is still a failed bubble with Retry available.
    expect(result.current.messages.at(-1)?.failed).toBe(true);
  });

  it("names a daemon 401 as a rejected credential with the env remediation", async () => {
    mocks.streamHarnessPrompt.mockRejectedValue(
      new HarnessApiError(
        401,
        "unauthenticated",
        "missing or invalid bearer token",
      ),
    );
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.error).toMatch(/^Credential rejected — /);
    expect(result.current.error).toMatch(/check its token or sign-in settings/);
    expect(mocks.refreshRuntime).toHaveBeenCalledTimes(1);
  });

  it("leaves every other failure's wording alone and never re-probes", async () => {
    mocks.streamHarnessPrompt.mockRejectedValue(
      new HarnessApiError(409, "session_leased_elsewhere", "leased elsewhere"),
    );
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.error).toBe("leased elsewhere");
    expect(mocks.refreshRuntime).not.toHaveBeenCalled();
  });

  it("a Retry whose start is refused with a 401 is named the same way", async () => {
    mocks.streamHarnessPrompt.mockImplementation(promptStream([failedResult]));
    mocks.retryHarnessRun.mockRejectedValue(
      new HarnessApiError(
        401,
        "oidc_login_required",
        "This deployment requires OIDC sign-in.",
      ),
    );
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("go");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.error).toBe("upstream 503");
    expect(mocks.refreshRuntime).not.toHaveBeenCalled();

    await act(async () => {
      await result.current.retryLast();
    });
    await waitFor(() =>
      expect(result.current.error).toMatch(/^Sign-in required — /),
    );
    expect(result.current.status).toBe("error");
    expect(mocks.retryHarnessRun).toHaveBeenCalledTimes(1);
    expect(mocks.refreshRuntime).toHaveBeenCalledTimes(1);
  });
});
