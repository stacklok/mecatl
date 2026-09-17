import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import { useSessionMcpConnectors } from "./use-session-mcp-connectors";

/**
 * The read-only broker connector inventory hook behind the chat's MCP
 * panel: it reads the exact session's connectors once connected, re-reads
 * when the caller's `revision` (the enrollment phase) changes, folds an
 * "inspection unavailable" null through unchanged, surfaces other failures
 * as `error`, refreshes on demand, and never calls the daemon while
 * disabled, disconnected, or without a session.
 */

const { api, runtime } = vi.hoisted(() => ({
  api: { fetchSessionConnectors: vi.fn() },
  runtime: { connected: true },
}));

vi.mock("@/lib/harness/enrollment", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/harness/enrollment")>();
  return { ...actual, ...api };
});

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

const inventory = (enrollmentState: string) => ({
  availability: "available",
  enrollmentState,
  connectors: [{ name: "github", toolCount: 3, catalogueState: "discovered" }],
  totalConnectors: 1,
  truncated: false,
});

beforeEach(() => {
  runtime.connected = true;
  api.fetchSessionConnectors.mockReset();
  api.fetchSessionConnectors.mockResolvedValue(inventory("not_started"));
});

describe("useSessionMcpConnectors", () => {
  it("reads the session's connectors once connected", async () => {
    const { result } = renderHook(() => useSessionMcpConnectors("s1"));
    expect(result.current.isLoading).toBe(true);
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.inventory).toEqual(inventory("not_started"));
    expect(result.current.error).toBeNull();
    expect(api.fetchSessionConnectors).toHaveBeenCalledTimes(1);
    expect(api.fetchSessionConnectors.mock.calls[0][0]).toBe("s1");
  });

  it("re-reads when the revision changes (the enrollment settled)", async () => {
    const { result, rerender } = renderHook(
      ({ revision }: { revision: string }) =>
        useSessionMcpConnectors("s1", { revision }),
      { initialProps: { revision: "not_connected" } },
    );
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    api.fetchSessionConnectors.mockResolvedValue(inventory("completed"));
    rerender({ revision: "connected" });
    await waitFor(() =>
      expect(result.current.inventory?.enrollmentState).toBe("completed"),
    );
    expect(api.fetchSessionConnectors).toHaveBeenCalledTimes(2);
  });

  it("keeps a null inventory (inspection unavailable) distinct from an error", async () => {
    api.fetchSessionConnectors.mockResolvedValue(null);
    const { result } = renderHook(() => useSessionMcpConnectors("s1"));
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.inventory).toBeNull();
    expect(result.current.error).toBeNull();
  });

  it("surfaces a failed read as error", async () => {
    api.fetchSessionConnectors.mockRejectedValue(
      new HarnessApiError(500, "internal", "broker exploded"),
    );
    const { result } = renderHook(() => useSessionMcpConnectors("s1"));
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.error).toBe("broker exploded");
    expect(result.current.inventory).toBeNull();
  });

  it("refreshes on demand without dropping the shown inventory", async () => {
    const { result } = renderHook(() => useSessionMcpConnectors("s1"));
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    api.fetchSessionConnectors.mockResolvedValue(inventory("completed"));
    await act(async () => {
      await result.current.refresh();
    });
    expect(result.current.refreshing).toBe(false);
    expect(result.current.inventory?.enrollmentState).toBe("completed");
    expect(api.fetchSessionConnectors).toHaveBeenCalledTimes(2);
  });

  it("resets to loading on a session change so the old chat's rows never show", async () => {
    const { result, rerender } = renderHook(
      ({ id }: { id: string }) => useSessionMcpConnectors(id),
      { initialProps: { id: "s1" } },
    );
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    let release: (value: unknown) => void = () => {};
    api.fetchSessionConnectors.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          release = resolve;
        }),
    );
    rerender({ id: "s2" });
    expect(result.current.isLoading).toBe(true);
    expect(result.current.inventory).toBeNull();
    await act(async () => {
      release(inventory("completed"));
    });
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(api.fetchSessionConnectors.mock.calls[1][0]).toBe("s2");
  });

  it("never calls the daemon while disabled, disconnected, or without a session", async () => {
    renderHook(() => useSessionMcpConnectors("s1", { enabled: false }));
    renderHook(() => useSessionMcpConnectors(null));
    runtime.connected = false;
    const { result } = renderHook(() => useSessionMcpConnectors("s1"));
    await act(async () => {
      await result.current.refresh();
    });
    expect(api.fetchSessionConnectors).not.toHaveBeenCalled();
  });
});
