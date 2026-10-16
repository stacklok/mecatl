// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins each suggested change's "Show current values" disclosure before it
 * moves from `<details>` to the Collapsible primitive: every change has its
 * own disclosure, closed at first, that opens on click to show the stored
 * value and description of the memory the change keeps and of each memory it
 * merges in, and closes again on a second click. The closed values stay in
 * the document (so the browser's find-in-page can reach them), and the
 * disclosure adds no heading or region to the change. Its state is read from
 * the trigger's `aria-expanded` or from the `<details>` around it, whichever
 * the control exposes; content counts as shown when it is rendered, so these
 * hold on both implementations. Keyboard use and find-in-page are pinned in
 * a real browser by `e2e/memory-consolidation.spec.ts`, which happy-dom
 * cannot model for a `<summary>`.
 */

import { client as apiClient } from "@mecatl-studio/contracts/client";
import type { GenerateMemoryConsolidationPlanResponse } from "@mecatl-studio/contracts/generated";
import { getRuntimeQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { ConsolidateMemoryCard } from "./consolidate-memory-card";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const initialApiConfig = apiClient.getConfig();

afterEach(() => {
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
  cleanup();
});

const plan: GenerateMemoryConsolidationPlanResponse = {
  expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(),
  id: "plan1",
  operations: [
    {
      exactDuplicateEligible: true,
      kind: "merge",
      reason: "near-duplicates",
      replacement: { description: "Merged editor preference", value: "Uses vim with keybindings" },
      sources: [{ description: "Older note", key: "editor_2", value: "Prefers vim keybindings" }],
      survivor: { description: "Editor preference", key: "editor", value: "Uses vim" },
    },
    {
      exactDuplicateEligible: false,
      kind: "merge",
      reason: "same fact",
      replacement: { description: "", value: "Works in Europe/Rome" },
      sources: [{ description: "", key: "tz", value: "" }],
      survivor: { description: "Time zone", key: "", value: "Europe/Rome" },
    },
  ],
  plannedOperationCount: 2,
  plannedSourceCount: 4,
  target: "user_model",
};

const runtime = {
  capabilities: { manualDream: { userModel: { decide: true, generate: true } } },
  connection: "online",
};

function json(body: unknown, status: number) {
  return new Response(JSON.stringify(body), {
    headers: { "content-type": status < 400 ? "application/json" : "application/problem+json" },
    status,
  });
}

const fakeFetch: typeof globalThis.fetch = async (input) => {
  const request = input instanceof Request ? input : new Request(String(input));
  const { pathname } = new URL(request.url);
  if (pathname === "/api/v1/runtime") return json(runtime, 200);
  if (request.method === "POST" && pathname === "/api/v1/user-memory/consolidation/plans")
    return json(plan, 201);
  return json({ detail: `unexpected ${pathname}`, status: 404 }, 404);
};

/** Renders the card for an agent that can consolidate, then asks for the suggestion. */
async function renderPlan() {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, retryOnMount: false, staleTime: Number.POSITIVE_INFINITY },
    },
  });
  // The query key carries the client's base URL, so configure the client first.
  apiClient.setConfig({ baseUrl: window.location.origin, fetch: fakeFetch });
  client.setQueryData(getRuntimeQueryKey(), runtime);
  render(
    <QueryClientProvider client={client}>
      <ConsolidateMemoryCard />
    </QueryClientProvider>,
  );
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Consolidate memory" }));
  const dialog = await screen.findByRole("alertdialog");
  await user.click(within(dialog).getByRole("button", { name: "Continue" }));
  await screen.findByTestId("dream-plan-summary");
  const [editor, timezone] = screen.getAllByRole("listitem");
  if (!editor || !timezone) throw new Error("expected two suggested changes");
  return { editor, timezone, user };
}

const trigger = (change: HTMLElement) => within(change).getByText("Show current values");

/** Open or closed, read from whichever the control exposes. */
function isExpanded(change: HTMLElement) {
  const control = trigger(change);
  const expanded = control.getAttribute("aria-expanded");
  return expanded === null ? Boolean(control.closest("details")?.open) : expanded === "true";
}

/** On screen: connected, outside a `hidden` subtree, and not inside a closed
 *  `<details>`, which renders only its summary. */
function isRendered(element: Element | null) {
  if (!element?.isConnected || element.closest("[hidden]")) return false;
  const details = element.closest("details");
  return !details || details.open || element.closest("summary") !== null;
}

const shows = (change: HTMLElement, text: string) => isRendered(within(change).queryByText(text));

const editorValues = ["Uses vim", "Editor preference", "Prefers vim keybindings", "Older note"];
const timezoneValues = ["Europe/Rome", "Time zone", "(unnamed)", "(empty)"];

describe("current values disclosure pins", () => {
  it("keeps each change's current values behind its own closed disclosure", async () => {
    const { editor, timezone } = await renderPlan();
    for (const [change, values] of [
      [editor, editorValues],
      [timezone, timezoneValues],
    ] as const) {
      expect(isRendered(trigger(change))).toBe(true);
      expect(isExpanded(change)).toBe(false);
      for (const value of values) expect(shows(change, value)).toBe(false);
    }
    expect(shows(editor, "Uses vim with keybindings")).toBe(true);
    expect(shows(editor, "Merged editor preference")).toBe(true);
    expect(editor.textContent).toContain("Keeps editor and merges in editor_2");
  });

  it("opens on click to show what the change keeps and merges in, then closes again", async () => {
    const { editor, timezone, user } = await renderPlan();

    await user.click(trigger(editor));
    expect(isExpanded(editor)).toBe(true);
    for (const value of editorValues) expect(shows(editor, value)).toBe(true);
    expect(
      within(editor)
        .getAllByRole("term")
        .map((term) => term.textContent),
    ).toEqual(["Keeps editor", "Merges in editor_2"]);
    expect(isExpanded(timezone)).toBe(false);
    for (const value of timezoneValues) expect(shows(timezone, value)).toBe(false);

    await user.click(trigger(editor));
    expect(isExpanded(editor)).toBe(false);
    for (const value of editorValues) expect(shows(editor, value)).toBe(false);
  });

  it("opens each change on its own, with placeholders for a nameless or empty memory", async () => {
    const { editor, timezone, user } = await renderPlan();

    await user.click(trigger(timezone));
    await user.click(trigger(editor));
    expect([isExpanded(editor), isExpanded(timezone)]).toEqual([true, true]);
    for (const value of timezoneValues) expect(shows(timezone, value)).toBe(true);
    expect(
      within(timezone)
        .getAllByRole("term")
        .map((term) => term.textContent),
    ).toEqual(["Keeps (unnamed)", "Merges in tz"]);

    await user.click(trigger(timezone));
    expect([isExpanded(editor), isExpanded(timezone)]).toEqual([true, false]);
    for (const value of editorValues) expect(shows(editor, value)).toBe(true);
  });

  it("keeps the closed values in the document, so find-in-page can reach them", async () => {
    const { editor, timezone } = await renderPlan();
    for (const [change, values] of [
      [editor, editorValues],
      [timezone, timezoneValues],
    ] as const) {
      expect(isExpanded(change)).toBe(false);
      for (const value of values) expect(within(change).getByText(value).isConnected).toBe(true);
    }
  });

  it("adds no heading or region to a change, closed or open", async () => {
    const { editor, timezone, user } = await renderPlan();
    const headings = () =>
      screen.queryAllByRole("heading", { hidden: true }).map((heading) => heading.textContent);
    expect(headings()).toEqual(["Consolidate memory"]);
    const landmarks = (change: HTMLElement) => [
      ...within(change).queryAllByRole("heading", { hidden: true }),
      ...within(change).queryAllByRole("region", { hidden: true }),
    ];
    expect(landmarks(editor)).toEqual([]);
    expect(landmarks(timezone)).toEqual([]);
    await user.click(trigger(editor));
    expect(isExpanded(editor)).toBe(true);
    expect(landmarks(editor)).toEqual([]);
    expect(headings()).toEqual(["Consolidate memory"]);
  });
});
