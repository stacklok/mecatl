import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { HarnessRuntimeSettingsDoc } from "@/lib/harness/runtime-settings";
import {
  currentLearning,
  LearningModeSection,
  learningChangeReport,
} from "./learning-mode-section";

/**
 * Settings → Learning → Learning: two plain choices. Pins that (1) the two
 * choices show the EFFECTIVE values (what the agent runs with) with no
 * source/file hints; (2) picking a new value shows what changed and the
 * restart sentence and enables Save, which saves nothing until the confirm
 * — the confirm calls `save` with BOTH values; Cancel and Discard save
 * nothing; (3) an imported operator settings file renders the values
 * read-only with a plain note; (4) external mode renders only the managed
 * note and offline the offline note, neither with a control; (5) busy
 * disables Save; error and notice render.
 */

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

vi.mock("@/features/agent/hooks/use-runtime-settings", () => ({
  useRuntimeSettings: () => runtimeSettings,
}));

const doc = (
  overrides: Partial<{
    studioMode: "" | "off" | "review" | "auto";
    studioSensitivity: "" | "conservative" | "balanced" | "eager";
    inheritedMode: string;
    inheritedSensitivity: string;
    effectiveMode: string;
    effectiveSensitivity: string;
    managedBy: "studio" | "operator-settings";
  }> = {},
): HarnessRuntimeSettingsDoc => ({
  config: {
    learning: {
      mode: overrides.studioMode ?? "",
      sensitivity: overrides.studioSensitivity ?? "",
    },
    steer: { enabled: true },
    soul: { enabled: true, strict: false, file: "" },
  },
  managedBy: {
    learning: overrides.managedBy ?? "studio",
    steer: "studio",
    soul: "studio",
  },
  inherited: {
    learning: {
      mode: overrides.inheritedMode ?? "",
      sensitivity: overrides.inheritedSensitivity ?? "",
    },
    steer: null,
  },
  effective: {
    learning: {
      mode: overrides.effectiveMode ?? "off",
      sensitivity: overrides.effectiveSensitivity ?? "balanced",
    },
    steer: true,
  },
  soulFileDefault: "/home/me/.config/mecatl/soul.md",
  soulCandidates: [],
});

const modeTrigger = () => screen.getByRole("button", { name: "Learning mode" });
const sensitivityTrigger = () =>
  screen.getByRole("button", { name: "Sensitivity" });
const saveButton = () =>
  screen.getByRole("button", { name: "Save and restart" });

async function pick(
  user: ReturnType<typeof userEvent.setup>,
  trigger: HTMLElement,
  label: RegExp,
) {
  await user.click(trigger);
  await user.click(await screen.findByRole("menuitem", { name: label }));
}

beforeEach(() => {
  runtimeSettings.live = true;
  runtimeSettings.manageable = true;
  runtimeSettings.doc = doc();
  runtimeSettings.isLoading = false;
  runtimeSettings.busy = "";
  runtimeSettings.error = null;
  runtimeSettings.notice = null;
  runtimeSettings.save.mockClear();
});

describe("LearningModeSection", () => {
  it("shows the effective mode and sensitivity without source hints, and keeps Save disabled", () => {
    runtimeSettings.doc = doc({
      effectiveMode: "review",
      effectiveSensitivity: "eager",
      inheritedMode: "review",
    });
    render(<LearningModeSection />);
    expect(modeTrigger()).toHaveTextContent("Review");
    expect(sensitivityTrigger()).toHaveTextContent("Eager");
    // Where a value comes from (Studio's file, the operator's file, the
    // default) is not the user's concern.
    expect(screen.queryByText(/settings\.yaml/)).toBeNull();
    expect(screen.queryByText(/Set by Studio/)).toBeNull();
    expect(screen.queryByText(/default/i)).toBeNull();
    expect(saveButton()).toBeDisabled();
    expect(screen.queryByTestId("learning-pending")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Discard" }),
    ).not.toBeInTheDocument();
  });

  it("describes each choice in one plain sentence", async () => {
    const user = userEvent.setup();
    render(<LearningModeSection />);
    await user.click(modeTrigger());
    const modeMenu = await screen.findByRole("menu");
    expect(modeMenu).toHaveTextContent("The agent does not learn from chats.");
    expect(modeMenu).toHaveTextContent(
      "The agent suggests things to remember; you approve each one.",
    );
    expect(modeMenu).toHaveTextContent(
      "The agent suggests things and remembers clear-cut facts on its own.",
    );
    // The old multi-clause explanations are gone.
    expect(modeMenu).not.toHaveTextContent(/reflection provider/);
    expect(modeMenu).not.toHaveTextContent(/standard-policy/);
    await user.keyboard("{Escape}");
    await user.click(sensitivityTrigger());
    const sensitivityMenu = await screen.findByRole("menu");
    expect(sensitivityMenu).toHaveTextContent(
      "Fewer suggestions, only when the evidence is strong.",
    );
    expect(sensitivityMenu).toHaveTextContent(
      "The standard amount of suggestions.",
    );
    expect(sensitivityMenu).toHaveTextContent(
      "More suggestions, with less evidence needed.",
    );
    expect(sensitivityMenu).not.toHaveTextContent(/points/);
  });

  it("shows what changed on a pending change and saves both values only after the confirm", async () => {
    const user = userEvent.setup();
    render(<LearningModeSection />);
    expect(modeTrigger()).toHaveTextContent("Off");

    await pick(user, modeTrigger(), /^Review/);

    expect(modeTrigger()).toHaveTextContent("Review");
    const pending = screen.getByTestId("learning-pending");
    expect(pending).toHaveTextContent("Learning mode: Off → Review");
    expect(pending).not.toHaveTextContent(/Sensitivity/);
    expect(pending).toHaveTextContent(
      "Changes restart the agent. Anything running will stop.",
    );
    expect(saveButton()).toBeEnabled();
    expect(runtimeSettings.save).not.toHaveBeenCalled();

    await user.click(saveButton());
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Save and restart the agent?");
    expect(dialog).toHaveTextContent("Learning mode: Off → Review");
    expect(dialog).toHaveTextContent(
      "Changes restart the agent. Anything running will stop.",
    );
    expect(runtimeSettings.save).not.toHaveBeenCalled();

    await user.click(
      screen.getAllByRole("button", { name: "Save and restart" }).at(-1) ??
        dialog,
    );
    expect(runtimeSettings.save).toHaveBeenCalledTimes(1);
    expect(runtimeSettings.save).toHaveBeenCalledWith({
      learning: { mode: "review", sensitivity: "balanced" },
    });
  });

  it("carries a changed sensitivity together with the (unchanged) mode", async () => {
    const user = userEvent.setup();
    runtimeSettings.doc = doc({ effectiveMode: "auto" });
    render(<LearningModeSection />);
    await pick(user, sensitivityTrigger(), /^Eager/);
    expect(screen.getByTestId("learning-pending")).toHaveTextContent(
      "Sensitivity: Balanced → Eager",
    );
    await user.click(saveButton());
    await screen.findByRole("alertdialog");
    await user.click(
      screen.getAllByRole("button", { name: "Save and restart" }).at(-1) ??
        document.body,
    );
    expect(runtimeSettings.save).toHaveBeenCalledWith({
      learning: { mode: "auto", sensitivity: "eager" },
    });
  });

  it("saves nothing on Cancel, and Discard returns to the effective values", async () => {
    const user = userEvent.setup();
    render(<LearningModeSection />);
    await pick(user, modeTrigger(), /^Auto/);
    await user.click(saveButton());
    await screen.findByRole("alertdialog");
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(runtimeSettings.save).not.toHaveBeenCalled();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    // The draft survives the cancel...
    expect(modeTrigger()).toHaveTextContent("Auto");
    // ...until Discard drops it.
    await user.click(screen.getByRole("button", { name: "Discard" }));
    expect(modeTrigger()).toHaveTextContent("Off");
    expect(saveButton()).toBeDisabled();
    expect(screen.queryByTestId("learning-pending")).not.toBeInTheDocument();
  });

  it("treats picking the current value again as no change", async () => {
    const user = userEvent.setup();
    render(<LearningModeSection />);
    await pick(user, modeTrigger(), /^Review/);
    expect(saveButton()).toBeEnabled();
    await pick(user, modeTrigger(), /^Off/);
    expect(saveButton()).toBeDisabled();
    expect(screen.queryByTestId("learning-pending")).not.toBeInTheDocument();
  });

  it("renders the values read-only with a plain note when an imported settings file owns learning", () => {
    runtimeSettings.doc = doc({
      managedBy: "operator-settings",
      effectiveMode: "review",
      effectiveSensitivity: "conservative",
    });
    render(<LearningModeSection />);
    expect(
      screen.queryByRole("button", { name: "Learning mode" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Save and restart" }),
    ).not.toBeInTheDocument();
    expect(screen.getByText("Review")).toBeInTheDocument();
    expect(screen.getByText("Conservative")).toBeInTheDocument();
    expect(
      screen.getByText(
        /Learning is set where the agent runs and can’t be changed here\./,
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText("learning:")).toBeNull();
  });

  it("renders only the managed note, and no control, in external mode", () => {
    runtimeSettings.manageable = false;
    runtimeSettings.doc = null;
    render(<LearningModeSection />);
    expect(
      screen.getByText(/The agent is run somewhere else/),
    ).toBeInTheDocument();
    expect(screen.queryByText(/settings\.yaml/)).toBeNull();
    expect(screen.queryByText("learning.mode")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Learning mode" }),
    ).not.toBeInTheDocument();
  });

  it("renders the offline note when the runtime is unreachable", () => {
    runtimeSettings.live = false;
    runtimeSettings.doc = null;
    render(<LearningModeSection />);
    expect(
      screen.getByText(/The agent is offline, so these settings/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Learning mode" }),
    ).not.toBeInTheDocument();
  });

  it("says it is loading while the document loads, and names a read failure", () => {
    runtimeSettings.doc = null;
    runtimeSettings.isLoading = true;
    const { unmount } = render(<LearningModeSection />);
    expect(screen.getByText("Loading…")).toBeInTheDocument();
    unmount();

    runtimeSettings.isLoading = false;
    runtimeSettings.error = "controller unreachable";
    render(<LearningModeSection />);
    expect(screen.getByText("controller unreachable")).toBeInTheDocument();

    runtimeSettings.error = null;
    render(<LearningModeSection />);
    expect(
      screen.getByText("Learning settings could not be loaded right now."),
    ).toBeInTheDocument();
  });

  it("disables Save while a save is in flight", async () => {
    const user = userEvent.setup();
    runtimeSettings.busy = "save";
    render(<LearningModeSection />);
    await pick(user, modeTrigger(), /^Review/);
    expect(screen.getByTestId("learning-pending")).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
    expect(screen.getByRole("button", { name: "Discard" })).toBeDisabled();
  });

  it("surfaces the hook's error and completion notice", () => {
    runtimeSettings.doc = doc({
      studioMode: "review",
      effectiveMode: "review",
    });
    runtimeSettings.error = "mecated refused to start";
    runtimeSettings.notice = "Saved. The agent restarted.";
    render(<LearningModeSection />);
    expect(screen.getByRole("alert")).toHaveTextContent(
      "mecated refused to start",
    );
    expect(screen.getByRole("status")).toHaveTextContent(/The agent restarted/);
  });
});

describe("learning helpers", () => {
  it("names only what changed in the pending report", () => {
    expect(
      learningChangeReport(
        { mode: "off", sensitivity: "balanced" },
        { mode: "review", sensitivity: "eager" },
      ),
    ).toBe("Learning mode: Off → Review · Sensitivity: Balanced → Eager");
    expect(
      learningChangeReport(
        { mode: "off", sensitivity: "balanced" },
        { mode: "off", sensitivity: "conservative" },
      ),
    ).toBe("Sensitivity: Balanced → Conservative");
    expect(
      learningChangeReport(
        { mode: "auto", sensitivity: "eager" },
        { mode: "auto", sensitivity: "eager" },
      ),
    ).toBe("");
  });

  it("falls back to the defaults for an unknown effective value", () => {
    expect(
      currentLearning(
        doc({ effectiveMode: "weird", effectiveSensitivity: "" }),
      ),
    ).toEqual({ mode: "off", sensitivity: "balanced" });
  });
});
