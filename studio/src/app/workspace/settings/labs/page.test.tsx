import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { memoryStorage } from "@/test/memory-storage";
import LabsSettingsPage from "./page";

/**
 * Labs hosts the two browser-local opt-ins: the demo chat and the developer
 * tools. Both write the keys the chat reads, default OFF. The runtime hook is
 * mocked with a mutable shape: most tests stay on the no-op external shape,
 * and the switch-back tests flip it to managed mode idling on the mock
 * provider — the one case where turning the demo chat off calls the agent.
 */

const MOCK_FEATURES_KEY = "mecatl-studio.mock-features";
const DEVELOPER_TOOLS_KEY = "mecatl-studio.developer-tools";

const { runtime, switchProvider } = vi.hoisted(() => {
  const switchProvider = vi.fn(async (_provider: string): Promise<void> => {});
  return {
    switchProvider,
    runtime: {
      mode: "external",
      isMock: false,
      configuredProviders: [] as string[],
      switchProvider,
    },
  };
});

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtime,
}));

function armManagedMock(provider: string) {
  runtime.mode = "managed";
  runtime.isMock = true;
  runtime.configuredProviders = [provider];
}

describe("LabsSettingsPage", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", memoryStorage());
    switchProvider.mockClear();
    runtime.mode = "external";
    runtime.isMock = false;
    runtime.configuredProviders = [];
  });

  it("offers the demo chat off by default and stores the preference via the switch", async () => {
    render(<LabsSettingsPage />);
    const toggle = screen.getByRole("switch", { name: "Show demo chat" });
    expect(toggle).not.toBeChecked();
    expect(window.localStorage.getItem(MOCK_FEATURES_KEY)).toBeNull();

    await userEvent.click(toggle);
    expect(toggle).toBeChecked();
    expect(window.localStorage.getItem(MOCK_FEATURES_KEY)).toBe("1");

    await userEvent.click(toggle);
    expect(toggle).not.toBeChecked();
    expect(window.localStorage.getItem(MOCK_FEATURES_KEY)).toBeNull();
    // Outside managed mock mode the demo chat is a pure browser preference.
    expect(switchProvider).not.toHaveBeenCalled();
  });

  it("offers Developer tools off by default and stores the preference via the switch", async () => {
    render(<LabsSettingsPage />);
    const toggle = screen.getByRole("switch", { name: "Developer tools" });
    expect(toggle).not.toBeChecked();
    expect(window.localStorage.getItem(DEVELOPER_TOOLS_KEY)).toBeNull();

    await userEvent.click(toggle);
    expect(toggle).toBeChecked();
    expect(window.localStorage.getItem(DEVELOPER_TOOLS_KEY)).toBe("1");

    await userEvent.click(toggle);
    expect(toggle).not.toBeChecked();
    expect(window.localStorage.getItem(DEVELOPER_TOOLS_KEY)).toBeNull();
    // The developer switch is a pure browser preference: never calls the agent.
    expect(switchProvider).not.toHaveBeenCalled();
  });

  it("hydrates a stored Developer tools preference as on", async () => {
    window.localStorage.setItem(DEVELOPER_TOOLS_KEY, "1");
    render(<LabsSettingsPage />);
    expect(
      await screen.findByRole("switch", {
        name: "Developer tools",
        checked: true,
      }),
    ).toBeInTheDocument();
  });

  it("describes each switch in one plain sentence and keeps them independent", async () => {
    render(<LabsSettingsPage />);
    expect(
      screen.getByText("Optional features that are still being tested."),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Adds a sample chat you can explore without sending anything.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        "Adds testing commands and technical details to the chat.",
      ),
    ).toBeInTheDocument();

    await userEvent.click(
      screen.getByRole("switch", { name: "Developer tools" }),
    );
    expect(window.localStorage.getItem(MOCK_FEATURES_KEY)).toBeNull();
    expect(
      screen.getByRole("switch", { name: "Show demo chat" }),
    ).not.toBeChecked();
  });

  it("switches the agent back to the configured provider when the demo chat is turned off in managed mock mode", async () => {
    armManagedMock("openrouter");
    let finishSwitch: () => void = () => {};
    switchProvider.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          finishSwitch = resolve;
        }),
    );
    render(<LabsSettingsPage />);
    const toggle = screen.getByRole("switch", { name: "Show demo chat" });

    await userEvent.click(toggle);
    // Turning the demo chat ON never touches the agent.
    expect(switchProvider).not.toHaveBeenCalled();

    await userEvent.click(toggle);
    expect(switchProvider).toHaveBeenCalledWith("openrouter");
    expect(
      screen.getByText("Switching the agent back to OpenRouter…"),
    ).toBeInTheDocument();

    finishSwitch();
    await waitFor(() =>
      expect(
        screen.queryByText("Switching the agent back to OpenRouter…"),
      ).toBeNull(),
    );
  });

  it("shows the error when switching the agent back fails", async () => {
    armManagedMock("openrouter");
    switchProvider.mockRejectedValueOnce(new Error("Could not switch"));
    render(<LabsSettingsPage />);
    const toggle = screen.getByRole("switch", { name: "Show demo chat" });

    await userEvent.click(toggle);
    await userEvent.click(toggle);
    expect(await screen.findByText("Could not switch")).toBeInTheDocument();
  });
});
