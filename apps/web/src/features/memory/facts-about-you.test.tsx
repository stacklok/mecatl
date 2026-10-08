// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import { listUserMemoryQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { FactsAboutYou } from "./facts-about-you";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

afterEach(cleanup);

type MemoryList = {
  items: { description: string; key: string }[];
  reason: string;
  sha256: string;
  sizeBytes: string;
  supported: boolean;
};

function seeded(list?: MemoryList, error?: unknown) {
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, retryOnMount: false, staleTime: Number.POSITIVE_INFINITY },
    },
  });
  if (list) client.setQueryData(listUserMemoryQueryKey(), list);
  if (error) {
    client
      .getQueryCache()
      .build(client, { queryKey: listUserMemoryQueryKey() })
      .setState({
        error: error as Error,
        status: "error",
      });
  }
  return client;
}

async function renderFacts(client: QueryClient) {
  const root = createRootRoute({ component: Outlet });
  const settings = createRoute({
    component: FactsAboutYou,
    getParentRoute: () => root,
    path: "/workspace/settings/memory",
  });
  const detail = createRoute({
    component: () => null,
    getParentRoute: () => root,
    path: "/workspace/memory",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/settings/memory"] }),
    routeTree: root.addChildren([settings, detail]),
  });
  await act(async () => {
    render(
      <QueryClientProvider client={client}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    await router.load();
  });
}

const supported = (items: MemoryList["items"]): MemoryList => ({
  items,
  reason: "",
  sha256: "abc",
  sizeBytes: "42",
  supported: true,
});

describe("FactsAboutYou", () => {
  it("says the store is off instead of showing an empty list", async () => {
    await renderFacts(seeded({ ...supported([]), reason: "not enabled", supported: false }));
    expect(screen.getByText("Facts about you is off, so there is nothing to show.")).toBeTruthy();
  });

  it("distinguishes an enabled but empty store", async () => {
    await renderFacts(seeded(supported([])));
    expect(screen.getByText("The agent hasn’t remembered anything about you yet.")).toBeTruthy();
  });

  it("counts facts without byte sizes and lists them in a table", async () => {
    await renderFacts(
      seeded(
        supported([
          { description: "Prefers Italian replies", key: "language" },
          { description: "", key: "team/voice" },
        ]),
      ),
    );
    expect(screen.getByTestId("memory-footprint").textContent).toBe("2 facts remembered");
    expect(document.body.textContent).not.toMatch(/bytes/);
    const table = screen.getByRole("table");
    expect(within(table).getByRole("button", { name: /Name/ })).toBeTruthy();
    expect(within(table).getByRole("button", { name: /Remembers/ })).toBeTruthy();
    const link = within(table).getByRole("link", { name: "team/voice" });
    expect(link.getAttribute("href")).toBe("/workspace/memory?item=team%2Fvoice");
    expect(within(table).getAllByText("No description recorded.").length).toBeGreaterThan(0);
  });

  it("sorts by name and by what each fact remembers", async () => {
    await renderFacts(
      seeded(
        supported([
          { description: "b remembers", key: "alpha" },
          { description: "a remembers", key: "beta" },
        ]),
      ),
    );
    const names = () =>
      within(screen.getByRole("table"))
        .getAllByRole("link")
        .map((link) => link.textContent);
    expect(names()).toEqual(["alpha", "beta"]);
    fireEvent.click(screen.getByRole("button", { name: /Name/ }));
    expect(names()).toEqual(["beta", "alpha"]);
    fireEvent.click(screen.getByRole("button", { name: /Remembers/ }));
    expect(names()).toEqual(["beta", "alpha"]);
  });

  it("uses singular wording for one fact", async () => {
    await renderFacts(seeded(supported([{ description: "x", key: "only" }])));
    expect(screen.getByTestId("memory-footprint").textContent).toBe("1 fact remembered");
  });

  it("reports a load failure as an alert", async () => {
    await renderFacts(seeded(undefined, { detail: "Memory could not be read." }));
    expect(screen.getByRole("alert").textContent).toBe("Memory could not be read.");
  });
});
