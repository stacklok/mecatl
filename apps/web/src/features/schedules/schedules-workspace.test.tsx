// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import { listScheduleFiresQueryKey, listSchedulesQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterContextProvider,
  RouterProvider,
} from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { ScheduleDetail } from "./schedule-detail";
import { SchedulesWorkspace, scheduleInventoryPolling } from "./schedules-workspace";

type Schedule = ListSchedulesResponse["items"][number];

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

const initialApiConfig = apiClient.getConfig();

afterEach(() => {
  cleanup();
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
});

function problem(detail: string, status = 500) {
  return new Response(
    JSON.stringify({ code: "", detail, instance: "", status, title: "", type: "" }),
    {
      headers: { "content-type": "application/problem+json" },
      status,
    },
  );
}

/** Fakes the BFF; a handler returns undefined for "204 No Content". */
function fakeApi(
  handler: (method: string, path: string) => Response | Promise<Response> | undefined,
) {
  apiClient.setConfig({
    baseUrl: window.location.origin,
    fetch: async (input, init) => {
      const request = new Request(input, init);
      const answer = handler(request.method, new URL(request.url).pathname);
      return (await answer) ?? new Response(null, { status: 204 });
    },
  });
}

function listResponse(items: Schedule[]) {
  return jsonResponse({ items, reason: "", supported: true });
}

function jsonResponse(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "content-type": "application/json" } });
}

describe("schedule inventory controls", () => {
  it("polls every five seconds without polling background tabs", () => {
    expect(scheduleInventoryPolling).toEqual({
      refetchInterval: 5_000,
      refetchIntervalInBackground: false,
    });
  });

  it("filters and sorts the rendered desktop table without loading inline history", async () => {
    const user = userEvent.setup();
    mount([
      schedule({
        name: "Alpha",
        nextFireAt: "2026-10-08T10:00:00Z",
        prompt: "Publish the report",
      }),
      schedule({
        name: "Beta",
        nextFireAt: "2026-10-08T09:00:00Z",
        prompt: "Remove stale previews",
      }),
      schedule({ enabled: false, name: "Done", status: "completed" }),
    ]);

    expect(renderedDesktopNames()).toEqual(["Alpha", "Beta", "Done"]);
    await user.click(screen.getByRole("button", { name: /Next run/ }));
    expect(renderedDesktopNames()).toEqual(["Beta", "Alpha", "Done"]);

    const search = screen.getByRole("textbox", { name: "Filter scheduled tasks" });
    await user.type(search, "REPORT");
    expect(renderedDesktopNames()).toEqual(["Alpha"]);

    await user.clear(search);
    await user.click(screen.getByRole("radio", { name: "scheduled" }));
    expect(renderedDesktopNames()).toEqual(["Beta", "Alpha"]);
    expect(screen.queryByText("Run history")).toBeNull();
  });

  it("buckets effective statuses without hiding a disabled schedule that is running", async () => {
    const user = userEvent.setup();
    mount([
      schedule({ enabled: false, name: "Live paused", status: "running" }),
      schedule({ name: "Claimed", status: "claimed" }),
      schedule({ name: "Future", status: "scheduled" }),
      schedule({ enabled: false, name: "Paused", status: "paused" }),
      schedule({ enabled: false, name: "Completed", status: "completed" }),
    ]);

    expect(renderedDesktopNames()).toEqual([
      "Live paused",
      "Claimed",
      "Future",
      "Paused",
      "Completed",
    ]);
    await user.click(screen.getByRole("radio", { name: "scheduled" }));
    expect(renderedDesktopNames()).toEqual(["Live paused", "Claimed", "Future"]);
    await user.click(screen.getByRole("radio", { name: "paused" }));
    expect(renderedDesktopNames()).toEqual(["Paused"]);
    await user.click(screen.getByRole("radio", { name: "all" }));
    expect(renderedDesktopNames()).toContain("Completed");
  });

  it("offers a visible clear action for a search with no matches", async () => {
    const user = userEvent.setup();
    mount([schedule({ name: "Alpha" })]);
    await user.type(screen.getByRole("textbox", { name: "Filter scheduled tasks" }), "missing");
    await user.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(
      screen.getByRole<HTMLInputElement>("textbox", { name: "Filter scheduled tasks" }).value,
    ).toBe("");
    expect(renderedDesktopNames()).toEqual(["Alpha"]);
  });

  it("applies the search query to both the desktop and mobile layouts", async () => {
    const user = userEvent.setup();
    mount([
      schedule({ name: "Alpha", prompt: "Publish the report" }),
      schedule({ name: "Beta", prompt: "Remove stale previews" }),
    ]);

    await user.type(screen.getByRole("textbox", { name: "Filter scheduled tasks" }), "report");
    expect(renderedDesktopNames()).toEqual(["Alpha"]);
    const mobile = document.querySelector<HTMLElement>("[data-schedule-mobile-list]");
    expect(within(mobile as HTMLElement).queryAllByRole("link").length).toBe(1);
    expect(within(mobile as HTMLElement).getByText("Alpha")).toBeTruthy();
  });

  it("focuses search with slash and clears then blurs with Escape", async () => {
    const user = userEvent.setup();
    mount([schedule({ name: "Alpha" })]);
    const search = screen.getByRole<HTMLInputElement>("textbox", {
      name: "Filter scheduled tasks",
    });

    await user.keyboard("/");
    expect(document.activeElement).toBe(search);
    await user.type(search, "alpha");
    fireEvent.keyDown(search, { key: "Escape" });
    expect(search.value).toBe("");
    expect(document.activeElement).toBe(search);
    fireEvent.keyDown(search, { key: "Escape" });
    expect(document.activeElement).not.toBe(search);
  });

  it("hides advanced schedule controls in the simplified form", async () => {
    const user = userEvent.setup();
    mount([schedule({ name: "Alpha" })]);

    await user.click(screen.getByRole("button", { name: "Schedule task" }));
    expect(screen.queryByText("Timezone")).toBeNull();
    expect(screen.queryByText(/Maximum runs/)).toBeNull();
    expect(screen.queryByText("Retry if the run fails")).toBeNull();
    expect(screen.queryByText("Maximum retries")).toBeNull();
  });

  it("restores focus after cancelling schedule creation", async () => {
    const user = userEvent.setup();
    mount([schedule({ name: "Alpha" })]);
    const trigger = screen.getByRole("button", { name: "Schedule task" });
    await user.click(trigger);
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(document.activeElement).toBe(trigger));
  });

  it("uses backend-safe eligibility in the desktop action menu", async () => {
    const user = userEvent.setup();
    mount([
      schedule({
        enabled: false,
        name: "Done once",
        status: "completed",
        trigger: { at: "2026-10-01T09:00:00Z", kind: "once" },
      }),
    ]);

    await user.click(screen.getByRole("button", { name: "Actions for Done once" }));
    expect(screen.getByRole("menuitem", { name: "Run now" }).hasAttribute("data-disabled")).toBe(
      true,
    );
    expect(screen.getByRole("menuitem", { name: "Resume" }).hasAttribute("data-disabled")).toBe(
      true,
    );
    expect(screen.getByRole("menuitem", { name: "Edit" }).hasAttribute("data-disabled")).toBe(true);
    expect(screen.getByRole("menuitem", { name: "Delete" }).hasAttribute("data-disabled")).toBe(
      false,
    );
  });

  it("does not act on a disabled menu item, even for a click that skips pointer events", async () => {
    const user = userEvent.setup();
    mount([
      schedule({
        enabled: true,
        name: "Past once",
        status: "paused",
        trigger: { at: "2026-10-01T09:00:00Z", kind: "once" },
      }),
    ]);

    await user.click(screen.getByRole("button", { name: "Actions for Past once" }));
    const edit = screen.getByRole("menuitem", { name: "Edit" });
    expect(edit.hasAttribute("data-disabled")).toBe(true);
    fireEvent.click(edit);

    expect(screen.queryByRole("dialog", { name: "Edit scheduled task" })).toBeNull();
  });
});

describe("schedule actions refresh run history", () => {
  const firesKeyFor = () => listScheduleFiresQueryKey({ path: { scheduleName: "Alpha" } });
  const alpha = schedule({ name: "Alpha" });

  async function runNow(user: ReturnType<typeof userEvent.setup>) {
    await user.click(screen.getByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Run now" }));
  }

  it("invalidates the schedule's fires after Run now", async () => {
    const user = userEvent.setup();
    fakeApi((method) => (method === "GET" ? listResponse([alpha]) : undefined));
    const { queryClient } = mount([alpha]);
    queryClient.setQueryData(firesKeyFor(), { items: [] });

    await runNow(user);
    await waitFor(() => expect(queryClient.getQueryState(firesKeyFor())?.isInvalidated).toBe(true));
  });

  it("still invalidates the fires when the fire is rejected", async () => {
    const user = userEvent.setup();
    fakeApi((method) => (method === "POST" ? problem("daemon refused") : listResponse([alpha])));
    const { queryClient } = mount([alpha]);
    queryClient.setQueryData(firesKeyFor(), { items: [] });

    await runNow(user);
    expect(await screen.findByText("daemon refused")).toBeTruthy();
    expect(queryClient.getQueryState(firesKeyFor())?.isInvalidated).toBe(true);
  });

  it("does not touch the fires after Pause", async () => {
    const user = userEvent.setup();
    const listFetches: string[] = [];
    fakeApi((method, path) => {
      if (method !== "GET") return undefined;
      if (path === "/api/v1/schedules") listFetches.push(path);
      return listResponse([alpha]);
    });
    const { queryClient } = mount([alpha]);
    queryClient.setQueryData(firesKeyFor(), { items: [] });

    await user.click(screen.getByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Pause" }));
    await waitFor(() => expect(listFetches.length).toBe(1));
    expect(queryClient.getQueryState(firesKeyFor())?.isInvalidated).toBe(false);
  });
});

describe("schedule detail actions", () => {
  it("still refetches the fires when Run now is rejected", async () => {
    const user = userEvent.setup();
    let firesFetches = 0;
    fakeApi((method, path) => {
      if (method === "POST") return problem("daemon refused");
      if (path.endsWith("/fires")) {
        firesFetches += 1;
        return jsonResponse({ items: [] });
      }
      return listResponse([schedule({ name: "Alpha" })]);
    });
    mountDetail([schedule({ name: "Alpha" })]);
    await waitFor(() => expect(firesFetches).toBe(1));

    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Run now" }));

    expect(await screen.findByText("daemon refused")).toBeTruthy();
    await waitFor(() => expect(firesFetches).toBe(2));
  });
});

describe("schedule detail delete", () => {
  async function confirmDelete(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Delete" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Delete" }));
  }

  it("clears the caches only after leaving, so the page never flashes not-found or refires", async () => {
    const user = userEvent.setup();
    const calls: string[] = [];
    fakeApi((method, path) => {
      calls.push(`${method} ${path}`);
      if (method === "GET" && path.endsWith("/fires")) return jsonResponse({ items: [] });
      if (method === "GET") return new Promise<Response>(() => {});
      return undefined;
    });
    const { queryClient, router } = mountDetail([schedule({ name: "Alpha" })]);

    // Hold the navigation so the cache state while the page is still mounted is observable.
    const navigate = router.navigate.bind(router);
    let release = () => {};
    const held = new Promise<void>((resolve) => {
      release = resolve;
    });
    router.navigate = (async (options: Parameters<typeof navigate>[0]) => {
      await held;
      return navigate(options);
    }) as typeof router.navigate;
    let flashed = false;
    const watcher = new MutationObserver(() => {
      if (screen.queryByRole("alert")) flashed = true;
    });
    watcher.observe(document.body, { childList: true, subtree: true });

    await confirmDelete(user);
    await waitFor(() => expect(calls).toContain("DELETE /api/v1/schedules/Alpha"));
    const afterDelete = calls.length;
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(
      queryClient.getQueryData<ListSchedulesResponse>(listSchedulesQueryKey())?.items,
    ).toHaveLength(1);

    release();
    await waitFor(() => expect(router.history.location.pathname).toBe("/workspace/schedules"));
    await waitFor(() =>
      expect(
        queryClient.getQueryData<ListSchedulesResponse>(listSchedulesQueryKey())?.items,
      ).toEqual([]),
    );
    await new Promise((resolve) => setTimeout(resolve, 50));
    watcher.disconnect();

    expect(flashed).toBe(false);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(calls.slice(afterDelete).filter((call) => call.endsWith("/fires"))).toEqual([]);
  });

  it("reports a rejected delete and stays on the page", async () => {
    const user = userEvent.setup();
    fakeApi((method, path) => {
      if (method === "GET" && path.endsWith("/fires")) return jsonResponse({ items: [] });
      return method === "DELETE" ? problem("delete refused") : undefined;
    });
    const { router } = mountDetail([schedule({ name: "Alpha" })]);

    await confirmDelete(user);
    expect(await screen.findByText("delete refused")).toBeTruthy();
    expect(router.history.location.pathname).toBe("/workspace/schedules/Alpha");
  });
});

function mount(items: Schedule[]) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  queryClient.setQueryData<ListSchedulesResponse>(listSchedulesQueryKey(), {
    items,
    reason: "",
    supported: true,
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/schedules"] }),
    routeTree,
  });
  render(
    <QueryClientProvider client={queryClient}>
      <RouterContextProvider router={router}>
        <ShortcutProvider>
          <SchedulesWorkspace />
        </ShortcutProvider>
      </RouterContextProvider>
    </QueryClientProvider>,
  );
  return { queryClient, router };
}

function renderedDesktopNames() {
  return within(screen.getByRole("table"))
    .queryAllByRole("link")
    .map((link) => link.textContent);
}

/** A two-route router so the detail page can really navigate away to the list. */
function mountDetail(items: Schedule[]) {
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
    component: () => <ScheduleDetail scheduleName="Alpha" />,
    getParentRoute: () => root,
    path: "/workspace/schedules/$scheduleName",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/schedules/Alpha"] }),
    routeTree: root.addChildren([list, detail]),
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
