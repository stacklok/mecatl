import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import type { SessionTranscript } from "@/lib/protocol";
import { recallSessionProfile } from "@/lib/session-profile-memory";
import { memoryStorage } from "@/test/memory-storage";
import type { StreamEvent } from "../types";
import { useAgentChat } from "./use-agent-chat";

/**
 * The draft's TOOL PROFILE rides the first send's session mint exactly as
 * the mode and effort picks do: `createProfile` is read at mint time, a
 * "no-fs" pick reaches `createHarnessSession` as `profile`, the default
 * ("") sends NO profile key, and the choice is remembered browser-locally
 * against the minted id (the daemon never reports it back). A daemon that
 * refuses the profile (a pre-ADR-0291 daemon 400s the unknown field) has its
 * words shown verbatim on the draft. The daemon client is mocked at the
 * module boundary.
 */

const mocks = vi.hoisted(() => ({
  createHarnessSession: vi.fn(async () => "s-minted"),
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

const cleanResult: StreamEvent = {
  type: "run_result",
  stop: "end_turn",
  text: "done",
  errorText: "",
  permanent: false,
};

describe("useAgentChat tool profile at mint", () => {
  beforeEach(() => {
    // A real Storage per test for the profile memory (the global afterEach
    // unstubs it, so nothing leaks between tests).
    vi.stubGlobal("localStorage", memoryStorage());
    mocks.fetchSessionTranscriptMessages.mockResolvedValue({
      sessionId: "s-minted",
      complete: true,
      messages: [],
    } satisfies SessionTranscript);
    mocks.streamHarnessPrompt.mockImplementation(
      async (...args: PromptArgs) => {
        const [, , , onEvent, , hooks] = args;
        hooks?.onRunStarted?.("run-1");
        onEvent(cleanResult);
      },
    );
  });

  afterEach(() => {
    vi.clearAllMocks();
    mocks.createHarnessSession.mockImplementation(async () => "s-minted");
  });

  it('a "no-fs" draft mints with profile: "no-fs" and remembers it against the new id', async () => {
    const { result } = renderHook(() =>
      useAgentChat(null, { createProfile: () => "no-fs" }),
    );
    await act(async () => {
      await result.current.sendMessage("hello");
    });
    expect(mocks.createHarnessSession).toHaveBeenCalledTimes(1);
    expect(mocks.createHarnessSession).toHaveBeenCalledWith(
      "default",
      expect.objectContaining({ profile: "no-fs" }),
    );
    expect(recallSessionProfile("s-minted")).toBe("no-fs");
    await waitFor(() => expect(result.current.status).toBe("idle"));
  });

  it("the default profile sends no profile key and is remembered as the known default", async () => {
    const { result } = renderHook(() =>
      useAgentChat(null, { createProfile: () => "" }),
    );
    await act(async () => {
      await result.current.sendMessage("hello");
    });
    const [, options] = mocks.createHarnessSession.mock.calls[0] as unknown as [
      string,
      Record<string, unknown>,
    ];
    expect(Object.hasOwn(options, "profile")).toBe(false);
    // Known (""), distinct from a chat Studio never minted (null).
    expect(recallSessionProfile("s-minted")).toBe("");
  });

  it("no createProfile getter at all behaves as the default (legacy callers)", async () => {
    const { result } = renderHook(() => useAgentChat(null));
    await act(async () => {
      await result.current.sendMessage("hello");
    });
    const [, options] = mocks.createHarnessSession.mock.calls[0] as unknown as [
      string,
      Record<string, unknown>,
    ];
    expect(Object.hasOwn(options, "profile")).toBe(false);
  });

  it("shows the daemon's refusal of the profile verbatim and remembers nothing", async () => {
    mocks.createHarnessSession.mockRejectedValueOnce(
      new HarnessApiError(
        400,
        "invalid_argument",
        'unknown field "profile" in CreateSessionRequest',
      ),
    );
    const { result } = renderHook(() =>
      useAgentChat(null, { createProfile: () => "no-fs" }),
    );
    await act(async () => {
      await result.current.sendMessage("hello");
    });
    await waitFor(() => expect(result.current.status).toBe("error"));
    expect(result.current.error).toBe(
      'unknown field "profile" in CreateSessionRequest',
    );
    expect(mocks.streamHarnessPrompt).not.toHaveBeenCalled();
    expect(recallSessionProfile("s-minted")).toBeNull();
  });
});
