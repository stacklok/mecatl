import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { HarnessRuntimeSettingsDoc } from "@/lib/harness/runtime-settings";
import {
  RuntimeBehaviourSection,
  steerRowDescription,
} from "./runtime-behaviour-section";

/**
 * Settings → Agent → Agent behaviour: the operator half of the TUI's steer
 * opt-out, in plain words. Pins that (1) the switch mirrors the saved
 * `steer.enabled`, a flip opens the restart confirm and only the confirm
 * calls `save({ steer: { enabled } })` — Cancel saves nothing; (2) an
 * operator `steer: false` in settings.yaml (inherited) replaces the switch
 * with Off and says why without naming the file's key; (3) external mode
 * renders the managed note and offline the offline note, neither with a
 * switch; (4) the row adds ONE "Right now" line only when the daemon's live
 * `capabilities.steer` differs from the saved switch; (5) the card never
 * shows a developer word.
 */

/** Words the product owner ruled out of this card's copy. */
const JARGON =
  /daemon|mecated|controller|--[a-z]|steer|session id|in-flight|client/i;

const runtimeSettings = vi.hoisted(() => ({
  live: true,
  manageable: true,
  doc: null as HarnessRuntimeSettingsDoc | null,
  isLoading: false,
  busy: "" as "" | "save" | "approve-soul",
  error: null as string | null,
  notice: null as string | null,
  save: vi.fn(async () => true),
}));

const runtimeStatus = vi.hoisted(() => ({
  serverCapabilities: {} as Record<string, unknown>,
}));

vi.mock("@/features/agent/hooks/use-runtime-settings", () => ({
  useRuntimeSettings: () => runtimeSettings,
}));

vi.mock("@/features/agent/runtime-status", () => ({
  useRuntimeStatus: () => runtimeStatus,
}));

const SWITCH = "Take messages while working";

const doc = (
  overrides: Partial<{
    enabled: boolean;
    inheritedSteer: boolean | null;
  }> = {},
): HarnessRuntimeSettingsDoc => {
  const enabled = overrides.enabled ?? true;
  const inheritedSteer = overrides.inheritedSteer ?? null;
  return {
    config: {
      learning: { mode: "", sensitivity: "" },
      steer: { enabled },
      soul: { enabled: true, strict: false, file: "" },
    },
    managedBy: { learning: "studio", steer: "studio", soul: "studio" },
    inherited: {
      learning: { mode: "", sensitivity: "" },
      steer: inheritedSteer,
    },
    effective: {
      learning: { mode: "off", sensitivity: "balanced" },
      steer: enabled && inheritedSteer !== false,
    },
    soulFileDefault: "/home/me/.config/mecatl/soul.md",
    soulCandidates: [],
  };
};

beforeEach(() => {
  runtimeSettings.live = true;
  runtimeSettings.manageable = true;
  runtimeSettings.doc = doc();
  runtimeSettings.isLoading = false;
  runtimeSettings.busy = "";
  runtimeSettings.error = null;
  runtimeSettings.notice = null;
  runtimeSettings.save.mockClear();
  runtimeStatus.serverCapabilities = {};
});

describe("RuntimeBehaviourSection", () => {
  it("mirrors the saved value and saves steer off only after the restart confirm", async () => {
    const user = userEvent.setup();
    render(<RuntimeBehaviourSection />);
    const toggle = screen.getByRole("switch", { name: SWITCH });
    expect(toggle).toBeChecked();

    await user.click(toggle);
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Turn this off?");
    expect(dialog).toHaveTextContent(
      "Changes restart the agent. Anything running will stop.",
    );
    // Nothing is saved (and the switch does not move) until the confirm.
    expect(runtimeSettings.save).not.toHaveBeenCalled();
    expect(toggle).toBeChecked();

    await user.click(screen.getByRole("button", { name: "Save and restart" }));
    expect(runtimeSettings.save).toHaveBeenCalledTimes(1);
    expect(runtimeSettings.save).toHaveBeenCalledWith({
      steer: { enabled: false },
    });
  });

  it("saves nothing when the confirm is cancelled", async () => {
    const user = userEvent.setup();
    render(<RuntimeBehaviourSection />);
    await user.click(screen.getByRole("switch", { name: SWITCH }));
    await screen.findByRole("alertdialog");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(runtimeSettings.save).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("offers to turn it back on when it is saved off", async () => {
    const user = userEvent.setup();
    runtimeSettings.doc = doc({ enabled: false });
    render(<RuntimeBehaviourSection />);
    const toggle = screen.getByRole("switch", { name: SWITCH });
    expect(toggle).not.toBeChecked();
    await user.click(toggle);
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(
      "Turn this on?",
    );
    await user.click(screen.getByRole("button", { name: "Save and restart" }));
    expect(runtimeSettings.save).toHaveBeenCalledWith({
      steer: { enabled: true },
    });
  });

  it("replaces the switch with Off and explains an operator steer: false in plain words", () => {
    runtimeSettings.doc = doc({ enabled: true, inheritedSteer: false });
    render(<RuntimeBehaviourSection />);
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
    expect(screen.getByText("Off")).toBeInTheDocument();
    expect(
      screen.getByText(/turned off where the agent runs/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/steer: false/)).toBeNull();
  });

  it("adds the Right-now line only when the live capability differs, and surfaces error and notice", () => {
    runtimeStatus.serverCapabilities = { steer: false };
    runtimeSettings.error = "mecated refused to start";
    runtimeSettings.notice = "Saved.";
    render(<RuntimeBehaviourSection />);
    expect(screen.getByText(/Right now this is off\./)).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "mecated refused to start",
    );
    expect(screen.getByRole("status")).toHaveTextContent("Saved.");
  });

  it("uses no developer vocabulary on the card or in the confirm", async () => {
    const user = userEvent.setup();
    runtimeStatus.serverCapabilities = { steer: false };
    const { container } = render(<RuntimeBehaviourSection />);
    expect(container.textContent).not.toMatch(JARGON);
    await user.click(screen.getByRole("switch", { name: SWITCH }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog.textContent).not.toMatch(JARGON);
  });

  it("disables the switch while a save is in flight", () => {
    runtimeSettings.busy = "save";
    render(<RuntimeBehaviourSection />);
    expect(screen.getByRole("switch", { name: SWITCH })).toBeDisabled();
  });

  it("renders the managed note, not a switch, in external mode", () => {
    runtimeSettings.manageable = false;
    runtimeSettings.doc = null;
    render(<RuntimeBehaviourSection />);
    expect(
      screen.getByText(/The agent is run somewhere else/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });

  it("renders the offline note when the runtime is unreachable", () => {
    runtimeSettings.live = false;
    runtimeSettings.doc = null;
    render(<RuntimeBehaviourSection />);
    expect(
      screen.getByText(/The agent is offline, so these settings/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("switch")).not.toBeInTheDocument();
  });

  it("says it is reading while the document loads, and names a read failure", () => {
    runtimeSettings.doc = null;
    runtimeSettings.isLoading = true;
    const { unmount } = render(<RuntimeBehaviourSection />);
    expect(
      screen.getByText(/Reading the agent.s settings/),
    ).toBeInTheDocument();
    unmount();

    runtimeSettings.isLoading = false;
    runtimeSettings.error = "controller unreachable";
    render(<RuntimeBehaviourSection />);
    expect(screen.getByText("controller unreachable")).toBeInTheDocument();
  });
});

describe("steerRowDescription", () => {
  const base = "Let the agent take your messages while it is still working.";

  it("is the plain two-part sentence when the daemon reports nothing", () => {
    expect(steerRowDescription(undefined, true)).toBe(base);
    expect(steerRowDescription("yes", false)).toBe(base);
  });

  it("stays silent when the live value agrees with the switch", () => {
    expect(steerRowDescription(true, true)).toBe(base);
    expect(steerRowDescription(false, false)).toBe(base);
  });

  it("adds one Right-now line when the live value differs", () => {
    expect(steerRowDescription(false, true)).toBe(
      `${base} Right now this is off.`,
    );
    expect(steerRowDescription(true, false)).toBe(
      `${base} Right now this is on.`,
    );
  });
});
