import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AboutDaemonCard } from "./about-daemon-card";

/**
 * The About the agent card is ALWAYS rendered: the agent's version (the
 * safe build id when the probe answered, otherwise one plain phrase saying
 * why not) and whether Studio runs it. "Copy details" copies Studio's
 * version, the agent's version and the managed answer as label: value lines
 * for a support request.
 */

const runtime = vi.hoisted(() => ({
  state: "connected" as "connecting" | "connected" | "offline",
  mode: "external" as "managed" | "external",
}));
vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

const probe = vi.hoisted(() => vi.fn());
vi.mock("@/lib/harness/server-info", () => ({
  probeHarnessServerInfo: probe,
}));

const OK_PROBE = {
  info: {
    buildId: "fixture",
    serverImplementation: "fixture-daemon",
    providerEndpoint: "https://openrouter.ai/api/v1",
  },
  lookup: "ok",
};

beforeEach(() => {
  runtime.state = "connected";
  runtime.mode = "external";
  probe.mockReset();
  probe.mockResolvedValue(OK_PROBE);
  vi.stubEnv("NEXT_PUBLIC_STUDIO_VERSION", "0.1.0");
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("AboutDaemonCard", () => {
  it("renders the agent's version and the managed answer when the probe answers", async () => {
    render(<AboutDaemonCard selectedProviderId="openrouter" />);
    expect(screen.getByText("About the agent")).toBeInTheDocument();
    // Before the probe resolves the version cell says it is checking — the
    // card is never blank.
    expect(screen.getByTestId("about-server-build")).toHaveTextContent(
      "Checking…",
    );
    expect(screen.getByTestId("about-server-mode")).toHaveTextContent("No");
    await waitFor(() =>
      expect(screen.getByTestId("about-server-build")).toHaveTextContent(
        "fixture",
      ),
    );
    expect(probe).toHaveBeenCalledWith("openrouter", expect.any(AbortSignal));
    // The technical rows are gone: no implementation family, no endpoint.
    expect(screen.queryByText("fixture-daemon")).toBeNull();
    expect(screen.queryByText("https://openrouter.ai/api/v1")).toBeNull();
    expect(screen.queryByText("Implementation")).toBeNull();
    expect(screen.queryByText("Provider endpoint")).toBeNull();
    expect(screen.queryByText("Posture")).toBeNull();
    expect(screen.queryByText("Deployment")).toBeNull();
  });

  it("answers Yes when Studio runs the agent", async () => {
    runtime.mode = "managed";
    render(<AboutDaemonCard />);
    expect(screen.getByText("Managed by Studio")).toBeInTheDocument();
    expect(screen.getByTestId("about-server-mode")).toHaveTextContent("Yes");
    // Let the probe settle inside the test so its state update is observed.
    await waitFor(() =>
      expect(screen.getByTestId("about-server-build")).toHaveTextContent(
        "fixture",
      ),
    );
  });

  it("reads 'Not available' against an older agent, one that did not answer, or an invalid answer", async () => {
    for (const lookup of ["not-supported", "unreachable", "invalid-response"]) {
      probe.mockResolvedValue({ info: null, lookup });
      const view = render(<AboutDaemonCard />);
      await waitFor(() =>
        expect(screen.getByTestId("about-server-build")).toHaveTextContent(
          "Not available",
        ),
      );
      view.unmount();
    }
  });

  it("reads 'Not available' when the probe answered without a build id", async () => {
    probe.mockResolvedValue({
      info: { buildId: "", serverImplementation: "", providerEndpoint: "" },
      lookup: "ok",
    });
    render(<AboutDaemonCard />);
    await waitFor(() =>
      expect(screen.getByTestId("about-server-build")).toHaveTextContent(
        "Not available",
      ),
    );
  });

  it("renders offline without probing", () => {
    runtime.state = "offline";
    render(<AboutDaemonCard />);
    expect(probe).not.toHaveBeenCalled();
    expect(screen.getByTestId("about-server-build")).toHaveTextContent(
      "The agent is offline",
    );
    // Copy still works offline: Studio's version is worth reporting alone.
    expect(screen.getByRole("button", { name: "Copy details" })).toBeEnabled();
  });

  it("copies Studio's version, the agent's version and the managed answer as label: value lines", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    render(<AboutDaemonCard selectedProviderId="openrouter" />);
    await waitFor(() =>
      expect(screen.getByTestId("about-server-build")).toHaveTextContent(
        "fixture",
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "Copy details" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    expect(writeText.mock.calls[0]?.[0]).toBe(
      [
        "Studio version: 0.1.0",
        "Agent version: fixture",
        "Managed by Studio: No",
      ].join("\n"),
    );
    expect(toast.success).toHaveBeenCalledWith("Details copied");
  });
});
