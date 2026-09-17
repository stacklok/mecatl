import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import {
  MCP_INVENTORY_UNSUPPORTED_TEXT,
  useMcpInventory,
} from "./use-mcp-inventory";

/**
 * The MCP inventory hook behind the `/mcp` panel analogue: sources and
 * ToolHive groups load together on connect; a groups failure degrades to
 * `groupsError` without hiding the sources (the TUI's best-effort groups);
 * `refresh()` re-reads and flips `refreshed` so the footer can say
 * "updated"; the daemon's no-provider refusal and a missing route read as
 * plain notices; and a disabled hook (no `mcp` capability) never calls the
 * daemon.
 */

const { api, runtime } = vi.hoisted(() => ({
  api: {
    listHarnessMcpSources: vi.fn(),
    listHarnessToolHiveGroups: vi.fn(),
  },
  runtime: { connected: true },
}));

vi.mock("@/lib/harness/mcp", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/harness/mcp")>();
  return { ...actual, ...api };
});

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

const staticSource = {
  name: "static",
  kind: "static",
  enabled: true,
  group: "",
  servers: [
    {
      name: "github",
      url: "http://127.0.0.1:1/gh",
      transport: "streamable-http",
      group: "",
    },
  ],
  diagnostics: [],
};

beforeEach(() => {
  runtime.connected = true;
  api.listHarnessMcpSources.mockReset();
  api.listHarnessToolHiveGroups.mockReset();
  api.listHarnessMcpSources.mockResolvedValue([staticSource]);
  api.listHarnessToolHiveGroups.mockResolvedValue(["default"]);
});

describe("useMcpInventory", () => {
  it("loads sources and groups together once connected", async () => {
    const { result } = renderHook(() => useMcpInventory());
    expect(result.current.isLoading).toBe(true);
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.sources).toEqual([staticSource]);
    expect(result.current.groups).toEqual(["default"]);
    expect(result.current.groupsLoaded).toBe(true);
    expect(result.current.groupsError).toBe(false);
    expect(result.current.error).toBeNull();
    expect(result.current.refreshed).toBe(false);
    expect(api.listHarnessMcpSources).toHaveBeenCalledTimes(1);
    expect(api.listHarnessToolHiveGroups).toHaveBeenCalledTimes(1);
  });

  it("keeps the sources when only the groups read fails", async () => {
    api.listHarnessToolHiveGroups.mockRejectedValue(
      new HarnessApiError(500, "internal", "toolhive down"),
    );
    const { result } = renderHook(() => useMcpInventory());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.sources).toEqual([staticSource]);
    expect(result.current.groups).toEqual([]);
    expect(result.current.groupsError).toBe(true);
    expect(result.current.groupsLoaded).toBe(true);
    expect(result.current.error).toBeNull();
  });

  it("flips refreshed after a manual refresh lands, and marks refreshing meanwhile", async () => {
    const { result } = renderHook(() => useMcpInventory());
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    let release: (value: (typeof staticSource)[]) => void = () => {};
    api.listHarnessMcpSources.mockImplementationOnce(
      () =>
        new Promise<(typeof staticSource)[]>((resolve) => {
          release = resolve;
        }),
    );
    let refreshDone: Promise<void> = Promise.resolve();
    act(() => {
      refreshDone = result.current.refresh();
    });
    await waitFor(() => expect(result.current.refreshing).toBe(true));
    // Already-shown sources stay on screen while the re-read is in flight.
    expect(result.current.sources).toEqual([staticSource]);
    expect(result.current.refreshed).toBe(false);

    const later = { ...staticSource, name: "static (later)" };
    await act(async () => {
      release([later]);
      await refreshDone;
    });
    expect(result.current.refreshing).toBe(false);
    expect(result.current.refreshed).toBe(true);
    expect(result.current.sources).toEqual([later]);
    expect(api.listHarnessMcpSources).toHaveBeenCalledTimes(2);
  });

  it("does not mark the inventory refreshed when the refresh itself fails", async () => {
    const { result } = renderHook(() => useMcpInventory());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    api.listHarnessMcpSources.mockRejectedValueOnce(
      new HarnessApiError(500, "internal", "boom"),
    );
    await act(async () => {
      await result.current.refresh();
    });
    expect(result.current.refreshed).toBe(false);
    expect(result.current.error).toBe("boom");
  });

  it("words the daemon's no-provider refusal and a missing route as notices", async () => {
    api.listHarnessMcpSources.mockRejectedValue(
      new HarnessApiError(412, "no_mcp_provider", "No MCP provider configured"),
    );
    const { result } = renderHook(() => useMcpInventory());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.error).toBe(
      "MCP tools aren't set up for this agent.",
    );
    expect(result.current.sources).toEqual([]);

    api.listHarnessMcpSources.mockRejectedValue(
      new HarnessApiError(404, "not_found", "no route"),
    );
    const older = renderHook(() => useMcpInventory());
    await waitFor(() => expect(older.result.current.isLoading).toBe(false));
    expect(older.result.current.error).toBe(MCP_INVENTORY_UNSUPPORTED_TEXT);
  });

  it("stays idle while disabled or disconnected, and never calls the daemon", async () => {
    const disabled = renderHook(() => useMcpInventory({ enabled: false }));
    await act(async () => {
      await disabled.result.current.refresh();
    });
    expect(api.listHarnessMcpSources).not.toHaveBeenCalled();
    expect(api.listHarnessToolHiveGroups).not.toHaveBeenCalled();

    runtime.connected = false;
    renderHook(() => useMcpInventory());
    await Promise.resolve();
    expect(api.listHarnessMcpSources).not.toHaveBeenCalled();
  });
});
