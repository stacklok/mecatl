import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionWalkProgress } from "@/lib/harness/sessions";
import type { SessionSummary } from "@/lib/protocol";
import { useAgentSessions } from "./use-agent-sessions";

/**
 * The sidebar's inventory walk as the user sees it: the first load is the
 * VISIBLE walk (progress per page, rows shown from the first page on),
 * Cancel stops it and keeps the rows already merged, a failed page surfaces
 * its message with Retry (which re-walks visibly and clears the error), a
 * background refresh never reads as in flight, and a page-bounded walk is
 * reported as incomplete so the UI can say the inventory is larger.
 */

const mocks = vi.hoisted(() => ({
  fetchAllSessions: vi.fn(),
}));

vi.mock("@/lib/harness/client", () => ({
  fetchAllSessions: mocks.fetchAllSessions,
  createHarnessSession: vi.fn(),
  deleteHarnessSession: vi.fn(),
  renameHarnessSession: vi.fn(),
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => ({ connected: true }),
}));

interface WalkCall {
  signal: AbortSignal | undefined;
  maxPages: number;
  onProgress: ((progress: SessionWalkProgress) => void) | undefined;
  resolve: (value: { sessions: SessionSummary[]; complete: boolean }) => void;
  reject: (reason: unknown) => void;
}

/** Makes every `fetchAllSessions` call a deferred the test drives by hand. */
function controllableWalks(): WalkCall[] {
  const calls: WalkCall[] = [];
  mocks.fetchAllSessions.mockImplementation(
    (
      signal: AbortSignal | undefined,
      maxPages: number,
      onProgress: WalkCall["onProgress"],
    ) =>
      new Promise((resolve, reject) => {
        // A real walk rejects when its signal aborts (fetch does).
        signal?.addEventListener("abort", () =>
          reject(new DOMException("The operation was aborted.", "AbortError")),
        );
        calls.push({ signal, maxPages, onProgress, resolve, reject });
      }),
  );
  return calls;
}

const row = (id: string, isChat = true): SessionSummary => ({
  sessionId: id,
  title: `Chat ${id}`,
  titleProvenance: "",
  debugTargetSessionId: "",
  state: "idle",
  modelId: "m",
  turns: 1,
  modifiedAt: 0,
  createdAt: 0,
  isChat,
  canRename: true,
  canDelete: true,
  canViewTranscript: true,
  canCopyId: true,
  canFork: true,
  renameReason: "",
  deleteReason: "",
  copyIdReason: "",
  forkReason: "",
  kind: isChat ? "main" : "subagent",
  activityState: "",
  relationship: {
    parentSessionId: isChat ? "" : "a",
    callId: "",
    branchIndex: null,
    scheduleName: "",
    originSessionId: "",
    teamId: "",
    memberName: "",
  },
  placement: { kind: "local", label: "studio", branch: "main", revision: "" },
  ownerName: "",
  titleRevision: null,
  canInspect: !isChat,
  publicChatReason: isChat ? "" : "inspect_only_kind",
  viewTranscriptReason: "",
});

const ids = (sessions: { id: string }[]) => sessions.map((s) => s.id);

beforeEach(() => {
  mocks.fetchAllSessions.mockReset();
});

describe("useAgentSessions inventory walk", () => {
  it("shows the first load's progress per page, rows from the first page on, and settles on completion", async () => {
    const calls = controllableWalks();
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(calls).toHaveLength(1));

    expect(result.current.walk).toMatchObject({
      inFlight: true,
      pages: 0,
      rows: 0,
      complete: null,
    });
    expect(result.current.isLoading).toBe(true);
    expect(calls[0].maxPages).toBe(25);

    // Page one: two rows, one of them an inspect-only kind the chat list
    // omits — it lands in `runs` instead of being dropped.
    act(() => {
      calls[0].onProgress?.({
        pages: 1,
        rows: 2,
        page: [row("a"), row("child", false)],
      });
    });
    expect(result.current.walk).toMatchObject({
      inFlight: true,
      pages: 1,
      rows: 1,
    });
    expect(ids(result.current.sessions)).toEqual(["a"]);
    expect(ids(result.current.runs)).toEqual(["child"]);
    expect(result.current.isLoading).toBe(false);

    await act(async () => {
      calls[0].resolve({
        sessions: [row("a"), row("child", false), row("b")],
        complete: true,
      });
    });
    await waitFor(() =>
      expect(result.current.walk).toMatchObject({
        inFlight: false,
        pages: 1,
        rows: 2,
        complete: true,
        cancelled: false,
      }),
    );
    expect(ids(result.current.sessions)).toEqual(["a", "b"]);
    expect(result.current.error).toBeNull();
  });

  it("Cancel aborts the visible walk and keeps the rows already merged", async () => {
    const calls = controllableWalks();
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(calls).toHaveLength(1));

    act(() => {
      calls[0].onProgress?.({ pages: 1, rows: 1, page: [row("a")] });
    });
    await act(async () => {
      result.current.cancelLoad();
    });

    expect(calls[0].signal?.aborted).toBe(true);
    await waitFor(() =>
      expect(result.current.walk).toMatchObject({
        inFlight: false,
        pages: 1,
        rows: 1,
        complete: false,
        cancelled: true,
      }),
    );
    expect(ids(result.current.sessions)).toEqual(["a"]);
    expect(result.current.error).toBeNull();
    expect(result.current.isLoading).toBe(false);
  });

  it("surfaces a failed walk's message; Retry re-walks visibly and clears it", async () => {
    const calls = controllableWalks();
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(calls).toHaveLength(1));

    await act(async () => {
      calls[0].reject(new Error("store unavailable"));
    });
    await waitFor(() => expect(result.current.error).toBe("store unavailable"));
    expect(result.current.walk.inFlight).toBe(false);

    act(() => {
      void result.current.retry();
    });
    expect(result.current.error).toBeNull();
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(result.current.walk.inFlight).toBe(true);

    await act(async () => {
      calls[1].resolve({ sessions: [row("a")], complete: true });
    });
    await waitFor(() => expect(result.current.walk.inFlight).toBe(false));
    expect(result.current.error).toBeNull();
    expect(ids(result.current.sessions)).toEqual(["a"]);
  });

  it("a background refresh never reads as in flight, but a bounded outcome still marks the walk incomplete", async () => {
    const calls = controllableWalks();
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(calls).toHaveLength(1));
    await act(async () => {
      calls[0].resolve({ sessions: [row("a")], complete: true });
    });
    await waitFor(() => expect(result.current.walk.complete).toBe(true));

    act(() => {
      void result.current.refreshSessions();
    });
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(result.current.walk.inFlight).toBe(false);

    await act(async () => {
      calls[1].resolve({ sessions: [row("a"), row("b")], complete: false });
    });
    await waitFor(() =>
      expect(result.current.walk).toMatchObject({
        inFlight: false,
        rows: 2,
        complete: false,
        cancelled: false,
      }),
    );
    // A bounded walk merges; it never drops a row it did not see.
    expect(ids(result.current.sessions)).toEqual(["a", "b"]);
  });

  it("lists every inspect-only row in `runs` with its kind, relationship and placement, never in `sessions`", async () => {
    const calls = controllableWalks();
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(calls).toHaveLength(1));

    const scheduled: SessionSummary = {
      ...row("fire-1", false),
      title: "",
      kind: "scheduled",
      relationship: {
        ...row("fire-1", false).relationship,
        parentSessionId: "",
        scheduleName: "nightly",
      },
      canViewTranscript: false,
      viewTranscriptReason: "transcript_unavailable",
    };
    await act(async () => {
      calls[0].resolve({
        sessions: [row("a"), row("child", false), scheduled],
        complete: true,
      });
    });
    await waitFor(() => expect(result.current.walk.complete).toBe(true));

    expect(ids(result.current.sessions)).toEqual(["a"]);
    expect(ids(result.current.runs)).toEqual(["child", "fire-1"]);
    expect(result.current.runs[0]).toMatchObject({
      kind: "subagent",
      isChat: false,
      canInspect: true,
      publicChatReason: "inspect_only_kind",
      placementLabel: "studio",
      placementBranch: "main",
      relationship: { parentSessionId: "a" },
    });
    // A run keeps an empty title (its row names the relationship instead of
    // a chat's placeholder) and carries the transcript refusal reason.
    expect(result.current.runs[1]).toMatchObject({
      title: "",
      kind: "scheduled",
      canViewTranscript: false,
      viewTranscriptReason: "transcript_unavailable",
      relationship: { scheduleName: "nightly" },
    });
    expect(result.current.sessions[0]).toMatchObject({
      kind: "main",
      isChat: true,
      canViewTranscript: true,
    });
  });

  it("merges runs on a partial walk and removes them only on a complete one, like chats", async () => {
    const calls = controllableWalks();
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(calls).toHaveLength(1));
    await act(async () => {
      calls[0].resolve({
        sessions: [row("a"), row("r1", false), row("r2", false)],
        complete: true,
      });
    });
    await waitFor(() => expect(ids(result.current.runs)).toEqual(["r1", "r2"]));

    // A bounded refresh that saw only r1 keeps r2.
    act(() => {
      void result.current.refreshSessions();
    });
    await waitFor(() => expect(calls).toHaveLength(2));
    await act(async () => {
      calls[1].resolve({
        sessions: [row("a"), row("r1", false)],
        complete: false,
      });
    });
    await waitFor(() => expect(result.current.walk.complete).toBe(false));
    expect(ids(result.current.runs)).toEqual(["r1", "r2"]);

    // A complete refresh that saw only r1 drops r2.
    act(() => {
      void result.current.refreshSessions();
    });
    await waitFor(() => expect(calls).toHaveLength(3));
    await act(async () => {
      calls[2].resolve({
        sessions: [row("a"), row("r1", false)],
        complete: true,
      });
    });
    await waitFor(() => expect(ids(result.current.runs)).toEqual(["r1"]));
    expect(ids(result.current.sessions)).toEqual(["a"]);
  });

  it("unmount aborts the in-flight walk and reports nothing", async () => {
    const calls = controllableWalks();
    const { result, unmount } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(calls).toHaveLength(1));
    const before = result.current;

    unmount();

    expect(calls[0].signal?.aborted).toBe(true);
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(before.error).toBeNull();
  });
});
