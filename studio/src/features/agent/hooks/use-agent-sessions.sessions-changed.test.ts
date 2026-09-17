import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { notifySessionsChanged } from "../sessions-changed";
import { useAgentSessions } from "./use-agent-sessions";

/**
 * The sidebar's sessions hook re-walks the inventory on the sessions-changed
 * signal (a bulk clean-up deleted rows) instead of waiting for its 20-second
 * poll, and stops listening once unmounted.
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

beforeEach(() => {
  mocks.fetchAllSessions.mockReset();
  mocks.fetchAllSessions.mockResolvedValue({ sessions: [], complete: true });
});

describe("useAgentSessions × sessions-changed", () => {
  it("re-walks the inventory when the signal fires, and not after unmount", async () => {
    const { unmount } = renderHook(() => useAgentSessions());
    await waitFor(() =>
      expect(mocks.fetchAllSessions).toHaveBeenCalledTimes(1),
    );

    act(() => notifySessionsChanged());
    await waitFor(() =>
      expect(mocks.fetchAllSessions).toHaveBeenCalledTimes(2),
    );

    unmount();
    act(() => notifySessionsChanged());
    // Give a stray listener the chance to fire before asserting it did not.
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(mocks.fetchAllSessions).toHaveBeenCalledTimes(2);
  });
});
