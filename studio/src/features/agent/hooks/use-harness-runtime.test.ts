import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useHarnessRuntime } from "./use-harness-runtime";

/**
 * The harness-runtime hook's two project-trust GRANTS — the browser side of
 * mecatui's pre-TUI "trust / trust once" answers. Each is a controller write
 * that restarts the daemon, so it rides the same one-at-a-time `runWrite`
 * as every other config write: `busy` reads "trust" while it runs, the
 * controller status (and with it `/status.trust`) is re-read afterwards so
 * the page shows the decision the NEW spawn actually got, a plain notice
 * says what happened, and a refused grant (mecated's own refusal, rolled
 * back by the controller) lands in `error` with no notice.
 */

const mocks = vi.hoisted(() => ({
  fetchHarnessControlStatus: vi.fn(),
  fetchHarnessPermissions: vi.fn(),
  listHarnessModels: vi.fn(),
  saveHarnessPermissions: vi.fn(),
  trustWorkspace: vi.fn(),
  trustWorkspaceOnce: vi.fn(),
  runtime: { connected: true, mode: "managed" as "managed" | "external" },
}));

vi.mock("@/lib/harness/client", () => ({
  connectHarnessGateway: vi.fn(),
  fetchHarnessControlStatus: mocks.fetchHarnessControlStatus,
  fetchHarnessPermissions: mocks.fetchHarnessPermissions,
  listHarnessModels: mocks.listHarnessModels,
  saveHarnessPermissions: mocks.saveHarnessPermissions,
  saveHarnessRetention: vi.fn(),
  saveHarnessStorageSettings: vi.fn(),
  startHarnessGatewayOAuth: vi.fn(),
  waitForHarnessGateway: vi.fn(),
  trustWorkspace: mocks.trustWorkspace,
  trustWorkspaceOnce: mocks.trustWorkspaceOnce,
}));

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => mocks.runtime,
}));

/** `/status` as the browser reads it, with the controller's trust registry
 *  reporting `decision` for the current spawn. */
const statusWith = (
  decision: "trusted" | "once" | "drifted" | "untrusted",
) => ({
  mode: "managed",
  provider: "mock",
  isMock: true,
  running: true,
  gateway: null,
  toolhiveGateway: null,
  modelRouter: null,
  operatorSettings: false,
  skillsDir: "",
  memoryDir: "",
  configuredProviders: [],
  selectedProvider: "mock",
  authFile: "",
  workspace: "/srv/checkout/mecatl",
  permissions: {
    posture: "strict",
    trustProject: decision === "trusted",
    noShell: false,
    trustOnce: decision === "once",
  },
  trust: {
    hasAuthority: true,
    decision,
    source:
      decision === "untrusted" || decision === "drifted" ? "none" : "studio",
    anchor: "a".repeat(64),
  },
  storage: null,
  retention: null,
});

beforeEach(() => {
  mocks.runtime.connected = true;
  mocks.runtime.mode = "managed";
  for (const fn of Object.values(mocks)) {
    if (typeof fn === "function" && "mockReset" in fn) fn.mockReset();
  }
  mocks.fetchHarnessControlStatus.mockResolvedValue(statusWith("untrusted"));
  mocks.fetchHarnessPermissions.mockResolvedValue(null);
  mocks.listHarnessModels.mockResolvedValue([]);
  mocks.trustWorkspace.mockResolvedValue(undefined);
  mocks.trustWorkspaceOnce.mockResolvedValue(undefined);
});

async function connected() {
  const rendered = renderHook(() => useHarnessRuntime());
  await waitFor(() => expect(rendered.result.current.status).not.toBeNull());
  await waitFor(() => expect(rendered.result.current.isLoading).toBe(false));
  return rendered;
}

describe("useHarnessRuntime", () => {
  it("reads the controller status (with its trust decision) once connected, and not before", async () => {
    mocks.runtime.connected = false;
    const { result, rerender } = renderHook(() => useHarnessRuntime());
    expect(result.current.status).toBeNull();
    expect(mocks.fetchHarnessControlStatus).not.toHaveBeenCalled();

    mocks.runtime.connected = true;
    rerender();
    await waitFor(() => expect(result.current.status).not.toBeNull());
    expect(result.current.status?.trust).toEqual(
      expect.objectContaining({ decision: "untrusted", hasAuthority: true }),
    );
    expect(mocks.fetchHarnessControlStatus).toHaveBeenCalledTimes(1);
    expect(result.current.trustProject).toEqual(expect.any(Function));
    expect(result.current.trustProjectOnce).toEqual(expect.any(Function));
  });

  it("trustProject grants the remembered trust, re-reads the status and says the agent restarted", async () => {
    const { result } = await connected();
    let release: () => void = () => {};
    mocks.trustWorkspace.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          release = resolve;
        }),
    );
    mocks.fetchHarnessControlStatus.mockResolvedValue(statusWith("trusted"));

    let pending: Promise<void> = Promise.resolve();
    act(() => {
      pending = result.current.trustProject();
    });
    await waitFor(() => expect(result.current.busy).toBe("trust"));
    expect(mocks.trustWorkspace).toHaveBeenCalledTimes(1);
    // Bodyless grant: the controller stamps the anchor, never the browser.
    expect(mocks.trustWorkspace).toHaveBeenCalledWith();

    release();
    await act(async () => {
      await pending;
    });

    expect(result.current.busy).toBe("");
    expect(result.current.error).toBeNull();
    expect(result.current.notice).toMatch(/^This project is now trusted\./);
    expect(result.current.notice).toMatch(/agent restarted/i);
    // The re-read is what makes the page show the NEW spawn's decision.
    expect(mocks.fetchHarnessControlStatus).toHaveBeenCalledTimes(2);
    expect(result.current.status?.trust?.decision).toBe("trusted");
    expect(result.current.status?.permissions?.trustProject).toBe(true);
  });

  it("trustProjectOnce grants for this controller process only and says so", async () => {
    const { result } = await connected();
    mocks.fetchHarnessControlStatus.mockResolvedValue(statusWith("once"));

    await act(async () => {
      await result.current.trustProjectOnce();
    });

    expect(mocks.trustWorkspaceOnce).toHaveBeenCalledTimes(1);
    expect(mocks.trustWorkspaceOnce).toHaveBeenCalledWith();
    expect(mocks.trustWorkspace).not.toHaveBeenCalled();
    expect(result.current.error).toBeNull();
    expect(result.current.notice).toMatch(/until Studio restarts/);
    expect(result.current.notice).toMatch(/nothing (is|was) saved/i);
    expect(result.current.status?.trust?.decision).toBe("once");
  });

  it("surfaces a refused grant as error, with no notice and no stale re-read", async () => {
    const { result } = await connected();
    mocks.trustWorkspace.mockRejectedValue(
      new Error(
        'posture "auto" refused: running as root outside MECATL_SANDBOX (previous permissions restored)',
      ),
    );

    await act(async () => {
      await result.current.trustProject();
    });

    expect(result.current.error).toMatch(/posture "auto" refused/);
    expect(result.current.notice).toBeNull();
    expect(result.current.busy).toBe("");
    expect(mocks.fetchHarnessControlStatus).toHaveBeenCalledTimes(1);
  });

  it("keeps the saved-permissions write on its own busy label and notice", async () => {
    const { result } = await connected();
    mocks.saveHarnessPermissions.mockResolvedValue(undefined);

    await act(async () => {
      await result.current.savePermissions({
        posture: "trusted",
        trustProject: false,
        noShell: false,
      });
    });

    expect(mocks.saveHarnessPermissions).toHaveBeenCalledWith({
      posture: "trusted",
      trustProject: false,
      noShell: false,
    });
    expect(result.current.notice).toBe("Saved. The agent restarted.");
    expect(mocks.trustWorkspace).not.toHaveBeenCalled();
  });
});
