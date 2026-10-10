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
import { describeShortcut, keycaps, shortcutGroups, shortcutRegistry } from "./shortcut-registry";

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
afterEach(() => {
  vi.restoreAllMocks();
  window.localStorage.clear();
});

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

  it("groups shortcuts under every reference heading, Conversation included", () => {
    const page = document.createElement("div");
    page.innerHTML = render(runtime);
    const headings = [...page.querySelectorAll("section > h2")].map((h) => h.textContent);
    expect(headings.slice(0, shortcutGroups.length)).toEqual([...shortcutGroups]);
    const conversation = page.querySelector('section[aria-label="Conversation"]');
    expect(conversation?.textContent).toContain("Act on the top chat layer");
    expect(conversation?.textContent).toContain("Expand or collapse details");
    expect(headings).toContain("Escape in a chat");
  });

  it.each(["queue", "steer"] as const)(
    "words Enter and Shift+Enter from the stored %s preference",
    (behavior) => {
      if (behavior !== "queue") {
        window.localStorage.setItem("studio.chat.composer.enterToSend", behavior);
      }
      const page = document.createElement("div");
      page.innerHTML = render(runtime);
      const composer = page.querySelector('section[aria-label="Composer"]');
      const send = shortcutRegistry.find((shortcut) => shortcut.id === "composer.send");
      const newline = shortcutRegistry.find((shortcut) => shortcut.id === "composer.newline");
      if (!send || !newline) throw new Error("composer rows missing");
      expect(composer?.textContent).toContain(describeShortcut(send, behavior));
      expect(composer?.textContent).toContain(describeShortcut(newline, behavior));
      expect(composer?.textContent).toContain(
        behavior === "steer"
          ? "Send message — while the agent is replying: steer the agent"
          : "Send message — while the agent is replying: queue the message",
      );
    },
  );

  it("lists Image attachments only when the deployment enables images", () => {
    const off = render(runtime);
    expect(off).not.toContain("Image attachments");
    const on = render({
      ...runtime,
      capabilities: { ...runtime.capabilities, image: true },
    } as GetRuntimeResponse);
    expect(on).toContain("Image attachments");
    expect(on).toContain("Attach images to a message from the composer");
  });

  it("announces the feature check as a status", () => {
    const page = document.createElement("div");
    page.innerHTML = render();
    expect(page.querySelector('[role="status"]')?.textContent).toContain(
      "Checking what's turned on",
    );
  });
});
