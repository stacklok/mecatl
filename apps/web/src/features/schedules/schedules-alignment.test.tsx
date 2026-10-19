// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

// #2299: the schedule inventory and detail page in the prototype's shape.

import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, onTestFinished } from "vitest";
import { formatDate } from "../../lib/formatters";
import { mountSchedules, resetApi, schedule } from "./schedules-test-kit";

afterEach(() => {
  cleanup();
  resetApi();
});

const HOUR = 3_600_000;

function iso(offset: number) {
  return new Date(Date.now() + offset).toISOString();
}

/** What a sighted user reads: the text without visually hidden spans. */
function visibleText(element: Element | undefined) {
  const clone = element?.cloneNode(true) as Element | undefined;
  for (const hidden of clone?.querySelectorAll(".sr-only") ?? []) hidden.remove();
  return clone?.textContent ?? "";
}

async function desktopRow(name: string) {
  const table = await screen.findByRole("table");
  const row = within(table).getByRole("link", { name }).closest("tr");
  if (!row) throw new Error(`no row for ${name}`);
  return row;
}

describe("schedule inventory", () => {
  it("shows next and last run as relative times, with the full date in a time element", async () => {
    const next = iso(2 * HOUR + 5 * 60_000);
    const last = iso(-3 * HOUR - 5 * 60_000);
    mountSchedules(
      [
        schedule({ lastFireAt: last, name: "Alpha", nextFireAt: next }),
        schedule({ enabled: false, name: "Beta", nextFireAt: next, status: "paused" }),
      ],
      "/workspace/schedules",
    );
    const [, , nextCell, lastCell] = within(await desktopRow("Alpha")).getAllByRole("cell");
    expect(visibleText(nextCell)).toBe("in 2h");
    expect(nextCell?.querySelector("time")?.getAttribute("datetime")).toBe(next);
    // The exact date is text, for keyboard, touch and screen readers; the title is the
    // pointer's extra, never the only way to it.
    expect(nextCell?.querySelector("time .sr-only")?.textContent).toContain(formatDate(next));
    expect(nextCell?.getAttribute("title")).toBe(formatDate(next));
    expect(visibleText(lastCell)).toBe("3h ago");
    expect(lastCell?.querySelector("time")?.getAttribute("datetime")).toBe(last);
    expect(lastCell?.querySelector("time .sr-only")?.textContent).toContain(formatDate(last));
    expect(lastCell?.getAttribute("title")).toBe(formatDate(last));

    // A paused schedule promises no next run, and one that never ran has no date.
    const [, , pausedNext, neverLast] = within(await desktopRow("Beta")).getAllByRole("cell");
    expect(pausedNext?.textContent).toBe("—");
    expect(pausedNext?.querySelector("time")).toBeNull();
    expect(pausedNext?.hasAttribute("title")).toBe(false);
    expect(neverLast?.textContent).toBe("Never");
    expect(neverLast?.querySelector("time")).toBeNull();
    expect(neverLast?.hasAttribute("title")).toBe(false);
  });

  it("reads an enabled schedule whose next run has passed as overdue", async () => {
    const past = iso(-2 * HOUR);
    mountSchedules([schedule({ name: "Alpha", nextFireAt: past })], "/workspace/schedules");
    const [, , nextCell] = within(await desktopRow("Alpha")).getAllByRole("cell");
    expect(visibleText(nextCell)).toBe("Overdue");
    expect(nextCell?.querySelector("time")?.textContent).toContain(formatDate(past));
  });

  it("shows the last run as relative time in the mobile list, with its date as text", async () => {
    const last = iso(-3 * HOUR - 60_000);
    mountSchedules(
      [schedule({ fireCount: 2, lastFireAt: last, name: "Alpha" })],
      "/workspace/schedules",
    );
    await screen.findByRole("table");
    const mobile = document.querySelector("[data-schedule-mobile-list]") as HTMLElement;
    const link = within(mobile).getByRole("link");
    expect(visibleText(link)).toContain("2 runs · last 3h ago");
    expect(link.querySelector("time")?.getAttribute("datetime")).toBe(last);
    expect(link.querySelector("time .sr-only")?.textContent).toContain(formatDate(last));
  });

  it("shows status as a dot and a Title-case label, and the mobile dot names it on hover", async () => {
    mountSchedules(
      [
        schedule({ name: "Alpha", status: "running" }),
        schedule({ enabled: false, name: "Beta", status: "completed" }),
      ],
      "/workspace/schedules",
    );
    const statusCell = within(await desktopRow("Alpha")).getAllByRole("cell")[5] as HTMLElement;
    expect(statusCell.textContent).toBe("Running");
    const dot = statusCell.querySelector("[data-schedule-status-dot].animate-pulse");
    expect(dot?.classList.contains("motion-reduce:animate-none")).toBe(true);
    expect(within(await desktopRow("Beta")).getAllByRole("cell")[5]?.textContent).toBe("Completed");

    const mobile = document.querySelector("[data-schedule-mobile-list]") as HTMLElement;
    const titled = [...mobile.querySelectorAll("[title]")].map((node) =>
      node.getAttribute("title"),
    );
    expect(titled).toEqual(["Running", "Completed"]);
  });

  it("opens the detail page when any part of a row is clicked", async () => {
    const user = userEvent.setup();
    const { router } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    await user.click(within(await desktopRow("Alpha")).getByText("prompt"));
    await waitFor(() =>
      expect(router.history.location.pathname).toBe("/workspace/schedules/Alpha"),
    );
  });

  for (const modifier of ["Control", "Meta", "Shift", "Alt"]) {
    it(`leaves a ${modifier}-click on the row body to the browser`, async () => {
      const user = userEvent.setup();
      const { router } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
      const prompt = within(await desktopRow("Alpha")).getByText("prompt");
      await user.keyboard(`{${modifier}>}`);
      await user.click(prompt);
      await user.keyboard(`{/${modifier}}`);
      // A plain click right after proves the row still navigates, and that the
      // modified one was seen and skipped rather than still pending.
      expect(router.history.location.pathname).toBe("/workspace/schedules");
      await user.click(prompt);
      await waitFor(() =>
        expect(router.history.location.pathname).toBe("/workspace/schedules/Alpha"),
      );
    });
  }

  it("keeps a click that ends a text selection in the row from opening the detail page", async () => {
    const { router } = mountSchedules(
      [schedule({ name: "Alpha", prompt: "Publish the report" })],
      "/workspace/schedules",
    );
    const prompt = within(await desktopRow("Alpha")).getByText("Publish the report");
    // A drag that selects the prompt ends in a click on the row.
    const range = document.createRange();
    range.selectNodeContents(prompt);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
    onTestFinished(() => window.getSelection()?.removeAllRanges());
    prompt.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(router.history.location.pathname).toBe("/workspace/schedules");

    selection?.removeAllRanges();
    prompt.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    await waitFor(() =>
      expect(router.history.location.pathname).toBe("/workspace/schedules/Alpha"),
    );
  });

  it("keeps the row's actions menu and its items from opening the detail page", async () => {
    const user = userEvent.setup();
    const { router } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    await user.click(await screen.findByRole("button", { name: "Actions for Alpha" }));
    await user.click(screen.getByRole("menuitem", { name: "Edit" }));
    expect(await screen.findByRole("dialog", { name: "Edit scheduled task" })).toBeTruthy();
    expect(router.history.location.pathname).toBe("/workspace/schedules");
  });

  it("navigates once when the name link itself is clicked", async () => {
    const user = userEvent.setup();
    const { router } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const before = router.history.length;
    await user.click(within(await desktopRow("Alpha")).getByRole("link", { name: "Alpha" }));
    await waitFor(() =>
      expect(router.history.location.pathname).toBe("/workspace/schedules/Alpha"),
    );
    expect(router.history.length).toBe(before + 1);
  });

  it("leaves a modified click on the name link to the browser, without opening the row", async () => {
    const user = userEvent.setup();
    const { router } = mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const link = within(await desktopRow("Alpha")).getByRole("link", { name: "Alpha" });
    // Ctrl-click opens the link in a new tab; the current page stays on the list.
    // The test stops the browser's own default only after the app has seen the click.
    const stopDefault = (event: Event) => event.preventDefault();
    window.addEventListener("click", stopDefault);
    onTestFinished(() => window.removeEventListener("click", stopDefault));
    await user.keyboard("{Control>}");
    await user.click(link);
    await user.keyboard("{/Control}");
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(router.history.location.pathname).toBe("/workspace/schedules");
  });

  it("has no subtitle and a small New scheduled task button with a short phone label", async () => {
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    const heading = await screen.findByRole("heading", { level: 1, name: "Scheduled" });
    expect(heading.parentElement?.querySelector("p")).toBeNull();
    expect(screen.queryByText(/Recurring and one-off work/)).toBeNull();
    const create = screen.getByRole("button", { name: "New scheduled task" });
    // size="sm" sets the type; the action pill keeps its own height, as in the prototype.
    expect(create.className).toContain("text-[0.8rem]");
    expect(
      [...create.querySelectorAll("span")].map((span) => [span.textContent, span.className]),
    ).toEqual([
      ["New scheduled task", "max-[499px]:hidden"],
      ["New", "min-[500px]:hidden"],
    ]);
  });

  it("draws the table and mobile list as plain rounded-lg boxes", async () => {
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    await screen.findByRole("table");
    for (const selector of ["[data-schedule-desktop-table]", "[data-schedule-mobile-list]"]) {
      const box = document.querySelector(selector) as HTMLElement;
      expect(box.classList.contains("rounded-lg")).toBe(true);
      expect(box.classList.contains("bg-card")).toBe(false);
    }
  });

  it("shows an empty filter as a plain box with a ghost Clear filter button", async () => {
    const user = userEvent.setup();
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules");
    await user.type(await screen.findByRole("textbox", { name: "Filter scheduled tasks" }), "zz");
    const empty = screen.getByRole("status");
    expect(empty.querySelector("svg")).toBeNull();
    expect(empty.getAttribute("data-slot")).toBeNull();
    expect(within(empty).getByRole("button", { name: "Clear filter" }).className).toContain(
      "hover:bg-accent",
    );
  });
});

describe("schedule detail", () => {
  it("names the fire history Run log and shows a spinner while it loads", async () => {
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules/Alpha", "pending");
    const heading = await screen.findByRole("heading", { level: 2, name: "Run log" });
    const loading = within(heading.closest("section") as HTMLElement).getByRole("status");
    expect(loading.textContent).toBe("Reading the run log…");
    expect(loading.querySelector("svg.animate-spin")).toBeTruthy();
  });

  it("uses the prototype's title size and caps the content at max-w-4xl", async () => {
    mountSchedules([schedule({ name: "Alpha" })], "/workspace/schedules/Alpha");
    const title = await screen.findByRole("heading", { level: 1, name: "Alpha" });
    expect(title.className).toContain("text-[44px]");
    expect(title.className).toContain("max-[499px]:text-3xl");
    expect(document.querySelector(".max-w-5xl")).toBeNull();
    const content = screen.getByText("prompt").closest(".max-w-4xl");
    expect(content?.contains(screen.getByRole("heading", { level: 2, name: "Run log" }))).toBe(
      true,
    );
    expect(content?.contains(title)).toBe(false);
  });

  it("shows a live schedule's badge in Title case with a pulse that respects reduced motion", async () => {
    mountSchedules([schedule({ name: "Alpha", status: "claimed" })], "/workspace/schedules/Alpha");
    await screen.findByRole("heading", { level: 1, name: "Alpha" });
    const badge = screen.getByText("Claimed");
    const pulse = badge.querySelector(".animate-pulse");
    expect(pulse?.classList.contains("motion-reduce:animate-none")).toBe(true);
  });

  it("shows the exact next and last run dates beside the relative ones, without hover", async () => {
    const next = iso(13 * HOUR + 5 * 60_000);
    const last = iso(-10 * HOUR - 5 * 60_000);
    mountSchedules(
      [schedule({ lastFireAt: last, name: "Alpha", nextFireAt: next })],
      "/workspace/schedules/Alpha",
    );
    await screen.findByRole("heading", { level: 1, name: "Alpha" });
    const fact = (label: string) => screen.getByText(label, { selector: "dt" }).nextElementSibling;
    for (const [label, at, relative] of [
      ["Next run", next, "in 13h"],
      ["Last run", last, "10h ago"],
    ] as const) {
      const value = fact(label);
      expect(visibleText(value ?? undefined)).toBe(`${relative} · ${formatDate(at)}`);
      expect(value?.querySelector("time")?.getAttribute("datetime")).toBe(at);
      // Visible now, so a hover title would only repeat it.
      expect(value?.hasAttribute("title")).toBe(false);
      // On a phone the row wraps after the dot, not inside the date or the label.
      expect(value?.querySelector("time .whitespace-nowrap")?.textContent).toBe(formatDate(at));
      expect(value?.previousElementSibling?.classList.contains("shrink-0")).toBe(true);
    }
  });

  it("promises no next run, and no date, for a paused schedule", async () => {
    mountSchedules(
      [schedule({ enabled: false, name: "Alpha", nextFireAt: iso(HOUR), status: "paused" })],
      "/workspace/schedules/Alpha",
    );
    await screen.findByRole("heading", { level: 1, name: "Alpha" });
    const value = screen.getByText("Next run", { selector: "dt" }).nextElementSibling;
    expect(value?.textContent).toBe("—");
    expect(value?.hasAttribute("title")).toBe(false);
  });
});
