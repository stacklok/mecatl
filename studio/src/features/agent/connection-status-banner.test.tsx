import type { ConnectionStatus } from "@stacklok-oss/mecatl-sdk";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SIGN_IN_SETTINGS_HREF } from "./auth-recovery-banner";
import {
  ConnectionStatusBanner,
  RECONNECTING_TITLE,
  UNAUTHORIZED_TITLE,
} from "./connection-status-banner";

/**
 * The live-feed banner: a reconnecting feed shows an amber status strip,
 * a refused credential shows a destructive alert that re-probes the runtime
 * ONCE per transition and offers the mode's remedy (sign-in settings in
 * external mode, a daemon restart in managed mode), every other status —
 * and any status while the runtime banner already owns the band — renders
 * nothing.
 */

const state = vi.hoisted(() => ({
  status: "online" as ConnectionStatus,
  runtime: {
    state: "connected" as "connecting" | "connected" | "offline",
    mode: "external" as "managed" | "external",
    refresh: vi.fn(async () => undefined),
  },
  restartHarnessDaemon: vi.fn(async () => undefined),
}));

vi.mock("./hooks/use-connection-status", () => ({
  useConnectionStatus: () => state.status,
}));

vi.mock("./runtime-status", () => ({
  useRuntimeStatus: () => state.runtime,
}));

vi.mock("@/lib/harness/client", () => ({
  restartHarnessDaemon: state.restartHarnessDaemon,
}));

beforeEach(() => {
  state.status = "online";
  state.runtime.state = "connected";
  state.runtime.mode = "external";
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("ConnectionStatusBanner", () => {
  it("renders nothing while the feed is online, connecting, offline or incompatible", () => {
    for (const status of [
      "online",
      "connecting",
      "offline",
      "incompatible",
    ] as const) {
      state.status = status;
      const { container, unmount } = render(<ConnectionStatusBanner />);
      expect(container).toBeEmptyDOMElement();
      unmount();
    }
    expect(state.runtime.refresh).not.toHaveBeenCalled();
  });

  it("shows a polite amber status strip while a durable watch reconnects", () => {
    state.status = "reconnecting";
    render(<ConnectionStatusBanner />);
    const strip = screen.getByRole("status");
    expect(strip).toHaveAttribute("data-connection-status", "reconnecting");
    expect(strip).toHaveTextContent(RECONNECTING_TITLE);
    expect(strip).toHaveTextContent(/reconnecting from the last event/);
    // The own-prompt-stream residual is named, not hidden.
    expect(strip).toHaveTextContent(/A run this tab started is not resumed/);
    fireEvent.click(screen.getByRole("button", { name: "Check connection" }));
    expect(state.runtime.refresh).toHaveBeenCalledTimes(1);
  });

  it("on unauthorized re-probes the runtime once and links to the sign-in settings in external mode", () => {
    state.status = "unauthorized";
    const { rerender } = render(<ConnectionStatusBanner />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveAttribute("data-connection-status", "unauthorized");
    expect(alert).toHaveTextContent(UNAUTHORIZED_TITLE);
    // The credential is server-held: the copy never claims a browser login
    // repairs an env token, and the remedy names the operator for that case.
    expect(alert).toHaveTextContent(/rejected static token needs the operator/);
    expect(
      screen.getByRole("link", { name: "Open sign-in settings" }),
    ).toHaveAttribute("href", SIGN_IN_SETTINGS_HREF);
    expect(screen.queryByRole("button", { name: "Restart daemon" })).toBeNull();
    // One re-probe per transition, not one per render.
    expect(state.runtime.refresh).toHaveBeenCalledTimes(1);
    rerender(<ConnectionStatusBanner />);
    expect(state.runtime.refresh).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(state.runtime.refresh).toHaveBeenCalledTimes(2);
  });

  it("re-probes again after the feed recovers and is refused a second time", () => {
    state.status = "unauthorized";
    const { rerender } = render(<ConnectionStatusBanner />);
    expect(state.runtime.refresh).toHaveBeenCalledTimes(1);
    state.status = "online";
    rerender(<ConnectionStatusBanner />);
    state.status = "unauthorized";
    rerender(<ConnectionStatusBanner />);
    expect(state.runtime.refresh).toHaveBeenCalledTimes(2);
  });

  it("offers a daemon restart in managed mode and re-probes after it", async () => {
    state.status = "unauthorized";
    state.runtime.mode = "managed";
    render(<ConnectionStatusBanner />);
    expect(screen.getByRole("alert")).toHaveTextContent(
      /Restarting the daemon issues it a fresh token/,
    );
    expect(screen.queryByRole("link")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Restart daemon" }));
    await waitFor(() =>
      expect(state.restartHarnessDaemon).toHaveBeenCalledTimes(1),
    );
    // The mount re-probe plus the post-restart one.
    await waitFor(() => expect(state.runtime.refresh).toHaveBeenCalledTimes(2));
  });

  it("reports a failed restart in the strip instead of swallowing it", async () => {
    state.status = "unauthorized";
    state.runtime.mode = "managed";
    state.restartHarnessDaemon.mockRejectedValueOnce(
      new Error("controller refused: external mode"),
    );
    render(<ConnectionStatusBanner />);
    fireEvent.click(screen.getByRole("button", { name: "Restart daemon" }));
    await waitFor(() =>
      expect(screen.getByRole("alert")).toHaveTextContent(
        "controller refused: external mode",
      ),
    );
  });

  it("stays quiet while the runtime banner already owns the band", () => {
    state.status = "reconnecting";
    state.runtime.state = "offline";
    const { container, rerender } = render(<ConnectionStatusBanner />);
    expect(container).toBeEmptyDOMElement();
    state.status = "unauthorized";
    rerender(<ConnectionStatusBanner />);
    expect(container).toBeEmptyDOMElement();
  });
});
