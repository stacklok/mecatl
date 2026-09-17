import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ScheduleRow } from "@/lib/protocol";
import ScheduleDetailPage from "./page";

/**
 * Pins the detail page's "Runs" fact — where the TUI inspect's `fire_count` and
 * `max_fires` land in Studio — and the absolute-instant title on "Last run".
 */

// The route segment is read from useParams; a mutable holder lets each test
// point the page at a different row without re-mocking the module.
const { params } = vi.hoisted(() => ({
  params: { scheduleId: "nightly-digest" },
}));

vi.mock("next/navigation", () => ({
  useParams: () => params,
  useRouter: () => ({ push: vi.fn() }),
}));

// The run log is read straight off the daemon; an empty log keeps the page on
// its "has not run yet" branch so only the fact groups are under test.
vi.mock("@/lib/harness/client", () => ({
  listScheduleFires: vi.fn(() => Promise.resolve([])),
  fetchSessionTranscriptMessages: vi.fn(() => Promise.resolve([])),
}));

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

const LAST_FIRE_AT = Date.now() - 3 * 60 * 60 * 1000;

vi.mock("@/features/agent", () => ({
  useAgentCron: () => ({
    jobs: [],
    rows: [
      makeRow({
        name: "nightly-digest",
        fireCount: 3,
        maxFires: 10,
        lastFireAt: LAST_FIRE_AT,
      }),
      makeRow({ name: "fresh-digest", fireCount: 0 }),
      makeRow({ name: "uncapped-digest", fireCount: 7 }),
      makeRow({
        name: "spent-digest",
        fireCount: 10,
        maxFires: 10,
        enabled: true,
      }),
      makeRow({ name: "nofs-digest", profile: "no-fs" }),
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
    pauseJob: vi.fn(() => Promise.resolve()),
    resumeJob: vi.fn(() => Promise.resolve()),
    refresh: vi.fn(() => Promise.resolve()),
  }),
}));

/** The value cell of the fact row labelled `label`. */
function factValue(label: string): HTMLElement {
  const labelEl = screen.getByText(label, { selector: "span" });
  const value = labelEl.nextElementSibling;
  if (!(value instanceof HTMLElement))
    throw new Error(`fact "${label}" has no value cell`);
  return value;
}

/**
 * Renders the detail page for `scheduleId` and waits for the run-log read to
 * settle (its resolved empty log is a state update the test must absorb).
 */
async function renderDetail(scheduleId: string) {
  params.scheduleId = scheduleId;
  render(<ScheduleDetailPage />);
  await screen.findByText("This schedule has not run yet.");
}

describe("schedule detail Runs fact", () => {
  it("reads the count against its cap", async () => {
    await renderDetail("nightly-digest");
    expect(factValue("Runs")).toHaveTextContent("3 of 10");
  });

  it("reads None yet before the first fire", async () => {
    await renderDetail("fresh-digest");
    expect(factValue("Runs")).toHaveTextContent("None yet");
  });

  it("reads the bare count when the spec sets no cap", async () => {
    await renderDetail("uncapped-digest");
    expect(factValue("Runs")).toHaveTextContent(/^7$/);
  });

  it("says the limit is reached once the cap is used up", async () => {
    await renderDetail("spent-digest");
    expect(factValue("Runs")).toHaveTextContent("10 of 10 — limit reached");
  });
});

/**
 * The fire session's TOOL PROFILE (the spec's `profile`): the Details group
 * names it, and an attenuated schedule also gets a badge in the header's
 * pill row — the list otherwise gives no hint that a schedule runs
 * file-less.
 */
describe("schedule detail Tools fact", () => {
  it("reads All tools with no badge for the default profile", async () => {
    await renderDetail("nightly-digest");
    expect(factValue("Tools")).toHaveTextContent("All tools");
    expect(screen.queryByText("no filesystem")).toBeNull();
  });

  it("reads No filesystem and shows the badge for a no-fs schedule", async () => {
    await renderDetail("nofs-digest");
    expect(factValue("Tools")).toHaveTextContent("No filesystem");
    expect(screen.getByText("no filesystem")).toBeInTheDocument();
  });
});

describe("schedule detail Last run fact", () => {
  it("shows the relative time with the absolute instant as its title", async () => {
    await renderDetail("nightly-digest");
    const value = factValue("Last run");
    expect(value).toHaveTextContent("3h ago");
    expect(value.querySelector("span")).toHaveAttribute(
      "title",
      new Date(LAST_FIRE_AT).toLocaleString(),
    );
  });

  it("reads Never with no title before the first fire", async () => {
    await renderDetail("fresh-digest");
    const value = factValue("Last run");
    expect(value).toHaveTextContent("Never");
    expect(value.querySelector("span")).not.toHaveAttribute("title");
  });
});
