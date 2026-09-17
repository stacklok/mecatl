import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { StorageHealth } from "@/lib/harness/storage";
import { useStorageHealth } from "./use-storage-health";

/**
 * The storage-health hook: reads on mount behind the capability gate,
 * stays quiet (null, not degraded) when the probe cannot run, and re-reads
 * on `refresh()` so the Storage page can show the policy a restarted daemon
 * applies.
 */

const runtimeStatus = {
  connected: true,
  serverCapabilities: { storage_health: true } as Record<string, unknown>,
};

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => runtimeStatus,
}));

const fetchStorageHealth =
  vi.fn<(signal?: AbortSignal) => Promise<StorageHealth>>();

vi.mock("@/lib/harness/storage", async () => {
  const actual = await vi.importActual<typeof import("@/lib/harness/storage")>(
    "@/lib/harness/storage",
  );
  return {
    ...actual,
    fetchStorageHealth: (signal?: AbortSignal) => fetchStorageHealth(signal),
  };
});

const healthy: StorageHealth = {
  available: true,
  unavailableReason: "",
  sessionCount: 1,
  corruptCount: 0,
  v1Count: 0,
  v2Count: 1,
  mainCount: 1,
  childCount: 0,
  scheduledCount: 0,
  unknownCount: 0,
  fileCount: 2,
  currentBytes: null,
  reclaimableBytes: null,
  policy: null,
  lastSweepAt: null,
  nextSweepAt: null,
  lastFailure: "",
  activeJob: "",
};

beforeEach(() => {
  fetchStorageHealth.mockReset();
  runtimeStatus.connected = true;
  runtimeStatus.serverCapabilities = { storage_health: true };
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("useStorageHealth", () => {
  it("reads once on mount and classifies the answer", async () => {
    fetchStorageHealth.mockResolvedValue({ ...healthy, corruptCount: 2 });
    const { result } = renderHook(() => useStorageHealth());
    expect(result.current.supported).toBe(true);
    await waitFor(() => expect(result.current.health).not.toBeNull());
    expect(result.current.degraded).toBe(true);
    expect(fetchStorageHealth).toHaveBeenCalledTimes(1);
  });

  it("re-reads on refresh() and adopts the newer answer", async () => {
    fetchStorageHealth.mockResolvedValueOnce(healthy);
    const { result } = renderHook(() => useStorageHealth());
    await waitFor(() => expect(result.current.health?.mainCount).toBe(1));

    fetchStorageHealth.mockResolvedValueOnce({ ...healthy, mainCount: 7 });
    act(() => result.current.refresh());
    await waitFor(() => expect(result.current.health?.mainCount).toBe(7));
    expect(fetchStorageHealth).toHaveBeenCalledTimes(2);
  });

  it("stays quiet — null, not degraded — when the probe fails", async () => {
    fetchStorageHealth.mockRejectedValue(new Error("management_unauthorized"));
    const { result } = renderHook(() => useStorageHealth());
    await waitFor(() => expect(fetchStorageHealth).toHaveBeenCalledTimes(1));
    expect(result.current.health).toBeNull();
    expect(result.current.degraded).toBe(false);
  });

  it("does not probe, and refresh() is a no-op, when the daemon lacks the capability", () => {
    runtimeStatus.serverCapabilities = {};
    const { result } = renderHook(() => useStorageHealth());
    expect(result.current.supported).toBe(false);
    act(() => result.current.refresh());
    expect(fetchStorageHealth).not.toHaveBeenCalled();
    expect(result.current.health).toBeNull();
  });
});
