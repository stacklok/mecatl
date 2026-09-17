import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  EMPTY_DAEMON_OPTIONS,
  type HarnessDaemonOptionsDoc,
} from "@/lib/harness/daemon-options";
import { mergeDaemonOptions, useDaemonOptions } from "./use-daemon-options";

/**
 * The daemon-options hook: loads the controller's document once connected
 * (managed mode ONLY — external mode never fetches, the controller would
 * answer 409), merges a partial patch over the saved document and PUTs the
 * WHOLE thing, re-reads and re-probes the daemon's capabilities afterwards
 * (the restarted daemon advertises the new catalog), and surfaces a refused
 * save as `error` while re-reading the document the daemon is actually
 * running on.
 */

const { fetchHarnessDaemonOptions, saveHarnessDaemonOptions, runtime } =
  vi.hoisted(() => ({
    fetchHarnessDaemonOptions: vi.fn(),
    saveHarnessDaemonOptions: vi.fn(),
    runtime: {
      connected: true,
      mode: "managed" as "managed" | "external",
      refresh: vi.fn(async () => undefined),
    },
  }));

vi.mock("@/lib/harness/daemon-options", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/harness/daemon-options")>()),
  fetchHarnessDaemonOptions,
  saveHarnessDaemonOptions,
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

const doc: HarnessDaemonOptionsDoc = {
  options: EMPTY_DAEMON_OPTIONS,
  defaults: {
    skillsDir: "/repo/.mecatl/skills",
    memoryDir: "/repo/.scratch/studio-memory",
    userModelDir: "/home/me/.config/mecatl/usermodel",
    commandDirs: [".mecatl/commands", ".claude/commands"],
  },
  effective: {
    skillsDir: "/repo/.mecatl/skills",
    memoryDir: "/repo/.scratch/studio-memory",
    userModelDir: "",
    commandsDir: "",
  },
  allowedRoots: ["/repo", "/home/me/.config/mecatl"],
};

beforeEach(() => {
  runtime.connected = true;
  runtime.mode = "managed";
  runtime.refresh.mockClear();
  fetchHarnessDaemonOptions.mockReset();
  saveHarnessDaemonOptions.mockReset();
  fetchHarnessDaemonOptions.mockResolvedValue(doc);
  saveHarnessDaemonOptions.mockResolvedValue(EMPTY_DAEMON_OPTIONS);
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("mergeDaemonOptions", () => {
  it("deep-merges a patch section by section, leaving the rest verbatim", () => {
    const merged = mergeDaemonOptions(EMPTY_DAEMON_OPTIONS, {
      skills: { enabled: false },
      mcp: { toolhiveGroup: "team" },
    });
    expect(merged.skills).toEqual({ enabled: false, dir: "" });
    expect(merged.mcp).toEqual({
      ...EMPTY_DAEMON_OPTIONS.mcp,
      toolhiveGroup: "team",
    });
    expect(merged.userModel).toEqual(EMPTY_DAEMON_OPTIONS.userModel);
  });
});

describe("useDaemonOptions", () => {
  it("loads the saved document once connected in managed mode", async () => {
    const { result } = renderHook(() => useDaemonOptions());
    expect(result.current.manageable).toBe(true);
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.doc).toEqual(doc);
    expect(fetchHarnessDaemonOptions).toHaveBeenCalledTimes(1);
    expect(result.current.error).toBeNull();
  });

  it("never fetches in external mode and reports it as not manageable", async () => {
    runtime.mode = "external";
    const { result } = renderHook(() => useDaemonOptions());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.manageable).toBe(false);
    expect(result.current.doc).toBeNull();
    expect(fetchHarnessDaemonOptions).not.toHaveBeenCalled();
  });

  it("waits for the connection before loading", () => {
    runtime.connected = false;
    const { result } = renderHook(() => useDaemonOptions());
    expect(result.current.live).toBe(false);
    expect(fetchHarnessDaemonOptions).not.toHaveBeenCalled();
  });

  it("PUTs the merged WHOLE document, re-reads, re-probes capabilities and posts the notice", async () => {
    const { result } = renderHook(() => useDaemonOptions());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    let ok = false;
    await act(async () => {
      ok = await result.current.save({
        projectMemory: { enabled: false },
        mcp: { toolhive: false },
      });
    });
    expect(ok).toBe(true);
    expect(saveHarnessDaemonOptions).toHaveBeenCalledWith({
      ...EMPTY_DAEMON_OPTIONS,
      projectMemory: { enabled: false, dir: "" },
      mcp: { ...EMPTY_DAEMON_OPTIONS.mcp, toolhive: false },
    });
    expect(fetchHarnessDaemonOptions).toHaveBeenCalledTimes(2);
    expect(runtime.refresh).toHaveBeenCalledTimes(1);
    expect(result.current.notice).toBe("Saved. The agent restarted.");
    expect(result.current.busy).toBe(false);
  });

  it("surfaces a refused save as error and re-reads the running document", async () => {
    saveHarnessDaemonOptions.mockRejectedValue(
      new Error("skills.dir must be inside the workspace /repo"),
    );
    const { result } = renderHook(() => useDaemonOptions());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    let ok = true;
    await act(async () => {
      ok = await result.current.save({ skills: { dir: "/etc" } });
    });
    expect(ok).toBe(false);
    expect(result.current.error).toMatch(/skills\.dir must be inside/);
    expect(result.current.notice).toBeNull();
    expect(fetchHarnessDaemonOptions).toHaveBeenCalledTimes(2);
    expect(runtime.refresh).not.toHaveBeenCalled();
  });

  it("refuses to save before the document loaded", async () => {
    runtime.connected = false;
    const { result } = renderHook(() => useDaemonOptions());
    let ok = true;
    await act(async () => {
      ok = await result.current.save({ skills: { enabled: false } });
    });
    expect(ok).toBe(false);
    expect(saveHarnessDaemonOptions).not.toHaveBeenCalled();
  });
});
