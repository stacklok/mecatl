import { act, renderHook } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HarnessApiError } from "@/lib/harness/errors";
import {
  ENROLLMENT_POLL_CAP_MS,
  ENROLLMENT_POLL_INTERVAL_MS,
  ENROLLMENT_TIMED_OUT,
  phaseFromConnectors,
  useWorkspaceEnrollment,
} from "./use-workspace-enrollment";

/**
 * The workspace-enrollment hook (the TUI's /tools-connect notice + actions):
 * capability- and debug-gated, probes the connector inventory once per open
 * where the daemon grants inspection, opens the consent window SYNCHRONOUSLY
 * on the click before any await, keeps the ephemeral URL out of state,
 * observes a pending enrollment every 3 s until it settles, and drives
 * retry / cancel through the exact enrollment id (falling back to a fresh
 * connect when the daemon has already settled it).
 */

const { api, runtime, refreshSlashCommands } = vi.hoisted(() => ({
  api: {
    connectWorkspaceServices: vi.fn(),
    retryWorkspaceEnrollment: vi.fn(),
    cancelWorkspaceEnrollment: vi.fn(),
    fetchSessionConnectors: vi.fn(),
  },
  runtime: { serverCapabilities: {} as Record<string, unknown> },
  refreshSlashCommands: vi.fn(async () => undefined),
}));

vi.mock("@/lib/harness/enrollment", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("@/lib/harness/enrollment")>();
  return { ...actual, ...api };
});

vi.mock("../runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

vi.mock("../composer-capabilities", () => ({ refreshSlashCommands }));

const CONSENT_URL = "https://broker.test/consent?e=1";

const pendingWithUrl = {
  enrollmentId: "e1",
  status: "pending",
  requiredServices: 2,
  presentationUrl: CONSENT_URL,
};
const pendingObserved = { ...pendingWithUrl, presentationUrl: "" };
const connected = {
  enrollmentId: "e1",
  status: "connected",
  requiredServices: 2,
  presentationUrl: "",
};
const settled = (status: string) => ({ ...pendingObserved, status });

function inventory(enrollmentState: string) {
  return {
    availability: "available",
    enrollmentState,
    connectors: [],
    totalConnectors: 0,
    truncated: false,
  };
}

interface FakePopup {
  closed: boolean;
  close: () => void;
  location: { href: string };
}

function fakePopup(): FakePopup {
  const popup: FakePopup = {
    closed: false,
    close: vi.fn(() => {
      popup.closed = true;
    }),
    location: { href: "" },
  };
  return popup;
}

/** Lets the hook's promise chains and effects settle. */
const flush = () =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(0);
  });
const tick = (ms = ENROLLMENT_POLL_INTERVAL_MS) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

function renderEnrollment(
  initial: { id: string | null; idle: boolean; debug?: boolean } = {
    id: "s1",
    idle: true,
  },
) {
  return renderHook(
    ({
      id,
      idle,
      debug,
    }: {
      id: string | null;
      idle: boolean;
      debug?: boolean;
    }) => useWorkspaceEnrollment(id, { idle, debugSession: Boolean(debug) }),
    { initialProps: initial },
  );
}

/** The state the notice reads — the URL must never appear in it. */
const serialized = (value: unknown) => JSON.stringify(value);

beforeEach(() => {
  vi.useFakeTimers();
  window.sessionStorage.clear();
  runtime.serverCapabilities = {
    workspace_enrollment: true,
    mcp_connector_status: true,
  };
  api.fetchSessionConnectors.mockReset();
  api.connectWorkspaceServices.mockReset();
  api.retryWorkspaceEnrollment.mockReset();
  api.cancelWorkspaceEnrollment.mockReset();
  api.fetchSessionConnectors.mockResolvedValue(inventory("not_started"));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("phaseFromConnectors", () => {
  it.each([
    ["completed", "connected"],
    ["not_required", "not_required"],
    ["pending", "pending"],
    ["not_started", "not_connected"],
    ["unknown", "not_connected"],
    ["something-new", "not_connected"],
  ])("maps the inventory word %s to phase %s", (word, phase) => {
    expect(phaseFromConnectors(inventory(word))).toBe(phase);
  });

  it("treats an unavailable inventory as not connected", () => {
    expect(phaseFromConnectors(null)).toBe("not_connected");
  });
});

describe("useWorkspaceEnrollment", () => {
  it("probes the connector inventory once per open and adopts its verdict", async () => {
    api.fetchSessionConnectors.mockResolvedValue(inventory("completed"));
    const { result } = renderEnrollment();
    expect(result.current.supported).toBe(true);
    expect(result.current.phase).toBe("unknown");
    await flush();
    expect(api.fetchSessionConnectors).toHaveBeenCalledTimes(1);
    expect(api.fetchSessionConnectors.mock.calls[0][0]).toBe("s1");
    expect(result.current.phase).toBe("connected");
    expect(api.connectWorkspaceServices).not.toHaveBeenCalled();
  });

  it("shows the notice straight away when the daemon grants no inspection", async () => {
    runtime.serverCapabilities = { workspace_enrollment: true };
    const { result } = renderEnrollment();
    await flush();
    expect(result.current.phase).toBe("not_connected");
    expect(api.fetchSessionConnectors).not.toHaveBeenCalled();
  });

  it("is inert without the capability, for a debug session, and without a session", async () => {
    runtime.serverCapabilities = {};
    const plain = renderEnrollment();
    await flush();
    expect(plain.result.current.supported).toBe(false);
    expect(plain.result.current.phase).toBe("unknown");
    act(() => plain.result.current.connect());
    await flush();
    expect(api.connectWorkspaceServices).not.toHaveBeenCalled();

    runtime.serverCapabilities = {
      workspace_enrollment: true,
      mcp_connector_status: true,
    };
    const debug = renderEnrollment({ id: "s1", idle: true, debug: true });
    await flush();
    expect(debug.result.current.supported).toBe(false);
    const draft = renderEnrollment({ id: null, idle: true });
    await flush();
    expect(draft.result.current.supported).toBe(false);
    expect(api.fetchSessionConnectors).not.toHaveBeenCalled();
  });

  it("connect opens the window first, points it at the consent URL, polls every 3 s and settles connected", async () => {
    const popup = fakePopup();
    const open = vi.fn(() => popup);
    vi.stubGlobal("open", open);
    api.connectWorkspaceServices
      .mockResolvedValueOnce(pendingWithUrl)
      .mockResolvedValueOnce(pendingObserved)
      .mockResolvedValueOnce(connected);
    const { result } = renderEnrollment();
    await flush();
    expect(result.current.phase).toBe("not_connected");

    act(() => result.current.connect());
    // The popup is created synchronously on the click, before the request.
    expect(open).toHaveBeenCalledWith(
      "about:blank",
      "mecatl-workspace-enrollment",
      "width=520,height=680",
    );
    expect(open.mock.invocationCallOrder[0]).toBeLessThan(
      api.connectWorkspaceServices.mock.invocationCallOrder[0],
    );
    expect(result.current.busy).toBe(true);
    await flush();
    expect(popup.location.href).toBe(CONSENT_URL);
    expect(result.current.phase).toBe("pending");
    expect(result.current.window).toBe("open");
    expect(result.current.enrollmentId).toBe("e1");
    expect(result.current.requiredServices).toBe(2);
    expect(result.current.busy).toBe(false);
    // The ephemeral URL never lands in the state the notice renders from.
    expect(serialized(result.current)).not.toContain(CONSENT_URL);
    expect(serialized(result.current)).not.toContain("broker.test");

    // Observed every 3 s through the connect route (its observe form).
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(1);
    await tick();
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(2);
    expect(result.current.phase).toBe("pending");
    await tick();
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(3);
    expect(result.current.phase).toBe("connected");
    expect(popup.close).toHaveBeenCalled();
    expect(toast.success).toHaveBeenCalledWith("Workspace services connected");
    expect(refreshSlashCommands).toHaveBeenCalledWith("s1");
    // Polling stopped.
    await tick();
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(3);
  });

  it("a denied observe settles failed with the status word; Retry then begins afresh", async () => {
    vi.stubGlobal("open", vi.fn(fakePopup));
    api.connectWorkspaceServices
      .mockResolvedValueOnce(pendingWithUrl)
      .mockResolvedValueOnce(settled("denied"))
      .mockResolvedValueOnce(connected);
    const { result } = renderEnrollment();
    await flush();
    act(() => result.current.connect());
    await flush();
    await tick();
    expect(result.current.phase).toBe("failed");
    expect(result.current.outcome).toBe("denied");
    // The daemon cleared its pending record on that observe: nothing to
    // retry by id, so Retry begins a fresh enrollment.
    act(() => result.current.retry());
    await flush();
    expect(api.retryWorkspaceEnrollment).not.toHaveBeenCalled();
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(3);
    expect(result.current.phase).toBe("connected");
  });

  it("gives up observing after the cap, keeps the id, and Retry replaces that enrollment", async () => {
    const popups: FakePopup[] = [];
    vi.stubGlobal(
      "open",
      vi.fn(() => {
        const popup = fakePopup();
        popups.push(popup);
        return popup;
      }),
    );
    api.connectWorkspaceServices.mockResolvedValueOnce(pendingWithUrl);
    api.connectWorkspaceServices.mockResolvedValue(pendingObserved);
    const { result } = renderEnrollment();
    await flush();
    act(() => result.current.connect());
    await flush();
    await tick(ENROLLMENT_POLL_CAP_MS + ENROLLMENT_POLL_INTERVAL_MS);
    expect(result.current.phase).toBe("failed");
    expect(result.current.outcome).toBe(ENROLLMENT_TIMED_OUT);
    expect(result.current.enrollmentId).toBe("e1");
    const observes = api.connectWorkspaceServices.mock.calls.length;

    api.retryWorkspaceEnrollment.mockResolvedValueOnce({
      ...pendingWithUrl,
      enrollmentId: "e2",
      presentationUrl: "https://broker.test/consent?e=2",
    });
    act(() => result.current.retry());
    await flush();
    expect(api.retryWorkspaceEnrollment).toHaveBeenCalledWith(
      "s1",
      "e1",
      expect.anything(),
    );
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(observes);
    expect(result.current.phase).toBe("pending");
    expect(result.current.enrollmentId).toBe("e2");
    expect(popups.at(-1)?.location.href).toBe(
      "https://broker.test/consent?e=2",
    );
  });

  it("Retry on a stale id falls back to a fresh connect", async () => {
    vi.stubGlobal("open", vi.fn(fakePopup));
    api.connectWorkspaceServices.mockResolvedValueOnce(pendingWithUrl);
    api.connectWorkspaceServices.mockResolvedValue(pendingObserved);
    const { result } = renderEnrollment();
    await flush();
    act(() => result.current.connect());
    await flush();
    await tick(ENROLLMENT_POLL_CAP_MS + ENROLLMENT_POLL_INTERVAL_MS);
    expect(result.current.phase).toBe("failed");

    api.retryWorkspaceEnrollment.mockRejectedValueOnce(
      new HarnessApiError(412, "failed_precondition", "stale"),
    );
    api.connectWorkspaceServices.mockResolvedValueOnce(connected);
    act(() => result.current.retry());
    await flush();
    expect(api.retryWorkspaceEnrollment).toHaveBeenCalledTimes(1);
    expect(result.current.phase).toBe("connected");
  });

  it("Cancel cancels the exact pending enrollment and returns to not connected; a stale id lands there too", async () => {
    const popup = fakePopup();
    vi.stubGlobal(
      "open",
      vi.fn(() => popup),
    );
    api.connectWorkspaceServices.mockResolvedValue(pendingWithUrl);
    api.cancelWorkspaceEnrollment.mockResolvedValueOnce(settled("cancelled"));
    const { result } = renderEnrollment();
    await flush();
    act(() => result.current.connect());
    await flush();
    expect(result.current.phase).toBe("pending");

    act(() => result.current.cancel());
    await flush();
    expect(api.cancelWorkspaceEnrollment).toHaveBeenCalledWith(
      "s1",
      "e1",
      expect.anything(),
    );
    expect(popup.close).toHaveBeenCalled();
    expect(result.current.phase).toBe("not_connected");
    expect(result.current.enrollmentId).toBe("");
    // No further observes after cancel.
    const calls = api.connectWorkspaceServices.mock.calls.length;
    await tick();
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(calls);

    act(() => result.current.connect());
    await flush();
    api.cancelWorkspaceEnrollment.mockRejectedValueOnce(
      new HarnessApiError(412, "failed_precondition", "stale"),
    );
    act(() => result.current.cancel());
    await flush();
    expect(result.current.phase).toBe("not_connected");
    expect(result.current.error).toBeNull();
  });

  it("a blocked popup is reported, and reopenWindow opens the consent URL from the click", async () => {
    const open = vi.fn<() => FakePopup | null>(() => null);
    vi.stubGlobal("open", open);
    api.connectWorkspaceServices.mockResolvedValue(pendingWithUrl);
    const { result } = renderEnrollment();
    await flush();
    act(() => result.current.connect());
    await flush();
    expect(result.current.phase).toBe("pending");
    expect(result.current.window).toBe("blocked");
    expect(serialized(result.current)).not.toContain(CONSENT_URL);

    const popup = fakePopup();
    open.mockImplementation(() => popup);
    act(() => result.current.reopenWindow());
    expect(open).toHaveBeenLastCalledWith(
      CONSENT_URL,
      "mecatl-workspace-enrollment",
      "width=520,height=680",
    );
    expect(result.current.window).toBe("open");
  });

  it("reports the window closed once the next observe still says pending", async () => {
    const popup = fakePopup();
    vi.stubGlobal(
      "open",
      vi.fn(() => popup),
    );
    api.connectWorkspaceServices
      .mockResolvedValueOnce(pendingWithUrl)
      .mockResolvedValue(pendingObserved);
    const { result } = renderEnrollment();
    await flush();
    act(() => result.current.connect());
    await flush();
    expect(result.current.window).toBe("open");
    popup.closed = true;
    await tick();
    expect(result.current.phase).toBe("pending");
    expect(result.current.window).toBe("closed");
  });

  it("an enrollment pending elsewhere is observed on Check now without a window", async () => {
    api.fetchSessionConnectors.mockResolvedValue(inventory("pending"));
    const popup = fakePopup();
    vi.stubGlobal(
      "open",
      vi.fn(() => popup),
    );
    api.connectWorkspaceServices.mockResolvedValue(pendingObserved);
    const { result } = renderEnrollment();
    await flush();
    expect(result.current.phase).toBe("pending");
    expect(result.current.window).toBe("elsewhere");
    expect(result.current.enrollmentId).toBe("");

    act(() => result.current.connect());
    await flush();
    // The observe form hands back no URL: the blank window is closed again.
    expect(popup.close).toHaveBeenCalled();
    expect(result.current.enrollmentId).toBe("e1");
    expect(result.current.window).toBe("elsewhere");
  });

  it("refuses connect, retry and cancel while the chat is not idle, and skips observes", async () => {
    vi.stubGlobal("open", vi.fn(fakePopup));
    api.connectWorkspaceServices.mockResolvedValue(pendingWithUrl);
    const { result, rerender } = renderEnrollment();
    await flush();
    rerender({ id: "s1", idle: false });
    act(() => result.current.connect());
    await flush();
    expect(api.connectWorkspaceServices).not.toHaveBeenCalled();
    expect(result.current.idle).toBe(false);

    rerender({ id: "s1", idle: true });
    act(() => result.current.connect());
    await flush();
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(1);
    rerender({ id: "s1", idle: false });
    await tick();
    // The observe waits for idle rather than hitting a daemon that refuses.
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(1);
    rerender({ id: "s1", idle: true });
    await tick();
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(2);
  });

  it("a request failure is a failed phase with the daemon's words", async () => {
    const popup = fakePopup();
    vi.stubGlobal(
      "open",
      vi.fn(() => popup),
    );
    api.connectWorkspaceServices.mockRejectedValueOnce(
      new HarnessApiError(
        412,
        "failed_precondition",
        "workspace services are not configured",
      ),
    );
    const { result } = renderEnrollment();
    await flush();
    act(() => result.current.connect());
    await flush();
    expect(result.current.phase).toBe("failed");
    expect(result.current.outcome).toBe("");
    expect(result.current.error).toBe("workspace services are not configured");
    expect(popup.close).toHaveBeenCalled();
  });

  it("dismiss hides the not-connected notice for this session in this tab", async () => {
    const { result } = renderEnrollment();
    await flush();
    expect(result.current.dismissed).toBe(false);
    act(() => result.current.dismiss());
    expect(result.current.dismissed).toBe(true);
    expect(
      window.sessionStorage.getItem("mecatl-studio.enrollment-dismissed:s1"),
    ).toBe("1");
    const again = renderEnrollment();
    await flush();
    expect(again.result.current.dismissed).toBe(true);
  });

  it("remembers a connection this tab saw when the daemon cannot be asked", async () => {
    runtime.serverCapabilities = { workspace_enrollment: true };
    vi.stubGlobal("open", vi.fn(fakePopup));
    api.connectWorkspaceServices.mockResolvedValue(connected);
    const first = renderEnrollment();
    await flush();
    act(() => first.result.current.connect());
    await flush();
    expect(first.result.current.phase).toBe("connected");
    first.unmount();
    const second = renderEnrollment();
    await flush();
    expect(second.result.current.phase).toBe("connected");
  });

  it("resets and re-probes on a session change, retiring the old poll", async () => {
    vi.stubGlobal("open", vi.fn(fakePopup));
    api.connectWorkspaceServices.mockResolvedValue(pendingWithUrl);
    const { result, rerender } = renderEnrollment();
    await flush();
    act(() => result.current.connect());
    await flush();
    expect(result.current.phase).toBe("pending");
    const observes = api.connectWorkspaceServices.mock.calls.length;

    api.fetchSessionConnectors.mockResolvedValue(inventory("completed"));
    rerender({ id: "s2", idle: true });
    await flush();
    expect(api.fetchSessionConnectors).toHaveBeenLastCalledWith(
      "s2",
      expect.anything(),
    );
    expect(result.current.phase).toBe("connected");
    expect(result.current.enrollmentId).toBe("");
    await tick();
    expect(api.connectWorkspaceServices).toHaveBeenCalledTimes(observes);
  });
});
