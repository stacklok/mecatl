import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EMPTY_DAEMON_DEFAULTS } from "@/lib/harness/daemon-defaults";
import { useDaemonDefaults } from "./use-daemon-defaults";

/**
 * The daemon-defaults hook: loads the controller's document once connected
 * (managed mode ONLY — external mode never fetches, the controller would
 * answer 409), saves through the PUT and re-reads, and surfaces a refused
 * save (mecated's fail-fast --default-model check, rolled back by the
 * controller) as `error` while re-reading the document the daemon is
 * actually running on.
 */

const { fetchHarnessDaemonDefaults, saveHarnessDaemonDefaults, runtime } =
  vi.hoisted(() => ({
    fetchHarnessDaemonDefaults: vi.fn(),
    saveHarnessDaemonDefaults: vi.fn(),
    runtime: { connected: true, mode: "managed" as "managed" | "external" },
  }));

vi.mock("@/lib/harness/client", () => ({
  fetchHarnessDaemonDefaults,
  saveHarnessDaemonDefaults,
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

const saved = {
  ...EMPTY_DAEMON_DEFAULTS,
  reasoningEffort: "high",
  activeProvider: "openrouter",
};

beforeEach(() => {
  runtime.connected = true;
  runtime.mode = "managed";
  fetchHarnessDaemonDefaults.mockReset();
  saveHarnessDaemonDefaults.mockReset();
  fetchHarnessDaemonDefaults.mockResolvedValue(saved);
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("useDaemonDefaults", () => {
  it("loads the saved document once connected in managed mode", async () => {
    const { result } = renderHook(() => useDaemonDefaults());
    expect(result.current.manageable).toBe(true);
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.defaults).toEqual(saved);
    expect(fetchHarnessDaemonDefaults).toHaveBeenCalledTimes(1);
    expect(result.current.error).toBeNull();
  });

  it("never fetches in external mode and reports it as not manageable", async () => {
    runtime.mode = "external";
    const { result } = renderHook(() => useDaemonDefaults());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.manageable).toBe(false);
    expect(result.current.defaults).toBeNull();
    expect(fetchHarnessDaemonDefaults).not.toHaveBeenCalled();
  });

  it("waits for the connection before loading", async () => {
    runtime.connected = false;
    const { result } = renderHook(() => useDaemonDefaults());
    expect(result.current.live).toBe(false);
    expect(fetchHarnessDaemonDefaults).not.toHaveBeenCalled();
  });

  it("saves the draft, adopts the controller's echo and posts the restart notice", async () => {
    const next = { ...saved, reasoningEffort: "low" };
    saveHarnessDaemonDefaults.mockResolvedValue(next);
    const { result } = renderHook(() => useDaemonDefaults());
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    const { activeProvider: _dropped, ...draft } = next;
    let ok = false;
    await act(async () => {
      ok = await result.current.save(draft);
    });
    expect(ok).toBe(true);
    expect(saveHarnessDaemonDefaults).toHaveBeenCalledWith(draft);
    expect(result.current.defaults).toEqual(next);
    expect(result.current.notice).toBe("Saved. The agent restarted.");
    expect(result.current.busy).toBe(false);
    // The echo stood in for a re-read: one fetch, the initial load.
    expect(fetchHarnessDaemonDefaults).toHaveBeenCalledTimes(1);
  });

  it("surfaces a refused save as the controller's message and re-reads the live document", async () => {
    saveHarnessDaemonDefaults.mockRejectedValue(
      new Error(
        '--default-model "nope": not catalogued for the default provider "openrouter"',
      ),
    );
    const { result } = renderHook(() => useDaemonDefaults());
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    const { activeProvider: _dropped, ...draft } = saved;
    let ok = true;
    await act(async () => {
      ok = await result.current.save({ ...draft, reasoningEffort: "max" });
    });
    expect(ok).toBe(false);
    expect(result.current.error).toMatch(/not catalogued/);
    expect(result.current.notice).toBeNull();
    // Rolled back server-side: the re-read shows the document that stood.
    expect(fetchHarnessDaemonDefaults).toHaveBeenCalledTimes(2);
    expect(result.current.defaults).toEqual(saved);
  });

  it("reports a failed load as error with no document", async () => {
    fetchHarnessDaemonDefaults.mockRejectedValue(new Error("controller down"));
    const { result } = renderHook(() => useDaemonDefaults());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.defaults).toBeNull();
    expect(result.current.error).toBe("controller down");
  });
});
