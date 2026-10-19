// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins Settings → Learning's suggestion filter before its pills become the
 * shadcn toggle group: Pending is selected first and lists staged
 * suggestions; choosing another pill selects it alone and lists that status
 * from the first page; choosing the selected pill again keeps it; and each
 * pill keeps its 44px target. A pill's state is read from `aria-pressed` or
 * `aria-checked`, whichever the control exposes, so these hold on both
 * implementations.
 */

import { client as apiClient } from "@mecatl-studio/contracts/client";
import { getRuntimeQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { routeTree } from "@/routeTree.gen";
import { LearningSettingsPage } from "./learning-settings-page";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

const FILTERS = ["Pending", "Deferred", "Approved", "Rejected"];
const listCalls: string[] = [];
const initialApiConfig = apiClient.getConfig();

afterEach(() => {
  cleanup();
  listCalls.length = 0;
  apiClient.setConfig(initialApiConfig);
});

function json(body: unknown) {
  return new Response(JSON.stringify(body), { headers: { "content-type": "application/json" } });
}

const fakeFetch: typeof globalThis.fetch = async (input) => {
  const request = input instanceof Request ? input : new Request(String(input));
  const url = new URL(request.url);
  if (url.pathname === "/api/v1/sessions") return json({ complete: true, items: [] });
  if (url.pathname === "/api/v1/learning-proposals") {
    listCalls.push(`${url.searchParams.get("status")}|${url.searchParams.get("cursor") ?? ""}`);
    return json({ complete: true, items: [], nextCursor: "", reason: "", supported: true });
  }
  return new Response("{}", { status: 404 });
};

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  apiClient.setConfig({ baseUrl: window.location.origin, fetch: fakeFetch });
  client.setQueryData(getRuntimeQueryKey(), {
    capabilities: { learningProposals: true, reflection: true },
    connection: "online",
  });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/settings/learning"] }),
    routeTree,
  });
  return render(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <LearningSettingsPage />
      </RouterContextProvider>
    </QueryClientProvider>,
  );
}

function pill(name: string) {
  const match = [...document.querySelectorAll<HTMLButtonElement>("button")].find(
    (button) => button.textContent === name,
  );
  if (!match) throw new Error(`no ${name} pill`);
  return match;
}

const selection = () =>
  FILTERS.map(
    (name) => pill(name).getAttribute("aria-pressed") ?? pill(name).getAttribute("aria-checked"),
  );

describe("learning filter pins", () => {
  it("starts on Pending and moves the selection with the listed status", async () => {
    const user = userEvent.setup();
    renderPage();
    expect(await screen.findByText("Nothing waiting for review.")).toBeTruthy();
    expect(selection()).toEqual(["true", "false", "false", "false"]);
    expect(listCalls).toEqual(["staged|"]);

    await user.click(pill("Approved"));
    expect(await screen.findByText("No approved suggestions.")).toBeTruthy();
    expect(selection()).toEqual(["false", "false", "true", "false"]);
    await waitFor(() => expect(listCalls.at(-1)).toBe("promoted|"));
  });

  it("keeps the selected pill when it is chosen again", async () => {
    const user = userEvent.setup();
    renderPage();
    await user.click(await screen.findByText("Rejected"));
    expect(await screen.findByText("No rejected suggestions.")).toBeTruthy();
    await user.click(pill("Rejected"));
    expect(selection()).toEqual(["false", "false", "false", "true"]);
    expect(screen.getByText("No rejected suggestions.")).toBeTruthy();
    expect(listCalls.every((call) => !call.startsWith("|") && !call.startsWith("null"))).toBe(true);
  });

  it("keeps a 44px target on every pill", async () => {
    renderPage();
    await screen.findByText("Nothing waiting for review.");
    for (const name of FILTERS) expect(pill(name).classList.contains("min-h-11"), name).toBe(true);
  });
});
