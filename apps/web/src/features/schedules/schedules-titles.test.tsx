// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

// #2341 for `features/schedules/`: an interactive control explains itself with
// the Tooltip primitive, never a native `title`. The audit found no control
// here with a native title; the titles that remain disclose full values on
// plain text (a date behind a relative time, a cron expression behind its
// description, a status behind its dot). This guard renders every schedule
// surface and keeps it that way.

import { cleanup, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { formatDate } from "../../lib/formatters";
import { fire, jsonResponse, mountSchedules, resetApi, schedule } from "./schedules-test-kit";

afterEach(() => {
  cleanup();
  resetApi();
});

const INTERACTIVE = [
  "a[href]",
  "button",
  "input",
  "select",
  "textarea",
  "summary",
  "[tabindex]",
  "[contenteditable]",
  '[role="button"]',
  '[role="checkbox"]',
  '[role="combobox"]',
  '[role="link"]',
  '[role="menuitem"]',
  '[role="option"]',
  '[role="radio"]',
  '[role="switch"]',
  '[role="tab"]',
].join(",");

/**
 * Every native `title` in the document, as "<tag>: <title>", failing the test
 * when one sits on an interactive element. A title inside an interactive
 * element is allowed only for the status dot, whose label is also visually
 * hidden text in the phone row's link.
 */
function auditTitles() {
  const titled = [...document.querySelectorAll<HTMLElement>("[title]")];
  for (const element of titled) {
    expect(element.matches(INTERACTIVE), element.outerHTML).toBe(false);
    const insideControl = element.parentElement?.closest(INTERACTIVE);
    if (insideControl) {
      expect(element.querySelector("[data-schedule-status-dot]"), element.outerHTML).toBeTruthy();
      expect(element.querySelector(".sr-only")?.textContent).toBe(element.title);
    }
  }
  return titled.map((element) => `${element.tagName.toLowerCase()}: ${element.title}`);
}

const NEXT = new Date(Date.now() + 3 * 3_600_000).toISOString();
const LAST = new Date(Date.now() - 5 * 3_600_000).toISOString();
const ITEMS = [
  schedule({ fireCount: 2, lastFireAt: LAST, name: "Alpha", nextFireAt: NEXT, status: "running" }),
  schedule({
    enabled: false,
    name: "Beta",
    status: "completed",
    trigger: { at: "2026-10-01T09:00:00Z", kind: "once" },
  }),
];
const FIRES = [fire({ id: "f1", sessionId: "s1" }), fire({ id: "f2", outcome: "failed" })];

function sessions() {
  return (request: { method: string; path: string }) => {
    if (request.method !== "GET") return undefined;
    if (request.path === "/api/v1/runtime")
      return jsonResponse({ capabilities: {}, connection: "online" });
    if (request.path === "/api/v1/sessions") return jsonResponse({ complete: true, items: [] });
    if (request.path.startsWith("/api/v1/sessions/")) return new Promise<Response>(() => {});
    return undefined;
  };
}

describe("native titles in the schedule workspace", () => {
  it("stay off controls in the inventory, its menus and the create dialog", async () => {
    const user = userEvent.setup();
    mountSchedules(ITEMS, "/workspace/schedules");
    await screen.findByRole("table");
    expect(auditTitles()).toEqual(
      expect.arrayContaining([
        "span: Running",
        "span: Completed",
        `td: ${formatDate(NEXT)}`,
        `td: ${formatDate(LAST)}`,
      ]),
    );

    await user.click(screen.getByRole("button", { name: "Actions for Alpha" }));
    auditTitles();
    await user.keyboard("{Escape}");

    await user.click(screen.getByRole("button", { name: "New scheduled task" }));
    const dialog = screen.getByRole("dialog");
    auditTitles();
    await user.click(within(dialog).getByRole("tab", { name: "Run once" }));
    auditTitles();
  });

  it("stay off controls on the detail page, its edit dialog and the transcript dialog", async () => {
    const user = userEvent.setup();
    mountSchedules(ITEMS, "/workspace/schedules/Alpha", FIRES, sessions());
    await screen.findByRole("table");
    // Only the Repeat fact keeps a title (its raw cron); the next and last run
    // facts show their exact dates as text (#2369 review).
    expect(auditTitles().filter((entry) => entry.startsWith("dd:"))).toEqual([
      "dd: 0 9 * * * (UTC)",
    ]);

    await user.click(screen.getByRole("button", { name: "Actions for Alpha" }));
    auditTitles();
    await user.click(screen.getByRole("menuitem", { name: "Edit" }));
    await screen.findByRole("dialog");
    auditTitles();
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    await user.click(
      within(screen.getByRole("table")).getByRole("button", { name: "View transcript" }),
    );
    await screen.findByRole("dialog");
    auditTitles();
  });
});
