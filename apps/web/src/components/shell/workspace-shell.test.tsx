// SPDX-License-Identifier: Apache-2.0

import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { WorkspaceShell } from "./workspace-shell";

const runtime = vi.hoisted(() => ({ connection: "online" }));

vi.mock("@mecatl-studio/contracts/query", () => ({ getRuntimeOptions: () => ({}) }));
vi.mock("@tanstack/react-query", () => ({
  useQuery: () => ({ data: { connection: runtime.connection }, isError: false, isPending: false }),
}));
vi.mock("@tanstack/react-router", () => ({ Outlet: () => <div>Route content</div> }));
vi.mock("../../features/shortcuts/shortcut-provider", () => ({
  ShortcutProvider: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("../ui/tooltip", () => ({
  TooltipProvider: ({ children }: { children: ReactNode }) => children,
}));
vi.mock("./top-nav", () => ({ TopNav: () => <header data-shell-nav="">Navigation</header> }));

function between(markup: string, start: string, end: string) {
  const from = markup.indexOf(start);
  const to = markup.indexOf(end);
  expect(from).toBeGreaterThanOrEqual(0);
  expect(to).toBeGreaterThan(from);
  return markup.slice(from, to);
}

describe("WorkspaceShell", () => {
  it("keeps recovery and transient notice bands in separate document-flow slots", () => {
    runtime.connection = "offline";
    let markup = renderToStaticMarkup(<WorkspaceShell />);
    expect(between(markup, "data-shell-global-status", "data-shell-gradient")).toContain(
      "Mecatl is unavailable right now.",
    );
    expect(between(markup, "data-shell-transient-status", "data-shell-nav")).not.toContain(
      "Mecatl is unavailable right now.",
    );
    expect(markup.match(/Mecatl is unavailable right now\./g)).toHaveLength(1);

    runtime.connection = "reconnecting";
    markup = renderToStaticMarkup(<WorkspaceShell />);
    expect(between(markup, "data-shell-global-status", "data-shell-gradient")).not.toContain(
      "Reconnecting to the Mecatl instance…",
    );
    expect(between(markup, "data-shell-transient-status", "data-shell-nav")).toContain(
      "Reconnecting to the Mecatl instance…",
    );

    runtime.connection = "online";
    markup = renderToStaticMarkup(<WorkspaceShell />);
    expect(markup).not.toContain("Mecatl is unavailable right now.");
    expect(markup).not.toContain("Reconnecting to the Mecatl instance…");
  });

  it("keeps the route outlet below both flexible banner slots and inside the safe-area frame", () => {
    runtime.connection = "offline";
    const markup = renderToStaticMarkup(<WorkspaceShell />);
    const route = markup.indexOf("Route content");
    expect(route).toBeGreaterThan(markup.indexOf("data-shell-nav"));
    expect(markup).toContain("env(safe-area-inset-top)");
    expect(markup).toContain("env(safe-area-inset-bottom)");
    expect(markup.match(/<main class="([^"]+)"/)?.[1]).toMatch(/min-h-0.*flex-1.*overflow-hidden/);
  });
});
