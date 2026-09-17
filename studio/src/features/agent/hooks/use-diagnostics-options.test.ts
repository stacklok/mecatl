import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useDiagnosticsOptions } from "./use-diagnostics-options";

/**
 * The diagnostics-options hook: loads the controller's document once
 * connected (managed mode ONLY — external mode never fetches, the controller
 * would answer 409), saves a partial patch then re-reads AND re-probes the
 * runtime (the restarted daemon may advertise differently), and surfaces a
 * refused save as `error` while re-reading the document the daemon is
 * actually running on.
 */

const {
  fetchHarnessDiagnosticsOptions,
  saveHarnessDiagnosticsOptions,
  runtime,
} = vi.hoisted(() => ({
  fetchHarnessDiagnosticsOptions: vi.fn(),
  saveHarnessDiagnosticsOptions: vi.fn(),
  runtime: {
    connected: true,
    mode: "managed" as "managed" | "external",
    refresh: vi.fn(async () => {}),
  },
}));

vi.mock("@/lib/harness/client", () => ({
  fetchHarnessDiagnosticsOptions,
  saveHarnessDiagnosticsOptions,
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

const saved = {
  options: {
    logLevel: "info",
    quiet: false,
    admin: { enabled: false, perfMcp: false, goroutineWarnThreshold: 0 },
    productMetrics: { enabled: true, dryRun: false },
  },
  productMetrics: { effective: true, source: "studio" as const },
};

beforeEach(() => {
  runtime.connected = true;
  runtime.mode = "managed";
  runtime.refresh.mockClear();
  fetchHarnessDiagnosticsOptions.mockReset();
  saveHarnessDiagnosticsOptions.mockReset();
  fetchHarnessDiagnosticsOptions.mockResolvedValue(saved);
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("useDiagnosticsOptions", () => {
  it("loads the saved document once connected in managed mode", async () => {
    const { result } = renderHook(() => useDiagnosticsOptions());
    expect(result.current.manageable).toBe(true);
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.options).toEqual(saved.options);
    expect(result.current.productMetrics).toEqual(saved.productMetrics);
    expect(fetchHarnessDiagnosticsOptions).toHaveBeenCalledTimes(1);
    expect(result.current.error).toBeNull();
  });

  it("never fetches in external mode and reports it as not manageable", async () => {
    runtime.mode = "external";
    const { result } = renderHook(() => useDiagnosticsOptions());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.manageable).toBe(false);
    expect(result.current.options).toBeNull();
    expect(fetchHarnessDiagnosticsOptions).not.toHaveBeenCalled();
  });

  it("waits for the connection before loading", () => {
    runtime.connected = false;
    const { result } = renderHook(() => useDiagnosticsOptions());
    expect(result.current.live).toBe(false);
    expect(fetchHarnessDiagnosticsOptions).not.toHaveBeenCalled();
  });

  it("saves a patch, re-reads, re-probes the runtime and posts the restart notice", async () => {
    const next = {
      ...saved,
      options: { ...saved.options, logLevel: "debug" },
    };
    saveHarnessDiagnosticsOptions.mockResolvedValue(next.options);
    const { result } = renderHook(() => useDiagnosticsOptions());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    fetchHarnessDiagnosticsOptions.mockResolvedValue(next);

    let stood: boolean | undefined;
    await waitFor(async () => {
      stood = await result.current.save({ logLevel: "debug" });
    });
    expect(stood).toBe(true);
    expect(saveHarnessDiagnosticsOptions).toHaveBeenCalledWith({
      logLevel: "debug",
    });
    await waitFor(() => expect(result.current.options?.logLevel).toBe("debug"));
    expect(runtime.refresh).toHaveBeenCalled();
    expect(result.current.notice).toBe("Saved. The agent restarted.");
    expect(result.current.busy).toBe(false);
  });

  it("words a quiet-only save without the restart claim", async () => {
    saveHarnessDiagnosticsOptions.mockResolvedValue(saved.options);
    const { result } = renderHook(() => useDiagnosticsOptions());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    await waitFor(async () => {
      await result.current.save({ quiet: true });
    });
    await waitFor(() => expect(result.current.notice).toBe("Saved."));
  });

  it("surfaces a refused save as error and re-reads the running document", async () => {
    saveHarnessDiagnosticsOptions.mockRejectedValue(
      new Error("posture flags are the Permissions page's"),
    );
    const { result } = renderHook(() => useDiagnosticsOptions());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    let stood: boolean | undefined;
    await waitFor(async () => {
      stood = await result.current.save({ admin: { perfMcp: true } });
    });
    expect(stood).toBe(false);
    await waitFor(() =>
      expect(result.current.error).toMatch(/Permissions page/),
    );
    // Initial load + the re-read after the failure.
    expect(fetchHarnessDiagnosticsOptions).toHaveBeenCalledTimes(2);
    expect(result.current.options).toEqual(saved.options);
  });
});
