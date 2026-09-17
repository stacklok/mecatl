import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { HarnessDaemonLog } from "@/lib/harness/client";
import {
  DaemonLogCard,
  EXTERNAL_LOG_NOTE,
  LOG_UNAVAILABLE_NOTE,
} from "./daemon-log-card";

/**
 * Settings → Diagnostics → "Logs". Pins that (1) managed mode shows the
 * tail as PLAIN TEXT (log content is model-influenced) with a same-origin
 * Download link, (2) a start the agent refused is reported above the tail
 * even while the agent is unreachable (the controller still answers),
 * (3) Refresh re-reads and Copy copies the tail, and (4) external, offline,
 * missing-route, failed and empty states render notes, not a viewer.
 */

const { runtime, fetchHarnessDaemonLog } = vi.hoisted(() => ({
  runtime: {
    connected: true,
    mode: "managed" as "managed" | "external",
  },
  fetchHarnessDaemonLog: vi.fn(),
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

vi.mock("@/lib/harness/client", () => ({
  fetchHarnessDaemonLog,
  DAEMON_LOG_DEFAULT_LINES: 200,
  DAEMON_LOG_DOWNLOAD_URL: "/api/mecatl-control/logs/download",
}));

const sampleLog: HarnessDaemonLog = {
  path: "/repo/studio/.scratch/mecated.log",
  rotatedPath: "/repo/studio/.scratch/mecated.log.1",
  sizeBytes: 4_096,
  maxBytes: 10 * 1024 * 1024,
  lines: [
    "time=1 level=INFO msg=ready",
    'time=2 level=WARN msg="provider retry" <b>not markup</b>',
  ],
  truncated: true,
  quiet: false,
  level: "info",
  running: true,
  startupError: "",
};

beforeEach(() => {
  runtime.connected = true;
  runtime.mode = "managed";
  fetchHarnessDaemonLog.mockReset();
  fetchHarnessDaemonLog.mockResolvedValue(sampleLog);
});

describe("DaemonLogCard", () => {
  it("renders the tail as plain text with a same-origin download link and no file details", async () => {
    const { container } = render(<DaemonLogCard />);
    const tail = await screen.findByTestId("daemon-log-tail");

    expect(fetchHarnessDaemonLog).toHaveBeenCalledWith(
      200,
      expect.any(AbortSignal),
    );
    // Model-influenced content renders as text: the literal tag is visible
    // and no element was created from it.
    expect(tail).toHaveTextContent("<b>not markup</b>");
    expect(container.querySelector("pre b")).toBeNull();
    expect(tail).toHaveAttribute("tabindex", "0");
    expect(tail).toHaveAccessibleName("Recent log lines");
    expect(
      screen.getByText(
        /Last 2 lines\. Older messages are in the downloaded file\./,
      ),
    ).toBeInTheDocument();

    const download = screen.getByTestId("daemon-log-download");
    expect(download).toHaveAttribute(
      "href",
      "/api/mecatl-control/logs/download",
    );
    expect(download).toHaveAccessibleName("Download logs");

    // The developer-only controls are gone: no path, no level, no mute.
    expect(screen.queryByText(sampleLog.path)).toBeNull();
    expect(screen.queryByRole("switch")).toBeNull();
    expect(screen.queryByRole("button", { name: "Log level" })).toBeNull();
    expect(screen.queryByText(/daemon|mecated/i)).toBeNull();
  });

  it("reports a start the agent refused above the tail even while the agent is unreachable", async () => {
    runtime.connected = false;
    fetchHarnessDaemonLog.mockResolvedValue({
      ...sampleLog,
      running: false,
      startupError:
        "OpenRouter could not start. Add providers.openrouter.api_key to auth.yaml, then switch to it again.",
    });
    render(<DaemonLogCard />);
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(
      /The agent could not start: OpenRouter could not start/,
    );
    expect(screen.getByTestId("daemon-log-tail")).toBeInTheDocument();
    expect(screen.getByText(/The agent is not running\./)).toBeInTheDocument();
    expect(
      screen.queryByText(/The agent is offline, so these settings/),
    ).toBeNull();
  });

  it("refreshes on demand and copies the tail to the clipboard", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    render(<DaemonLogCard />);
    await screen.findByTestId("daemon-log-tail");
    expect(fetchHarnessDaemonLog).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: /Refresh/ }));
    await waitFor(() => expect(fetchHarnessDaemonLog).toHaveBeenCalledTimes(2));

    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith(sampleLog.lines.join("\n"));
  });

  it("renders the external note and never reads the controller in external mode", () => {
    runtime.mode = "external";
    render(<DaemonLogCard />);
    expect(screen.getByText(EXTERNAL_LOG_NOTE)).toBeInTheDocument();
    expect(fetchHarnessDaemonLog).not.toHaveBeenCalled();
    expect(screen.queryByTestId("daemon-log-tail")).toBeNull();
  });

  it("renders the offline note when neither the agent nor the controller answers", async () => {
    runtime.connected = false;
    fetchHarnessDaemonLog.mockResolvedValue(null);
    render(<DaemonLogCard />);
    expect(
      await screen.findByText(/The agent is offline, so these settings/),
    ).toBeVisible();
    expect(screen.queryByTestId("daemon-log-tail")).toBeNull();
  });

  it("renders the unavailable note when the agent is up but the route is missing", async () => {
    fetchHarnessDaemonLog.mockResolvedValue(null);
    render(<DaemonLogCard />);
    expect(await screen.findByText(LOG_UNAVAILABLE_NOTE)).toBeInTheDocument();
  });

  it("surfaces a controller failure as the note", async () => {
    fetchHarnessDaemonLog.mockRejectedValue(new Error("controller exploded"));
    render(<DaemonLogCard />);
    expect(await screen.findByText("controller exploded")).toBeInTheDocument();
  });

  it("disables Download and Copy while nothing has been written", async () => {
    fetchHarnessDaemonLog.mockResolvedValue({
      ...sampleLog,
      sizeBytes: 0,
      lines: [],
      truncated: false,
    });
    render(<DaemonLogCard />);
    const tail = await screen.findByTestId("daemon-log-tail");
    expect(tail).toHaveTextContent("No messages yet.");
    expect(screen.queryByTestId("daemon-log-download")).toBeNull();
    expect(
      screen.getByRole("button", { name: /Download logs/ }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "Copy" })).toBeDisabled();
  });
});
