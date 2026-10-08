// SPDX-License-Identifier: Apache-2.0

import { getAuthSessionOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { TooltipProvider } from "../ui/tooltip";
import { TopNav } from "./top-nav";

const destinations = [
  ["Chats", "/workspace/chat"],
  ["Scheduled", "/workspace/schedules"],
  ["Skills", "/workspace/skills"],
  ["Settings", "/workspace/settings"],
] as const;

// The real nav and search render in a memory router; the auth session search
// needs to render its button is seeded, so a static render makes no requests.
async function renderNav(pathname: string) {
  const client = new QueryClient();
  client.setQueryData(getAuthSessionOptions().queryKey, { mode: "none", status: "disabled" });
  const rootRoute = createRootRoute({ component: TopNav });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: [pathname] }),
    routeTree: rootRoute.addChildren(
      destinations.map(([, path]) => createRoute({ getParentRoute: () => rootRoute, path })),
    ),
  });
  await router.load();
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <TooltipProvider>
        <RouterProvider router={router} />
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

function links(markup: string) {
  return [...markup.matchAll(/<a\b[^>]*>[\s\S]*?<\/a>/g)].map(([html]) => ({
    html,
    href: html.match(/href="([^"]+)"/)?.[1],
    name: html.replace(/<[^>]+>/g, "").trim(),
  }));
}

describe("TopNav", () => {
  it("keeps four destinations accessible at desktop and mobile widths", async () => {
    for (const [, pathname] of destinations) {
      const markup = await renderNav(pathname);
      const rendered = links(markup);
      expect(rendered[0]).toMatchObject({ href: "/workspace/chat" });
      expect(rendered[0]?.html).not.toContain("sessionId=");
      expect(rendered.slice(1).map(({ href, name }) => [name, href])).toEqual(destinations);
      expect(
        rendered.slice(1).filter(({ html }) => html.includes('aria-current="page"')),
      ).toHaveLength(1);
      expect(rendered.slice(1).find(({ href }) => href === pathname)?.html).toContain(
        'aria-current="page"',
      );
      for (const link of rendered) {
        expect(link.html).toMatch(/(?:size-11|h-11|min-h-11)/);
        expect(link.html).toMatch(/(?:size-11|w-11|min-w-11)/);
      }
      expect(markup).toContain('aria-label="Search"');
      expect(markup).toContain("[&amp;&gt;button]:min-h-11");
      expect(markup).toContain("[&amp;&gt;button]:min-w-11");
      expect(markup).not.toContain("min-[500px]:w-[214px]");
    }
  });
});
