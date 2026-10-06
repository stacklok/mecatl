// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { getRuntimeOptions } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from "@tanstack/react-router";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { WorkspaceShell } from "../../components/shell/workspace-shell";
import { AuthRecoveryContext } from "../auth/auth-recovery-context";
import { WriterWorkspace } from "./writer-workspace";

vi.mock("../search/global-search", () => ({ GlobalSearch: () => null }));

afterEach(cleanup);

async function mount(flag?: boolean, errored = false) {
  const query = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  });
  query.setQueryData<unknown>(getRuntimeOptions().queryKey, { experimentalWriter: flag });
  if (errored)
    query
      .getQueryCache()
      .find({ queryKey: getRuntimeOptions().queryKey })
      ?.setState({ status: "error", error: new Error("runtime unavailable") });
  const root = createRootRoute({ component: WorkspaceShell });
  const writer = createRoute({
    getParentRoute: () => root,
    path: "/workspace/writer",
    component: WriterWorkspace,
  });
  const router = createRouter({
    routeTree: root.addChildren([writer]),
    history: createMemoryHistory({ initialEntries: ["/workspace/writer"] }),
  });
  render(
    <QueryClientProvider client={query}>
      <AuthRecoveryContext.Provider
        value={{
          banner: { authenticated: true, publicStatusFailed: false, sessionCheckFailed: false },
          loginUrl: "/api/v1/auth/login",
          phase: "ready",
          popupIssue: null,
          retrySession: () => {},
          startPopupLogin: () => {},
        }}
      >
        <RouterProvider router={router} />
      </AuthRecoveryContext.Provider>
    </QueryClientProvider>,
  );
  await waitFor(() =>
    expect(
      screen.getByText(flag && !errored ? "No open observations." : "Writer is unavailable."),
    ).toBeTruthy(),
  );
}

it("direct navigation fails closed and hides the Writer nav when disabled", async () => {
  await mount(false);
  expect(screen.queryByRole("link", { name: "Writer" })).toBeNull();
});

it("keeps direct navigation and navigation hidden when the runtime flag is unknown", async () => {
  await mount();
  expect(screen.queryByRole("link", { name: "Writer" })).toBeNull();
});

it("fails closed on runtime error even when cached data previously enabled Writer", async () => {
  await mount(true, true);
  expect(screen.getByText("Writer is unavailable.")).toBeTruthy();
  expect(screen.queryByRole("link", { name: "Writer" })).toBeNull();
});

it("exposes Writer nav only with an explicit true runtime flag", async () => {
  await mount(true);
  expect(screen.getByRole("link", { name: "Writer" }).getAttribute("aria-current")).toBe("page");
});
