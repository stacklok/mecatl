// SPDX-License-Identifier: Apache-2.0

import { getRuntimeOptions, getStorageHealthOptions } from "@mecatl-studio/contracts/query";
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
import { AuthRecoveryContext } from "../../features/auth/auth-recovery-context";
import type { StatusBannerInput } from "./connection-status-banner-state";
import { WorkspaceShell } from "./workspace-shell";

const baseBanner: StatusBannerInput = {
  authenticated: false,
  publicStatus: { connection: "reachable", signInRequired: true },
  publicStatusFailed: false,
  sessionCheckFailed: false,
};

// The real shell and nav render inside a real router and query client. A static
// render runs no effects, so nothing is fetched and the search stays unrendered.
async function renderShell(
  banner: StatusBannerInput = baseBanner,
  phase: "ready" | "sign-in" = "sign-in",
  client = new QueryClient(),
) {
  const rootRoute = createRootRoute({ component: WorkspaceShell });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/chat"] }),
    routeTree: rootRoute.addChildren([
      createRoute({
        getParentRoute: () => rootRoute,
        path: "/workspace/chat",
        component: () => <div>Route content</div>,
      }),
    ]),
  });
  await router.load();
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <AuthRecoveryContext.Provider
        value={{
          banner,
          loginUrl: "/api/v1/auth/login?return_to=%2Fworkspace%2Fchat",
          phase,
          popupIssue: null,
          retrySession: () => {},
          startPopupLogin: () => {},
        }}
      >
        <RouterProvider router={router} />
      </AuthRecoveryContext.Provider>
    </QueryClientProvider>,
  );
}

function between(markup: string, start: string, end: string) {
  const from = markup.indexOf(start);
  const to = markup.indexOf(end);
  expect(from).toBeGreaterThanOrEqual(0);
  expect(to).toBeGreaterThan(from);
  return markup.slice(from, to);
}

describe("WorkspaceShell", () => {
  it("shows each public recovery cause only in the global status slot", async () => {
    let markup = await renderShell({
      ...baseBanner,
      publicStatus: { connection: "unavailable", signInRequired: true },
    });
    expect(between(markup, "data-shell-global-status", "data-shell-gradient")).toContain(
      "The Mecatl instance is unavailable right now.",
    );
    expect(between(markup, "data-shell-transient-status", "<header")).not.toContain(
      "The Mecatl instance is unavailable right now.",
    );
    expect(markup.match(/The Mecatl instance is unavailable right now\./g)).toHaveLength(1);

    markup = await renderShell();
    expect(between(markup, "data-shell-global-status", "data-shell-gradient")).toContain(
      "Sign in to use this Mecatl workspace.",
    );
    expect(between(markup, "data-shell-transient-status", "<header")).not.toContain(
      "Sign in to use this Mecatl workspace.",
    );

    markup = await renderShell({
      ...baseBanner,
      publicStatus: { connection: "checking", signInRequired: true },
    });
    expect(markup).not.toContain("The Mecatl instance is unavailable right now.");
    expect(markup).not.toContain("Sign in to use this Mecatl workspace.");
  });

  it("keeps the route outlet below both flexible banner slots and inside the safe-area frame", async () => {
    const markup = await renderShell({
      ...baseBanner,
      publicStatus: { connection: "unavailable", signInRequired: true },
    });
    const route = markup.indexOf("Route content");
    expect(route).toBeGreaterThan(markup.indexOf("<header"));
    expect(markup).toContain("env(safe-area-inset-top)");
    expect(markup).toContain("env(safe-area-inset-bottom)");
    expect(markup.match(/<main class="([^"]+)"/)?.[1]).toMatch(/min-h-0.*flex-1.*overflow-hidden/);
  });

  it("puts the storage notice in the transient band inside the gradient", async () => {
    const client = new QueryClient();
    client.setQueryData(getRuntimeOptions().queryKey, {
      capabilities: { storageHealth: true },
      connection: "online",
    } as never);
    client.setQueryData(getStorageHealthOptions().queryKey, {
      activeJob: false,
      available: true,
      childCount: "0",
      corruptCount: "1",
      currentBytes: "1024",
      lastFailure: false,
      mainCount: "1",
      reclaimableBytes: "0",
      scheduledCount: "0",
      sessionCount: "1",
      supported: true,
      unknownCount: "0",
    });
    const markup = await renderShell(
      {
        ...baseBanner,
        authenticated: true,
        publicStatus: { connection: "reachable", signInRequired: false },
      },
      "ready",
      client,
    );
    expect(between(markup, "data-shell-transient-status", "<header")).toContain(
      "Session storage is degraded. Some chats may be missing.",
    );
    expect(between(markup, "data-shell-global-status", "data-shell-gradient")).not.toContain(
      "Session storage is degraded.",
    );
  });
});
