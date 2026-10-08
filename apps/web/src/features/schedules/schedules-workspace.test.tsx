// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { ListSchedulesResponse } from "@mecatl-studio/contracts/generated";
import { listSchedulesQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
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

afterEach(cleanup);

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
    await user.click(screen.getByRole("button", { name: "scheduled" }));
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
    await user.click(screen.getByRole("button", { name: "scheduled" }));
    expect(renderedDesktopNames()).toEqual(["Live paused", "Claimed", "Future"]);
    await user.click(screen.getByRole("button", { name: "paused" }));
    expect(renderedDesktopNames()).toEqual(["Paused"]);
    await user.click(screen.getByRole("button", { name: "all" }));
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
}

function renderedDesktopNames() {
  return within(screen.getByRole("table"))
    .queryAllByRole("link")
    .map((link) => link.textContent);
}
