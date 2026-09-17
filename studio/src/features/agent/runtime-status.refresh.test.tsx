import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resetHarnessClient } from "@/lib/harness/sdk";
import { stubHarnessFetch } from "@/lib/harness/sdk-test-stub";
import { RuntimeStatusProvider, useRuntimeStatus } from "./runtime-status";

/**
 * The runtime-status provider's forced `refresh()` re-reads the daemon's
 * compatibility document, not only its liveness. A controller-side settings
 * save restarts the daemon in one request, so the 5 s poll may never observe
 * it down — and the capabilities the restart changed (learning.mode flips
 * `learning_proposals`) would otherwise stay stale until a reload.
 */

vi.mock("./composer-capabilities", () => ({
  refreshComposerCapabilities: vi.fn(async () => undefined),
}));

function CapabilityProbe() {
  const { state, serverCapabilities, refresh } = useRuntimeStatus();
  return (
    <>
      <output data-testid="probe">
        {state}:{String(serverCapabilities.learning_proposals ?? "unset")}
      </output>
      <button type="button" onClick={() => void refresh()}>
        refresh
      </button>
    </>
  );
}

const live = (request: { path: string }) => {
  if (request.path === "/v1/models") return { models: [] };
  if (request.path === "/api/mecatl-control/status")
    return { mode: "managed", provider: "mock" };
  if (request.path === "/api/auth/oidc/status")
    return { configured: false, state: "not-configured" };
  return undefined;
};

afterEach(async () => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  await resetHarnessClient();
});

describe("RuntimeStatusProvider refresh", () => {
  it("re-reads the compatibility document on a forced refresh", async () => {
    stubHarnessFetch(live, { capabilities: { learning_proposals: false } });
    render(
      <RuntimeStatusProvider>
        <CapabilityProbe />
      </RuntimeStatusProvider>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("probe")).toHaveTextContent("connected:false"),
    );

    // The daemon restarted with learning on: the SAME liveness answer, a
    // different capabilities document.
    stubHarnessFetch(live, { capabilities: { learning_proposals: true } });
    expect(screen.getByTestId("probe")).toHaveTextContent("connected:false");

    await act(async () => {
      screen.getByRole("button", { name: "refresh" }).click();
    });
    await waitFor(() =>
      expect(screen.getByTestId("probe")).toHaveTextContent("connected:true"),
    );
  });
});
