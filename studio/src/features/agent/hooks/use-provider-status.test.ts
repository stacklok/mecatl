import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useProviderStatus } from "./use-provider-status";

/**
 * The daemon provider-status hook: reads `provider_status` off the models
 * list once connected (both modes — the rows are daemon-owned and
 * read-only), exposes a per-provider lookup for the inventory rows to
 * merge, folds a failed read into `error` with NO fabricated rows, and
 * re-reads on refresh.
 */

const { listHarnessModelInventory, runtime } = vi.hoisted(() => ({
  listHarnessModelInventory: vi.fn(),
  runtime: { connected: true, mode: "managed" as "managed" | "external" },
}));

vi.mock("@/lib/harness/client", () => ({ listHarnessModelInventory }));
vi.mock("../runtime-status", () => ({ useRuntimeStatus: () => runtime }));

const toolhive = {
  providerId: "toolhive",
  state: "unreachable",
  hint: "start it with `thv llm proxy start`",
  defaultModelAutoSelected: false,
  modelCount: 0,
  availableNotDefault: false,
};

beforeEach(() => {
  runtime.connected = true;
  runtime.mode = "managed";
  listHarnessModelInventory.mockReset();
  listHarnessModelInventory.mockResolvedValue({
    models: [],
    providerStatus: [toolhive],
  });
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("useProviderStatus", () => {
  it("loads the daemon's status rows once connected and looks them up by provider", async () => {
    const { result } = renderHook(() => useProviderStatus());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.rows).toEqual([toolhive]);
    expect(result.current.forProvider("toolhive")).toEqual(toolhive);
    expect(result.current.forProvider("openrouter")).toBeNull();
    expect(result.current.error).toBeNull();
  });

  it("reads in external mode too — the rows are the daemon's, not the controller's", async () => {
    runtime.mode = "external";
    const { result } = renderHook(() => useProviderStatus());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(listHarnessModelInventory).toHaveBeenCalledTimes(1);
    expect(result.current.rows).toEqual([toolhive]);
  });

  it("waits for the connection", () => {
    runtime.connected = false;
    const { result } = renderHook(() => useProviderStatus());
    expect(result.current.live).toBe(false);
    expect(listHarnessModelInventory).not.toHaveBeenCalled();
  });

  it("folds a failed read into error with no rows, and refresh re-reads", async () => {
    listHarnessModelInventory.mockRejectedValueOnce(new Error("draining"));
    const { result } = renderHook(() => useProviderStatus());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.rows).toEqual([]);
    expect(result.current.error).toBe("draining");

    await act(async () => {
      await result.current.refresh();
    });
    expect(listHarnessModelInventory).toHaveBeenCalledTimes(2);
    expect(result.current.rows).toEqual([toolhive]);
    expect(result.current.error).toBeNull();
  });
});
