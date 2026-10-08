// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { client as apiClient } from "@mecatl-studio/contracts/client";
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
import { createRoot } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TooltipProvider } from "../ui/tooltip";
import { TopNav } from "./top-nav";

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

const initialApiConfig = apiClient.getConfig();

beforeEach(() => {
  // Search asks for the auth session; answer it at the network boundary.
  const fetch = vi.fn<typeof globalThis.fetch>(async () =>
    Response.json({ mode: "none", status: "disabled" }),
  );
  apiClient.setConfig({ baseUrl: window.location.origin, fetch });
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  apiClient.setConfig({ baseUrl: initialApiConfig.baseUrl, fetch: initialApiConfig.fetch });
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
      root.render(
        <QueryClientProvider client={new QueryClient()}>
          <TooltipProvider>
            <RouterProvider router={router} />
          </TooltipProvider>
        </QueryClientProvider>,
      );
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
