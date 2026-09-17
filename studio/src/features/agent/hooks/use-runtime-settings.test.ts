import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  HarnessRuntimeSettings,
  HarnessRuntimeSettingsDoc,
} from "@/lib/harness/runtime-settings";
import {
  mergeRuntimeSettings,
  useRuntimeSettings,
} from "./use-runtime-settings";

/**
 * The runtime-settings hook: loads the controller document in managed mode
 * only (external mode fetches nothing — the deployment owns its flags),
 * `save` merges a partial patch over the SAVED document and PUTs the whole
 * thing before re-reading, a refused save lands in `error` with the
 * document untouched, and `approveSoul` posts the one-shot approval then
 * re-reads. Busy names the action for its duration.
 */

const {
  fetchHarnessRuntimeSettings,
  saveHarnessRuntimeSettings,
  approveHarnessSoulBaseline,
  runtime,
} = vi.hoisted(() => ({
  fetchHarnessRuntimeSettings: vi.fn(),
  saveHarnessRuntimeSettings: vi.fn(),
  approveHarnessSoulBaseline: vi.fn(),
  runtime: {
    connected: true,
    mode: "managed" as "managed" | "external",
    serverCapabilities: {} as Record<string, unknown>,
    refresh: vi.fn(async () => undefined),
  },
}));

vi.mock("@/lib/harness/runtime-settings", () => ({
  fetchHarnessRuntimeSettings,
  saveHarnessRuntimeSettings,
  approveHarnessSoulBaseline,
}));

vi.mock("../runtime-status", () => ({ useRuntimeStatus: () => runtime }));

const config: HarnessRuntimeSettings = {
  learning: { mode: "", sensitivity: "" },
  steer: { enabled: true },
  soul: { enabled: true, strict: false, file: "" },
};

const doc = (overrides: Partial<HarnessRuntimeSettings> = {}) =>
  ({
    config: { ...config, ...overrides },
    managedBy: { learning: "studio", steer: "studio", soul: "studio" },
    inherited: { learning: { mode: "", sensitivity: "" }, steer: null },
    effective: {
      learning: { mode: "off", sensitivity: "balanced" },
      steer: true,
    },
    soulFileDefault: "/home/me/.config/mecatl/soul.md",
    soulCandidates: [],
  }) satisfies HarnessRuntimeSettingsDoc;

beforeEach(() => {
  runtime.connected = true;
  runtime.mode = "managed";
  runtime.serverCapabilities = { soul: true };
  fetchHarnessRuntimeSettings.mockReset();
  saveHarnessRuntimeSettings.mockReset();
  approveHarnessSoulBaseline.mockReset();
  fetchHarnessRuntimeSettings.mockResolvedValue(doc());
  saveHarnessRuntimeSettings.mockImplementation(async (next) => next);
  approveHarnessSoulBaseline.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("mergeRuntimeSettings", () => {
  it("merges a partial patch section by section, leaving the rest as saved", () => {
    const saved: HarnessRuntimeSettings = {
      learning: { mode: "review", sensitivity: "eager" },
      steer: { enabled: false },
      soul: { enabled: true, strict: true, file: "/x/soul.md" },
    };
    expect(mergeRuntimeSettings(saved, { learning: { mode: "auto" } })).toEqual(
      {
        ...saved,
        learning: { mode: "auto", sensitivity: "eager" },
      },
    );
    expect(mergeRuntimeSettings(saved, { soul: { file: "" } }).soul).toEqual({
      enabled: true,
      strict: true,
      file: "",
    });
    expect(mergeRuntimeSettings(saved, {})).toEqual(saved);
  });
});

describe("useRuntimeSettings", () => {
  it("loads the document when connected in managed mode", async () => {
    const { result } = renderHook(() => useRuntimeSettings());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(fetchHarnessRuntimeSettings).toHaveBeenCalledTimes(1);
    expect(result.current.doc?.config).toEqual(config);
    expect(result.current.manageable).toBe(true);
    expect(result.current.soulSupported).toBe(true);
    expect(result.current.error).toBeNull();
  });

  it("fetches nothing in external mode: the deployment owns its flags", async () => {
    runtime.mode = "external";
    runtime.serverCapabilities = {};
    const { result } = renderHook(() => useRuntimeSettings());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(fetchHarnessRuntimeSettings).not.toHaveBeenCalled();
    expect(result.current.manageable).toBe(false);
    expect(result.current.soulSupported).toBe(false);
    expect(result.current.doc).toBeNull();
  });

  it("waits for the connection before loading", async () => {
    runtime.connected = false;
    const { result } = renderHook(() => useRuntimeSettings());
    expect(fetchHarnessRuntimeSettings).not.toHaveBeenCalled();
    expect(result.current.isLoading).toBe(true);
  });

  it("save merges the patch over the saved document, PUTs the whole thing, then re-reads", async () => {
    const saved = doc({ learning: { mode: "review", sensitivity: "" } });
    fetchHarnessRuntimeSettings
      .mockResolvedValueOnce(doc())
      .mockResolvedValueOnce(saved);
    const { result } = renderHook(() => useRuntimeSettings());
    await waitFor(() => expect(result.current.doc).not.toBeNull());

    let outcome: boolean | undefined;
    await act(async () => {
      outcome = await result.current.save({ learning: { mode: "review" } });
    });
    expect(outcome).toBe(true);
    expect(saveHarnessRuntimeSettings).toHaveBeenCalledTimes(1);
    expect(saveHarnessRuntimeSettings).toHaveBeenCalledWith({
      ...config,
      learning: { mode: "review", sensitivity: "" },
    });
    expect(fetchHarnessRuntimeSettings).toHaveBeenCalledTimes(2);
    // The restart may have changed what the daemon advertises: the runtime
    // is re-probed once, after the re-read, so capability gates flip now.
    expect(runtime.refresh).toHaveBeenCalledTimes(1);
    expect(result.current.doc?.config.learning.mode).toBe("review");
    expect(result.current.notice).toBe("Saved. The agent restarted.");
    expect(result.current.busy).toBe("");
  });

  it("a refused save lands in error and leaves the document unchanged", async () => {
    saveHarnessRuntimeSettings.mockRejectedValueOnce(
      new Error(
        "Learning mode and sensitivity are managed by the imported operator settings file while it is active.",
      ),
    );
    const { result } = renderHook(() => useRuntimeSettings());
    await waitFor(() => expect(result.current.doc).not.toBeNull());

    let outcome: boolean | undefined;
    await act(async () => {
      outcome = await result.current.save({ steer: { enabled: false } });
    });
    expect(outcome).toBe(false);
    expect(result.current.error).toMatch(
      /managed by the imported operator settings/,
    );
    expect(result.current.doc?.config).toEqual(config);
    expect(fetchHarnessRuntimeSettings).toHaveBeenCalledTimes(1);
    expect(result.current.notice).toBeNull();
    // Nothing restarted, so nothing to re-probe.
    expect(runtime.refresh).not.toHaveBeenCalled();
  });

  it("approveSoul posts the one-shot approval then re-reads", async () => {
    const { result } = renderHook(() => useRuntimeSettings());
    await waitFor(() => expect(result.current.doc).not.toBeNull());

    await act(async () => {
      await result.current.approveSoul();
    });
    expect(approveHarnessSoulBaseline).toHaveBeenCalledTimes(1);
    expect(fetchHarnessRuntimeSettings).toHaveBeenCalledTimes(2);
    expect(result.current.notice).toBe("Saved. The agent restarted.");
    expect(result.current.busy).toBe("");
  });

  it("a failed approval lands in error", async () => {
    approveHarnessSoulBaseline.mockRejectedValueOnce(
      new Error(
        "The persona is disabled; enable it before accepting a baseline.",
      ),
    );
    const { result } = renderHook(() => useRuntimeSettings());
    await waitFor(() => expect(result.current.doc).not.toBeNull());
    await act(async () => {
      await result.current.approveSoul();
    });
    expect(result.current.error).toMatch(/persona is disabled/);
    expect(fetchHarnessRuntimeSettings).toHaveBeenCalledTimes(1);
  });
});
