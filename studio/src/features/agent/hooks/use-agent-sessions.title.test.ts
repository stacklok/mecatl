import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SessionSummary } from "@/lib/protocol";
import { useAgentSessions } from "./use-agent-sessions";

/**
 * `applyTitle`: the sessions hook adopts a live `session.title` event onto
 * the inventory row (header, sidebar and tab title all read that row), keeps
 * the row's title lifecycle revision so a replayed older title never
 * regresses it, and carries the daemon's revision through from the inventory
 * decode in the first place.
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

const row = (
  id: string,
  over: Partial<SessionSummary> = {},
): SessionSummary => ({
  sessionId: id,
  title: `Chat ${id}`,
  titleProvenance: "first-prompt",
  debugTargetSessionId: "",
  state: "idle",
  modelId: "m",
  turns: 1,
  modifiedAt: 0,
  createdAt: 0,
  isChat: true,
  canRename: true,
  canDelete: true,
  canViewTranscript: true,
  canCopyId: true,
  canFork: true,
  renameReason: "",
  deleteReason: "",
  copyIdReason: "",
  forkReason: "",
  kind: "main",
  activityState: "",
  relationship: {
    parentSessionId: "",
    callId: "",
    branchIndex: null,
    scheduleName: "",
    originSessionId: "",
    teamId: "",
    memberName: "",
  },
  placement: { kind: "local", label: "studio", branch: "main", revision: "" },
  ownerName: "",
  titleRevision: 2,
  canInspect: false,
  publicChatReason: "",
  viewTranscriptReason: "",
  ...over,
});

beforeEach(() => {
  mocks.fetchAllSessions.mockReset();
  mocks.fetchAllSessions.mockResolvedValue({
    sessions: [row("a"), row("b", { titleRevision: null })],
    complete: true,
  });
});

describe("useAgentSessions applyTitle", () => {
  it("carries the inventory's title revision onto the row", async () => {
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(result.current.sessions).toHaveLength(2));
    expect(result.current.sessions[0]).toMatchObject({
      id: "a",
      title: "Chat a",
      titleProvenance: "first-prompt",
      titleRevision: 2,
    });
    expect(result.current.sessions[1].titleRevision).toBeNull();
  });

  it("adopts a newer revision's title live and ignores an older one", async () => {
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(result.current.sessions).toHaveLength(2));

    act(() => {
      result.current.applyTitle("a", {
        title: "Fix the scheduler flake",
        provenance: "first-prompt",
        revision: 3,
      });
    });
    expect(result.current.sessions[0]).toMatchObject({
      title: "Fix the scheduler flake",
      titleProvenance: "first-prompt",
      titleRevision: 3,
    });

    // A replayed older title (revision 1 < 3) must not regress the row.
    act(() => {
      result.current.applyTitle("a", {
        title: "Untitled chat",
        provenance: "",
        revision: 1,
      });
    });
    expect(result.current.sessions[0].title).toBe("Fix the scheduler flake");
    expect(result.current.sessions[0].titleRevision).toBe(3);
  });

  it("adopts onto a row with no revision and with an event carrying none", async () => {
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(result.current.sessions).toHaveLength(2));

    act(() => {
      result.current.applyTitle("b", {
        title: "Renamed from the TUI",
        provenance: "operator",
        revision: 5,
      });
    });
    expect(result.current.sessions[1]).toMatchObject({
      title: "Renamed from the TUI",
      titleProvenance: "operator",
      titleRevision: 5,
    });

    act(() => {
      result.current.applyTitle("b", {
        title: "Renamed again",
        provenance: "operator",
        revision: null,
      });
    });
    expect(result.current.sessions[1]).toMatchObject({
      title: "Renamed again",
      titleRevision: 5,
    });
  });

  it("leaves the list untouched for an unknown id", async () => {
    const { result } = renderHook(() => useAgentSessions());
    await waitFor(() => expect(result.current.sessions).toHaveLength(2));
    const before = result.current.sessions;
    act(() => {
      result.current.applyTitle("zzz", {
        title: "x",
        provenance: "operator",
        revision: 9,
      });
    });
    expect(result.current.sessions).toBe(before);
  });
});
