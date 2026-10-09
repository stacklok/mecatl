// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
import { getAuthSessionOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ShortcutProvider } from "@/features/shortcuts/shortcut-provider";
import { GlobalSearch } from "./global-search";

/**
 * Behaviour pins for the palette's markup-independent contract: result order
 * and contents, accessible roles and names, the keyboard model, focus restore,
 * and each result kind's navigation target. They query roles, ARIA
 * relationships, and accessible names only, so the same assertions hold for
 * any palette implementation that keeps the behaviour.
 */

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const account = { account: "account-a", mode: "oidc", status: "authenticated" } as const;

const inventory: Record<string, unknown> = {
  "/api/v1/learned-skills": {
    items: [
      {
        description: "Condenses a report into bullet points",
        id: "learned-1",
        name: "Summaries",
        ownerAgent: "agent",
        state: "active",
        version: "2",
      },
    ],
    supported: true,
  },
  "/api/v1/schedules": {
    items: [{ modelId: "model-s", name: "nightly-report", owner: "agent", status: "scheduled" }],
    supported: true,
  },
  "/api/v1/sessions": {
    items: [
      { id: "s-desc", modelId: "report-model", state: "idle", title: "Weekly sync" },
      { id: "s-prefix", modelId: "model-a", state: "idle", title: "Report draft" },
      { id: "s-exact", modelId: "model-b", state: "idle", title: "report" },
    ],
  },
  "/api/v1/skills": {
    items: [
      {
        activeVersion: "1",
        description: "Writes weekly reports",
        name: "report-writer",
        ownerAgent: "agent",
      },
    ],
    supported: true,
  },
  "/api/v1/user-memory": {
    items: [{ description: "Keep reports short", key: "report-style" }],
    supported: true,
  },
};

let authSession: Record<string, unknown> = account;

const fetch = vi.fn<typeof globalThis.fetch>(async (input) => {
  const request = input instanceof Request ? input : new Request(input);
  const pathname = new URL(request.url).pathname;
  if (pathname === "/api/v1/auth/session") return Response.json(authSession);
  const body = inventory[pathname];
  if (!body) throw new Error(`Unexpected request: ${pathname}`);
  return Response.json(body);
});

const initialApiConfig = apiClient.getConfig();
let root: Root | undefined;
let container: HTMLDivElement | undefined;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  container?.remove();
  container = undefined;
  document.body.replaceChildren();
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
});

async function mount(session: Record<string, unknown> = account) {
  authSession = session;
  apiClient.setConfig({ baseUrl: window.location.origin, fetch });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  queryClient.setQueryData(getAuthSessionOptions().queryKey, session as never);
  const rootRoute = createRootRoute({
    component: () => (
      <ShortcutProvider>
        <button type="button">Behind search</button>
        <GlobalSearch />
      </ShortcutProvider>
    ),
  });
  const destination = (path: string) =>
    createRoute({ component: () => null, getParentRoute: () => rootRoute, path });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/"] }),
    routeTree: rootRoute.addChildren([
      destination("/"),
      destination("/workspace/chat"),
      destination("/workspace/schedules/$scheduleName"),
      destination("/workspace/settings"),
      destination("/workspace/settings/$section"),
      destination("/workspace/shortcuts"),
      destination("/workspace/skills"),
      destination("/workspace/skills/$view/$item"),
    ]),
  });
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () => {
    root?.render(
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    await router.load();
  });
  return router;
}

function keydown(target: EventTarget, key: string, init: KeyboardEventInit = {}) {
  const event = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, key, ...init });
  target.dispatchEvent(event);
  return event;
}

const trigger = () => document.querySelector<HTMLButtonElement>('button[aria-label="Search"]');
const combobox = () => document.querySelector<HTMLInputElement>('[role="combobox"]');
const listbox = () => document.querySelector<HTMLElement>('[role="listbox"]');
const options = () => [...document.querySelectorAll<HTMLElement>('[role="option"]')];

/** The accessible name, from aria-labelledby when present, else aria-label. */
function accessibleName(element: Element | null): string | undefined {
  if (!element) return undefined;
  const labelledBy = element.getAttribute("aria-labelledby");
  if (labelledBy) {
    return labelledBy
      .split(/\s+/)
      .map((id) => document.getElementById(id)?.textContent?.trim() ?? "")
      .join(" ")
      .trim();
  }
  return element.getAttribute("aria-label") ?? undefined;
}

async function openAndSearch(query: string) {
  await act(async () => trigger()?.click());
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
  const input = combobox();
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
  await act(async () => {
    if (input) setter?.call(input, query);
    input?.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await vi.waitFor(() =>
    expect(document.querySelector('[aria-live="polite"]')?.textContent).not.toContain("Loading"),
  );
  return input as HTMLInputElement;
}

const activeOption = () =>
  document.getElementById(combobox()?.getAttribute("aria-activedescendant") ?? "");

describe("GlobalSearch behaviour pins", () => {
  it("ranks results inside a fixed section order and shows each title and description", async () => {
    await mount();
    await openAndSearch("report");
    const groups = [...(listbox()?.querySelectorAll('[role="group"]') ?? [])];
    expect(groups.map((group) => accessibleName(group))).toEqual([
      "Chats",
      "Schedules",
      "Skills",
      "Memory",
      "Pages",
    ]);
    expect(
      groups.map((group) =>
        [...group.querySelectorAll('[role="option"]')].map((option) => option.textContent),
      ),
    ).toEqual([
      ["reportmodel-b", "Report draftmodel-a", "Weekly syncreport-model"],
      ["nightly-reportmodel-s"],
      ["report-writerWrites weekly reports", "SummariesCondenses a report into bullet points"],
      ["report-styleKeep reports short"],
      ["AboutThis deployment's status, agent version, and how to report a problem."],
    ]);
    expect(document.querySelector('[aria-live="polite"]')?.textContent).toBe("8 results");
  });

  it("keeps the accessible roles, names, and relationships", async () => {
    await mount();
    expect(trigger()?.getAttribute("aria-keyshortcuts")).toBe("Control+K Meta+K");
    const input = await openAndSearch("report");
    const dialog = document.querySelector('[role="dialog"]');
    expect(accessibleName(dialog)).toBe("Search Mecatl");
    expect(dialog?.hasAttribute("aria-describedby")).toBe(false);
    expect(accessibleName(input)).toBe("Search chats, schedules, skills, and memory");
    expect(input.getAttribute("aria-autocomplete")).toBe("list");
    expect(input.getAttribute("aria-expanded")).toBe("true");
    expect(input.getAttribute("aria-controls")).toBe(listbox()?.id);
    expect(accessibleName(listbox())).toBe("Search results");
    expect(options().every((option) => listbox()?.contains(option))).toBe(true);
    const selected = options().filter((option) => option.getAttribute("aria-selected") === "true");
    expect(selected).toHaveLength(1);
    expect(selected[0]).toBe(options()[0]);
    expect(activeOption()).toBe(options()[0]);
    expect(
      options()
        .slice(1)
        .every((option) => option.getAttribute("aria-selected") === "false"),
    ).toBe(true);
    expect(accessibleName(document.querySelector('button[aria-label="Close search"]'))).toBe(
      "Close search",
    );
  });

  it("names the help-only combobox when the account is unknown", async () => {
    await mount({ mode: "oidc", status: "authenticated" });
    const input = await openAndSearch("shortcuts");
    expect(accessibleName(input)).toBe("Search help and pages");
    expect(options().map((option) => option.textContent)).toEqual([
      "Keyboard shortcutsEvery keyboard shortcut in the app.",
    ]);
  });

  it("stops the highlight at both ends and leaves Home and End to the text field", async () => {
    await mount();
    const input = await openAndSearch("report");
    const all = options();
    expect(all).toHaveLength(8);
    await act(async () => keydown(input, "ArrowUp"));
    expect(activeOption()).toBe(all[0]);
    for (let step = 0; step < all.length + 2; step += 1) {
      await act(async () => keydown(input, "ArrowDown"));
    }
    expect(activeOption()).toBe(all[all.length - 1]);
    expect(all[all.length - 1]?.getAttribute("aria-selected")).toBe("true");

    let home: KeyboardEvent | undefined;
    await act(async () => {
      home = keydown(input, "Home");
    });
    expect(home?.defaultPrevented).toBe(false);
    expect(activeOption()).toBe(all[all.length - 1]);
    await act(async () => keydown(input, "ArrowUp"));
    const beforeEnd = activeOption();
    let end: KeyboardEvent | undefined;
    await act(async () => {
      end = keydown(input, "End");
    });
    expect(end?.defaultPrevented).toBe(false);
    expect(activeOption()).toBe(beforeEnd);
  });

  it("ignores Enter without a result", async () => {
    const router = await mount();
    const input = await openAndSearch("no-such-thing");
    expect(options()).toHaveLength(0);
    await act(async () => keydown(input, "Enter"));
    expect(router.state.location.pathname).toBe("/");
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
  });

  it("clears the active descendant when results disappear", async () => {
    await mount();
    const input = await openAndSearch("report");
    expect(input.hasAttribute("aria-activedescendant")).toBe(true);
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(input, "reportzzz");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(options()).toHaveLength(0);
    expect(input.hasAttribute("aria-activedescendant")).toBe(false);
  });

  it("toggles closed with the open shortcut and restores focus to the prior element", async () => {
    await mount();
    const behind = document.querySelector<HTMLButtonElement>("button:not([aria-label])");
    behind?.focus();
    await act(async () => keydown(document, "k", { ctrlKey: true }));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.activeElement).toBe(combobox());
    await act(async () => keydown(combobox() as HTMLInputElement, "k", { ctrlKey: true }));
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeNull());
    await vi.waitFor(() => expect(document.activeElement).toBe(behind));

    await act(async () => keydown(document, "k", { metaKey: true }));
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    await act(async () => keydown(combobox() as HTMLInputElement, "Escape"));
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeNull());
    await vi.waitFor(() => expect(document.activeElement).toBe(behind));
  });

  it.each([
    ["report", 0, "/workspace/chat", { sessionId: "s-exact" }],
    ["nightly-report", 0, "/workspace/schedules/nightly-report", {}],
    ["report-writer", 0, "/workspace/skills/configured/report-writer", {}],
    ["Summaries", 0, "/workspace/skills/learned/learned-1", {}],
    ["report-style", 0, "/workspace/settings/memory", { item: "report-style" }],
    ["About", 0, "/workspace/settings/about", {}],
    ["shortcuts", 0, "/workspace/shortcuts", {}],
  ])("navigates %s to its route", async (query, index, pathname, search) => {
    const router = await mount();
    const input = await openAndSearch(query);
    for (let step = 0; step < index; step += 1) {
      await act(async () => keydown(input, "ArrowDown"));
    }
    await act(async () => keydown(input, "Enter"));
    await vi.waitFor(() => expect(router.state.location.pathname).toBe(pathname));
    expect(router.state.location.search).toEqual(search);
    await vi.waitFor(() => expect(document.querySelector('[role="dialog"]')).toBeNull());
  });
});
