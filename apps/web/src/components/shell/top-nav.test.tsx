// SPDX-License-Identifier: Apache-2.0

import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { TopNav } from "./top-nav";

const router = vi.hoisted(() => ({ pathname: "/workspace/chat" }));

vi.mock("@tanstack/react-router", async () => {
  const React = await import("react");
  return {
    Link: ({
      children,
      search,
      to,
      ...props
    }: React.PropsWithChildren<{ search?: unknown; to: string }>) =>
      React.createElement("a", { ...props, href: to }, children),
    useRouterState: ({
      select,
    }: {
      select: (state: { location: { pathname: string } }) => string;
    }) => select({ location: { pathname: router.pathname } }),
  };
});

vi.mock("../../features/search/global-search", async () => {
  const React = await import("react");
  return {
    GlobalSearch: () => React.createElement("button", { "aria-label": "Search", type: "button" }),
  };
});

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

function links(markup: string) {
  return [...markup.matchAll(/<a\b[^>]*>[\s\S]*?<\/a>/g)].map(([html]) => ({
    html,
    href: html.match(/href="([^"]+)"/)?.[1],
    name: html.replace(/<[^>]+>/g, "").trim(),
  }));
}

describe("TopNav", () => {
  it("keeps four destinations accessible at desktop and mobile widths", () => {
    for (const [, pathname] of destinations) {
      router.pathname = pathname;
      const markup = renderToStaticMarkup(<TopNav />);
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
