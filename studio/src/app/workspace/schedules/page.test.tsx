import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { ScheduleRow } from "@/lib/protocol";
import { ShortcutsProvider } from "@/lib/shortcuts/use-shortcuts";
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

/** Two hours before the test ran: formatRelativeTime reads it as "2h". */
const TWO_HOURS_AGO = Date.now() - 2 * 60 * 60 * 1000;

vi.mock("@/features/agent", () => ({
  useAgentCron: () => ({
    jobs: [],
    // One enabled row and one paused row (enabled:false + fireStage idle is
    // exactly what scheduleStatusOf derives "Paused" from). The enabled row
    // has fired (uncapped); the paused one is capped and has never fired.
    rows: [
      makeRow({
        name: "nightly-digest",
        prompt: "Summarise the day",
        enabled: true,
        fireCount: 3,
        lastFireAt: TWO_HOURS_AGO,
      }),
      makeRow({
        name: "paused-digest",
        prompt: "Weekly recap",
        enabled: false,
        fireCount: 3,
        maxFires: 10,
        lastFireAt: null,
      }),
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

const FILTER_PLACEHOLDER = "Filter by name or schedule";

/**
 * Pins the fire-count and last-run columns (the TUI row's `fires:` / `last:`
 * segments) on the desktop table and the mobile cell.
 */
describe("schedules list fire count and last run", () => {
  it("renders Last run and Runs columns with the count against its cap", () => {
    render(<WorkspaceSchedulesPage />);
    const table = within(screen.getByRole("table"));

    expect(table.getByRole("button", { name: "Last run" })).toBeInTheDocument();
    expect(table.getByRole("button", { name: "Runs" })).toBeInTheDocument();

    // Uncapped: the bare count. Capped: count/cap, with the long form as title.
    expect(table.getByText("3")).toBeInTheDocument();
    const capped = table.getByText("3/10");
    expect(capped).toHaveAttribute("title", "3 of 10 runs");

    // Last run: relative for a fired row (absolute instant as title), "Never"
    // for one that has not fired.
    const lastRun = table.getByText("2h ago");
    expect(lastRun).toHaveAttribute(
      "title",
      new Date(TWO_HOURS_AGO).toLocaleString(),
    );
    expect(table.getByText("Never")).toBeInTheDocument();
  });

  it("summarises runs on the mobile cell", () => {
    render(<WorkspaceSchedulesPage />);
    expect(screen.getByText("3 runs · last 2h ago")).toBeInTheDocument();
    expect(screen.getByText("3/10 runs")).toBeInTheDocument();
  });
});

/**
 * Pins the in-list text filter (the TUI's `/` mode): name + trigger summary
 * per the TUI contract, prompt as a Studio extra, the no-match copy with its
 * reset, the two-stage Escape, and the `/` shortcut that focuses it.
 */
describe("schedules list text filter", () => {
  it("narrows the list to rows whose name matches", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSchedulesPage />);

    await user.type(screen.getByPlaceholderText(FILTER_PLACEHOLDER), "paused");

    expect(
      screen.getByRole("button", { name: "Actions for paused-digest" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Actions for nightly-digest" }),
    ).not.toBeInTheDocument();
  });

  it("matches the plain-English trigger summary and the prompt", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSchedulesPage />);
    const filter = screen.getByPlaceholderText(FILTER_PLACEHOLDER);

    // Both rows are "0 9 * * *" → "Daily at 9:00 AM"-ish; "daily" keeps both.
    await user.type(filter, "daily");
    expect(
      screen.getByRole("button", { name: "Actions for nightly-digest" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Actions for paused-digest" }),
    ).toBeInTheDocument();

    // A word of the prompt (case-insensitive) keeps only its row.
    await user.clear(filter);
    await user.type(filter, "SUMMARISE");
    expect(
      screen.getByRole("button", { name: "Actions for nightly-digest" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Actions for paused-digest" }),
    ).not.toBeInTheDocument();
  });

  it("says which query excluded everything and offers to clear it", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSchedulesPage />);

    await user.type(screen.getByPlaceholderText(FILTER_PLACEHOLDER), "zzz");
    expect(
      screen.getByText("No scheduled tasks match “zzz”."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(screen.getByPlaceholderText(FILTER_PLACEHOLDER)).toHaveValue("");
    expect(
      screen.getByRole("button", { name: "Actions for nightly-digest" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Actions for paused-digest" }),
    ).toBeInTheDocument();
  });

  it("keeps the pills-only wording when no text query is set", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSchedulesPage />);

    // "Paused" pill + a name only the enabled row has → nothing left, but the
    // copy names the query, not the pill; clearing the query brings the
    // paused row back under the still-active pill.
    await user.click(screen.getByRole("button", { name: "Paused" }));
    await user.type(screen.getByPlaceholderText(FILTER_PLACEHOLDER), "nightly");
    expect(
      screen.getByText("No scheduled tasks match “nightly”."),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(
      screen.getByRole("button", { name: "Actions for paused-digest" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Actions for nightly-digest" }),
    ).not.toBeInTheDocument();
  });

  it("clears on Escape with a query, leaves the field on a second Escape", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSchedulesPage />);
    const filter = screen.getByPlaceholderText(FILTER_PLACEHOLDER);

    await user.type(filter, "zzz");
    expect(filter).toHaveValue("zzz");
    await user.keyboard("{Escape}");
    expect(filter).toHaveValue("");
    expect(filter).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(filter).not.toHaveFocus();
  });

  it("focuses the filter on `/` without typing the slash into it", async () => {
    const user = userEvent.setup();
    render(
      <ShortcutsProvider>
        <WorkspaceSchedulesPage />
      </ShortcutsProvider>,
    );
    const filter = screen.getByPlaceholderText(FILTER_PLACEHOLDER);
    expect(filter).not.toHaveFocus();

    await user.keyboard("/");
    expect(filter).toHaveFocus();
    expect(filter).toHaveValue("");

    // Once the caret is in the field a slash is plain text: the dispatcher
    // suppresses bare keys while typing, so it lands in the query.
    await user.keyboard("/");
    expect(filter).toHaveValue("/");
  });
});

/**
 * Pins the create dialog's write-access opt-in (the TUI form's y/n mutating
 * toggle): the switch opens unchecked and the submit stays gated on name and
 * prompt whichever way it is set.
 */
describe("new scheduled task dialog write access", () => {
  it("opens with the switch off and the submit gated on name and prompt", async () => {
    const user = userEvent.setup();
    render(<WorkspaceSchedulesPage />);

    await user.click(
      screen.getByRole("button", { name: /New scheduled task/ }),
    );
    const dialog = await screen.findByRole("dialog");
    const writes = within(dialog).getByRole("switch", {
      name: "Allow file and shell writes",
    });
    expect(writes).not.toBeChecked();
    expect(
      within(dialog).getByRole("button", { name: "Create task" }),
    ).toBeDisabled();

    // Opting in reveals the mode picker but does not unlock the submit: the
    // name and prompt gate is independent of the posture choice.
    await user.click(writes);
    expect(writes).toBeChecked();
    expect(
      within(dialog).getByRole("combobox", { name: "Permission mode" }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Create task" }),
    ).toBeDisabled();
  });
});
