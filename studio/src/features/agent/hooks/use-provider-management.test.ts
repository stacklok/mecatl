import { act, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useProviderManagement } from "./use-provider-management";

/**
 * The provider management hook: loads the controller inventory (managed
 * mode only) and — for the ToolHive delegation — `startToolhive()` posts
 * the proxy start, re-reads the inventory, and turns the controller's
 * bounded-poll verdict into a NOTICE (reachable, or the "run `thv llm
 * login` first" hint) while a refused start (thv absent, external mode)
 * lands in `error`. Busy is `toolhive:start` for the duration.
 */

const {
  createHarnessCustomProvider,
  listHarnessProviders,
  listKnownHarnessProviders,
  removeHarnessProvider,
  startHarnessToolhiveGateway,
  runtime,
} = vi.hoisted(() => ({
  createHarnessCustomProvider: vi.fn(),
  listHarnessProviders: vi.fn(),
  listKnownHarnessProviders: vi.fn(),
  removeHarnessProvider: vi.fn(),
  startHarnessToolhiveGateway: vi.fn(),
  runtime: { connected: true, mode: "managed" as "managed" | "external" },
}));

vi.mock("@/lib/harness/client", () => ({
  createHarnessCustomProvider,
  listHarnessProviders,
  listKnownHarnessProviders,
  removeHarnessProvider,
  restartHarnessDaemon: vi.fn(),
  setActiveHarnessProvider: vi.fn(),
  startHarnessToolhiveGateway,
  testHarnessProviderKey: vi.fn(),
}));

vi.mock("../runtime-status", () => ({ useRuntimeStatus: () => runtime }));

const toolhiveRow = (reachable: boolean) => ({
  name: "toolhive",
  configured: reachable,
  keyPresent: true,
  source: "thv llm proxy",
  testable: false,
  class: "external",
  authMethod: "external",
  envShadowed: false,
  authState: reachable ? "gateway reachable" : "gateway not reachable",
  defaultModel: "",
  nextStep: reachable
    ? "set as active to use it"
    : "start the gateway (thv llm proxy start)",
  active: false,
  baseURL: "http://127.0.0.1:14000/v1",
  reachable,
  thvOnPath: true,
});

beforeEach(() => {
  runtime.connected = true;
  runtime.mode = "managed";
  createHarnessCustomProvider.mockReset();
  listHarnessProviders.mockReset();
  listKnownHarnessProviders.mockReset();
  removeHarnessProvider.mockReset();
  startHarnessToolhiveGateway.mockReset();
  listHarnessProviders.mockResolvedValue([toolhiveRow(false)]);
  listKnownHarnessProviders.mockResolvedValue([]);
});

/** A complete NON-secret custom definition (no key field exists). */
const gateway = {
  id: "my-gw",
  apiFlavor: "openai-responses",
  baseURL: "https://gw.example/v1",
  defaultModel: "org/model",
  authMethod: "api_key" as const,
};

describe("useProviderManagement — definition write and scoped removal", () => {
  it("addCustomProvider posts the definition, re-reads, and points an api_key provider at the key step", async () => {
    createHarnessCustomProvider.mockResolvedValue({
      restarted: false,
      restartError: "",
    });
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    let outcome: unknown;
    await act(async () => {
      outcome = await result.current.addCustomProvider(gateway);
    });
    expect(createHarnessCustomProvider).toHaveBeenCalledWith(gateway);
    expect(listHarnessProviders).toHaveBeenCalledTimes(2);
    expect(outcome).toEqual({ ok: true, restarted: false, error: undefined });
    expect(result.current.notice).toMatch(
      /^Saved\. Add the key for .+ to the agent's key file, then restart the agent\.$/,
    );
    expect(result.current.error).toBeNull();
    expect(result.current.busy).toBe("");
  });

  it("a keyless definition reports the controller's restart", async () => {
    createHarnessCustomProvider.mockResolvedValue({
      restarted: true,
      restartError: "",
    });
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    let outcome: unknown;
    await act(async () => {
      outcome = await result.current.addCustomProvider({
        ...gateway,
        authMethod: "none",
      });
    });
    expect(outcome).toEqual({ ok: true, restarted: true, error: undefined });
    expect(result.current.notice).toBe("Saved. The agent restarted.");
  });

  it("a saved definition whose restart failed keeps the save and surfaces the cause", async () => {
    createHarnessCustomProvider.mockResolvedValue({
      restarted: false,
      restartError: "mecated exited before the ready file",
    });
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    let outcome: unknown;
    await act(async () => {
      outcome = await result.current.addCustomProvider({
        ...gateway,
        authMethod: "none",
      });
    });
    expect(outcome).toEqual({
      ok: true,
      restarted: false,
      error: "mecated exited before the ready file",
    });
    expect(result.current.notice).toBe("Saved.");
    expect(result.current.error).toMatch(/could not restart: mecated exited/);
  });

  it("a refused write (409: already configured / operator settings active) lands in error and returns ok:false", async () => {
    createHarnessCustomProvider.mockRejectedValue(
      new Error(
        "Refused while an imported operator settings file is active: Studio does not write the settings file then.",
      ),
    );
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    let outcome: unknown;
    await act(async () => {
      outcome = await result.current.addCustomProvider(gateway);
    });
    expect(outcome).toEqual({
      ok: false,
      restarted: false,
      error: expect.stringMatching(/imported operator settings file/),
    });
    expect(result.current.error).toMatch(/imported operator settings file/);
    expect(result.current.notice).toBeNull();
    expect(result.current.busy).toBe("");
  });

  it("removeProvider passes the scope through and words the notice per scope", async () => {
    removeHarnessProvider.mockResolvedValue(undefined);
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    await act(async () => {
      await result.current.removeProvider("my-gw", "credential");
    });
    expect(removeHarnessProvider).toHaveBeenLastCalledWith(
      "my-gw",
      "credential",
    );
    expect(result.current.notice).toBe("Key removed. The agent restarted.");
    await act(async () => {
      await result.current.removeProvider("my-gw");
    });
    expect(removeHarnessProvider).toHaveBeenLastCalledWith("my-gw", "all");
    expect(result.current.notice).toBe("my-gw removed. The agent restarted.");
  });
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("useProviderManagement — startToolhive", () => {
  it("posts the start, re-reads the inventory and notices a reachable gateway", async () => {
    startHarnessToolhiveGateway.mockResolvedValue({
      available: true,
      hint: "",
    });
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.providers[0]?.reachable).toBe(false);

    listHarnessProviders.mockResolvedValue([toolhiveRow(true)]);
    let busyDuring = "";
    await act(async () => {
      const pending = result.current.startToolhive();
      // The busy label is set synchronously before the first await.
      busyDuring = "toolhive:start";
      await pending;
    });
    expect(busyDuring).toBe("toolhive:start");
    expect(startHarnessToolhiveGateway).toHaveBeenCalledTimes(1);
    expect(listHarnessProviders).toHaveBeenCalledTimes(2);
    expect(result.current.providers[0]?.reachable).toBe(true);
    expect(result.current.notice).toMatch(/reachable\. Set it as active/);
    expect(result.current.error).toBeNull();
    expect(result.current.busy).toBe("");
  });

  it("surfaces the controller's timeout hint as a notice, not an error", async () => {
    startHarnessToolhiveGateway.mockResolvedValue({
      available: false,
      hint: "The gateway did not answer within 8 seconds. If ToolHive has no cached login, run `thv llm login` in a terminal first, then re-check.",
    });
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    await act(async () => {
      await result.current.startToolhive();
    });
    expect(result.current.error).toBeNull();
    expect(result.current.notice).toMatch(/thv llm login/);
  });

  it("lands a refused start (thv absent / external 409) in error", async () => {
    startHarnessToolhiveGateway.mockRejectedValue(
      new Error("ToolHive's thv CLI is not on the controller's PATH"),
    );
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    await act(async () => {
      await result.current.startToolhive();
    });
    expect(result.current.error).toMatch(/not on the controller's PATH/);
    expect(result.current.notice).toBeNull();
    expect(result.current.busy).toBe("");
  });

  it("never reads the controller inventory in external mode", async () => {
    runtime.mode = "external";
    const { result } = renderHook(() => useProviderManagement());
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.manageable).toBe(false);
    expect(listHarnessProviders).not.toHaveBeenCalled();
  });
});
