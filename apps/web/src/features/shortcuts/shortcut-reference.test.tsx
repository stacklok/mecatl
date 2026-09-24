// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

import type { GetRuntimeResponse } from "@mecatl-studio/contracts/generated";
import { getRuntimeQueryKey } from "@mecatl-studio/contracts/query";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { ShortcutReference } from "./shortcut-reference";
import { keycaps, shortcutRegistry } from "./shortcut-registry";

function render(runtime?: GetRuntimeResponse) {
  const client = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity } } });
  const router = createRouter({
    history: createMemoryHistory({ initialEntries: ["/workspace/shortcuts"] }),
    routeTree,
  });
  if (runtime) client.setQueryData(getRuntimeQueryKey(), runtime);
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <RouterContextProvider router={router}>
        <ShortcutReference />
      </RouterContextProvider>
    </QueryClientProvider>,
  );
}

const runtime = {
  capabilities: { skills: true, steer: true, scheduling: false },
  connection: "online",
} as GetRuntimeResponse;

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
afterEach(() => vi.restoreAllMocks());

describe("shortcut reference", () => {
  it("renders shortcuts from the registry on direct load", async () => {
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/shortcuts"] }),
      routeTree,
    });
    await router.load();
    expect(router.state.matches.at(-1)?.routeId).toBe("/workspace/shortcuts");

    const html = render(runtime);
    const text = document.createElement("div");
    text.innerHTML = html;
    for (const shortcut of shortcutRegistry) {
      expect(text.textContent).toContain(shortcut.description);
      for (const keycap of keycaps(shortcut.combo, navigator.platform.includes("Mac"))) {
        expect(html).toContain(keycap);
      }
    }
    expect(html).toContain("Skills");
    expect(html).toContain("Steer");
    expect(html).toContain("Features on this agent");
    expect(html).not.toContain("Scheduled runs live under the Scheduled tab");

    const offline = render({ ...runtime, connection: "offline" });
    expect(offline).toContain("Connect to an agent to see which features are turned on.");
    expect(offline).not.toContain("Browse and manage skills under Skills");
    expect(render()).toContain("Checking what&#x27;s turned on");
  });

  it.each(["offline", "failure"] as const)(
    "hides cached deployment features while a fresh runtime read becomes %s",
    async (outcome) => {
      let finish!: (response: Response) => void;
      const response = new Promise<Response>((resolve) => {
        finish = resolve;
      });
      vi.spyOn(globalThis, "fetch").mockImplementation(() => response);
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      client.setQueryData(getRuntimeQueryKey(), runtime);
      const router = createRouter({
        history: createMemoryHistory({ initialEntries: ["/workspace/shortcuts"] }),
        routeTree,
      });
      await router.load();
      const host = document.createElement("div");
      document.body.append(host);
      const root = createRoot(host);
      try {
        await act(async () => {
          root.render(
            <QueryClientProvider client={client}>
              <RouterContextProvider router={router}>
                <ShortcutReference />
              </RouterContextProvider>
            </QueryClientProvider>,
          );
        });
        expect(host.textContent).toContain("Checking what's turned on");
        expect(host.textContent).not.toContain("Browse and manage skills under Skills");
        await act(async () => {
          finish(
            outcome === "offline"
              ? Response.json({ ...runtime, connection: "offline" })
              : Response.json({ code: "runtime_unavailable" }, { status: 503 }),
          );
        });
        expect(host.textContent).toContain("Connect to an agent");
        expect(host.textContent).not.toContain("Browse and manage skills under Skills");
      } finally {
        await act(async () => root.unmount());
        host.remove();
      }
    },
  );

  it("refreshes visible feature availability when the agent disconnects", async () => {
    vi.useFakeTimers();
    let reads = 0;
    vi.spyOn(globalThis, "fetch").mockImplementation(async () =>
      Response.json(reads++ === 0 ? runtime : { ...runtime, connection: "offline" }),
    );
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(getRuntimeQueryKey(), runtime);
    const router = createRouter({
      history: createMemoryHistory({ initialEntries: ["/workspace/shortcuts"] }),
      routeTree,
    });
    await router.load();
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => {
        root.render(
          <QueryClientProvider client={client}>
            <RouterContextProvider router={router}>
              <ShortcutReference />
            </RouterContextProvider>
          </QueryClientProvider>,
        );
      });
      expect(host.textContent).toContain("Browse and manage skills under Skills");
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
        await vi.advanceTimersByTimeAsync(1);
      });
      await act(async () => Promise.resolve());
      expect(reads).toBeGreaterThanOrEqual(2);
      expect(client.getQueryData<GetRuntimeResponse>(getRuntimeQueryKey())?.connection).toBe(
        "offline",
      );
      expect(host.textContent).toContain("Connect to an agent");
      expect(host.textContent).not.toContain("Browse and manage skills under Skills");
    } finally {
      await act(async () => root.unmount());
      host.remove();
      vi.useRealTimers();
    }
  });
});
