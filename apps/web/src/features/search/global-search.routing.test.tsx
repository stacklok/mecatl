// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ShortcutProvider } from "../shortcuts/shortcut-provider";
import { GlobalSearch } from "./global-search";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

vi.mock("@mecatl-studio/contracts/query", () => {
  const options = (key: string, data: unknown) => () => ({
    queryFn: async () => data,
    queryKey: [key],
  });
  return {
    getAuthSessionOptions: options("auth", { mode: "none", status: "disabled" }),
    listConfiguredSkillsOptions: options("configured-skills", { items: [], supported: true }),
    listLearnedSkillsOptions: options("learned-skills", { items: [], supported: true }),
    listSchedulesOptions: options("schedules", { items: [], supported: true }),
    listSessionsOptions: options("sessions", { items: [] }),
    listUserMemoryOptions: options("memory", { items: [], supported: true }),
  };
});

let root: Root | undefined;
let container: HTMLDivElement | undefined;

afterEach(async () => {
  await act(async () => root?.unmount());
  root = undefined;
  container?.remove();
  container = undefined;
  document.body.replaceChildren();
});

describe("GlobalSearch routing", () => {
  it("focuses the heading rendered by the destination route", async () => {
    const rootRoute = createRootRoute({
      component: () => (
        <ShortcutProvider>
          <GlobalSearch />
          <main>
            <Outlet />
          </main>
        </ShortcutProvider>
      ),
    });
    const chatRoute = createRoute({
      component: () => <h1>Chat draft</h1>,
      getParentRoute: () => rootRoute,
      path: "/workspace/chat",
    });
    const shortcutsRoute = createRoute({
      component: () => <h1>Keyboard shortcuts destination</h1>,
      getParentRoute: () => rootRoute,
      path: "/workspace/shortcuts",
    });
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/chat"] }),
      routeTree: rootRoute.addChildren([chatRoute, shortcutsRoute]),
    });
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, staleTime: Infinity } },
    });
    client.setQueryData(["auth"], { mode: "none", status: "disabled" });
    container = document.createElement("div");
    document.body.append(container);
    root = createRoot(container);
    await act(async () => {
      root?.render(
        <QueryClientProvider client={client}>
          <RouterProvider router={router} />
        </QueryClientProvider>,
      );
      await router.load();
    });

    await act(async () =>
      container?.querySelector<HTMLButtonElement>('button[aria-label="Search"]')?.click(),
    );
    const input = document.querySelector<HTMLInputElement>('input[role="combobox"]');
    expect(document.activeElement).toBe(input);
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set;
    await act(async () => {
      setter?.call(input, "shortcuts");
      input?.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(document.querySelector('[role="option"]')?.textContent).toContain("Keyboard shortcuts");
    await act(async () =>
      input?.dispatchEvent(new KeyboardEvent("keydown", { bubbles: true, key: "Enter" })),
    );
    await vi.waitFor(() => expect(router.state.location.pathname).toBe("/workspace/shortcuts"));
    const destination = container.querySelector<HTMLElement>("main h1");
    expect(destination?.textContent).toBe("Keyboard shortcuts destination");
    await vi.waitFor(() => expect(document.activeElement).toBe(destination));
  });
});
