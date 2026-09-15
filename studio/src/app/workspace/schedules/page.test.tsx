import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ScheduleRow } from "@/lib/protocol";
import WorkspaceSchedulesPage from "./page";

// Rows navigate on click via useRouter; jsdom has no app-router context.
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: vi.fn() }),
}));

/**
 * Pins the pause/resume interaction on the schedules list: the paused row's
 * kebab offers "Resume" wired to resumeJob, the enabled row's offers "Pause"
 * wired to pauseJob — the user-reported "can't unpause" path.
 */

function makeRow(overrides: Partial<ScheduleRow>): ScheduleRow {
  return {
    name: "row",
    prompt: "do the thing",
    cron: "0 9 * * *",
    oneShotAt: null,
    timezone: "",
    profile: "",
    mode: 2,
    mutating: false,
    maxFires: 0,
    limits: { maxTurns: 0, maxToolCalls: 0, maxConsecutiveFailures: 0 },
    oneShotRetry: false,
    oneShotMaxRetries: 0,
    enabled: true,
    fireCount: 0,
    nextFireAt: null,
    lastFireAt: null,
    fireStage: "idle",
    lastFireSessionId: "",
    owner: "",
    carried: {
      selectorProvider: "",
      selectorModel: "",
      misfire: 0,
      singleton: false,
      carryContext: false,
      fireTimeoutSeconds: 0,
      parts: [],
    },
    ...overrides,
  };
}

const { pauseJob, resumeJob } = vi.hoisted(() => ({
  pauseJob: vi.fn(() => Promise.resolve()),
  resumeJob: vi.fn(() => Promise.resolve()),
}));

vi.mock("@/features/agent", () => ({
  useAgentCron: () => ({
    jobs: [],
    // One enabled row and one paused row (enabled:false + fireStage idle is
    // exactly what scheduleStatusOf derives "Paused" from).
    rows: [
      makeRow({ name: "nightly-digest", enabled: true }),
      makeRow({ name: "paused-digest", enabled: false }),
    ],
    isLoading: false,
    isSupported: true,
    notWired: null,
    error: null,
    harnessLive: true,
    createJob: vi.fn(),
    createFromDraft: vi.fn(),
    updateFromDraft: vi.fn(),
    renameAndUpdateFromDraft: vi.fn(),
    runJob: vi.fn(() => Promise.resolve()),
    deleteJob: vi.fn(() => Promise.resolve()),
    pauseJob,
    resumeJob,
    refresh: vi.fn(() => Promise.resolve()),
  }),
}));

describe("schedules list pause/resume actions", () => {
  it("offers Resume on a paused row and calls resumeJob with the row name", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSchedulesPage />);

    await user.click(
      screen.getByRole("button", { name: "Actions for paused-digest" }),
    );
    const resume = await screen.findByRole("menuitem", { name: "Resume" });
    await user.click(resume);

    expect(resumeJob).toHaveBeenCalledWith("paused-digest");
    expect(pauseJob).not.toHaveBeenCalled();
  });

  it("offers Pause on an enabled row and calls pauseJob with the row name", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSchedulesPage />);

    await user.click(
      screen.getByRole("button", { name: "Actions for nightly-digest" }),
    );
    const pause = await screen.findByRole("menuitem", { name: "Pause" });
    await user.click(pause);

    expect(pauseJob).toHaveBeenCalledWith("nightly-digest");
    expect(resumeJob).not.toHaveBeenCalled();
  });
});
