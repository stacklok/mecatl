// SPDX-License-Identifier: Apache-2.0
// @vitest-environment happy-dom

/**
 * Pins the Skills page's All / Learned switch before it becomes the shadcn
 * toggle group: the pill for the current view is the selected one, choosing
 * the other pill asks for its view, choosing the selected pill again keeps the
 * view, and the Learned pill is absent without a learned-skill inventory. A
 * pill's state is read from `aria-pressed` or `aria-checked`, whichever the
 * control exposes, so these hold on both implementations.
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterContextProvider } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { routeTree } from "../../routeTree.gen";
import { KnowledgeWorkspace } from "./knowledge-workspace";
import { resetPublicationNotice } from "./learned-skills";

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let root: Root | undefined;

afterEach(async () => {
  resetPublicationNotice();
  await act(async () => root?.unmount());
  root = undefined;
  document.body.replaceChildren();
  vi.unstubAllGlobals();
});

const empty = { complete: true, items: [], reason: "", supported: true };

async function render(learnedSkills: boolean, onViewChange: (view: string) => void) {
  vi.stubGlobal("fetch", async (request: Request) => {
    const path = new URL(request.url).pathname;
    const body =
      path === "/api/v1/runtime"
        ? { capabilities: { learnedSkills, skills: true }, connection: "online" }
        : path === "/api/v1/learned-skills/changes"
          ? { items: [], reason: "", supported: true }
          : empty;
    return new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } });
  });
  const container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  await act(async () =>
    root?.render(
      <RouterContextProvider
        router={createRouter({
          history: createMemoryHistory({ initialEntries: ["/workspace/skills"] }),
          routeTree,
        })}
      >
        <QueryClientProvider
          client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
        >
          <KnowledgeWorkspace
            onItemChange={() => {}}
            onViewChange={onViewChange}
            view="configured"
          />
        </QueryClientProvider>
      </RouterContextProvider>,
    ),
  );
  await act(async () => new Promise((resolve) => setTimeout(resolve, 20)));
  return container;
}

function pill(container: HTMLElement, name: string) {
  return [...container.querySelectorAll<HTMLButtonElement>("button")].find(
    (button) => button.textContent === name,
  );
}

const selected = (button?: HTMLButtonElement) =>
  button?.getAttribute("aria-pressed") ?? button?.getAttribute("aria-checked");

describe("skills view switch pins", () => {
  it("marks the current view and asks for the other one", async () => {
    const onViewChange = vi.fn();
    const container = await render(true, onViewChange);
    expect(selected(pill(container, "All"))).toBe("true");
    expect(selected(pill(container, "Learned"))).toBe("false");
    await act(async () => pill(container, "Learned")?.click());
    expect(onViewChange).toHaveBeenLastCalledWith("learned");
  });

  it("keeps the view when the selected pill is chosen again", async () => {
    const onViewChange = vi.fn();
    const container = await render(true, onViewChange);
    await act(async () => pill(container, "All")?.click());
    expect(onViewChange.mock.calls.every(([view]) => view === "configured")).toBe(true);
    expect(selected(pill(container, "All"))).toBe("true");
  });

  it("offers only All without a learned-skill inventory", async () => {
    const container = await render(false, () => {});
    expect(pill(container, "All")).toBeTruthy();
    expect(pill(container, "Learned")).toBeUndefined();
  });
});
