// SPDX-License-Identifier: Apache-2.0

import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { act, type ReactNode } from "react";
import type { Root } from "react-dom/client";

/**
 * Renders `children` inside a real memory router that knows the destinations
 * global search navigates to, and records each navigation the router starts.
 */
export async function renderInRouter(root: Root, children: ReactNode) {
  const rootRoute = createRootRoute({ component: () => children });
  const destination = (path: string) =>
    createRoute({ component: () => null, getParentRoute: () => rootRoute, path });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/"] }),
    routeTree: rootRoute.addChildren([
      destination("/"),
      destination("/workspace/settings"),
      destination("/workspace/shortcuts"),
    ]),
  });
  await act(async () => {
    root.render(<RouterProvider router={router} />);
    await router.load();
  });
  const navigations: string[] = [];
  router.subscribe("onBeforeNavigate", ({ toLocation }) => navigations.push(toLocation.pathname));
  return { navigations, router };
}
