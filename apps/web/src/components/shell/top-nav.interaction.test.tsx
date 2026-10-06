// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TopNav } from "./top-nav";

vi.mock("../../features/search/global-search", () => ({
  GlobalSearch: () => <button aria-label="Search" type="button" />,
}));

vi.mock("../ui/tooltip", () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => children,
  TooltipContent: () => null,
  TooltipTrigger: ({ children }: { children: React.ReactNode }) => children,
}));

const destinations = [
  ["Chats", "/workspace/chat"],
  ["Scheduled", "/workspace/schedules"],
  ["Skills", "/workspace/skills"],
  ["Settings", "/workspace/settings"],
] as const;

function testRouter() {
  const rootRoute = createRootRoute({
    component: () => (
      <>
        <TopNav />
        <Outlet />
      </>
    ),
  });
  const routes = destinations.map(([label, path]) =>
    createRoute({ getParentRoute: () => rootRoute, path, component: () => <h1>{label}</h1> }),
  );
  return createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/chat"] }),
    routeTree: rootRoute.addChildren(routes),
  });
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = false;
  document.body.replaceChildren();
});

describe("TopNav routing", () => {
  it("activates a destination anchor and updates the current page", async () => {
    const router = testRouter();
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(<RouterProvider router={router} />);
      await router.load();
    });

    const scheduled = host.querySelector<HTMLAnchorElement>('nav a[href="/workspace/schedules"]');
    expect(scheduled?.textContent).toContain("Scheduled");
    expect(scheduled?.getAttribute("aria-current")).toBeNull();
    expect(scheduled?.tabIndex).toBe(0);
    await act(async () => {
      scheduled?.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(router.state.location.pathname).toBe("/workspace/schedules");
    expect(host.querySelector('nav a[aria-current="page"]')?.textContent).toContain("Scheduled");

    await act(async () => {
      host.querySelector<HTMLAnchorElement>('header a[href="/workspace/chat"]')?.click();
    });
    expect(router.state.location.pathname).toBe("/workspace/chat");
    expect(router.state.location.search).toEqual({});
    await act(async () => root.unmount());
  });
});
