// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import {
  getRuntimeQueryKey,
  getUserMemoryQueryKey,
  listUserMemoryQueryKey,
} from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { describe, expect, it } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { MemoryFactDetail, MemorySettings } from "./memory-settings";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

describe("memory navigation", () => {
  it("opens a fact from the list with in-app navigation", async () => {
    const key = "team/voice";
    const queryClient = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
    queryClient.setQueryData(getRuntimeQueryKey(), { capabilities: {} });
    queryClient.setQueryData(listUserMemoryQueryKey(), {
      items: [{ description: "A voice preference", key }],
      reason: "",
      sha256: "abc",
      sizeBytes: "42",
      supported: true,
    });
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/settings/memory"] }),
      routeTree,
    });
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <RouterContextProvider router={router}>
            <MemorySettings />
          </RouterContextProvider>
        </QueryClientProvider>,
      );
    });
    const link = host.querySelector<HTMLAnchorElement>(
      'a[href="/workspace/memory?item=team%2Fvoice"]',
    );
    expect(link).not.toBeNull();
    const click = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
    await act(async () => link?.dispatchEvent(click));
    expect(click.defaultPrevented).toBe(true);
    expect(router.history.location.href).toBe("/workspace/memory?item=team%2Fvoice");
    await act(async () => root.unmount());
    host.remove();
  });

  it("returns from a fact to the Memory list without a document navigation", async () => {
    const key = "team/voice";
    const queryClient = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
    queryClient.setQueryData(getUserMemoryQueryKey({ path: { memoryKey: key } }), {
      current: {
        description: "A voice preference",
        key,
        origin: "conversation",
        sourceSessionId: "session-1",
        status: "active",
        updatedAt: null,
        value: "Use a calm voice.",
        version: "2",
        writer: "mecatl",
      },
      history: [],
      historyAvailable: true,
    });
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/memory?item=team%2Fvoice"] }),
      routeTree,
    });
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    await act(async () => {
      root.render(
        <QueryClientProvider client={queryClient}>
          <RouterContextProvider router={router}>
            <MemoryFactDetail memoryKey={key} />
          </RouterContextProvider>
        </QueryClientProvider>,
      );
    });
    const link = host.querySelector<HTMLAnchorElement>('a[href="/workspace/settings/memory"]');
    expect(link).not.toBeNull();
    const click = new MouseEvent("click", { bubbles: true, cancelable: true, button: 0 });
    await act(async () => link?.dispatchEvent(click));
    expect(click.defaultPrevented).toBe(true);
    expect(router.history.location.pathname).toBe("/workspace/settings/memory");
    await act(async () => root.unmount());
    host.remove();
  });
});
