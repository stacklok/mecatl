// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

// Pins for #2299: what the schedule inventory and detail page must keep while
// their presentation moves to the prototype's. Each test passes on the code
// before the alignment and after it.

import { client as apiClient } from "@mecatl-studio/contracts/client";
import type {
  ListScheduleFiresResponse,
  ListSchedulesResponse,
} from "@mecatl-studio/contracts/generated";
import { listSchedulesQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { formatDate } from "../../lib/formatters";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { describeCron } from "./describe-cron";
import { ScheduleDetail } from "./schedule-detail";
import { SchedulesWorkspace } from "./schedules-workspace";

type Schedule = ListSchedulesResponse["items"][number];
type Fire = ListScheduleFiresResponse["items"][number];

// Relative to the clock, so the next run stays in the future (a past one reads overdue).
const NEXT = new Date(Date.now() + 26 * 3_600_000).toISOString();
const LAST = new Date(Date.now() - 26 * 3_600_000).toISOString();

function schedule(overrides: Partial<Schedule> & Pick<Schedule, "name">): Schedule {
  return {
    enabled: true,
    fireCount: 0,
    lastFireAt: "",
    lastFireSessionId: "",
    maxFires: 0,
    mode: "default",
    modelId: "",
    mutating: false,
    nextFireAt: "",
    oneShotMaxRetries: 0,
    oneShotRetry: false,
    owner: "",
    profile: "all",
    prompt: "prompt",
    providerId: "",
    status: "scheduled",
    trigger: { expression: "0 9 * * *", kind: "cron", timezone: "" },
    ...overrides,
  };
}

function fire(overrides: Partial<Fire> & Pick<Fire, "id">): Fire {
  return {
    deadline: "",
    error: "",
    firedAt: "2026-10-07T09:00:00Z",
    inFlight: false,
    outcome: "completed",
    progressAt: "",
    scheduleName: "Alpha",
    sessionId: "",
    startedAt: "2026-10-07T09:00:00Z",
    stop: "end_turn",
    ...overrides,
  };
}

const initialApiConfig = apiClient.getConfig();

afterEach(() => {
  cleanup();
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
});

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "content-type": "application/json" } });
}

/** Fakes the BFF: the schedule list, one schedule's fires, and 204 for any write. */
function fakeApi(items: Schedule[], fires: Fire[] | "pending" = []) {
  apiClient.setConfig({
    baseUrl: window.location.origin,
    fetch: async (input, init) => {
      const request = new Request(input, init);
      const path = new URL(request.url).pathname;
      if (request.method !== "GET") return new Response(null, { status: 204 });
      if (path.endsWith("/fires")) {
        return fires === "pending"
          ? new Promise<Response>(() => {})
          : jsonResponse({ items: fires });
      }
      return jsonResponse({ items, reason: "", supported: true });
    },
  });
}

/** Mounts the list and detail routes; starts on `path`. */
function mount(items: Schedule[], path: string, fires: Fire[] | "pending" = []) {
  fakeApi(items, fires);
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  queryClient.setQueryData<ListSchedulesResponse>(listSchedulesQueryKey(), {
    items,
    reason: "",
    supported: true,
  });
  const root = createRootRoute();
  const list = createRoute({
    component: SchedulesWorkspace,
    getParentRoute: () => root,
    path: "/workspace/schedules",
  });
  const detail = createRoute({
    component: function Detail() {
      const { scheduleName } = detail.useParams();
      return <ScheduleDetail scheduleName={scheduleName} />;
    },
    getParentRoute: () => root,
    path: "/workspace/schedules/$scheduleName",
  });
  const chat = createRoute({
    component: () => <p>chat</p>,
    getParentRoute: () => root,
    path: "/workspace/chat",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [path] }),
    routeTree: root.addChildren([list, detail, chat]),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <ShortcutProvider>
        <RouterProvider router={router} />
      </ShortcutProvider>
    </QueryClientProvider>,
  );
  return { queryClient, router };
}

/**
 * The exact date a `<time dateTime>` inside `element` gives for `instant`, as text a
 * screen reader reads (visible or visually hidden). A native `title` does not count:
 * keyboard and touch users can't reach it.
 */
function exactTime(element: Element, instant: string) {
  const time = element.querySelector(`time[datetime="${instant}"]`);
  return time?.textContent ?? "";
}

async function desktopRow(name: string) {
  const table = await screen.findByRole("table");
  const link = within(table).getByRole("link", { name });
  const row = link.closest("tr");
  if (!row) throw new Error(`no row for ${name}`);
  return row;
}

function mobileList() {
  const list = document.querySelector<HTMLElement>("[data-schedule-mobile-list]");
  if (!list) throw new Error("no mobile list");
  return list;
}

describe("schedule inventory keeps its data and actions", () => {
  const alpha = schedule({
    fireCount: 3,
    lastFireAt: LAST,
    maxFires: 10,
    name: "Alpha",
    nextFireAt: NEXT,
    prompt: "Publish the report",
  });

  it("shows each desktop row's name link, prompt, schedule, run count and status", async () => {
    mount([alpha], "/workspace/schedules");
    const row = await desktopRow("Alpha");
    expect(within(row).getByRole("link", { name: "Alpha" }).getAttribute("href")).toBe(
      "/workspace/schedules/Alpha",
    );
    expect(within(row).getByText("Publish the report")).toBeTruthy();
    expect(within(row).getByText(describeCron("0 9 * * *"))).toBeTruthy();
    expect(within(row).getByText("3/10")).toBeTruthy();
    expect(within(row).getAllByText(/^scheduled$/i).length).toBeGreaterThan(0);
  });

  it("keeps the full next and last run dates in the row's text, not only on hover", async () => {
    mount([alpha], "/workspace/schedules");
    const row = await desktopRow("Alpha");
    expect(exactTime(row, NEXT)).toContain(formatDate(NEXT));
    expect(exactTime(row, LAST)).toContain(formatDate(LAST));
  });

  it("keeps the full last run date in the phone row's text", async () => {
    mount([alpha], "/workspace/schedules");
    await screen.findByRole("table");
    const link = within(mobileList()).getByRole("link");
    expect(exactTime(link, LAST)).toContain(formatDate(LAST));
  });

  it("shows a dash for a paused schedule's next run and Never for one that has not run", async () => {
    mount(
      [schedule({ enabled: false, name: "Idle", nextFireAt: NEXT, status: "paused" })],
      "/workspace/schedules",
    );
    const row = await desktopRow("Idle");
    const cells = within(row).getAllByRole("cell");
    expect(cells[2]?.textContent).toBe("—");
    expect(cells[3]?.textContent).toBe("Never");
  });

  it("keeps the same four row actions behind the row's Actions button", async () => {
    const user = userEvent.setup();
    mount([alpha], "/workspace/schedules");
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "Run now",
      "Pause",
      "Edit",
      "Delete",
    ]);
  });

  it("lets the keyboard reach only the name link and the Actions button in a row", async () => {
    mount([alpha], "/workspace/schedules");
    const row = await desktopRow("Alpha");
    const focusable = [...row.querySelectorAll<HTMLElement>("a[href], button, [tabindex]")];
    expect(
      focusable.map((element) => element.getAttribute("aria-label") ?? element.textContent),
    ).toEqual(["Alpha", "Actions for Alpha"]);
    expect(row.hasAttribute("tabindex")).toBe(false);
  });

  it("opens the detail page from the name link", async () => {
    const user = userEvent.setup();
    const { router } = mount([alpha], "/workspace/schedules");
    await user.click(within(await desktopRow("Alpha")).getByRole("link", { name: "Alpha" }));
    await waitFor(() =>
      expect(router.history.location.pathname).toBe("/workspace/schedules/Alpha"),
    );
  });

  it("links each mobile row to its detail page and names its status for screen readers", async () => {
    mount(
      [alpha, schedule({ enabled: false, name: "Beta", status: "paused" })],
      "/workspace/schedules",
    );
    await screen.findByRole("table");
    const links = within(mobileList()).getAllByRole("link");
    expect(links.map((link) => link.getAttribute("href"))).toEqual([
      "/workspace/schedules/Alpha",
      "/workspace/schedules/Beta",
    ]);
    expect(links[0]?.textContent).toMatch(/scheduled/i);
    expect(links[0]?.textContent).toContain("3/10 runs");
    expect(links[1]?.textContent).toMatch(/paused/i);
    expect(links[1]?.textContent).toContain("never run");
  });

  it("opens the create dialog from the header and returns focus when it closes", async () => {
    const user = userEvent.setup();
    mount([alpha], "/workspace/schedules");
    const create = await screen.findByRole("button", {
      name: /^(Schedule task|New scheduled task)$/,
    });
    await user.click(create);
    expect(screen.getByRole("dialog", { name: "Schedule a task" })).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(document.activeElement).toBe(create));
  });

  it("keeps the search and status-filter empty messages distinct", async () => {
    const user = userEvent.setup();
    mount([alpha], "/workspace/schedules");
    await user.type(await screen.findByRole("textbox", { name: "Filter scheduled tasks" }), "zzz");
    expect(screen.getByRole("status").textContent).toContain("No scheduled tasks match “zzz”.");
    await user.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(screen.queryByRole("status")).toBeNull();

    await user.click(screen.getByRole("radio", { name: /paused/i }));
    expect(screen.getByRole("status").textContent).toContain(
      "No scheduled tasks match this filter.",
    );
    expect(screen.queryByRole("button", { name: "Clear filter" })).toBeNull();
  });
});

describe("schedule detail keeps its data and actions", () => {
  const alpha = schedule({
    fireCount: 2,
    lastFireAt: LAST,
    maxFires: 5,
    mode: "acceptEdits",
    modelId: "model-a",
    mutating: true,
    name: "Alpha",
    nextFireAt: NEXT,
    owner: "operator",
    prompt: "Publish the report",
    providerId: "provider-a",
    trigger: { expression: "0 9 * * *", kind: "cron", timezone: "Europe/Rome" },
  });

  it("shows the name, back link, prompt and every fact", async () => {
    mount([alpha], "/workspace/schedules/Alpha");
    expect(await screen.findByRole("heading", { level: 1, name: "Alpha" })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Back to scheduled/ }).getAttribute("href")).toBe(
      "/workspace/schedules",
    );
    expect(screen.getByText("Publish the report")).toBeTruthy();
    const facts = Object.fromEntries(
      [...document.querySelectorAll("dt")].map((term) => [
        term.textContent,
        term.nextElementSibling?.textContent,
      ]),
    );
    expect(facts).toMatchObject({
      "Last run": expect.stringMatching(/ ago\b/),
      Mode: "Accept edits",
      Model: "provider-a / model-a",
      "Next run": expect.stringMatching(/^in /),
      Owner: "operator",
      Repeat: describeCron("0 9 * * *"),
      Runs: "2 of 5",
      Timezone: "Europe/Rome",
      Tools: "All",
      "Write access": "Writes allowed",
    });
    expect(exactTime(document.body, NEXT)).toContain(formatDate(NEXT));
    expect(exactTime(document.body, LAST)).toContain(formatDate(LAST));
    expect(screen.getByText(describeCron("0 9 * * *")).getAttribute("title")).toBe(
      "0 9 * * * (Europe/Rome)",
    );
  });

  it("names its status for every state", async () => {
    mount(
      [schedule({ enabled: false, name: "Alpha", status: "paused" })],
      "/workspace/schedules/Alpha",
    );
    await screen.findByRole("heading", { level: 1, name: "Alpha" });
    expect(screen.getAllByText(/^paused$/i).length).toBeGreaterThan(0);
  });

  it("keeps the four actions in the header menu", async () => {
    const user = userEvent.setup();
    mount([alpha], "/workspace/schedules/Alpha");
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    expect(screen.getAllByRole("menuitem").map((item) => item.textContent)).toEqual([
      "Run now",
      "Pause",
      "Edit",
      "Delete",
    ]);
  });

  it("announces the fire history while it loads, under its own heading", async () => {
    mount([alpha], "/workspace/schedules/Alpha", "pending");
    const heading = await screen.findByRole("heading", { level: 2, name: /^Run (history|log)$/ });
    const section = heading.closest("section");
    expect(section).toBeTruthy();
    expect(within(section as HTMLElement).getByRole("status")).toBeTruthy();
  });

  it("lists each fire's time, outcome and error", async () => {
    mount([alpha], "/workspace/schedules/Alpha", [
      fire({ firedAt: "2026-10-07T09:00:00Z", id: "ok" }),
      fire({
        error: "Provider unavailable",
        firedAt: "2026-10-06T09:00:00Z",
        id: "bad",
        outcome: "failed",
        stop: "error",
      }),
    ]);
    const table = await screen.findByRole("table");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows.map((row) => within(row).getAllByRole("cell")[0]?.textContent)).toEqual([
      formatDate("2026-10-07T09:00:00Z"),
      formatDate("2026-10-06T09:00:00Z"),
    ]);
    expect(within(rows[0] as HTMLElement).getByText("Completed")).toBeTruthy();
    expect(within(rows[1] as HTMLElement).getByText("Failed")).toBeTruthy();
    expect(within(rows[1] as HTMLElement).getByText("Provider unavailable")).toBeTruthy();
  });
});
